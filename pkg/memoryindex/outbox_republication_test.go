package memoryindex

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOutboxRepublication(t *testing.T) {
	for _, dialect := range []struct {
		name string
		open func(testing.TB) *sql.DB
	}{{"sqlite", openSQLiteOutboxTestDB}, {"postgres", openPostgresOutboxTestDB}} {
		t.Run(dialect.name, func(t *testing.T) {
			for _, transactional := range []bool{false, true} {
				name := "Enqueue"
				if transactional {
					name = "EnqueueTx"
				}
				t.Run(name, func(t *testing.T) {
					testOutboxRepublication(t, dialect.open(t), transactional)
				})
			}
		})
	}
}

func testOutboxRepublication(t *testing.T, db *sql.DB, transactional bool) {
	s, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	base := Job{ID: "initial", MemoryID: "memory", Operation: "upsert_vector", Collection: "levara", OwnerID: "owner", Digest: "digest", Model: "model-a", Dimension: 1024}
	enqueue := func(j Job) (Job, error) {
		if !transactional {
			return s.Enqueue(ctx, j)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return Job{}, err
		}
		defer tx.Rollback()
		got, err := s.EnqueueTx(ctx, tx, j)
		if err != nil {
			return Job{}, err
		}
		return got, tx.Commit()
	}
	mustEnqueue := func(t *testing.T, j Job) Job {
		t.Helper()
		got, err := enqueue(j)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	claim := func(t *testing.T) Job {
		t.Helper()
		got, ok, err := s.Claim(ctx)
		if err != nil || !ok {
			t.Fatalf("claim=%+v ok=%v err=%v", got, ok, err)
		}
		return got
	}
	reset := func(t *testing.T) {
		t.Helper()
		if _, err := db.Exec(`DELETE FROM memory_index_jobs`); err != nil {
			t.Fatal(err)
		}
	}
	type snapshot struct {
		Job
		created, updated string
	}
	read := func(t *testing.T, tx *sql.Tx) snapshot {
		t.Helper()
		q := s.bind(`SELECT id,memory_id,operation,collection_name,owner_id,digest,embed_model,embed_dimension,status,attempts,next_run_at,last_error,created_at,updated_at FROM memory_index_jobs WHERE memory_id=? AND operation=? AND digest=?`)
		var row *sql.Row
		if tx == nil {
			row = db.QueryRowContext(ctx, q, base.MemoryID, base.Operation, base.Digest)
		} else {
			row = tx.QueryRowContext(ctx, q, base.MemoryID, base.Operation, base.Digest)
		}
		var got snapshot
		if err := row.Scan(&got.ID, &got.MemoryID, &got.Operation, &got.Collection, &got.OwnerID, &got.Digest, &got.Model, &got.Dimension, &got.Status, &got.Attempts, &got.NextRunAt, &got.LastError, &got.created, &got.updated); err != nil {
			t.Fatal(err)
		}
		return got
	}
	complete := func(t *testing.T) Job {
		t.Helper()
		mustEnqueue(t, base)
		got := claim(t)
		if err := s.Finish(ctx, got, nil, 3, 0); err != nil {
			t.Fatal(err)
		}
		return got
	}

	t.Run("completed-repeat-stale-claims", func(t *testing.T) {
		reset(t)
		old := []Job{complete(t)}
		seen := map[string]bool{old[0].ID: true}
		for cycle := 0; cycle < 3; cycle++ {
			request := base
			// Even deliberately reusing the first claim ID must not recreate ABA.
			request.ID = old[0].ID
			reopened := mustEnqueue(t, request)
			if reopened.Status != Pending || reopened.Attempts != 0 || reopened.ID == "" || seen[reopened.ID] || reopened.NextRunAt != "" || reopened.LastError != "" {
				t.Fatalf("cycle=%d completed publication not reopened safely: %+v", cycle, reopened)
			}
			seen[reopened.ID] = true
			if s.WaitReady(ctx, base.Collection, base.OwnerID, 0) {
				t.Fatal("reopened pending job reported ready")
			}
			current := claim(t)
			if current.ID != reopened.ID || current.Attempts != 1 || current.Status != Running {
				t.Fatalf("reopened claim=%+v", current)
			}
			before := read(t, nil)
			for _, stale := range old {
				if stale.Attempts != current.Attempts || stale.NextRunAt != current.NextRunAt {
					t.Fatalf("ABA case did not actually repeat attempts/next marker: old=%+v current=%+v", stale, current)
				}
				for _, action := range []struct {
					name string
					run  func() error
				}{
					{"success", func() error { return s.Finish(ctx, stale, nil, 3, 0) }},
					{"error", func() error { return s.Finish(ctx, stale, errors.New("stale error"), 3, time.Hour) }},
					{"dead-letter", func() error { return s.Finish(ctx, stale, errors.New("stale exhaustion"), 1, 0) }},
					{"defer", func() error { return s.Defer(ctx, stale, time.Now().Add(time.Hour), 3) }},
					{"defer-exhausted", func() error { return s.Defer(ctx, stale, time.Now().Add(time.Hour), 1) }},
				} {
					if err := action.run(); err != nil {
						t.Fatal(err)
					}
					if after := read(t, nil); after != before {
						t.Fatalf("stale %s changed reclaimed row: before=%+v after=%+v", action.name, before, after)
					}
				}
			}
			if err := s.Finish(ctx, current, nil, 3, 0); err != nil {
				t.Fatal(err)
			}
			old = append(old, current)
			if !s.WaitReady(ctx, base.Collection, base.OwnerID, 0) {
				t.Fatal("completed republication did not become ready")
			}
		}
	})

	t.Run("retired-id-reused-by-another-tuple", func(t *testing.T) {
		for _, axis := range []string{"memory", "operation", "digest"} {
			t.Run(axis, func(t *testing.T) {
				reset(t)
				stale := complete(t)
				mustEnqueue(t, base)
				rotated := claim(t)
				if rotated.ID == stale.ID {
					t.Fatal("completed publication did not rotate ID")
				}
				if err := s.Finish(ctx, rotated, nil, 3, 0); err != nil {
					t.Fatal(err)
				}
				request := base
				request.ID = stale.ID
				switch axis {
				case "memory":
					request.MemoryID = "other-memory"
				case "operation":
					request.Operation = "delete_vector"
				case "digest":
					request.Digest = "other-digest"
				}
				mustEnqueue(t, request)
				current := claim(t)
				if current.ID != stale.ID || current.Attempts != stale.Attempts || current.NextRunAt != stale.NextRunAt {
					t.Fatalf("caller-ID reuse ABA not constructed: stale=%+v current=%+v", stale, current)
				}
				readCurrent := func() Job {
					jobs, err := s.List(ctx, base.OwnerID, 100)
					if err != nil {
						t.Fatal(err)
					}
					for _, job := range jobs {
						if job.ID == current.ID {
							return job
						}
					}
					t.Fatal("new claim disappeared")
					return Job{}
				}
				for _, run := range []func() error{
					func() error { return s.Finish(ctx, stale, nil, 3, 0) },
					func() error { return s.Finish(ctx, stale, errors.New("old failure"), 3, time.Hour) },
					func() error { return s.Finish(ctx, stale, errors.New("old exhaustion"), 1, 0) },
					func() error { return s.Defer(ctx, stale, time.Now().Add(time.Hour), 3) },
					func() error { return s.Defer(ctx, stale, time.Now().Add(time.Hour), 1) },
				} {
					if err := run(); err != nil {
						t.Fatal(err)
					}
					if got := readCurrent(); got != current {
						t.Fatalf("old claim clobbered caller-ID reuse across %s: current=%+v got=%+v", axis, current, got)
					}
				}
			})
		}
	})

	t.Run("claimed-namespace-must-match", func(t *testing.T) {
		reset(t)
		mustEnqueue(t, base)
		current := claim(t)
		before := read(t, nil)
		for _, axis := range []string{"owner", "collection"} {
			mismatched := current
			if axis == "owner" {
				mismatched.OwnerID = "foreign"
			} else {
				mismatched.Collection = "foreign"
			}
			for _, run := range []func() error{
				func() error { return s.Finish(ctx, mismatched, nil, 3, 0) },
				func() error { return s.Finish(ctx, mismatched, errors.New("wrong namespace"), 3, time.Hour) },
				func() error { return s.Defer(ctx, mismatched, time.Now().Add(time.Hour), 3) },
				func() error { return s.Defer(ctx, mismatched, time.Now().Add(time.Hour), 1) },
			} {
				if err := run(); err != nil {
					t.Fatal(err)
				}
				if after := read(t, nil); after != before {
					t.Fatalf("mismatched claimed %s changed publication: before=%+v after=%+v", axis, before, after)
				}
			}
		}
		if err := s.Finish(ctx, current, nil, 3, 0); err != nil {
			t.Fatal(err)
		}
		if after := read(t, nil); after.Status != Completed {
			t.Fatalf("canonical claim failed to finish: %+v", after)
		}
	})

	t.Run("pending-running-failed-dedupe", func(t *testing.T) {
		reset(t)
		first := mustEnqueue(t, base)
		duplicate := Job{ID: "ignored", MemoryID: base.MemoryID, Operation: base.Operation, Digest: base.Digest}
		check := func(t *testing.T) {
			t.Helper()
			before := read(t, nil)
			got := mustEnqueue(t, duplicate)
			if got != before.Job || got.ID != first.ID || read(t, nil) != before {
				t.Fatalf("duplicate changed/misrepresented canonical row: before=%+v got=%+v", before, got)
			}
		}
		check(t)
		current := claim(t)
		check(t)
		if err := s.Finish(ctx, current, errors.New("provider down"), 3, time.Hour); err != nil {
			t.Fatal(err)
		}
		check(t)
		if got, ok, err := s.Claim(ctx); err != nil || ok {
			t.Fatalf("duplicate erased failed backoff: job=%+v ok=%v err=%v", got, ok, err)
		}
	})

	t.Run("caller-transaction-rollback", func(t *testing.T) {
		reset(t)
		complete(t)
		before := read(t, nil)
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		reopened, err := s.EnqueueTx(ctx, tx, base)
		if err != nil || reopened.Status != Pending || reopened.ID == before.ID || reopened.Attempts != 0 {
			t.Fatalf("transaction did not reopen completed publication: %+v err=%v", reopened, err)
		}
		if inside := read(t, tx); inside.Status != Pending || inside.ID != reopened.ID {
			t.Fatalf("reopen not visible inside caller transaction: %+v", inside)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		if after := read(t, nil); after != before {
			t.Fatalf("rollback retained reopened state: before=%+v after=%+v", before, after)
		}
		if got, ok, err := s.Claim(ctx); err != nil || ok {
			t.Fatalf("rolled-back publication claimable: %+v ok=%v err=%v", got, ok, err)
		}
	})

	t.Run("completed-namespace-preserved-current-model", func(t *testing.T) {
		reset(t)
		complete(t)
		before := read(t, nil)
		for _, axis := range []string{"owner", "collection", "empty-owner", "empty-collection"} {
			request := base
			switch axis {
			case "owner":
				request.OwnerID = "foreign"
			case "collection":
				request.Collection = "foreign"
			case "empty-owner":
				request.OwnerID = ""
			case "empty-collection":
				request.Collection = ""
			}
			if got, err := enqueue(request); err == nil || !strings.Contains(err.Error(), "namespace") {
				t.Fatalf("completed %s mismatch accepted: got=%+v err=%v", axis, got, err)
			}
			if after := read(t, nil); after != before {
				t.Fatalf("completed %s mismatch changed namespace/state: %+v", axis, after)
			}
		}
		request := base
		request.Model, request.Dimension = "model-b", 0
		got := mustEnqueue(t, request)
		if got.ID == before.ID || got.Status != Pending || got.Collection != base.Collection || got.OwnerID != base.OwnerID || got.Model != "model-b" || got.Dimension != 0 || read(t, nil).Job != got {
			t.Fatalf("new authorized model/unspecified dimension not persisted: %+v", got)
		}
		current := claim(t)
		if current.Model != got.Model || current.Dimension != got.Dimension || current.Collection != base.Collection || current.OwnerID != base.OwnerID {
			t.Fatalf("worker received wrong publication metadata: %+v", current)
		}
	})

	t.Run("retry-budget-per-publication", func(t *testing.T) {
		reset(t)
		mustEnqueue(t, base)
		for attempt := 1; attempt <= 2; attempt++ {
			current := claim(t)
			if current.Attempts != attempt {
				t.Fatalf("attempt=%+v", current)
			}
			if err := s.Finish(ctx, current, errors.New("provider down"), 2, time.Hour); err != nil {
				t.Fatal(err)
			}
			before := read(t, nil)
			if got := mustEnqueue(t, base); got != before.Job || read(t, nil) != before {
				t.Fatalf("duplicate reset retry budget: before=%+v got=%+v", before, got)
			}
			if ok, err := s.Retry(ctx, current.ID, base.OwnerID); err != nil || !ok {
				t.Fatalf("retry ok=%v err=%v", ok, err)
			}
		}
		current := claim(t)
		if current.Attempts != 3 {
			t.Fatalf("manual retry refunded attempts: %+v", current)
		}
		if err := s.Finish(ctx, current, nil, 2, 0); err != nil {
			t.Fatal(err)
		}
		if got := mustEnqueue(t, base); got.Attempts != 0 || got.Status != Pending {
			t.Fatalf("new publication did not reset budget: %+v", got)
		}
		current = claim(t)
		if current.Attempts != 1 {
			t.Fatalf("new publication claim=%+v", current)
		}
		if err := s.Finish(ctx, current, errors.New("new failure"), 2, time.Hour); err != nil {
			t.Fatal(err)
		}
		if got := read(t, nil); got.Status != Failed || got.Attempts != 1 || got.LastError != "new failure" || got.NextRunAt == "" {
			t.Fatalf("new budget exhausted prematurely: %+v", got)
		}
	})

	t.Run("empty-id-and-initial-explicit-id", func(t *testing.T) {
		reset(t)
		first := mustEnqueue(t, base)
		if first.ID != base.ID {
			t.Fatalf("initial explicit id changed: %+v", first)
		}
		seen := map[string]bool{first.ID: true}
		for i := 0; i < 2; i++ {
			request := base
			request.ID, request.MemoryID = "", fmt.Sprintf("empty-id-%d", i)
			got := mustEnqueue(t, request)
			if got.ID == "" || seen[got.ID] || got.Status != Pending {
				t.Fatalf("empty caller ID not assigned unique job ID: %+v", got)
			}
			seen[got.ID] = true
		}
	})

	t.Run("concurrent-completed-enqueue", func(t *testing.T) {
		reset(t)
		if s.postgres {
			db.SetMaxOpenConns(4)
			defer db.SetMaxOpenConns(1)
		}
		old := complete(t)
		start := make(chan struct{})
		var wg sync.WaitGroup
		type outcome struct {
			job Job
			err error
		}
		results := make(chan outcome, 8)
		for i := 0; i < cap(results); i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				request := base
				if i%2 != 0 {
					request.ID = fmt.Sprintf("caller-%d", i)
				}
				got, err := enqueue(request)
				results <- outcome{got, err}
			}(i)
		}
		close(start)
		wg.Wait()
		close(results)
		canonical := read(t, nil)
		for result := range results {
			if result.err != nil || result.job != canonical.Job || result.job.ID == old.ID || result.job.Status != Pending {
				t.Fatalf("concurrent publication not canonical: got=%+v err=%v canonical=%+v", result.job, result.err, canonical)
			}
		}
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM memory_index_jobs`).Scan(&count); err != nil || count != 1 {
			t.Fatalf("concurrent duplicate rows=%d err=%v", count, err)
		}
		current := claim(t)
		if current.ID != canonical.ID || current.Attempts != 1 {
			t.Fatalf("concurrent publication claim=%+v", current)
		}
		if extra, ok, err := s.Claim(ctx); err != nil || ok {
			t.Fatalf("duplicate claimable: %+v ok=%v err=%v", extra, ok, err)
		}
	})
}
