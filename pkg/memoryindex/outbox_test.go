package memoryindex

import (
	"context"
	"database/sql"
	"errors"
	_ "github.com/ncruces/go-sqlite3/driver"
	"path/filepath"
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
