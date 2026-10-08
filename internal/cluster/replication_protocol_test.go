package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stek0v/levara/internal/store"
)

func TestDirectBatchMetadataListenerParity(t *testing.T) {
	for _, mode := range []string{"no_replication", "no_listener", "http_listener"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "primary.bin")
			db, err := store.NewLevara(2, path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			node := &DirectNode{DB: db}
			var replica *store.Levara
			var client *ReplicaClient
			if mode != "no_replication" {
				node.Repl = NewReplicationServer("primary", nil, db)
			}
			if mode == "http_listener" {
				replica, err = store.NewLevara(2, filepath.Join(t.TempDir(), "replica.bin"))
				if err != nil {
					t.Fatal(err)
				}
				defer replica.Close()
				server := httptest.NewServer(http.HandlerFunc(node.Repl.HandleStreamWAL))
				defer server.Close()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				client = NewReplicaClient(strings.TrimPrefix(server.URL, "http://"), "replica", replica, nil)
				if err := client.Start(ctx); err != nil {
					t.Fatal(err)
				}
				defer client.Stop()
			}
			values := map[string]any{
				"object_string": `{"a":1}`, "array_string": "[1,2]",
				"bytes": []byte(`{"a":1}`), "raw": json.RawMessage(`{"a":1}`),
				"map": map[string]any{"a": 1}, "plain": "plain",
			}
			records := make([]store.BatchItem, 0, len(values))
			want := make(map[string][]byte, len(values))
			for id, value := range values {
				data, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				want[id] = data
				records = append(records, store.BatchItem{ID: id, Vector: []float32{1, 0}, Data: value})
			}
			if errs := node.BatchInsert(records); len(errs) != 0 {
				t.Fatal(errs)
			}
			check := func(db *store.Levara) {
				t.Helper()
				for id, data := range want {
					_, got, ok := db.Get(id)
					if !ok || !bytes.Equal(got, data) {
						t.Errorf("%s metadata=%s want native batch=%s", id, got, data)
					}
				}
			}
			check(db)
			if replica != nil {
				deadline := time.Now().Add(5 * time.Second)
				for replica.Count() != len(values) && time.Now().Before(deadline) {
					time.Sleep(10 * time.Millisecond)
				}
				check(replica)
			}
			if errs := node.BatchInsert([]store.BatchItem{{ID: "invalid", Vector: []float32{1, 0}, Data: make(chan int)}}); len(errs) != 1 {
				t.Fatalf("invalid marshal errors=%v", errs)
			}
			if _, _, ok := db.Get("invalid"); ok {
				t.Error("invalid metadata inserted")
			}
			if node.Repl != nil {
				node.Repl.mu.RLock()
				count, seq := len(node.Repl.listeners), node.Repl.seq.Load()
				node.Repl.mu.RUnlock()
				if mode == "http_listener" && (count != 1 || seq != uint64(len(values))) {
					t.Error("deterministic marshal failure changed listener or emitted event")
				}
			}
			if client != nil {
				client.Stop()
			}
			for _, current := range []*store.Levara{db, replica} {
				if current == nil {
					continue
				}
				path := current.DiskPath()
				if err := current.Close(); err != nil {
					t.Fatal(err)
				}
				reopened, err := store.NewLevara(2, path)
				if err != nil {
					t.Fatal(err)
				}
				check(reopened)
				if _, _, ok := reopened.Get("invalid"); ok {
					t.Error("invalid metadata recovered")
				}
				if err := reopened.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestReplicationSnapshotFrameInventory(t *testing.T) {
	for _, payload := range []string{
		`{"version":1,"kind":"snapshot","records":[]}`,
		`{"version":1,"kind":"snapshot","records":[{"id":"bad","vector":[1,0],"data":{}}]}`,
		`{"seq":0,"version":1,"kind":"snapshot"}`,
		`{"seq":0,"version":1,"kind":"snapshot","records":null}`,
		`{"seq":0,"version":2,"kind":"snapshot","records":[]}`,
		`{"seq":0,"version":1,"kind":"wrong","records":[]}`,
		`{"seq":0,"version":1,"kind":"snapshot","records":[{"id":"bad","vector":[1],"data":{}}]}`,
	} {
		t.Run(payload, func(t *testing.T) {
			db := newReplicaStore(t, 2)
			defer db.Close()
			if err := db.Insert("keep", []float32{1, 0}, json.RawMessage(`{"old":true}`)); err != nil {
				t.Fatal(err)
			}
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, payload) }))
			defer ts.Close()
			rc := NewReplicaClient(strings.TrimPrefix(ts.URL, "http://"), "r", db, nil)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := rc.Start(ctx); err == nil {
				rc.Stop()
				t.Fatal("invalid snapshot accepted")
			}
			_, meta, ok := db.Get("keep")
			if !ok || string(meta) != `{"old":true}` || db.Count() != 1 {
				t.Fatal("invalid snapshot mutated old state")
			}
		})
	}
	t.Run("explicit_empty", func(t *testing.T) {
		db := newReplicaStore(t, 2)
		defer db.Close()
		if err := db.Insert("old", []float32{1, 0}, nil); err != nil {
			t.Fatal(err)
		}
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, `{"seq":0,"version":1,"kind":"snapshot","records":[]}`)
		}))
		defer ts.Close()
		rc := NewReplicaClient(strings.TrimPrefix(ts.URL, "http://"), "r", db, nil)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := rc.Start(ctx); err != nil {
			t.Fatal(err)
		}
		rc.Stop()
		if db.Count() != 0 {
			t.Fatal("explicit empty snapshot not applied")
		}
	})
}

func TestReplicationInvalidEntriesFailBeforeApply(t *testing.T) {
	for name, payload := range map[string]string{
		"gap":             `{"op":1,"id":"bad","seq":2,"vector":[1,0]}`,
		"empty_id":        `{"op":1,"seq":1,"vector":[1,0]}`,
		"unknown_op":      `{"op":99,"id":"bad","seq":1}`,
		"invalid_json":    `{"op":`,
		"wrong_dimension": `{"op":1,"id":"bad","seq":1,"vector":[1]}`,
		"missing_delete":  `{"op":2,"id":"missing","seq":1}`,
	} {
		t.Run(name, func(t *testing.T) {
			db := newReplicaStore(t, 2)
			defer db.Close()
			if err := db.Insert("keep", []float32{1, 0}, nil); err != nil {
				t.Fatal(err)
			}
			rc := NewReplicaClient("", "r", db, nil)
			if err := rc.readStream(context.Background(), json.NewDecoder(strings.NewReader(payload)), 0); err == nil {
				t.Fatal("invalid entry accepted")
			}
			if _, _, ok := db.Get("keep"); !ok || db.Count() != 1 {
				t.Fatal("invalid entry mutated control")
			}
		})
	}
}

func TestReplicationOverflowAndListenerReplacement(t *testing.T) {
	rs := NewReplicationServer("p", nil, nil)
	old := rs.AddReplica("r")
	current := rs.AddReplica("r")
	if _, ok := <-old; ok {
		t.Fatal("replaced listener remained open")
	}
	for i := 0; i <= cap(current); i++ {
		rs.Broadcast(WALEntry{Op: store.OpInsert, ID: "x"})
	}
	if rs.ReplicaCount() != 0 {
		t.Fatal("overflow did not invalidate listener")
	}
	for i := 0; i < cap(current); i++ {
		entry, ok := <-current
		if !ok || entry.Seq != uint64(i+1) {
			t.Fatal("buffered delivery reordered")
		}
	}
	if _, ok := <-current; ok {
		t.Fatal("overflowed listener remained open")
	}
}

func TestReplicationLiveStringJSONAndLargeSnapshot(t *testing.T) {
	primary := newPrimary(t, 2)
	defer primary.store.Close()
	node := &DirectNode{DB: primary.store, Repl: primary.server}
	large := map[string]string{"large": strings.Repeat("x", 768<<10)}
	for _, id := range []string{"large-A", "large-B"} {
		if err := node.Insert(id, []float32{1, 0}, large); err != nil {
			t.Fatal(err)
		}
	}
	wire, err := json.Marshal(primary.store.AllRecords())
	if err != nil || len(wire) <= 1<<20 {
		t.Fatal("fixture did not exceed legacy scanner ceiling")
	}
	db := newReplicaStore(t, 2)
	defer db.Close()
	rc := NewReplicaClient(primary.addr(), "r", db, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rc.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer rc.Stop()
	for _, id := range []string{"large-A", "large-B"} {
		_, want, ok := primary.store.Get(id)
		_, got, present := db.Get(id)
		if !ok || !present || !reflect.DeepEqual(got, want) {
			t.Fatal("large snapshot truncated")
		}
	}
	for id, value := range map[string]string{"object": `{"x":1}`, "array": `[1,2]`, "plain": "text"} {
		if err := node.Insert(id, []float32{0, 1}, value); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(time.Second)
		for {
			pv, pm, po := primary.store.Get(id)
			rv, rm, ro := db.Get(id)
			if po && ro && reflect.DeepEqual(pv, rv) && reflect.DeepEqual(pm, rm) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("metadata diverged for %s: primary=%s replica=%s", id, pm, rm)
			}
			time.Sleep(time.Millisecond)
		}
	}
	rc.Stop()
	cancel()
}

func TestReplicationOldStreamCleanupPreservesReplacement(t *testing.T) {
	db := newReplicaStore(t, 2)
	defer db.Close()
	rs := NewReplicationServer("p", nil, db)
	ts := httptest.NewServer(http.HandlerFunc(rs.HandleStreamWAL))
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"?replica_id=same", nil)
	old, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Body.Close()
	var frame replicationSnapshot
	if err := json.NewDecoder(old.Body).Decode(&frame); err != nil {
		t.Fatal(err)
	}
	req2, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"?replica_id=same", nil)
	current, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer current.Body.Close()
	if err := json.NewDecoder(current.Body).Decode(&frame); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(old.Body); err != nil {
		t.Fatal(err)
	}
	if rs.ReplicaCount() != 1 {
		t.Fatal("old handler removed current listener")
	}
	cancel()
}

func TestReplicationQueuedMetadataOwnsCallerBytes(t *testing.T) {
	for _, kind := range []string{"bytes", "raw"} {
		for _, batch := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/batch=%v", kind, batch), func(t *testing.T) {
				node, cleanup := newDirectNode(t, 2, true)
				defer cleanup()
				ch := node.Repl.AddReplica("r")
				input := []byte(`{"value":"a"}`)
				var metadata interface{} = input
				if kind == "raw" {
					metadata = json.RawMessage(input)
				}
				expected := append([]byte(nil), input...)
				if batch {
					var err error
					expected, err = json.Marshal(metadata)
					if err != nil {
						t.Fatal(err)
					}
					if errs := node.BatchInsert([]store.BatchItem{{ID: "id", Vector: []float32{1, 0}, Data: metadata}}); len(errs) != 0 {
						t.Fatal(errs)
					}
				} else if err := node.Insert("id", []float32{1, 0}, metadata); err != nil {
					t.Fatal(err)
				}
				copy(input, `{"value":"b"}`)
				entry := <-ch
				_, persisted, ok := node.DB.Get("id")
				if !ok || !bytes.Equal(persisted, expected) {
					t.Fatal("native persistence control invalid")
				}
				if !reflect.DeepEqual(entry.Metadata, json.RawMessage(persisted)) {
					t.Fatalf("queued metadata aliases input: queued=%s persisted=%s", entry.Metadata, persisted)
				}
			})
		}
	}
}
