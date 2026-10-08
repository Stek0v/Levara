package chatimport

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func TestImportScopeLocalJanitors(t *testing.T) {
	importScopeDialects(t, func(t *testing.T, db *sql.DB, q Q) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		for _, ensure := range []func(context.Context, *sql.DB, Q) error{EnsureSchema, EnsureRagSchema, EnsureDistillSchema} {
			if err := ensure(ctx, db, q); err != nil {
				t.Fatal(err)
			}
		}
		var private []ChatIdentity
		for _, scope := range []ImportScope{{OwnerID: "alice", TenantID: "a"}, {OwnerID: "bob", TenantID: "a"}, {TrustedLocal: true}} {
			run, err := StartScopedRun(ctx, db, q, scope, RunInfo{ID: "same-run", Platform: PlatformCodex, StartedAt: "2026-10-06T00:00:00Z"})
			if err != nil {
				t.Fatal(err)
			}
			conv := &Conversation{Platform: PlatformCodex, SessionID: "same-source", Title: "local versus personal"}
			for i, role := range []string{"user", "assistant", "user", "assistant", "user", "assistant"} {
				conv.Messages = append(conv.Messages, Message{ExternalID: string(rune('a' + i)), Ordinal: i, Role: role, Kind: KindText, Content: "keep source isolated"})
			}
			chat, count, err := InsertScopedConversation(ctx, db, q, scope, run, conv, nil, time.Now())
			if err != nil || count != 6 {
				t.Fatalf("seed count=%d err=%v", count, err)
			}
			if scope.OwnerID != "" {
				private = append(private, chat)
			}
		}
		// A project association must not turn a scoped chat into daemon-local data.
		if _, err := db.ExecContext(ctx, q("UPDATE chat_import_sessions SET project_id=$1 WHERE id=$2 AND platform=$3"), "project-a", private[1].ID, "codex"); err != nil {
			t.Fatal(err)
		}
		for _, chat := range private {
			if err := RecordRagVersion(ctx, db, q, chat.Platform, chat.ID, 0, "private-derived-item"); err != nil {
				t.Fatal(err)
			}
			if err := RecordDistillOutcome(ctx, db, q, chat.Platform, chat.ID, "decision", "ok", []string{"private-key"}, ""); err != nil {
				t.Fatal(err)
			}
			if conv, err := LoadConversation(ctx, db, q, chat.Platform, chat.ID); err == nil || conv != nil {
				t.Errorf("unscoped loader returned protected source %s: %+v/%v", chat.ID, conv, err)
			}
		}
		stale, err := StaleRagSessions(ctx, db, q, RenderVersion, 100)
		if err != nil || len(stale) != 1 || stale[0].SessionID != "same-source" {
			t.Fatalf("stale=%+v err=%v", stale, err)
		}
		candidates, err := DistillCandidates(ctx, db, q, "decision", 6, 100)
		if err != nil || len(candidates) != 1 || candidates[0].SessionID != "same-source" {
			t.Fatalf("candidates=%+v err=%v", candidates, err)
		}
		stats, err := DistillStatsFor(ctx, db, q, "decision")
		if err != nil || stats != (DistillStats{}) {
			t.Fatalf("private stats leaked: %+v err=%v", stats, err)
		}
		// Existing unclaimed local rows retain compatibility after migration.
		if _, err := db.ExecContext(ctx, q("DELETE FROM chat_import_sessions WHERE id=$1 AND platform=$2"), "same-source", "codex"); err != nil {
			t.Fatal(err)
		}
		local, err := LoadConversation(ctx, db, q, PlatformCodex, "same-source")
		if err != nil || len(local.Messages) != 6 {
			t.Fatalf("local legacy read=%+v err=%v", local, err)
		}
		var count int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM chat_import_messages WHERE content='keep source isolated'").Scan(&count); err != nil || count != 18 {
			t.Fatalf("source rows changed count=%d err=%v", count, err)
		}
	})
}
