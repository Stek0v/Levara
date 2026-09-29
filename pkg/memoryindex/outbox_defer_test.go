package memoryindex

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

const deferTestMaxAttempts = 5

func TestOutboxDeferParksWithoutRefund(t *testing.T) {
	testOutboxDeferParksWithoutRefund(t, openSQLiteOutboxTestDB(t))
}

func TestOutboxPostgresDeferParksWithoutRefund(t *testing.T) {
	testOutboxDeferParksWithoutRefund(t, openPostgresOutboxTestDB(t))
}

func testOutboxDeferParksWithoutRefund(t *testing.T, db *sql.DB) {
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
	cooldown := func() {
		t.Helper()
		if _, err := db.Exec(s.bind(`UPDATE memory_index_jobs SET next_run_at=? WHERE id='job'`), time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	first := claim()
	if err := s.Finish(ctx, first, errors.New("provider connection refused"), deferTestMaxAttempts, 0); err != nil {
		t.Fatal(err)
	}
	blocked := claim()
	if blocked.Attempts != 2 {
		t.Fatalf("expected attempts=2 after reclaim, got %d", blocked.Attempts)
	}
	until := time.Now().Add(time.Hour)
	if err := s.Defer(ctx, blocked, until, deferTestMaxAttempts); err != nil {
		t.Fatal(err)
	}
	job := read()
	if job.Status != Pending || job.Attempts != 2 || job.LastError != "provider connection refused" || job.NextRunAt != until.UTC().Format(time.RFC3339Nano) || job.OwnerID != "owner" || job.Collection != "collection" {
		t.Fatalf("defer lost budget/cause/scope: %+v", job)
	}
	if _, ok, err := s.Claim(ctx); err != nil || ok || s.WaitReady(ctx, "collection", "owner", 0) {
		t.Fatalf("deferred job became claimable/ready: ok=%v err=%v", ok, err)
	}
	// A stale worker whose view of the row no longer matches must be fenced out.
	if err := s.Defer(ctx, blocked, time.Now(), deferTestMaxAttempts); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != job {
		t.Fatalf("stale defer changed state: %+v", got)
	}
	cooldown()
	recovered := claim()
	if recovered.Attempts != 3 {
		t.Fatalf("deferral must not refund the attempt: got attempts=%d", recovered.Attempts)
	}
	if err := s.Defer(ctx, blocked, until, deferTestMaxAttempts); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(ctx, blocked, nil, deferTestMaxAttempts, 0); err != nil {
		t.Fatal(err)
	}
	if got := read(); got.Status != Running || got.Attempts != 3 || got.NextRunAt != recovered.NextRunAt {
		t.Fatalf("stale worker changed new claim: %+v", got)
	}
	if err := s.Finish(ctx, recovered, errors.New("still unavailable"), deferTestMaxAttempts, 0); err != nil {
		t.Fatal(err)
	}
	if got := read(); got.Status != Failed || got.Attempts != 3 || got.LastError != "still unavailable" {
		t.Fatalf("real failures under cap stay retryable: %+v", got)
	}
}

func TestOutboxDeferBudgetExhaustsToDeadLetter(t *testing.T) {
	testOutboxDeferBudgetExhaustsToDeadLetter(t, openSQLiteOutboxTestDB(t))
}

func TestOutboxPostgresDeferBudgetExhaustsToDeadLetter(t *testing.T) {
	testOutboxDeferBudgetExhaustsToDeadLetter(t, openPostgresOutboxTestDB(t))
}

func testOutboxDeferBudgetExhaustsToDeadLetter(t *testing.T, db *sql.DB) {
	s, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_, err = s.Enqueue(ctx, Job{ID: "fresh", MemoryID: "m1", Operation: "upsert_vector", Digest: "d1", OwnerID: "owner", Collection: "collection"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Enqueue(ctx, Job{ID: "scared", MemoryID: "m2", Operation: "upsert_vector", Digest: "d2", OwnerID: "owner", Collection: "collection"})
	if err != nil {
		t.Fatal(err)
	}
	// One job records a real provider failure first: its cause must survive.
	claimed, ok, err := s.Claim(ctx)
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if err := s.Finish(ctx, claimed, errors.New("connection refused"), deferTestMaxAttempts, 0); err != nil {
		t.Fatal(err)
	}
	failedID, parkedID := claimed.ID, "fresh"
	if failedID == "fresh" {
		parkedID = "scared"
	}
	deferUntilExhaustion := func() {
		t.Helper()
		for i := 0; i < deferTestMaxAttempts+2; i++ {
			if _, err := db.Exec(s.bind(`UPDATE memory_index_jobs SET next_run_at=? WHERE status IN ('pending','failed')`), time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)); err != nil {
				t.Fatal(err)
			}
			for {
				j, ok, err := s.Claim(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if !ok {
					break
				}
				if err := s.Defer(ctx, j, time.Now().Add(time.Hour), deferTestMaxAttempts); err != nil {
					t.Fatal(err)
				}
			}
			counts, err := s.Counts(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if counts[DeadLetter] == 2 {
				return
			}
		}
		t.Fatal("jobs never reached dead_letter within the deferral budget")
	}
	deferUntilExhaustion()
	readByID := func(id string) Job {
		t.Helper()
		jobs, err := s.List(ctx, "owner", 10)
		if err != nil {
			t.Fatal(err)
		}
		for _, j := range jobs {
			if j.ID == id {
				return j
			}
		}
		t.Fatalf("%s missing", id)
		return Job{}
	}
	defaulted := readByID(parkedID)
	if defaulted.Status != DeadLetter || defaulted.NextRunAt != "" || defaulted.LastError != "defer budget exhausted: embedding service unavailable" || defaulted.Attempts != deferTestMaxAttempts {
		t.Fatalf("never-failed job must dead-letter with default cause: %+v", defaulted)
	}
	failed := readByID(failedID)
	if failed.Status != DeadLetter || failed.NextRunAt != "" || failed.LastError != "connection refused" || failed.Attempts != deferTestMaxAttempts {
		t.Fatalf("preceding real failure must be retained: %+v", failed)
	}
	counts, err := s.Counts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts[Pending] != 0 || counts[Running] != 0 || counts[DeadLetter] != 2 {
		t.Fatalf("drain must terminate: %v", counts)
	}
}
