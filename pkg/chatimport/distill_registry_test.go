package chatimport

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/ncruces/go-sqlite3/driver"
)

func openDistillDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "distill.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, stmt := range append(append(SchemaStatements, RagSchemaStatements...), DistillSchemaStatements...) {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func seedDistillSession(t *testing.T, db *sql.DB, platform, session string, userTurns, asstTurns int) {
	t.Helper()
	_ = StartRun(context.Background(), db, SQLiteQ, RunInfo{ID: "r-" + session, Platform: Platform(platform), StartedAt: "2026-09-21T00:00:00Z"})
	n := userTurns + asstTurns
	for i := 0; i < n; i++ {
		role := "assistant"
		if i < userTurns {
			role = "user"
		}
		if _, err := db.Exec(`INSERT INTO chat_import_messages
			(id, run_id, platform, session_id, session_title, external_id, ordinal, role, kind, model, content, source_created_at, metadata, imported_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			session+"-m"+string(rune('a'+i)), "r-"+session, platform, session, "T-"+session,
			"x-"+string(rune('a'+i)), i, role, "text", "m", "content "+string(rune('a'+i)),
			"2026-09-21T00:00:00Z", "{}", "2026-09-21T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDistillCandidatesAndOutcome(t *testing.T) {
	db := openDistillDB(t)
	ctx := context.Background()
	seedDistillSession(t, db, "codex", "rich", 3, 5)  // 8 msgs, 3 user turns
	seedDistillSession(t, db, "codex", "thin", 1, 2)  // 3 msgs, 1 user turn — below thresholds
	seedDistillSession(t, db, "codex", "small", 1, 1) // 2 msgs — below min

	cands, err := DistillCandidates(ctx, db, SQLiteQ, "decision", 6, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].SessionID != "rich" {
		t.Fatalf("candidates = %+v, want only rich", cands)
	}
	if cands[0].Messages != 8 {
		t.Fatalf("messages = %d", cands[0].Messages)
	}

	if err := RecordDistillOutcome(ctx, db, SQLiteQ, PlatformCodex, "rich", "decision", "ok", []string{"k1", "k2"}, ""); err != nil {
		t.Fatal(err)
	}
	cands, _ = DistillCandidates(ctx, db, SQLiteQ, "decision", 6, 10)
	if len(cands) != 0 {
		t.Fatalf("still candidate after ok: %+v", cands)
	}

	// fresh failure backs off (a hard-to-distill session must not crowd out
	// other candidates every tick); an aged failure retries.
	_ = RecordDistillOutcome(ctx, db, SQLiteQ, PlatformCodex, "rich", "discovery", "failed", nil, "llm timeout")
	cands, _ = DistillCandidates(ctx, db, SQLiteQ, "discovery", 6, 10)
	if len(cands) != 0 {
		t.Fatalf("recent failure must back off: %+v", cands)
	}
	if _, err := db.Exec(`UPDATE chat_import_distill SET distilled_at = '2020-01-01T00:00:00Z' WHERE hall = 'discovery'`); err != nil {
		t.Fatal(err)
	}
	cands, _ = DistillCandidates(ctx, db, SQLiteQ, "discovery", 6, 10)
	if len(cands) != 1 {
		t.Fatalf("aged failure should retry: %+v", cands)
	}

	stats, _ := DistillStatsFor(ctx, db, SQLiteQ, "decision")
	if stats.OK != 1 {
		t.Fatalf("stats = %+v", stats)
	}
}
