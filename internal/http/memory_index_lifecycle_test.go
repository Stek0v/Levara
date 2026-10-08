package http

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stek0v/levara/pkg/embed"
	"github.com/stek0v/levara/pkg/memoryindex"
)

func TestMemoryIndexDelayedDeletePreservesRestoredMemory(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		cfg, cleanup := newWorkspaceTestConfig(t)
		defer cleanup()
		cfg.DB = f.db
		f.db.SetMaxOpenConns(1)
		for _, tc := range []struct {
			name, change  string
			keep, failure bool
		}{
			{"restored", `UPDATE memories SET superseded_by='' WHERE id=$1`, true, false},
			{"retired", "", false, false},
			{"expired", "", false, false},
			{"missing", `DELETE FROM memories WHERE id=$1`, false, false},
			{"foreign_owner", "", true, false},
			{"sibling_collection", "", true, false},
			{"cancelled", "", true, true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				fixture := *f
				fixture.t = t
				f := &fixture
				id := "delete_" + tc.name
				owner, collection := "peer", "main"
				if tc.name == "foreign_owner" {
					owner = "foreign"
				}
				if tc.name == "sibling_collection" {
					collection = "other"
				}
				// A stale job carries its original scope; physical memory IDs never move scopes.
				f.exec(`INSERT INTO memories(id,key,value,type,owner_id,collection_name,superseded_by) VALUES($1,$1,'value','user',$2,$3,'abstract')`, id, owner, collection)
				if tc.name == "expired" {
					f.exec(`UPDATE memories SET superseded_by='',valid_until='2026-10-08T00:00:00Z' WHERE id=$1`, id)
				}
				if tc.change != "" {
					f.exec(tc.change, id)
				}
				if err := cfg.Collections.Insert("_memories_main", id, []float32{1, 0}, nil); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				if tc.name == "cancelled" {
					cancel()
				}
				err := executeMemoryIndexJob(ctx, cfg, memoryindex.Job{MemoryID: id, Operation: "delete_vector", Collection: "main", OwnerID: "peer"})
				if (err != nil) != tc.failure {
					t.Fatalf("error=%v want failure=%t", err, tc.failure)
				}
				if got := cfg.Collections.HasRecord("_memories_main", id); got != tc.keep {
					t.Fatalf("vector present=%t want %t after %s", got, tc.keep, tc.name)
				}
			})
		}
		t.Run("sql_failure", func(t *testing.T) {
			fixture := *f
			fixture.t = t
			f := &fixture
			id := "sql_failure"
			if err := cfg.Collections.Insert("_memories_main", id, []float32{1, 0}, nil); err != nil {
				t.Fatal(err)
			}
			f.exec(`ALTER TABLE memories RENAME TO memories_unavailable`)
			defer f.exec(`ALTER TABLE memories_unavailable RENAME TO memories`)
			err := executeMemoryIndexJob(context.Background(), cfg, memoryindex.Job{MemoryID: id, Operation: "delete_vector", Collection: "main", OwnerID: "peer"})
			if err == nil || !cfg.Collections.HasRecord("_memories_main", id) {
				t.Fatalf("failed authoritative check changed vector: err=%v", err)
			}
		})
	})
}

func TestMemoryIndexChecksSourceAfterEmbed(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		for _, tc := range []struct {
			name, change string
			keep         bool
		}{
			{"type", "UPDATE memories SET type='semantic' WHERE id=$1", true},
			{"owner", "", false},
			{"collection", "", false},
			{"key", "UPDATE memories SET key='changed' WHERE id=$1", false},
			{"value", "UPDATE memories SET value='changed' WHERE id=$1", false},
			{"retired", "UPDATE memories SET superseded_by='abstract' WHERE id=$1", false},
			{"expired", "UPDATE memories SET valid_until='2026-10-08T00:00:00Z' WHERE id=$1", false},
			{"rollback", "", true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				fixture := *f
				fixture.t = t
				f := &fixture
				cfg, cleanup := newWorkspaceTestConfig(t)
				defer cleanup()
				cfg.DB = f.db
				id := "embed_" + tc.name
				f.exec(`INSERT INTO memories(id,key,value,type,owner_id,collection_name) VALUES($1,'source','value','user','peer','main')`, id)
				defer f.exec(`DELETE FROM memories WHERE id=$1`, id)
				entered, resume := make(chan struct{}), make(chan struct{})
				var once, release sync.Once
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					once.Do(func() { close(entered) })
					<-resume
					_, _ = io.WriteString(w, `{"data":[{"index":0,"embedding":[1,0]}]}`)
				}))
				defer server.Close()
				defer release.Do(func() { close(resume) })
				cfg.EmbedClient = embed.NewClient(server.URL, "test", 1, 1)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				done := make(chan error, 1)
				job := memoryindex.Job{MemoryID: id, Operation: "upsert_vector", OwnerID: "peer", Collection: "main", Digest: fmt.Sprintf("%x", sha256.Sum256([]byte("source\x00value")))}
				go func() { done <- executeMemoryIndexJob(ctx, cfg, job) }()
				select {
				case <-entered:
				case <-ctx.Done():
					t.Fatal("embedding did not start")
				}
				replacementID, replacementOwner, replacementCollection := "", "peer", "main"
				if tc.name == "owner" || tc.name == "collection" {
					// Scope changes create a fresh incarnation, while the original job remains in flight.
					f.exec(`DELETE FROM memories WHERE id=$1`, id)
					replacementID = id + "_replacement"
					if tc.name == "owner" {
						replacementOwner = "foreign"
					} else {
						replacementCollection = "other"
					}
					f.exec(`INSERT INTO memories(id,key,value,type,owner_id,collection_name) VALUES($1,'source','value','user',$2,$3)`, replacementID, replacementOwner, replacementCollection)
					defer f.exec(`DELETE FROM memories WHERE id=$1`, replacementID)
					if err := cfg.Collections.Insert(memoryCollectionNameHTTP(replacementCollection), replacementID, []float32{0, 1}, nil); err != nil {
						t.Fatal(err)
					}
				} else if tc.change != "" {
					f.exec(tc.change, id)
				} else {
					tx, err := f.db.BeginTx(ctx, nil)
					if err != nil {
						t.Fatal(err)
					}
					if _, err = tx.ExecContext(ctx, Q(`UPDATE memories SET superseded_by='abstract' WHERE id=$1`), id); err != nil {
						_ = tx.Rollback()
						t.Fatal(err)
					}
					if err = tx.Rollback(); err != nil {
						t.Fatal(err)
					}
				}
				release.Do(func() { close(resume) })
				select {
				case err := <-done:
					if err != nil {
						t.Fatal(err)
					}
				case <-ctx.Done():
					t.Fatal("index job did not return")
				}
				if got := cfg.Collections.HasRecord("_memories_main", id); got != tc.keep {
					t.Fatalf("late vector present=%t want %t after %s", got, tc.keep, tc.name)
				}
				if replacementID != "" {
					var owner, collection, key, value string
					if err := f.db.QueryRow(Q(`SELECT owner_id,collection_name,key,value FROM memories WHERE id=$1`), replacementID).Scan(&owner, &collection, &key, &value); err != nil {
						t.Fatal(err)
					}
					if owner != replacementOwner || collection != replacementCollection || key != "source" || value != "value" || !cfg.Collections.HasRecord(memoryCollectionNameHTTP(replacementCollection), replacementID) {
						t.Fatal("stale index job changed independently scoped replacement")
					}
					var terminalState string
					if err := f.db.QueryRow(Q(`SELECT state FROM memory_sync_incarnations WHERE memory_id=$1`), id).Scan(&terminalState); err != nil || terminalState != "deleted" {
						t.Fatalf("original incarnation not terminal: state=%q err=%v", terminalState, err)
					}
				}
			})
		}
	})
}

func independentMemoryIndexDB(t *testing.T, f *documentHTTPFixture) *sql.DB {
	t.Helper()
	var db *sql.DB
	var err error
	if GetDBProvider() == DBSQLite {
		var mode string
		if err = f.db.QueryRow("PRAGMA journal_mode=WAL").Scan(&mode); err != nil || mode != "wal" {
			t.Fatalf("WAL mode=%q err=%v", mode, err)
		}
		var seq int
		var name, path string
		if err = f.db.QueryRow("PRAGMA database_list").Scan(&seq, &name, &path); err != nil {
			t.Fatal(err)
		}
		db, err = sql.Open("sqlite3", "file:"+path+"?_pragma=busy_timeout(10000)")
	} else {
		var schema string
		if err = f.db.QueryRow("SELECT current_schema()").Scan(&schema); err != nil {
			t.Fatal(err)
		}
		config, parseErr := pgx.ParseConfig(os.Getenv("LEVARA_TEST_POSTGRES_DSN"))
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		config.RuntimeParams["search_path"] = schema
		db = stdlib.OpenDB(*config)
	}
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err = db.Ping(); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestMemoryIndexEffectFenceSurvivesObserverCancellation(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		writerDB := independentMemoryIndexDB(t, f)
		f.exec(`INSERT INTO memories(id,key,value,owner_id,collection_name) VALUES('fenced','key','before','peer','main')`)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		tx, release, err := beginMemoryVectorEffect(ctx, f.db, "fenced")
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		var value string
		if err = tx.QueryRowContext(ctx, "SELECT value FROM memories WHERE id='fenced'").Scan(&value); err != nil {
			t.Fatal(err)
		}
		writerCtx, writerCancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer writerCancel()
		started, done := make(chan struct{}), make(chan error, 1)
		go func() {
			close(started)
			_, err := writerDB.ExecContext(writerCtx, "UPDATE memories SET value='after' WHERE id='fenced'")
			done <- err
		}()
		<-started
		cancel()
		select {
		case err := <-done:
			t.Fatalf("writer passed before actual effect release: %v", err)
		case <-time.After(150 * time.Millisecond):
		}
		release()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-writerCtx.Done():
			t.Fatal("writer did not pass after effect returned")
		}
		if err = f.db.QueryRow("SELECT value FROM memories WHERE id='fenced'").Scan(&value); err != nil || value != "after" {
			t.Fatalf("released writer value=%q err=%v", value, err)
		}
	})
}

func TestMemoryIndexEffectAcquisitionDeadline(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		cfg, cleanup := newWorkspaceTestConfig(t)
		defer cleanup()
		cfg.DB = f.db
		f.db.SetMaxOpenConns(1)
		if err := cfg.Collections.Insert("_memories_main", "busy", []float32{1, 0}, nil); err != nil {
			t.Fatal(err)
		}
		conn, err := f.db.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
		defer cancel()
		err = executeMemoryIndexJob(ctx, cfg, memoryindex.Job{MemoryID: "busy", Operation: "delete_vector", Collection: "main", OwnerID: "peer"})
		if !errors.Is(err, context.DeadlineExceeded) || !cfg.Collections.HasRecord("_memories_main", "busy") {
			t.Fatalf("bounded acquisition changed vector: %v", err)
		}
	})
}

func TestMemoryIndexEffectFenceScope(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		writer := independentMemoryIndexDB(t, f)
		for _, absent := range []bool{false, true} {
			t.Run(fmt.Sprintf("absent_%t", absent), func(t *testing.T) {
				id := fmt.Sprintf("scope_%t", absent)
				if !absent {
					f.exec(`INSERT INTO memories(id,key,value,owner_id,collection_name) VALUES($1,$1,'before','peer','main')`, id)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				tx, release, err := beginMemoryVectorEffect(ctx, f.db, id)
				if err != nil {
					t.Fatal(err)
				}
				defer release()
				var count int
				if err = tx.QueryRowContext(ctx, Q(`SELECT COUNT(*) FROM memories WHERE id=$1`), id).Scan(&count); err != nil || count != map[bool]int{true: 0, false: 1}[absent] {
					t.Fatalf("fenced source count=%d err=%v", count, err)
				}
				if !absent && GetDBProvider() == DBPostgres {
					otherCtx, otherCancel := context.WithTimeout(ctx, time.Second)
					defer otherCancel()
					_, err = writer.ExecContext(otherCtx, Q(`INSERT INTO memories(id,key,value,owner_id,collection_name) VALUES($1,$1,'unrelated','peer','main')`), id+"_other")
					if err != nil {
						t.Fatalf("unrelated save blocked by row fence: %v", err)
					}
				}
				done := make(chan error, 1)
				go func() {
					query := `UPDATE memories SET value='after' WHERE id=$1`
					args := []any{id}
					if absent {
						query = `INSERT INTO memories(id,key,value,owner_id,collection_name) VALUES($1,$2,'after','peer','main')`
						args = append(args, id)
					}
					_, err := writer.ExecContext(ctx, Q(query), args...)
					done <- err
				}()
				select {
				case err := <-done:
					t.Fatalf("same-ID mutation passed fence: %v", err)
				case <-time.After(150 * time.Millisecond):
				}
				release()
				select {
				case err := <-done:
					if err != nil {
						t.Fatal(err)
					}
				case <-ctx.Done():
					t.Fatal("writer did not resume")
				}
				var value string
				if err = f.db.QueryRowContext(ctx, Q(`SELECT value FROM memories WHERE id=$1`), id).Scan(&value); err != nil || value != "after" {
					t.Fatalf("released source value=%q err=%v", value, err)
				}
			})
		}
	})
}
