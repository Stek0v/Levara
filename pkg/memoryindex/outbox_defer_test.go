package memoryindex

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func TestOutboxDeferPreservesAttemptsAndFailure(t *testing.T) {
	testOutboxDeferPreservesAttemptsAndFailure(t, openSQLiteOutboxTestDB(t))
}

func TestOutboxPostgresDeferPreservesAttemptsAndFailure(t *testing.T) {
	testOutboxDeferPreservesAttemptsAndFailure(t, openPostgresOutboxTestDB(t))
}

func testOutboxDeferPreservesAttemptsAndFailure(t *testing.T, db *sql.DB) {
	s, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_, err = s.Enqueue(ctx, Job{ID: "job", MemoryID: "memory", Operation: "upsert_vector", Digest: "digest", OwnerID: "owner", Collection: "collection"})
	if err != nil {
		t.Fatal(err)
	}
	claim := func() Job {
		t.Helper()
		j, ok, err := s.Claim(ctx)
		if err != nil || !ok {
			t.Fatalf("claim: %v %v", ok, err)
		}
		return j
	}
	read := func() Job {
		t.Helper()
		jobs, err := s.List(ctx, "owner", 10)
		if err != nil || len(jobs) != 1 {
			t.Fatalf("list: %v %v", jobs, err)
		}
		return jobs[0]
	}
	first := claim()
	if err := s.Finish(ctx, first, errors.New("provider connection refused"), 2, 0); err != nil {
		t.Fatal(err)
	}
	blocked := claim()
	until := time.Now().Add(time.Hour)
	if err := s.Defer(ctx, blocked, until); err != nil {
		t.Fatal(err)
	}
	job := read()
	if job.Status != Pending || job.Attempts != 1 || job.LastError != "provider connection refused" || job.NextRunAt != until.UTC().Format(time.RFC3339Nano) || job.OwnerID != "owner" || job.Collection != "collection" {
		t.Fatalf("defer lost budget/cause/scope: %+v", job)
	}
	if _, ok, err := s.Claim(ctx); err != nil || ok || s.WaitReady(ctx, "collection", "owner", 0) {
		t.Fatalf("deferred job became claimable/ready: ok=%v err=%v", ok, err)
	}
	if err := s.Defer(ctx, blocked, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != job {
		t.Fatalf("repeated defer changed state: %+v", got)
	}
	// Simulate elapsed cooldown without sleeping. A stale worker must not
	// change the reclaimed job even though the refunded attempt is reused.
	if _, err := db.Exec(s.bind(`UPDATE memory_index_jobs SET next_run_at=? WHERE id='job'`), time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	recovered := claim()
	if err := s.Defer(ctx, blocked, until); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(ctx, blocked, nil, 2, 0); err != nil {
		t.Fatal(err)
	}
	if got := read(); got.Status != Running || got.Attempts != 2 || got.NextRunAt != recovered.NextRunAt {
		t.Fatalf("stale worker changed new claim: %+v", got)
	}
	if err := s.Finish(ctx, recovered, errors.New("still unavailable"), 2, 0); err != nil {
		t.Fatal(err)
	}
	if got := read(); got.Status != DeadLetter || got.Attempts != 2 || got.LastError != "still unavailable" {
		t.Fatalf("real failures must still exhaust: %+v", got)
	}
}
