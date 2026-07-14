package memoryindex

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func openPostgresOutboxTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("LEVARA_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("LEVARA_TEST_POSTGRES_DSN is not set")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	schema := fmt.Sprintf("memoryindex_test_%d", time.Now().UnixNano())
	schema = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			return r
		}
		return '_'
	}, schema)
	if _, err := db.Exec(`CREATE SCHEMA ` + schema); err != nil {
		db.Close()
		t.Fatalf("create schema: %v", err)
	}
	if _, err := db.Exec(`SET search_path TO ` + schema); err != nil {
		db.Close()
		t.Fatalf("set search_path: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`)
		_ = db.Close()
	})
	return db
}

func TestOutboxPostgresLifecycleIdempotencyAndOwnerRetry(t *testing.T) {
	db := openPostgresOutboxTestDB(t)
	s, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if !s.postgres {
		t.Fatalf("postgres driver not detected: %T", db.Driver())
	}
	ctx := context.Background()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.EnqueueTx(ctx, tx, Job{MemoryID: "m1", Operation: "upsert_vector", Digest: "d1", Collection: "levara", OwnerID: "owner-a", Model: "embed", Dimension: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.EnqueueTx(ctx, tx, Job{MemoryID: "m1", Operation: "upsert_vector", Digest: "d1", Collection: "levara", OwnerID: "owner-a"})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if again.ID != first.ID {
		t.Fatalf("duplicate id=%s want %s", again.ID, first.ID)
	}

	claimed, ok, err := s.Claim(ctx)
	if err != nil || !ok || claimed.Attempts != 1 || claimed.OwnerID != "owner-a" || claimed.Collection != "levara" {
		t.Fatalf("claim=%+v ok=%v err=%v", claimed, ok, err)
	}
	if err := s.Finish(ctx, claimed, errors.New("embed down"), 2, 0); err != nil {
		t.Fatal(err)
	}

	if ok, err := s.Retry(ctx, claimed.ID, "owner-b"); err != nil || ok {
		t.Fatalf("foreign retry ok=%v err=%v", ok, err)
	}
	if ok, err := s.Retry(ctx, claimed.ID, "owner-a"); err != nil || !ok {
		t.Fatalf("owner retry ok=%v err=%v", ok, err)
	}

	reclaimed, ok, err := s.Claim(ctx)
	if err != nil || !ok || reclaimed.Attempts != 2 {
		t.Fatalf("reclaim=%+v ok=%v err=%v", reclaimed, ok, err)
	}
	if err := s.Finish(ctx, reclaimed, errors.New("still down"), 2, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	counts, err := s.Counts(ctx)
	if err != nil || counts[DeadLetter] != 1 {
		t.Fatalf("counts=%v err=%v", counts, err)
	}

	jobs, err := s.List(ctx, "owner-a", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].ID != first.ID || jobs[0].Status != DeadLetter {
		t.Fatalf("jobs=%+v", jobs)
	}
}

func TestOutboxPostgresRecoverRunningAndWaitReady(t *testing.T) {
	db := openPostgresOutboxTestDB(t)
	s, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnqueueTx(ctx, tx, Job{MemoryID: "m2", Operation: "delete_vector", Digest: "d2", Collection: "levara", OwnerID: "owner-a"}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := s.Claim(ctx)
	if err != nil || !ok {
		t.Fatalf("claim=%+v ok=%v err=%v", claimed, ok, err)
	}
	if ready := s.WaitReady(ctx, "levara", "owner-a", 5*time.Millisecond); ready {
		t.Fatal("WaitReady returned true while job is running")
	}
	n, err := s.RecoverRunning(ctx)
	if err != nil || n != 1 {
		t.Fatalf("recover n=%d err=%v", n, err)
	}
	reclaimed, ok, err := s.Claim(ctx)
	if err != nil || !ok || reclaimed.ID != claimed.ID {
		t.Fatalf("reclaim=%+v ok=%v err=%v", reclaimed, ok, err)
	}
	if err := s.Finish(ctx, reclaimed, nil, 2, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if ready := s.WaitReady(ctx, "levara", "owner-a", time.Second); !ready {
		t.Fatal("WaitReady returned false after completion")
	}
}

func TestOutboxPostgresBind(t *testing.T) {
	db := openPostgresOutboxTestDB(t)
	s, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.bind("SELECT ?::text, ?::int"); got != "SELECT $1::text, $2::int" {
		t.Fatalf("bind=%q", got)
	}
}
