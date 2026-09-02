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
