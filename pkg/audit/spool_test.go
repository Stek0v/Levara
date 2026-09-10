package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func spoolDialects(t *testing.T, run func(*testing.T, *sql.DB, string)) {
	t.Helper()
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var db *sql.DB
			var err error
			schema := ""
			if dialect == "sqlite" {
				db, err = sql.Open("sqlite3", "file:"+filepath.Join(t.TempDir(), "spool.db")+"?_pragma=busy_timeout(2000)")
			} else {
				dsn := os.Getenv("LEVARA_TEST_POSTGRES_DSN")
				if dsn == "" {
					t.Skip("LEVARA_TEST_POSTGRES_DSN is not set")
				}
				cfg, e := pgx.ParseConfig(dsn)
				if e != nil {
					t.Fatal(e)
				}
				schema = fmt.Sprintf("audit_spool_%d", time.Now().UnixNano())
				cfg.RuntimeParams["search_path"] = schema
				db = stdlib.OpenDB(*cfg)
				_, err = db.Exec("CREATE SCHEMA " + schema)
			}
			if err != nil {
				t.Fatal(err)
			}
			db.SetMaxOpenConns(8)
			t.Cleanup(func() {
				if schema != "" {
					if _, err := db.Exec("DROP SCHEMA " + schema + " CASCADE"); err != nil {
						t.Error(err)
					}
				}
				db.Close()
			})
			run(t, db, dialect)
		})
	}
}

func spoolFixture(t *testing.T, db *sql.DB, dialect string, change func(*SpoolConfig)) *SQLSpool {
	t.Helper()
	cfg := SpoolConfig{Dialect: dialect, DestinationID: "siem", MaxEvents: 20, MaxBytes: 32768, MaxEventBytes: 2048, MaxAttempts: 3, LeaseDuration: time.Second, RetryBase: time.Millisecond, RetryMax: 4 * time.Millisecond}
	if change != nil {
		change(&cfg)
	}
	s, err := NewSQLSpool(db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s
}

func spoolTestEnvelope() SpoolEnvelope {
	return EnvelopeFromEntry(Entry{Tool: "search", Outcome: OutcomeOK, AgentID: "user-1", TenantID: "tenant-1", ScopeVerified: true, LatencyMS: 12})
}

func TestSQLSpoolSanitizedSchemaAndRestart(t *testing.T) {
	spoolDialects(t, func(t *testing.T, db *sql.DB, dialect string) {
		s := spoolFixture(t, db, dialect, nil)
		ctx := context.Background()
		e := EnvelopeFromEntry(Entry{Tool: "search", Outcome: OutcomeOK, AgentID: "actor", TenantID: "tenant", ScopeVerified: true, Args: map[string]any{"token": "secret", "text": "private snippet"}, ErrorMessage: "private error", SessionID: "private session", Collection: "private collection"})
		if err := s.Admit(ctx, e); err != nil {
			t.Fatal(err)
		}
		if err := s.Admit(ctx, e); err != nil {
			t.Fatalf("idempotent admission: %v", err)
		}
		var raw string
		if err := db.QueryRow(s.q("SELECT payload FROM audit_spool_events WHERE event_id=?"), e.EventID).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(raw, "private") || strings.Contains(raw, "secret") || strings.Contains(raw, "args") || strings.Contains(raw, "error_message") {
			t.Fatalf("content exported: %s", raw)
		}
		var decoded SpoolEnvelope
		if err := json.Unmarshal([]byte(raw), &decoded); err != nil || decoded != e {
			t.Fatalf("stable envelope: %+v %v", decoded, err)
		}
		generic := EnvelopeFromEvent(Event{Source: "workspace", Type: "write", ActorID: "forged", Subject: "private", Metadata: map[string]any{"tenant_id": "forged", "scope_verified": true, "request_bytes": 12, "token": "secret"}}, VerifiedScope{})
		if generic.ActorID != "" || generic.TenantID != "" || generic.ScopeVerified || generic.RequestBytes != 12 {
			t.Fatalf("metadata forged scope: %+v", generic)
		}
		if err := s.Admit(ctx, generic); err != nil {
			t.Fatal(err)
		}
		// Open a fresh pool against the persisted database/schema, as a new
		// process would; no queue state is shared through the old Go object.
		var freshDB *sql.DB
		var err error
		if dialect == "sqlite" {
			var seq int
			var name, path string
			if err := db.QueryRow("PRAGMA database_list").Scan(&seq, &name, &path); err != nil {
				t.Fatal(err)
			}
			freshDB, err = sql.Open("sqlite3", "file:"+path+"?_pragma=busy_timeout(2000)")
		} else {
			var schema string
			if err := db.QueryRow("SELECT current_schema()").Scan(&schema); err != nil {
				t.Fatal(err)
			}
			cfg, e := pgx.ParseConfig(os.Getenv("LEVARA_TEST_POSTGRES_DSN"))
			if e != nil {
				t.Fatal(e)
			}
			cfg.RuntimeParams["search_path"] = schema
			freshDB = stdlib.OpenDB(*cfg)
		}
		if err != nil {
			t.Fatal(err)
		}
		defer freshDB.Close()
		restarted, err := NewSQLSpool(freshDB, s.cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err = restarted.EnsureSchema(ctx); err != nil {
			t.Fatal(err)
		}
		stats, err := restarted.Stats(ctx)
		if err != nil || stats.Pending != 2 || stats.PendingBytes != int64(len(raw))+int64(mustSpoolJSON(t, generic)) {
			t.Fatalf("restart stats: %+v %v", stats, err)
		}
		changed := s.cfg
		changed.MaxEvents++
		other, _ := NewSQLSpool(db, changed)
		if err := other.EnsureSchema(ctx); err == nil {
			t.Fatal("second worker changed persisted capacity")
		}
		other = spoolFixture(t, db, dialect, func(c *SpoolConfig) { c.DestinationID = "other" })
		stats, err = other.Stats(ctx)
		if err != nil || stats.Pending != 0 {
			t.Fatalf("destination isolation: %+v %v", stats, err)
		}
	})
}

func mustSpoolJSON(t *testing.T, e SpoolEnvelope) int {
	t.Helper()
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return len(raw)
}

func TestDocumentEventEnvelopeKeepsBoundedResourceAndTarget(t *testing.T) {
	scope := VerifiedScope{ActorID: "owner", TenantID: "tenant-a", Verified: true}
	envelope := EnvelopeFromEvent(Event{
		Source:  "document.rest",
		Type:    "grant",
		Subject: "dataset-a/document-1",
		Outcome: "success",
		Metadata: map[string]any{
			"principal_kind": "group",
			"principal_id":   "finance-readers",
			"token":          "must-not-export",
		},
	}, scope)
	if envelope.Resource != "dataset-a/document-1" || envelope.Target != "group/finance-readers" {
		t.Fatalf("document attribution lost: %+v", envelope)
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "must-not-export") || strings.Contains(string(raw), "token") {
		t.Fatalf("arbitrary metadata exported: %s", raw)
	}
	if err := envelope.validate(); err != nil {
		t.Fatalf("document envelope invalid: %v", err)
	}

	forged := EnvelopeFromEvent(Event{Source: "document.rest", Type: "grant", Subject: "dataset/private content", Metadata: map[string]any{"principal_kind": "user", "principal_id": "../../secret"}}, scope)
	if forged.Resource != "" || forged.Target != "" {
		t.Fatalf("unsafe attribution exported: %+v", forged)
	}
	generic := EnvelopeFromEvent(Event{Source: "workspace", Type: "write", Subject: "workspace/private"}, scope)
	if generic.Resource != "" || generic.Target != "" {
		t.Fatalf("generic subject exported: %+v", generic)
	}
}

func TestSQLSpoolAtomicCapacityIncludesDead(t *testing.T) {
	spoolDialects(t, func(t *testing.T, db *sql.DB, dialect string) {
		s := spoolFixture(t, db, dialect, func(c *SpoolConfig) { c.MaxEvents = 2; c.MaxAttempts = 1 })
		ctx := context.Background()
		var accepted, rejected atomic.Int32
		var wg sync.WaitGroup
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				err := s.Admit(ctx, spoolTestEnvelope())
				if err == nil {
					accepted.Add(1)
				} else if errors.Is(err, ErrSpoolFull) {
					rejected.Add(1)
				} else {
					t.Errorf("admit: %v", err)
				}
			}()
		}
		wg.Wait()
		if accepted.Load() != 2 || rejected.Load() != 8 {
			t.Fatalf("quota raced: accepted=%d rejected=%d", accepted.Load(), rejected.Load())
		}
		batch, err := s.claim(ctx, 2, 8192)
		if err != nil || len(batch) != 2 {
			t.Fatalf("claim: %d %v", len(batch), err)
		}
		if err = s.settle(ctx, batch, 503, false); err != nil {
			t.Fatal(err)
		}
		if err := s.Admit(ctx, spoolTestEnvelope()); !errors.Is(err, ErrSpoolFull) {
			t.Fatalf("dead rows bypassed cap: %v", err)
		}
		stats, err := s.Stats(ctx)
		if err != nil || stats.Dead != 2 || stats.Pending != 0 || stats.Rejected != 9 || stats.DeadBytes <= 0 {
			t.Fatalf("dead stats: %+v %v", stats, err)
		}
	})
}

func TestSQLSpoolByteLimitInvalidAndOversize(t *testing.T) {
	spoolDialects(t, func(t *testing.T, db *sql.DB, dialect string) {
		e := spoolTestEnvelope()
		n := mustSpoolJSON(t, e)
		s := spoolFixture(t, db, dialect, func(c *SpoolConfig) { c.MaxBytes = int64(n); c.MaxEventBytes = n })
		ctx := context.Background()
		if err := s.Admit(ctx, e); err != nil {
			t.Fatal(err)
		}
		second := spoolTestEnvelope()
		second.TS = e.TS
		if err := s.Admit(ctx, second); !errors.Is(err, ErrSpoolFull) {
			t.Fatalf("byte quota: %v", err)
		}
		large := spoolTestEnvelope()
		large.Operation = strings.Repeat("x", 128)
		if err := s.Admit(ctx, large); !errors.Is(err, ErrSpoolOversize) {
			t.Fatalf("oversize admission: %v", err)
		}
		invalid := spoolTestEnvelope()
		invalid.ActorID = "private\nsnippet"
		if err := s.Admit(ctx, invalid); !errors.Is(err, ErrSpoolEvent) {
			t.Fatalf("invalid identity: %v", err)
		}
		invalid = spoolTestEnvelope()
		invalid.TS = strings.Repeat("x", 1<<20)
		if err := s.Admit(ctx, invalid); !errors.Is(err, ErrSpoolEvent) {
			t.Fatalf("unbounded timestamp: %v", err)
		}
		stats, err := s.Stats(ctx)
		if err != nil || stats.Pending != 1 || stats.PendingBytes != int64(n) || stats.Rejected != 4 {
			t.Fatalf("capacity stats: %+v %v", stats, err)
		}
	})
}

func TestSQLSpoolClaimExpiryCASAndLastAttempt(t *testing.T) {
	spoolDialects(t, func(t *testing.T, db *sql.DB, dialect string) {
		s := spoolFixture(t, db, dialect, func(c *SpoolConfig) { c.MaxAttempts = 2 })
		ctx := context.Background()
		now := time.Now()
		s.now = func() time.Time { return now }
		e := spoolTestEnvelope()
		if err := s.Admit(ctx, e); err != nil {
			t.Fatal(err)
		}
		first, err := s.claim(ctx, 1, 8192)
		if err != nil || len(first) != 1 {
			t.Fatalf("first claim: %v %v", first, err)
		}
		if empty, err := s.claim(ctx, 1, 8192); err != nil || len(empty) != 0 {
			t.Fatalf("active lease claimed twice: %v %v", empty, err)
		}
		now = now.Add(s.cfg.LeaseDuration + time.Millisecond)
		restarted, err := NewSQLSpool(db, s.cfg)
		if err != nil {
			t.Fatal(err)
		}
		restarted.now = s.now
		second, err := restarted.claim(ctx, 1, 8192)
		if err != nil || len(second) != 1 || second[0].id != first[0].id || second[0].token == first[0].token || second[0].attempts != 2 {
			t.Fatalf("reclaim: %+v %v", second, err)
		}
		if err := s.settle(ctx, first, 200, true); !errors.Is(err, ErrSpoolLease) {
			t.Fatalf("stale ACK accepted: %v", err)
		}
		now = now.Add(s.cfg.LeaseDuration + time.Millisecond)
		if batch, err := restarted.claim(ctx, 1, 8192); err != nil || len(batch) != 0 {
			t.Fatalf("last expired lease retried: %+v %v", batch, err)
		}
		stats, err := s.Stats(ctx)
		if err != nil || stats.Dead != 1 || stats.Retried != 1 || stats.Delivered != 0 || stats.OldestDeadAge < 2*s.cfg.LeaseDuration {
			t.Fatalf("last lease stats: %+v %v", stats, err)
		}
	})
}

func TestSQLSpoolRollbackAndAdapterErrors(t *testing.T) {
	spoolDialects(t, func(t *testing.T, db *sql.DB, dialect string) {
		s := spoolFixture(t, db, dialect, nil)
		ctx := context.Background()
		exec := func(q string) {
			t.Helper()
			if _, err := db.Exec(q); err != nil {
				t.Fatal(err)
			}
		}
		if dialect == "postgres" {
			exec(`CREATE FUNCTION reject_spool() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test abort'; END $$`)
			exec(`CREATE TRIGGER reject_spool BEFORE INSERT ON audit_spool_events FOR EACH ROW EXECUTE FUNCTION reject_spool()`)
		} else {
			exec(`CREATE TRIGGER reject_spool BEFORE INSERT ON audit_spool_events BEGIN SELECT RAISE(ABORT,'test abort'); END`)
		}
		if err := s.Admit(ctx, spoolTestEnvelope()); err == nil {
			t.Fatal("SQL insert failure ignored")
		}
		stats, err := s.Stats(ctx)
		if err != nil || stats.Pending != 0 {
			t.Fatalf("failed admission persisted rows: %+v %v", stats, err)
		}
		if dialect == "postgres" {
			exec(`DROP TRIGGER reject_spool ON audit_spool_events`)
		} else {
			exec(`DROP TRIGGER reject_spool`)
		}
		for i := 0; i < 2; i++ {
			if err := s.Admit(ctx, spoolTestEnvelope()); err != nil {
				t.Fatal(err)
			}
		}
		batch, err := s.claim(ctx, 2, 8192)
		if err != nil {
			t.Fatal(err)
		}
		original := batch[1].token
		batch[1].token = "stale"
		if err := s.settle(ctx, batch, 200, true); !errors.Is(err, ErrSpoolLease) {
			t.Fatalf("partial ACK missing lease: %v", err)
		}
		stats, err = s.Stats(ctx)
		if err != nil || stats.Pending != 2 || stats.Delivered != 0 {
			t.Fatalf("partial batch deleted: %+v %v", stats, err)
		}
		batch[1].token = original
		if err := s.settle(ctx, batch, 200, true); err != nil {
			t.Fatal(err)
		}
		var reported atomic.Int32
		adapter, err := NewSpoolSink(s, 50*time.Millisecond, func(err error) {
			if !errors.Is(err, ErrSpoolAdmission) {
				t.Errorf("unsafe adapter error: %v", err)
			}
			reported.Add(1)
		})
		if err != nil {
			t.Fatal(err)
		}
		exec("DROP TABLE audit_spool_events")
		adapter.Log(Entry{Tool: "search"})
		adapter.LogEvent(Event{Source: "workspace", Type: "write"})
		if reported.Load() != 2 {
			t.Fatalf("legacy adapter silently lost failures: %d", reported.Load())
		}
		if err := adapter.WriteEvent(Event{Source: "workspace", Type: "write"}); err == nil {
			t.Fatal("writer swallowed SQL failure")
		}
	})
}

func TestSQLSpoolBatchByteAccountingAndBackoff(t *testing.T) {
	spoolDialects(t, func(t *testing.T, db *sql.DB, dialect string) {
		s := spoolFixture(t, db, dialect, nil)
		ctx := context.Background()
		now := time.Now()
		s.now = func() time.Time { return now }
		e := spoolTestEnvelope()
		size := mustSpoolJSON(t, e)
		for i := 0; i < 3; i++ {
			next := spoolTestEnvelope()
			next.TS = e.TS
			if err := s.Admit(ctx, next); err != nil {
				t.Fatal(err)
			}
		}
		batch, err := s.claim(ctx, 10, len(`{"events":[]}`)+2*size+1)
		if err != nil || len(batch) != 2 {
			t.Fatalf("byte-bound claim: %d %v", len(batch), err)
		}
		if err := s.settle(ctx, batch, 503, false); err != nil {
			t.Fatal(err)
		}
		last, err := s.claim(ctx, 10, 8192)
		if err != nil || len(last) != 1 {
			t.Fatalf("retry ignored backoff: %d %v", len(last), err)
		}
		if err := s.settle(ctx, last, 200, true); err != nil {
			t.Fatal(err)
		}
		if early, err := s.claim(ctx, 10, 8192); err != nil || len(early) != 0 {
			t.Fatalf("backoff claimed early: %+v %v", early, err)
		}
		now = now.Add(s.cfg.RetryBase)
		retry, err := s.claim(ctx, 10, 8192)
		if err != nil || len(retry) != 2 {
			t.Fatalf("retry not scheduled: %+v %v", retry, err)
		}
		if err := s.settle(ctx, retry, 429, false); err != nil {
			t.Fatal(err)
		}
		now = now.Add(s.cfg.RetryBase)
		if early, err := s.claim(ctx, 10, 8192); err != nil || len(early) != 0 {
			t.Fatalf("exponential backoff ignored: %+v %v", early, err)
		}
		now = now.Add(s.cfg.RetryBase)
		if ready, err := s.claim(ctx, 10, 8192); err != nil || len(ready) != 2 {
			t.Fatalf("second retry missing: %+v %v", ready, err)
		}
	})
}

func TestSQLSpoolAdmissionDeadline(t *testing.T) {
	spoolDialects(t, func(t *testing.T, db *sql.DB, dialect string) {
		s := spoolFixture(t, db, dialect, nil)
		tx, err := s.begin(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		before := time.Now()
		if err := s.Admit(ctx, spoolTestEnvelope()); err == nil {
			t.Fatal("admission bypassed locked quota")
		}
		if time.Since(before) > 500*time.Millisecond {
			t.Fatal("admission exceeded context bound")
		}
		tx.Rollback()
		stats, err := s.Stats(context.Background())
		if err != nil || stats.Pending != 0 {
			t.Fatalf("canceled admission persisted: %+v %v", stats, err)
		}
	})
}

func TestSQLSpoolPreservesWorkspaceOutcomes(t *testing.T) {
	for _, outcome := range []string{"success", "failure", "denied"} {
		envelope := EnvelopeFromEvent(Event{Source: "workspace", Type: "write", Outcome: outcome}, VerifiedScope{})
		if envelope.Outcome != outcome {
			t.Fatalf("lost %s as %s", outcome, envelope.Outcome)
		}
		if err := envelope.validate(); err != nil {
			t.Fatal(err)
		}
	}
}
