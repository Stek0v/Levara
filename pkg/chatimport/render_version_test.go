package chatimport

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/ncruces/go-sqlite3/driver"
)

func openRagDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "rag.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, stmt := range append(SchemaStatements, RagSchemaStatements...) {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func seedRaw(t *testing.T, db *sql.DB, platform, session string, n int) {
	t.Helper()
	if err := StartRun(context.Background(), db, SQLiteQ, RunInfo{ID: "r1", Platform: Platform(platform), StartedAt: "2026-09-21T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if _, err := db.Exec(`INSERT INTO chat_import_messages
			(id, run_id, platform, session_id, session_title, external_id, ordinal, role, kind, model, content, source_created_at, metadata, imported_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			"id-"+session+"-"+string(rune('a'+i)), "r1", platform, session, "T",
			"x-"+string(rune('a'+i)), i, "user", "text", "m", "msg "+string(rune('a'+i)), "2026-09-21T00:00:0"+string(rune('0'+i))+"Z", "{}", "2026-09-21T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRagVersionLifecycle(t *testing.T) {
	db := openRagDB(t)
	ctx := context.Background()
	seedRaw(t, db, "codex", "s1", 2)

	if v := RagVersion(ctx, db, SQLiteQ, PlatformCodex, "s1"); v != 0 {
		t.Fatalf("fresh version = %d, want 0", v)
	}
	stale, err := StaleRagSessions(ctx, db, SQLiteQ, RenderVersion, 10)
	if err != nil || len(stale) != 1 || stale[0].SessionID != "s1" {
		t.Fatalf("stale = %+v err=%v", stale, err)
	}
	conv, err := LoadConversation(ctx, db, SQLiteQ, PlatformCodex, "s1")
	if err != nil || len(conv.Messages) != 2 || conv.Title != "T" {
		t.Fatalf("load = %+v err=%v", conv, err)
	}
	if err := RecordRagVersion(ctx, db, SQLiteQ, PlatformCodex, "s1", RenderVersion, "d1"); err != nil {
		t.Fatal(err)
	}
	if v := RagVersion(ctx, db, SQLiteQ, PlatformCodex, "s1"); v != RenderVersion {
		t.Fatalf("recorded = %d", v)
	}
	stale, _ = StaleRagSessions(ctx, db, SQLiteQ, RenderVersion, 10)
	if len(stale) != 0 {
		t.Fatalf("still stale: %+v", stale)
	}
}
