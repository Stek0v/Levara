package memoryindex

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	_ "github.com/ncruces/go-sqlite3/driver"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOutboxLifecycleAndIdempotency(t *testing.T) {
	db, _ := sql.Open("sqlite3", "file:"+filepath.Join(t.TempDir(), "jobs.db"))
	defer db.Close()
	s, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tx, _ := db.Begin()
	j := Job{MemoryID: "m1", Operation: "upsert_vector", Digest: "d1", Collection: "levara"}
	j, err = s.EnqueueTx(ctx, tx, j)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	tx, _ = db.Begin()
	again, err := s.EnqueueTx(ctx, tx, Job{MemoryID: "m1", Operation: "upsert_vector", Digest: "d1"})
	if err != nil {
		t.Fatal(err)
	}
	_ = tx.Commit()
	if again.ID != j.ID {
		t.Fatalf("duplicate id=%s want %s", again.ID, j.ID)
	}
	claimed, ok, err := s.Claim(ctx)
	if err != nil || !ok || claimed.Attempts != 1 {
		t.Fatalf("claim=%+v ok=%v err=%v", claimed, ok, err)
	}
	if err = s.Finish(ctx, claimed, errors.New("embed down"), 2, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * time.Millisecond)
	claimed, ok, err = s.Claim(ctx)
	if err != nil || !ok || claimed.Attempts != 2 {
		t.Fatalf("retry=%+v ok=%v err=%v", claimed, ok, err)
	}
	if err = s.Finish(ctx, claimed, errors.New("still down"), 2, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	counts, err := s.Counts(ctx)
	if err != nil || counts[DeadLetter] != 1 {
		t.Fatalf("counts=%v err=%v", counts, err)
	}
}

func TestRecoverRunning(t *testing.T) {
	db, _ := sql.Open("sqlite3", "file:"+filepath.Join(t.TempDir(), "recover.db"))
	defer db.Close()
	s, _ := NewStore(db)
	ctx := context.Background()
	tx, _ := db.Begin()
	_, _ = s.EnqueueTx(ctx, tx, Job{MemoryID: "m", Operation: "upsert_vector", Digest: "d"})
	_ = tx.Commit()
	_, ok, _ := s.Claim(ctx)
	if !ok {
		t.Fatal("claim")
	}
	n, err := s.RecoverRunning(ctx)
	if err != nil || n != 1 {
		t.Fatalf("recover n=%d err=%v", n, err)
	}
	_, ok, err = s.Claim(ctx)
	if err != nil || !ok {
		t.Fatalf("reclaim ok=%v err=%v", ok, err)
	}
}

func openSQLiteOutboxTestDB(t testing.TB) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(t.TempDir(), "outbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

func TestOutboxReadinessAndClaimOrdering(t *testing.T) {
	testOutboxReadinessAndClaimOrdering(t, openSQLiteOutboxTestDB(t))
}

func testOutboxReadinessAndClaimOrdering(t *testing.T, db *sql.DB) {
	s, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	insert := func(id, collection, owner string, status Status, created, next string) {
		t.Helper()
		_, err := db.Exec(s.bind(`INSERT INTO memory_index_jobs
			(id,memory_id,operation,digest,collection_name,owner_id,status,created_at,updated_at,next_run_at)
			VALUES (?,?,?,?,?,?,?,?,?,?)`), id, id, "upsert_vector", id, collection, owner, status, created, created, next)
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Run("readiness scopes and statuses", func(t *testing.T) {
		insert("foreign-owner", "target", "other", Pending, "", "")
		insert("foreign-collection", "other", "owner", Pending, "", "")
		if !s.WaitReady(ctx, "target", "owner", 0) {
			t.Fatal("foreign collection/owner blocked readiness")
		}
		insert("target", "target", "owner", Pending, "", "")
		for _, status := range []Status{Pending, Running, Failed, Completed, DeadLetter} {
			if _, err := db.Exec(s.bind(`UPDATE memory_index_jobs SET status=? WHERE id='target'`), status); err != nil {
				t.Fatal(err)
			}
			want := status == Completed || status == DeadLetter
			if ready := s.WaitReady(ctx, "target", "owner", 0); ready != want {
				t.Errorf("status=%s ready=%v, want %v", status, ready, want)
			}
		}
		insert("anonymous", "target", "", Pending, "", "")
		insert("empty-collection", "", "owner", Running, "", "")
		for _, tc := range []struct {
			collection, owner string
			ready             bool
		}{
			{"target", "owner", true}, {"target", "", false}, {"", "owner", false}, {"", "", true},
		} {
			if ready := s.WaitReady(ctx, tc.collection, tc.owner, 0); ready != tc.ready {
				t.Errorf("scope=(%q,%q) ready=%v, want %v", tc.collection, tc.owner, ready, tc.ready)
			}
		}
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		if s.WaitReady(cancelled, "missing", "owner", time.Second) {
			t.Fatal("cancelled query reported ready")
		}
		waiting, stop := context.WithTimeout(ctx, 20*time.Millisecond)
		defer stop()
		if ready := s.WaitReady(waiting, "target", "", time.Minute); ready || waiting.Err() != context.DeadlineExceeded {
			t.Fatalf("pending wait: ready=%v context error=%v", ready, waiting.Err())
		}
	})
	t.Run("oldest due pending or failed only", func(t *testing.T) {
		if _, err := db.Exec(`DELETE FROM memory_index_jobs`); err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		for i, row := range []struct {
			id     string
			status Status
			next   string
		}{
			{"completed", Completed, ""}, {"dead", DeadLetter, ""}, {"running", Running, ""},
			{"future", Failed, now.Add(time.Hour).Format(time.RFC3339Nano)},
			{"first", Pending, ""}, {"second", Failed, now.Add(-time.Hour).Format(time.RFC3339Nano)},
			{"third", Pending, now.Add(-time.Minute).Format(time.RFC3339Nano)},
		} {
			insert(row.id, "target", "owner", row.status, now.Add(time.Duration(i-8)*time.Hour).Format(time.RFC3339Nano), row.next)
		}
		for _, want := range []string{"first", "second", "third"} {
			job, ok, err := s.Claim(ctx)
			if err != nil || !ok || job.ID != want || job.Status != Running || job.Attempts != 1 {
				t.Fatalf("Claim=%+v ok=%v err=%v, want running %s attempt 1", job, ok, err, want)
			}
		}
		if job, ok, err := s.Claim(ctx); err != nil || ok {
			t.Fatalf("non-due/terminal job claimed: %+v ok=%v err=%v", job, ok, err)
		}
	})
}

const outboxCountQuery = `SELECT COUNT(*) > 0 FROM memory_index_jobs WHERE collection_name=? AND owner_id=? AND status IN ('pending','running','failed')`
const outboxExistsQuery = `SELECT EXISTS(SELECT 1 FROM memory_index_jobs WHERE collection_name=? AND owner_id=? AND status IN ('pending','running','failed'))`

func seedOutboxHistory(t testing.TB, db *sql.DB) *Store {
	t.Helper()
	s, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(s.bind(`INSERT INTO memory_index_jobs
		(id,memory_id,operation,digest,collection_name,owner_id,status,created_at,updated_at)
		VALUES (?,?,'upsert_vector',?,'target','owner',?,?,'')`))
	if err != nil {
		t.Fatal(err)
	}
	defer stmt.Close()
	for i := 0; i < 20000; i++ {
		id := fmt.Sprintf("job-%05d", i)
		status := Completed
		if i >= 19000 {
			status = Pending
		}
		if _, err := stmt.Exec(id, id, id, status, time.Unix(int64(i), 0).UTC().Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ANALYZE memory_index_jobs`); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestOutboxHistoryQueryPlans(t *testing.T) {
	testOutboxHistoryQueryPlans(t, openSQLiteOutboxTestDB(t))
}

func testOutboxHistoryQueryPlans(t *testing.T, db *sql.DB) {
	s := seedOutboxHistory(t, db)
	for _, tc := range []struct {
		name, query, index string
		args               []any
	}{
		{"count", outboxCountQuery, "memory_index_jobs_ready", []any{"target", "owner"}},
		{"exists", outboxExistsQuery, "memory_index_jobs_ready", []any{"target", "owner"}},
		{"claim", `SELECT id FROM memory_index_jobs WHERE status IN ('pending','failed') AND (next_run_at='' OR next_run_at<=?) ORDER BY created_at LIMIT 8`, "memory_index_jobs_claim", []any{time.Now().UTC().Format(time.RFC3339Nano)}},
	} {
		prefix := "EXPLAIN QUERY PLAN "
		if s.postgres {
			prefix = "EXPLAIN "
		}
		rows, err := db.Query(s.bind(prefix+tc.query), tc.args...)
		if err != nil {
			t.Fatal(err)
		}
		var lines []string
		for rows.Next() {
			var detail string
			if s.postgres {
				err = rows.Scan(&detail)
			} else {
				var id, parent, unused int
				err = rows.Scan(&id, &parent, &unused, &detail)
			}
			if err != nil {
				t.Fatal(err)
			}
			lines = append(lines, detail)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		plan := strings.Join(lines, "\n")
		t.Logf("%s:\n%s", tc.name, plan)
		// EXISTS is diagnostic on PostgreSQL: pending rows at the end of a
		// long history can make its early-exit estimate prefer a slow seq scan.
		if s.postgres && tc.name == "exists" {
			continue
		}
		if !strings.Contains(plan, tc.index) {
			t.Errorf("%s plan does not use %s", tc.name, tc.index)
		}
	}
}

func BenchmarkOutboxReadinessQueries(b *testing.B) {
	benchmarkOutboxReadinessQueries(b, openSQLiteOutboxTestDB(b))
}

func benchmarkOutboxReadinessQueries(b *testing.B, db *sql.DB) {
	s := seedOutboxHistory(b, db)
	for _, tc := range []struct{ name, query string }{{"COUNT", outboxCountQuery}, {"EXISTS", outboxExistsQuery}} {
		b.Run(tc.name, func(b *testing.B) {
			query := s.bind(tc.query)
			ctx := context.Background()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				var result bool
				if err := db.QueryRowContext(ctx, query, "target", "owner").Scan(&result); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
	b.Run("COUNT_without_ready_index", func(b *testing.B) {
		if _, err := db.Exec(`DROP INDEX IF EXISTS memory_index_jobs_ready`); err != nil {
			b.Fatal(err)
		}
		query := s.bind(outboxCountQuery)
		ctx := context.Background()
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			var pending bool
			if err := db.QueryRowContext(ctx, query, "target", "owner").Scan(&pending); err != nil {
				b.Fatal(err)
			}
		}
	})
}
