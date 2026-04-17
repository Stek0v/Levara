// replication_chaos_test.go — FIX-6 chaos + convergence tests for the
// Mac↔Pi WAL streaming layer.
//
// What's already covered by replication_test.go: the in-process fan-out
// (AddReplica, Broadcast seq monotonicity, listener channels). What's
// NOT covered is the network round-trip: ReplicaClient fetching
// snapshot over HTTP, replaying NDJSON WAL entries, reconnecting with
// exponential backoff when the primary disappears.
//
// These tests wire a real httptest.Server in front of a real
// ReplicationServer and a real *store.Levara on both sides. No mocks —
// the only unreality is using localhost instead of Ethernet.
//
// Structure:
//   * TestConvergence_ReplicaCatchesUpFromSnapshot — seed primary, start
//     replica, verify snapshot bootstrap restores every record.
//   * TestConvergence_PropertyStyle — random mixed insert/delete stream,
//     replica state must equal primary state at steady-state.
//   * TestChaos_ReplicaReconnectsAfterPrimaryGap — stop the primary
//     server mid-stream, restart it, verify the replica re-establishes
//     the stream and picks up new writes.
//   * TestChaos_ReplicaSurvivesBriefDisconnect — inject a mid-stream
//     error via a wrapper handler, verify the streamLoop backoff path.
package cluster

import (
	"context"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stek0v/cognevra/internal/store"
)

// --- test harness -----------------------------------------------------------

// primaryHarness bundles the primary side: a store, a replication server,
// and the httptest.Server exposing the two endpoints the replica consumes.
type primaryHarness struct {
	store  *store.Levara
	server *ReplicationServer
	http   *httptest.Server
}

func newPrimary(t *testing.T, dim int) *primaryHarness {
	t.Helper()
	dir := t.TempDir()
	st, err := store.NewLevara(dim, filepath.Join(dir, "db"))
	if err != nil {
		t.Fatalf("new primary store: %v", err)
	}
	rs := NewReplicationServer("primary-1", nil, st)

	mux := http.NewServeMux()
	mux.HandleFunc("/cluster/wal/stream", rs.HandleStreamWAL)
	mux.HandleFunc("/cluster/snapshot", rs.HandleSnapshot)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	return &primaryHarness{store: st, server: rs, http: ts}
}

// addr returns the host:port form the ReplicaClient expects (no scheme).
func (p *primaryHarness) addr() string {
	return strings.TrimPrefix(p.http.URL, "http://")
}

// newReplicaStore creates the replica-side *store.Levara.
func newReplicaStore(t *testing.T, dim int) *store.Levara {
	t.Helper()
	dir := t.TempDir()
	st, err := store.NewLevara(dim, filepath.Join(dir, "db"))
	if err != nil {
		t.Fatalf("new replica store: %v", err)
	}
	return st
}

// snapshotIDs returns the sorted set of record IDs in a Levara store. Used
// for comparing primary vs replica state — vector/data contents are checked
// separately only where it matters so divergence is diagnosable.
func snapshotIDs(st *store.Levara) []string {
	recs := st.AllRecords()
	ids := make([]string, len(recs))
	for i, r := range recs {
		ids[i] = r.ID
	}
	sort.Strings(ids)
	return ids
}

// waitFor polls fn until it returns true or the deadline elapses. Returns
// true on success; callers get the last value for error reporting.
func waitFor(deadline time.Duration, fn func() bool) bool {
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		if fn() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fn()
}

// seed inserts n records into the store with deterministic IDs.
func seed(t *testing.T, st *store.Levara, prefix string, n int, dim int) {
	t.Helper()
	for i := 0; i < n; i++ {
		vec := make([]float32, dim)
		vec[i%dim] = float32(i + 1)
		id := fmt.Sprintf("%s-%d", prefix, i)
		if err := st.Insert(id, vec, []byte(fmt.Sprintf(`{"i":%d}`, i))); err != nil {
			t.Fatalf("seed insert %s: %v", id, err)
		}
	}
}

// --- convergence tests ------------------------------------------------------

// Fresh replica must pull the full snapshot and match primary on IDs.
func TestConvergence_ReplicaCatchesUpFromSnapshot(t *testing.T) {
	const dim = 4
	primary := newPrimary(t, dim)
	seed(t, primary.store, "p", 20, dim)

	replicaDB := newReplicaStore(t, dim)
	rc := NewReplicaClient(primary.addr(), "replica-1", replicaDB, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := rc.Start(ctx); err != nil {
		t.Fatalf("replica start: %v", err)
	}
	defer rc.Stop()

	ok := waitFor(2*time.Second, func() bool {
		return reflect.DeepEqual(snapshotIDs(primary.store), snapshotIDs(replicaDB))
	})
	if !ok {
		t.Errorf("replica did not converge\n primary: %v\n replica: %v",
			snapshotIDs(primary.store), snapshotIDs(replicaDB))
	}
}

// After snapshot, new Broadcasts must stream through and be applied.
// Checks the snapshot→WAL handoff doesn't lose entries issued just after
// the snapshot request returned.
func TestConvergence_PostSnapshotWALApplies(t *testing.T) {
	const dim = 4
	primary := newPrimary(t, dim)
	seed(t, primary.store, "snap", 5, dim)

	replicaDB := newReplicaStore(t, dim)
	rc := NewReplicaClient(primary.addr(), "replica-2", replicaDB, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := rc.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer rc.Stop()

	// Wait for snapshot phase.
	if !waitFor(2*time.Second, func() bool { return primary.server.ReplicaCount() >= 1 }) {
		t.Fatalf("replica never connected to WAL stream")
	}

	// Broadcast 10 fresh inserts. The replica's streamLoop must consume them.
	for i := 0; i < 10; i++ {
		vec := []float32{float32(i), 0, 0, 0}
		if err := primary.store.Insert(fmt.Sprintf("live-%d", i), vec, []byte(`{}`)); err != nil {
			t.Fatalf("primary insert: %v", err)
		}
		primary.server.Broadcast(WALEntryFromInsert(fmt.Sprintf("live-%d", i), vec, []byte(`{}`)))
	}

	ok := waitFor(3*time.Second, func() bool {
		return reflect.DeepEqual(snapshotIDs(primary.store), snapshotIDs(replicaDB))
	})
	if !ok {
		t.Errorf("post-snapshot WAL did not propagate\n primary: %v\n replica: %v",
			snapshotIDs(primary.store), snapshotIDs(replicaDB))
	}
}

// Property-style test: deterministic PRNG issues a mix of inserts/deletes
// against the primary; the replica must match the primary's ID set once the
// stream drains. Repeated over a small fixed seed because chaos tests with
// real sockets under -race get expensive.
func TestConvergence_PropertyStyleRandomOps(t *testing.T) {
	const dim = 4
	primary := newPrimary(t, dim)

	replicaDB := newReplicaStore(t, dim)
	rc := NewReplicaClient(primary.addr(), "replica-prop", replicaDB, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := rc.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer rc.Stop()

	if !waitFor(2*time.Second, func() bool { return primary.server.ReplicaCount() >= 1 }) {
		t.Fatalf("replica never connected")
	}

	rng := rand.New(rand.NewSource(42))
	const nOps = 200
	live := make(map[string]bool)

	for i := 0; i < nOps; i++ {
		// 70% insert / 30% delete. Delete picks an existing id when possible.
		if rng.Intn(10) < 7 || len(live) == 0 {
			id := fmt.Sprintf("k-%d", i)
			vec := []float32{rng.Float32(), rng.Float32(), rng.Float32(), rng.Float32()}
			if err := primary.store.Insert(id, vec, []byte(`{}`)); err == nil {
				primary.server.Broadcast(WALEntryFromInsert(id, vec, []byte(`{}`)))
				live[id] = true
			}
		} else {
			// pick an arbitrary live id
			var victim string
			for id := range live {
				victim = id
				break
			}
			if err := primary.store.Delete(victim); err == nil {
				primary.server.Broadcast(WALEntryFromDelete(victim))
				delete(live, victim)
			}
		}
	}

	ok := waitFor(5*time.Second, func() bool {
		return reflect.DeepEqual(snapshotIDs(primary.store), snapshotIDs(replicaDB))
	})
	if !ok {
		p := snapshotIDs(primary.store)
		r := snapshotIDs(replicaDB)
		t.Errorf("property convergence failed\n primary (%d): %v\n replica (%d): %v",
			len(p), p, len(r), r)
	}
}

// --- chaos tests ------------------------------------------------------------

// flakyListener wraps a net.Listener so tests can toggle whether new
// connections succeed. When closed=true every Accept call returns
// ErrClosed, which lets the test simulate "primary offline" without
// tearing down the underlying store state.
type flakyListener struct {
	net.Listener
	down atomic.Bool
	// conns are tracked so we can forcibly close them when flipping to down.
	connsMu sync.Mutex
	conns   []net.Conn
}

// Using sync for the conns mu (imported implicitly via sync/atomic? no —
// we need to add the import explicitly). See imports block.

func (f *flakyListener) Accept() (net.Conn, error) {
	for {
		c, err := f.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if f.down.Load() {
			c.Close()
			continue
		}
		f.connsMu.Lock()
		f.conns = append(f.conns, c)
		f.connsMu.Unlock()
		return c, nil
	}
}

func (f *flakyListener) goDown() {
	f.down.Store(true)
	f.connsMu.Lock()
	for _, c := range f.conns {
		c.Close()
	}
	f.conns = nil
	f.connsMu.Unlock()
}

func (f *flakyListener) goUp() { f.down.Store(false) }

// TestChaos_ReplicaReconnectsAfterPrimaryGap simulates a transient network
// gap: the primary's listener rejects new connections and drops existing
// ones for ~600ms, then recovers. The replica's exponential-backoff
// streamLoop must notice the gap and re-establish the stream. We verify
// the replica receives entries broadcast AFTER the recovery window.
func TestChaos_ReplicaReconnectsAfterPrimaryGap(t *testing.T) {
	if testing.Short() {
		t.Skip("chaos timing test — skipped under -short")
	}
	const dim = 4

	// Build primary with a flaky listener wrapped around a raw net.Listener.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	flaky := &flakyListener{Listener: ln}

	dir := t.TempDir()
	st, err := store.NewLevara(dim, filepath.Join(dir, "db"))
	if err != nil {
		t.Fatal(err)
	}
	rs := NewReplicationServer("primary-chaos", nil, st)

	mux := http.NewServeMux()
	mux.HandleFunc("/cluster/wal/stream", rs.HandleStreamWAL)
	mux.HandleFunc("/cluster/snapshot", rs.HandleSnapshot)
	httpSrv := &http.Server{Handler: mux}
	go httpSrv.Serve(flaky)
	defer httpSrv.Close()

	// Seed primary before the replica connects.
	seed(t, st, "pre", 3, dim)

	replicaDB := newReplicaStore(t, dim)
	rc := NewReplicaClient(flaky.Addr().String(), "replica-gap", replicaDB, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := rc.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer rc.Stop()

	// Phase 1: replica converges on snapshot.
	if !waitFor(2*time.Second, func() bool {
		return reflect.DeepEqual(snapshotIDs(st), snapshotIDs(replicaDB))
	}) {
		t.Fatalf("snapshot phase failed before gap")
	}

	// Phase 2: inject the gap. Drop all in-flight conns; Accept rejects new.
	flaky.goDown()

	// Broadcast during gap — replica cannot receive these immediately. Since
	// the replica is not connected, Broadcast would fan-out to 0 listeners
	// for the new stream, so we must push to primary store AND broadcast;
	// the replica will see these via a fresh WAL stream AFTER snapshot
	// refetch. To avoid relying on WAL-history replay we instead push post-
	// recovery — the invariant we care about is "stream resumes after gap".
	time.Sleep(400 * time.Millisecond)

	// Phase 3: recover and push new work.
	flaky.goUp()

	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("post-%d", i)
		vec := []float32{float32(i), 0, 0, 0}
		if err := st.Insert(id, vec, []byte(`{}`)); err != nil {
			t.Fatal(err)
		}
		// The replica needs to be reconnected first, so we wait before Broadcast.
	}

	// Wait for replica reconnect (ReplicaCount increments once streamLoop
	// succeeds on retry — backoff starts at 1s, so budget 5s).
	if !waitFor(8*time.Second, func() bool { return rs.ReplicaCount() >= 1 }) {
		t.Fatalf("replica did not reconnect after gap")
	}

	// Now broadcast — replica is listening again.
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("post-%d", i)
		vec := []float32{float32(i), 0, 0, 0}
		rs.Broadcast(WALEntryFromInsert(id, vec, []byte(`{}`)))
	}

	if !waitFor(3*time.Second, func() bool {
		return reflect.DeepEqual(snapshotIDs(st), snapshotIDs(replicaDB))
	}) {
		t.Errorf("replica did not re-converge after reconnect\n primary: %v\n replica: %v",
			snapshotIDs(st), snapshotIDs(replicaDB))
	}
}

// Stray case: the primary stops mid-stream without draining. The replica
// must not hang forever — the streamLoop's backoff should kick in and
// periodic retries should happen. We detect success by watching errors
// get rate-limited (backoff grows) rather than the stream hot-looping.
func TestChaos_ReplicaExponentialBackoffOnFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}
	const dim = 4
	replicaDB := newReplicaStore(t, dim)

	var attempts atomic.Int32
	// Handler that always 500s on the WAL stream → forces streamOnce to
	// return error and streamLoop to back off.
	mux := http.NewServeMux()
	mux.HandleFunc("/cluster/wal/stream", func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		http.Error(w, "nope", 500)
	})
	// Snapshot is valid so Start() succeeds; empty body.
	mux.HandleFunc("/cluster/snapshot", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("[]"))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	rc := NewReplicaClient(strings.TrimPrefix(ts.URL, "http://"), "replica-backoff", replicaDB, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := rc.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer rc.Stop()

	// 2.5s window: initial attempt ~immediately, then backoff 1s → 2s → 4s.
	// With perfect timing we'd see attempts at 0, 1, 3 — i.e. 2-3 attempts.
	// The invariant is bounded: NOT hundreds (which would indicate tight-loop).
	time.Sleep(2500 * time.Millisecond)

	n := attempts.Load()
	if n < 1 {
		t.Errorf("streamLoop never retried (attempts=%d)", n)
	}
	if n > 10 {
		t.Errorf("streamLoop hot-looping without backoff: %d attempts in 2.5s", n)
	}
}
