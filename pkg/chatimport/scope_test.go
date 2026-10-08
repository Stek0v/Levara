package chatimport

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func importScopeDialects(t *testing.T, test func(*testing.T, *sql.DB, Q)) {
	t.Helper()
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var db *sql.DB
			var q Q
			if dialect == "sqlite" {
				var err error
				db, err = sql.Open("sqlite3", filepath.Join(t.TempDir(), "scope.db"))
				if err != nil {
					t.Fatal(err)
				}
				q = SQLiteQ
				db.SetMaxOpenConns(1)
				t.Cleanup(func() { db.Close() })
				if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
					t.Fatal(err)
				}
			} else {
				dsn := os.Getenv("LEVARA_TEST_POSTGRES_DSN")
				if dsn == "" {
					t.Skip("LEVARA_TEST_POSTGRES_DSN not set")
				}
				cfg, err := pgx.ParseConfig(dsn)
				if err != nil {
					t.Fatal(err)
				}
				admin := stdlib.OpenDB(*cfg)
				schema := "chat_scope_" + strings.ReplaceAll(uuid.NewString(), "-", "")
				if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
					admin.Close()
					t.Fatal(err)
				}
				cfg.RuntimeParams["search_path"] = schema
				db = stdlib.OpenDB(*cfg)
				db.SetMaxOpenConns(1)
				db.SetMaxIdleConns(1)
				q = NoopQ
				t.Cleanup(func() {
					db.Close()
					if _, err := admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); err != nil {
						t.Error(err)
					}
					admin.Close()
				})
			}
			test(t, db, q)
		})
	}
}

func TestImportScopeSchemaMigration(t *testing.T) {
	importScopeDialects(t, func(t *testing.T, db *sql.DB, q Q) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		// Populate the exact pre-registry schema without invoking updated wrappers.
		for _, stmt := range SchemaStatements[:4] {
			if _, err := db.ExecContext(ctx, q(stmt)); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := db.ExecContext(ctx, q(`INSERT INTO chat_import_runs(id,platform,started_at) VALUES($1,$2,$3)`), "legacy-run", "codex", "2026-10-06T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, q(`INSERT INTO chat_import_messages(id,run_id,platform,session_id,external_id,ordinal,role,kind,content,imported_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`), "legacy-message", "legacy-run", "codex", "legacy-session", "m1", 0, "user", "text", "legacy private source", "2026-10-06T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 3; i++ {
			if err := EnsureSchema(ctx, db, q); err != nil {
				t.Fatal(err)
			}
		}
		for _, table := range []string{"chat_import_sessions", "chat_import_run_scopes"} {
			var count int
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&count); err != nil {
				t.Fatalf("additive registry %s missing: %v", table, err)
			}
			if count != 0 {
				t.Fatalf("migration adopted %d legacy rows into %s", count, table)
			}
		}
		var content, session, run string
		if err := db.QueryRowContext(ctx, `SELECT content,session_id,run_id FROM chat_import_messages WHERE id='legacy-message'`).Scan(&content, &session, &run); err != nil {
			t.Fatal(err)
		}
		if content != "legacy private source" || session != "legacy-session" || run != "legacy-run" {
			t.Fatalf("legacy source changed: %q/%q/%q", content, session, run)
		}
	})
}

func importScopeSnapshot(t *testing.T, db *sql.DB) string {
	t.Helper()
	var data []string
	for _, table := range []string{"chat_import_runs", "chat_import_messages", "chat_import_sessions", "chat_import_run_scopes"} {
		rows, err := db.Query(`SELECT * FROM ` + table + ` ORDER BY 1,2`)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		for rows.Next() {
			values, destinations := make([]any, len(columns)), make([]any, len(columns))
			for i := range values {
				destinations[i] = &values[i]
			}
			if err := rows.Scan(destinations...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			parts := []string{table}
			for _, value := range values {
				if raw, ok := value.([]byte); ok {
					value = string(raw)
				}
				parts = append(parts, fmt.Sprint(value))
			}
			raw, _ := json.Marshal(parts)
			data = append(data, string(raw))
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return strings.Join(data, "\n")
}

func assertImportScopeSnapshot(t *testing.T, db *sql.DB, before string) {
	t.Helper()
	if after := importScopeSnapshot(t, db); after != before {
		t.Fatalf("unexpected storage mutation\nbefore: %s\nafter: %s", before, after)
	}
}

func scopeConversation(session string) *Conversation {
	return &Conversation{Platform: PlatformCodex, SessionID: session, Title: "source title", Messages: []Message{
		{ExternalID: "m1", Ordinal: 0, Role: "user", Kind: KindText, Content: "source fact", Metadata: map[string]string{"binary": "a\x00b"}},
		{ExternalID: "m2", Ordinal: 1, Role: "assistant", Kind: KindText, Content: "source response"},
	}}
}

func scopeRun(source string) RunInfo {
	return RunInfo{ID: source, Platform: PlatformCodex, SourcePath: "private-source", SourceSHA256: "digest", StartedAt: "2026-10-06T00:00:00Z"}
}

func ensureImportScopeSchema(t *testing.T, db *sql.DB, q Q) {
	t.Helper()
	if err := EnsureSchema(context.Background(), db, q); err != nil {
		t.Fatal(err)
	}
}

func TestImportScopeIsolationAndRetry(t *testing.T) {
	importScopeDialects(t, func(t *testing.T, db *sql.DB, q Q) {
		ensureImportScopeSchema(t, db, q)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		scopes := []ImportScope{{TrustedLocal: true}, {OwnerID: "alice", TenantID: "tenant-a"}, {OwnerID: "bob", TenantID: "tenant-a"}, {OwnerID: "alice", TenantID: "tenant-b"}, {OwnerID: "alice"}, {OwnerID: "a/b", TenantID: "c"}, {OwnerID: "a", TenantID: "b/c"}}
		seenRuns, seenChats := map[string]bool{}, map[string]bool{}
		for _, scope := range scopes {
			label := scope.OwnerID + "/" + scope.TenantID
			if scope.TrustedLocal {
				label = "local"
			}
			t.Run(label, func(t *testing.T) {
				run, err := StartScopedRun(ctx, db, q, scope, scopeRun("same/source/run"))
				if err != nil {
					t.Fatal(err)
				}
				if seenRuns[run] {
					t.Fatalf("run scope collision: %s", run)
				}
				seenRuns[run] = true
				if scope.TrustedLocal && run != "same/source/run" {
					t.Fatal("local run ID changed")
				}
				conv := scopeConversation("same/source/session")
				var warnings []string
				chat, count, err := InsertScopedConversation(ctx, db, q, scope, run, conv, &warnings, time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC))
				if err != nil || count != 2 {
					t.Fatalf("first insert %d: %v", count, err)
				}
				if chat.OwnerID != scope.OwnerID || chat.TenantID != scope.TenantID || chat.Platform != conv.Platform || chat.SourceSessionID != conv.SessionID || chat.ProjectID != "" {
					t.Fatalf("identity mismatch: %+v", chat)
				}
				if seenChats[chat.ID] {
					t.Fatalf("chat scope collision: %+v", chat)
				}
				seenChats[chat.ID] = true
				if scope.TrustedLocal && chat.ID != conv.SessionID {
					t.Fatal("local session ID changed")
				}
				if !scope.TrustedLocal {
					if _, err := uuid.Parse(chat.ID); err != nil {
						t.Fatalf("canonical session not UUID: %v", err)
					}
					if _, err := uuid.Parse(run); err != nil {
						t.Fatalf("canonical run not UUID: %v", err)
					}
				}
				if conv.SessionID != "same/source/session" || conv.Messages[0].Metadata["binary"] != "a\x00b" {
					t.Fatal("caller transcript mutated")
				}
				// Preserve sharing and all source/ledger fields on both source and
				// returned canonical run retries.
				if _, err := db.ExecContext(ctx, q(`UPDATE chat_import_sessions SET project_id=$1 WHERE id=$2 AND platform=$3`), "explicit-project", chat.ID, string(chat.Platform)); err != nil {
					t.Fatal(err)
				}
				before := importScopeSnapshot(t, db)
				for _, selector := range []string{"same/source/run", run} {
					retry := scopeRun(selector)
					retry.SourcePath = "must not replace"
					retry.SourceSHA256 = "other"
					id, err := StartScopedRun(ctx, db, q, scope, retry)
					if err != nil || id != run {
						t.Fatalf("run retry %s -> %s: %v", selector, id, err)
					}
				}
				conv.Messages[0].Content = "must not overwrite"
				got, count, err := InsertScopedConversation(ctx, db, q, scope, run, conv, nil, time.Now())
				if err != nil || count != 0 || got.ProjectID != "explicit-project" {
					t.Fatalf("chat retry %+v/%d: %v", got, count, err)
				}
				assertImportScopeSnapshot(t, db, before)
			})
		}
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM chat_import_messages`).Scan(&count); err != nil || count != 2*len(scopes) {
			t.Fatalf("messages %d: %v", count, err)
		}
		// Historical platform/session pairs remain independent in local mode.
		conv := scopeConversation("same/source/session")
		conv.Platform = PlatformCursor
		if err := StartRun(ctx, db, q, RunInfo{ID: "cursor-run", Platform: PlatformCursor, StartedAt: "now"}); err != nil {
			t.Fatal(err)
		}
		if n, err := InsertConversation(ctx, db, q, "cursor-run", conv, nil, time.Now()); err != nil || n != 2 {
			t.Fatalf("local other platform %d: %v", n, err)
		}
	})
}

func TestImportScopeRejectedRequestsNoMutation(t *testing.T) {
	importScopeDialects(t, func(t *testing.T, db *sql.DB, q Q) {
		ensureImportScopeSchema(t, db, q)
		ctx := context.Background()
		own := ImportScope{OwnerID: "alice", TenantID: "a"}
		foreign := ImportScope{OwnerID: "bob", TenantID: "a"}
		run, err := StartScopedRun(ctx, db, q, own, scopeRun("own-source"))
		if err != nil {
			t.Fatal(err)
		}
		foreignRun, err := StartScopedRun(ctx, db, q, foreign, scopeRun("foreign-source"))
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := InsertScopedConversation(ctx, db, q, foreign, foreignRun, scopeConversation("control"), nil, time.Now()); err != nil {
			t.Fatal(err)
		}
		before := importScopeSnapshot(t, db)
		for name, scope := range map[string]ImportScope{
			"empty": {}, "tenant_only": {TenantID: "a"}, "local_owner": {OwnerID: "alice", TrustedLocal: true}, "local_tenant": {TenantID: "a", TrustedLocal: true},
			"owner_space": {OwnerID: " alice"}, "tenant_space": {OwnerID: "alice", TenantID: "a "}, "owner_nul": {OwnerID: "a\x00b"}, "tenant_invalid_utf8": {OwnerID: "alice", TenantID: string([]byte{255})},
		} {
			t.Run(name, func(t *testing.T) {
				if id, err := StartScopedRun(ctx, db, q, scope, scopeRun("invalid")); err == nil || id != "" {
					t.Fatalf("invalid scope accepted %q: %v", id, err)
				}
				if chat, count, err := InsertScopedConversation(ctx, db, q, scope, run, scopeConversation("invalid"), nil, time.Now()); err == nil || count != 0 || chat.ID != "" {
					t.Fatalf("invalid scope accepted %+v/%d: %v", chat, count, err)
				}
				assertImportScopeSnapshot(t, db, before)
			})
		}
		for name, test := range map[string]func() error{
			"foreign_canonical_run": func() error { _, err := StartScopedRun(ctx, db, q, own, scopeRun(foreignRun)); return err },
			"changed_run_platform": func() error {
				info := scopeRun(run)
				info.Platform = PlatformCursor
				_, err := StartScopedRun(ctx, db, q, own, info)
				return err
			},
			"changed_source_platform": func() error {
				info := scopeRun("own-source")
				info.Platform = PlatformCursor
				_, err := StartScopedRun(ctx, db, q, own, info)
				return err
			},
			"foreign_insert_run": func() error {
				_, _, err := InsertScopedConversation(ctx, db, q, own, foreignRun, scopeConversation("bad"), nil, time.Now())
				return err
			},
			"tenant_insert_run": func() error {
				_, _, err := InsertScopedConversation(ctx, db, q, ImportScope{OwnerID: "alice", TenantID: "b"}, run, scopeConversation("bad"), nil, time.Now())
				return err
			},
			"raw_run_not_canonical": func() error {
				_, _, err := InsertScopedConversation(ctx, db, q, own, "own-source", scopeConversation("bad"), nil, time.Now())
				return err
			},
			"missing_local_run": func() error {
				_, err := InsertConversation(ctx, db, q, "missing", scopeConversation("bad"), nil, time.Now())
				return err
			},
			"nil_conversation": func() error {
				_, _, err := InsertScopedConversation(ctx, db, q, own, run, nil, nil, time.Now())
				return err
			},
			"empty_session": func() error {
				_, _, err := InsertScopedConversation(ctx, db, q, own, run, scopeConversation(""), nil, time.Now())
				return err
			},
		} {
			t.Run(name, func(t *testing.T) {
				if err := test(); err == nil {
					t.Fatal("invalid request accepted")
				}
				assertImportScopeSnapshot(t, db, before)
			})
		}
	})
}

func TestImportScopePhysicalCollisions(t *testing.T) {
	importScopeDialects(t, func(t *testing.T, db *sql.DB, q Q) {
		ensureImportScopeSchema(t, db, q)
		ctx := context.Background()
		scope := ImportScope{OwnerID: "alice", TenantID: "a"}
		run, err := StartScopedRun(ctx, db, q, scope, scopeRun("normal-run"))
		if err != nil {
			t.Fatal(err)
		}
		chat, _, err := InsertScopedConversation(ctx, db, q, scope, run, scopeConversation("normal-session"), nil, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if err := StartRun(ctx, db, q, scopeRun("local-control")); err != nil {
			t.Fatal(err)
		}
		before := importScopeSnapshot(t, db)
		t.Run("reverse_local_run", func(t *testing.T) {
			if err := StartRun(ctx, db, q, scopeRun(run)); err == nil {
				t.Fatal("local writer adopted authenticated run")
			}
			assertImportScopeSnapshot(t, db, before)
		})
		t.Run("reverse_local_session", func(t *testing.T) {
			if _, err := InsertConversation(ctx, db, q, "local-control", scopeConversation(chat.ID), nil, time.Now()); err == nil {
				t.Fatal("local writer adopted authenticated session")
			}
			assertImportScopeSnapshot(t, db, before)
		})
		// Unregistered legacy physical keys can be arbitrary, including the
		// UUID a future authenticated request would otherwise derive.
		blockedRun := importTupleID("run", scope.OwnerID, scope.TenantID, "blocked-source-run")
		if _, err := db.Exec(q(`INSERT INTO chat_import_runs(id,platform,started_at) VALUES($1,$2,$3)`), blockedRun, "codex", "legacy"); err != nil {
			t.Fatal(err)
		}
		blockedChat := importTupleID("session", scope.OwnerID, scope.TenantID, "codex", "blocked-source-session")
		if _, err := db.Exec(q(`INSERT INTO chat_import_messages(id,run_id,platform,session_id,external_id,ordinal,role,kind,content,imported_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`), "raw-legacy", blockedRun, "codex", blockedChat, "m1", 0, "user", "text", "legacy untouched", "legacy"); err != nil {
			t.Fatal(err)
		}
		before = importScopeSnapshot(t, db)
		t.Run("unclaimed_run", func(t *testing.T) {
			if _, err := StartScopedRun(ctx, db, q, scope, scopeRun("blocked-source-run")); err == nil || !strings.Contains(err.Error(), "unclaimed run") {
				t.Fatalf("wrong collision error: %v", err)
			}
			assertImportScopeSnapshot(t, db, before)
		})
		t.Run("unclaimed_session", func(t *testing.T) {
			if _, _, err := InsertScopedConversation(ctx, db, q, scope, run, scopeConversation("blocked-source-session"), nil, time.Now()); err == nil || !strings.Contains(err.Error(), "unclaimed session") {
				t.Fatalf("wrong collision error: %v", err)
			}
			assertImportScopeSnapshot(t, db, before)
		})
		registeredRun := importTupleID("run", scope.OwnerID, scope.TenantID, "registered-source-run")
		if err := StartRun(ctx, db, q, scopeRun(registeredRun)); err != nil {
			t.Fatal(err)
		}
		registeredChat := importTupleID("session", scope.OwnerID, scope.TenantID, "codex", "registered-source-session")
		if _, err := InsertConversation(ctx, db, q, "local-control", scopeConversation(registeredChat), nil, time.Now()); err != nil {
			t.Fatal(err)
		}
		before = importScopeSnapshot(t, db)
		t.Run("registered_local_run", func(t *testing.T) {
			if _, err := StartScopedRun(ctx, db, q, scope, scopeRun("registered-source-run")); err == nil || !strings.Contains(err.Error(), "identity collision") {
				t.Fatalf("registered local run adopted: %v", err)
			}
			assertImportScopeSnapshot(t, db, before)
		})
		t.Run("registered_local_session", func(t *testing.T) {
			if _, _, err := InsertScopedConversation(ctx, db, q, scope, run, scopeConversation("registered-source-session"), nil, time.Now()); err == nil || !strings.Contains(err.Error(), "identity collision") {
				t.Fatalf("registered local session adopted: %v", err)
			}
			assertImportScopeSnapshot(t, db, before)
		})
		// A source selector resembling a registered foreign chat ID creates
		// a different own tuple; it cannot inherit that row's identity/content.
		other := ImportScope{OwnerID: "bob", TenantID: "a"}
		otherRun, err := StartScopedRun(ctx, db, q, other, scopeRun("normal-run"))
		if err != nil {
			t.Fatal(err)
		}
		otherChat, _, err := InsertScopedConversation(ctx, db, q, other, otherRun, scopeConversation(chat.ID), nil, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if otherChat.ID == chat.ID || otherChat.SourceSessionID != chat.ID || otherChat.OwnerID != "bob" {
			t.Fatalf("canonical-looking source adopted: %+v", otherChat)
		}
	})
}

func TestImportScopeRollbackAndPoolOne(t *testing.T) {
	importScopeDialects(t, func(t *testing.T, db *sql.DB, q Q) {
		ensureImportScopeSchema(t, db, q)
		ctx := context.Background()
		syntheticToken := "ghp_" + strings.Repeat("x", 36)
		scope := ImportScope{OwnerID: "alice", TenantID: "a"}
		run, err := StartScopedRun(ctx, db, q, scope, scopeRun("run"))
		if err != nil {
			t.Fatal(err)
		}
		before := importScopeSnapshot(t, db)
		t.Run("canceled_before_start", func(t *testing.T) {
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			if _, err := StartScopedRun(canceled, db, q, scope, scopeRun("canceled")); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel start: %v", err)
			}
			if _, _, err := InsertScopedConversation(canceled, db, q, scope, run, scopeConversation("canceled"), nil, time.Now()); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel insert: %v", err)
			}
			assertImportScopeSnapshot(t, db, before)
		})
		t.Run("pool_wait_deadline", func(t *testing.T) {
			conn, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			deadline, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
			_, err = StartScopedRun(deadline, db, q, scope, scopeRun("waiting"))
			cancel()
			conn.Close()
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("pool wait: %v", err)
			}
			assertImportScopeSnapshot(t, db, before)
		})
		t.Run("cancel_after_first_message", func(t *testing.T) {
			canceled, cancel := context.WithCancel(ctx)
			defer cancel()
			calls := 0
			cancelQ := func(query string) string {
				if strings.Contains(query, "INSERT INTO chat_import_messages") {
					calls++
					if calls == 2 {
						cancel()
					}
				}
				return q(query)
			}
			conv := scopeConversation("canceled-late")
			conv.Messages[0].Content = syntheticToken
			warnings := []string{"original"}
			chat, count, err := InsertScopedConversation(canceled, db, cancelQ, scope, run, conv, &warnings, time.Now())
			if !errors.Is(err, context.Canceled) || count != 0 || chat.ID != "" || calls != 2 {
				t.Fatalf("late cancellation %+v/%d/calls%d: %v", chat, count, calls, err)
			}
			if len(warnings) != 1 || warnings[0] != "original" {
				t.Fatalf("failed transaction appended warnings: %v", warnings)
			}
			assertImportScopeSnapshot(t, db, before)
		})
		t.Run("caller_transaction_rollback", func(t *testing.T) {
			bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			tx, err := db.BeginTx(bounded, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			id, err := StartScopedRunTx(bounded, tx, q, scope, scopeRun("transaction"))
			if err != nil {
				t.Fatal(err)
			}
			if _, count, err := InsertScopedConversationTx(bounded, tx, q, scope, id, scopeConversation("transaction"), nil, time.Now()); err != nil || count != 2 {
				t.Fatalf("Tx insert %d: %v", count, err)
			}
			if err := FinishRunTx(bounded, tx, q, id, "ok", 2, 0, 0, nil, "finished"); err != nil {
				t.Fatal(err)
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			assertImportScopeSnapshot(t, db, before)
		})
		// Inject a second-message primary-key conflict while preserving a
		// distinct source triple: the first INSERT succeeds inside the Tx.
		bad := scopeConversation("sql-failure")
		bad.Messages[0].Content = syntheticToken
		physical := importTupleID("session", scope.OwnerID, scope.TenantID, "codex", bad.SessionID)
		collision := importTupleID("message", "codex", physical, "m2")
		if _, err := db.Exec(q(`INSERT INTO chat_import_messages(id,run_id,platform,session_id,external_id,ordinal,role,kind,content,imported_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`), collision, run, "cursor", "control", "other-message", 0, "user", "text", "untouched control", "old"); err != nil {
			t.Fatal(err)
		}
		before = importScopeSnapshot(t, db)
		t.Run("second_message_SQL_failure", func(t *testing.T) {
			warnings := []string{"original"}
			chat, count, err := InsertScopedConversation(ctx, db, q, scope, run, bad, &warnings, time.Now())
			if err == nil || count != 0 || chat.ID != "" || !strings.Contains(strings.ToLower(err.Error()), "unique") {
				t.Fatalf("wrong forced SQL failure %+v/%d: %v", chat, count, err)
			}
			if len(warnings) != 1 {
				t.Fatalf("rollback warnings: %v", warnings)
			}
			assertImportScopeSnapshot(t, db, before)
		})
		// A conflicting source tuple must roll back the just-inserted ledger.
		if _, err := db.Exec(q(`INSERT INTO chat_import_runs(id,platform,started_at) VALUES($1,$2,$3)`), "registry-control", "codex", "old"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(q(`INSERT INTO chat_import_run_scopes(run_id,owner_id,tenant_id,source_run_id,trusted_local) VALUES($1,$2,$3,$4,FALSE)`), "registry-control", scope.OwnerID, scope.TenantID, "registry-source"); err != nil {
			t.Fatal(err)
		}
		before = importScopeSnapshot(t, db)
		t.Run("registry_SQL_failure", func(t *testing.T) {
			if _, err := StartScopedRun(ctx, db, q, scope, scopeRun("registry-source")); err == nil || !strings.Contains(strings.ToLower(err.Error()), "unique") {
				t.Fatalf("wrong register SQL failure: %v", err)
			}
			assertImportScopeSnapshot(t, db, before)
		})
	})
}

func TestImportScopeConcurrentRetry(t *testing.T) {
	importScopeDialects(t, func(t *testing.T, db *sql.DB, q Q) {
		ensureImportScopeSchema(t, db, q)
		// PostgreSQL exercises concurrent transactions. SQLite exercises the
		// intended one-connection writer deployment without a timing oracle.
		if q("$1") != "?" {
			db.SetMaxOpenConns(4)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		scope := ImportScope{OwnerID: "alice", TenantID: "a"}
		var group sync.WaitGroup
		results := make(chan error, 6)
		for i := 0; i < 6; i++ {
			group.Add(1)
			go func() {
				defer group.Done()
				id, err := StartScopedRun(ctx, db, q, scope, scopeRun("concurrent"))
				if err == nil {
					_, _, err = InsertScopedConversation(ctx, db, q, scope, id, scopeConversation("concurrent"), nil, time.Now())
				}
				results <- err
			}()
		}
		group.Wait()
		close(results)
		for err := range results {
			if err != nil {
				t.Fatal(err)
			}
		}
		for table, expected := range map[string]int{"chat_import_runs": 1, "chat_import_run_scopes": 1, "chat_import_sessions": 1, "chat_import_messages": 2} {
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != expected {
				t.Fatalf("concurrent %s count%d want%d: %v", table, count, expected, err)
			}
		}
	})
}

func TestImportScopeLocalFinishGuard(t *testing.T) {
	importScopeDialects(t, func(t *testing.T, db *sql.DB, q Q) {
		ensureImportScopeSchema(t, db, q)
		ctx := context.Background()
		for _, scope := range []ImportScope{{OwnerID: "alice", TenantID: "a"}, {OwnerID: "alice"}} {
			label := scope.TenantID
			if label == "" {
				label = "tenantless"
			}
			run, err := StartScopedRun(ctx, db, q, scope, scopeRun("protected-finish"))
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := InsertScopedConversation(ctx, db, q, scope, run, scopeConversation("protected-finish"), nil, time.Now()); err != nil {
				t.Fatal(err)
			}
			t.Run(label+"_authenticated_running", func(t *testing.T) {
				before := importScopeSnapshot(t, db)
				err := FinishRun(ctx, db, q, run, "failed", 999, 998, 997, []string{"forged local finish"}, "forged time")
				if err == nil || !strings.Contains(err.Error(), "scope mismatch") {
					t.Errorf("local FinishRun accepted authenticated run or wrong error: %v", err)
				}
				assertImportScopeSnapshot(t, db, before)
			})
			// The fenced Tx API retains its caller-prevalidated contract.
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := FinishRunTx(ctx, tx, q, run, "ok", 2, 0, 0, nil, "authorized time"); err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			t.Run(label+"_authenticated_closed_retry", func(t *testing.T) {
				before := importScopeSnapshot(t, db)
				err := FinishRun(ctx, db, q, run, "failed", 999, 998, 997, []string{"forged retry"}, "forged time")
				if err == nil || !strings.Contains(err.Error(), "scope mismatch") {
					t.Errorf("local retry accepted authenticated run or wrong error: %v", err)
				}
				assertImportScopeSnapshot(t, db, before)
			})
		}
		for _, registered := range []bool{true, false} {
			label := "legacy"
			if registered {
				label = "registered_local"
			}
			t.Run(label+"_positive_and_retry", func(t *testing.T) {
				info := scopeRun(label)
				if registered {
					if err := StartRun(ctx, db, q, info); err != nil {
						t.Fatal(err)
					}
				} else {
					if _, err := db.Exec(q(`INSERT INTO chat_import_runs(id,platform,source_path,source_sha256,started_at) VALUES($1,$2,$3,$4,$5)`), info.ID, string(info.Platform), info.SourcePath, info.SourceSHA256, info.StartedAt); err != nil {
						t.Fatal(err)
					}
				}
				before := importScopeSnapshot(t, db)
				if err := FinishRun(ctx, db, q, info.ID, "ok", 2, 3, 1, []string{"local warning"}, "local finished"); err != nil {
					t.Fatal(err)
				}
				var status, warnings, finished, path, digest, started string
				var imported, skipped, warned int
				if err := db.QueryRow(q(`SELECT status,imported_count,skipped_count,warned_count,warnings,finished_at,source_path,source_sha256,started_at FROM chat_import_runs WHERE id=$1`), info.ID).Scan(&status, &imported, &skipped, &warned, &warnings, &finished, &path, &digest, &started); err != nil {
					t.Fatal(err)
				}
				if status != "ok" || imported != 2 || skipped != 3 || warned != 1 || warnings != `["local warning"]` || finished != "local finished" || path != info.SourcePath || digest != info.SourceSHA256 || started != info.StartedAt {
					t.Fatalf("local ledger incorrect: %s/%d/%d/%d/%s/%s/%s/%s/%s", status, imported, skipped, warned, warnings, finished, path, digest, started)
				}
				// Only this explicit local/legacy ledger may differ. Every other
				// complete row, including authenticated controls, stays intact.
				withoutLedger := func(snapshot string) string {
					var retained []string
					for _, line := range strings.Split(snapshot, "\n") {
						var fields []string
						if err := json.Unmarshal([]byte(line), &fields); err != nil {
							t.Fatal(err)
						}
						if fields[0] != "chat_import_runs" || fields[1] != info.ID {
							retained = append(retained, line)
						}
					}
					return strings.Join(retained, "\n")
				}
				after := importScopeSnapshot(t, db)
				if withoutLedger(before) != withoutLedger(after) {
					t.Fatal("local finish mutated another complete row")
				}
				if err := FinishRun(ctx, db, q, info.ID, "failed", 99, 99, 99, []string{"retry"}, "later"); err != nil {
					t.Fatal(err)
				}
				assertImportScopeSnapshot(t, db, after)
			})
		}
	})
}
