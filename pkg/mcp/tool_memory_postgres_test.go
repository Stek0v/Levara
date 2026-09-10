package mcp

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stek0v/levara/pkg/memoryindex"
)

type postgresMemoryDeps struct{ *fakeDeps }

func (d *postgresMemoryDeps) Q(query string) string { return query }

func openPostgresMemoryTestDB(t *testing.T) *sql.DB {
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
	schema := fmt.Sprintf("mcp_memory_test_%d", time.Now().UnixNano())
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

func TestToolMemoryPostgresPinUnpin(t *testing.T) {
	db := openPostgresMemoryTestDB(t)
	if _, err := db.Exec(`CREATE TABLE memories (
		id TEXT PRIMARY KEY,
		key TEXT NOT NULL,
		owner_id TEXT NOT NULL DEFAULT '',
		is_pinned BOOLEAN NOT NULL DEFAULT FALSE,
		pin_priority INTEGER NOT NULL DEFAULT 0,
		updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`); err != nil {
		t.Fatalf("create memories: %v", err)
	}
	deps := &postgresMemoryDeps{fakeDeps: &fakeDeps{db: db}}

	t.Run("pin", func(t *testing.T) {
		if _, err := db.Exec(`INSERT INTO memories (id, key) VALUES ('pin', 'pin-me')`); err != nil {
			t.Fatalf("seed pin row: %v", err)
		}
		got := ToolPinMemory(context.Background(), deps, map[string]any{
			"key": "pin-me", "priority": float64(8),
		})
		if got.IsError {
			t.Fatalf("pin_memory failed: %s", got.Content[0].Text)
		}
		var pinned bool
		var priority int
		if err := db.QueryRow(`SELECT is_pinned, pin_priority FROM memories WHERE key = 'pin-me'`).Scan(&pinned, &priority); err != nil {
			t.Fatalf("read pinned row: %v", err)
		}
		if !pinned || priority != 8 {
			t.Fatalf("pin state=(%v,%d), want (true,8)", pinned, priority)
		}
	})

	t.Run("unpin", func(t *testing.T) {
		if _, err := db.Exec(`INSERT INTO memories (id, key, is_pinned, pin_priority) VALUES ('unpin', 'unpin-me', TRUE, 8)`); err != nil {
			t.Fatalf("seed unpin row: %v", err)
		}
		got := ToolUnpinMemory(context.Background(), deps, map[string]any{"key": "unpin-me"})
		if got.IsError {
			t.Fatalf("unpin_memory failed: %s", got.Content[0].Text)
		}
		var pinned bool
		var priority int
		if err := db.QueryRow(`SELECT is_pinned, pin_priority FROM memories WHERE key = 'unpin-me'`).Scan(&pinned, &priority); err != nil {
			t.Fatalf("read unpinned row: %v", err)
		}
		if pinned || priority != 0 {
			t.Fatalf("unpin state=(%v,%d), want (false,0)", pinned, priority)
		}
	})
}

func TestToolDeleteMemoryPostgresExactIDAndAmbiguousLegacy(t *testing.T) {
	db := openPostgresMemoryTestDB(t)
	if _, err := db.Exec(`CREATE TABLE memories (
		id TEXT PRIMARY KEY, key TEXT NOT NULL, value TEXT NOT NULL DEFAULT '', type TEXT NOT NULL DEFAULT '',
		owner_id TEXT NOT NULL DEFAULT '', collection_name TEXT NOT NULL DEFAULT '', room TEXT NOT NULL DEFAULT '', hall TEXT NOT NULL DEFAULT '',
		is_pinned BOOLEAN NOT NULL DEFAULT FALSE, pin_priority INTEGER NOT NULL DEFAULT 0,
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), superseded_by TEXT NOT NULL DEFAULT '',
		UNIQUE(key,owner_id,collection_name)
	)`); err != nil {
		t.Fatal(err)
	}
	outbox, err := memoryindex.NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	deps := &postgresMemoryDeps{fakeDeps: &fakeDeps{db: db, hasColls: true, memoryIndexOutbox: outbox}}
	for _, stmt := range []string{
		`INSERT INTO memories(id,key,value,owner_id,collection_name) VALUES('selected','dup','one','alice','one')`,
		`INSERT INTO memories(id,key,value,owner_id,collection_name) VALUES('sibling','dup','two','alice','two')`,
		`INSERT INTO memories(id,key,value,owner_id,collection_name) VALUES('shared','dup','shared','','one')`,
		`INSERT INTO memories(id,key,value,owner_id,collection_name) VALUES('foreign','dup','foreign','bob','one')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.WithValue(context.Background(), UserIDKey, "alice")
	if got := ToolDeleteMemory(ctx, deps, map[string]any{"key": "dup"}); !got.IsError || !strings.Contains(got.Content[0].Text, "ambiguous") {
		t.Fatalf("legacy ambiguity result=%+v", got)
	}
	var before int
	if err := db.QueryRow(`SELECT COUNT(*) FROM memories`).Scan(&before); err != nil || before != 4 {
		t.Fatalf("ambiguous delete changed rows: count=%d err=%v", before, err)
	}
	if got := ToolDeleteMemory(ctx, deps, map[string]any{"memory_id": "selected"}); got.IsError {
		t.Fatalf("ID delete failed: %+v", got)
	}
	var remaining, jobs int
	if err := db.QueryRow(`SELECT COUNT(*) FROM memories WHERE id IN ('sibling','shared','foreign')`).Scan(&remaining); err != nil || remaining != 3 {
		t.Fatalf("control rows=%d err=%v", remaining, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM memory_index_jobs WHERE memory_id='selected' AND operation='delete_vector' AND collection_name='one' AND owner_id='alice' AND digest='delete:selected'`).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatalf("exact vector jobs=%d err=%v", jobs, err)
	}
}
