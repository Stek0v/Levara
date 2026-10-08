package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/chatimport"
	"github.com/stek0v/levara/pkg/llm"
)

// Complete the native fixture's policy schema without changing its evidence setup.
func distillAuthorityPolicySchema(t *testing.T, d Deps) {
	t.Helper()
	for _, stmt := range []string{
		"ALTER TABLE datasets ADD COLUMN owner_id TEXT DEFAULT ''",
		"ALTER TABLE dataset_shares ADD COLUMN dataset_id TEXT DEFAULT ''",
		"ALTER TABLE dataset_shares ADD COLUMN user_id TEXT DEFAULT ''",
		"ALTER TABLE dataset_shares ADD COLUMN role TEXT DEFAULT ''",
		"CREATE TABLE document_index_publications(id TEXT)",
		"CREATE TABLE document_pipeline_statuses(id TEXT)",
	} {
		if _, err := d.DB().Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	d.DB().SetMaxOpenConns(1)
}

func distillAuthorityScope(d *memoryCommitEvidenceDeps) chatimport.ImportScope {
	return chatimport.ImportScope{OwnerID: d.actor.UserID, TenantID: d.actor.TenantID}
}

type distillAuthorityDeps struct {
	*distillContextDeps
	metadata map[string]string
	hook     func()
}

func (d *distillAuthorityDeps) CollectionInsertDeferredHook(collection, id string, vec []float32, meta any) (func(), error) {
	if err := json.Unmarshal(meta.([]byte), &d.metadata); err != nil {
		return nil, err
	}
	err := d.CollectionInsert(collection, id, vec, meta)
	return d.hook, err
}

func TestChatDistillSourceAuthority(t *testing.T) {
	for _, pg := range []bool{false, true} {
		dialect := "sqlite"
		if pg {
			dialect = "postgres"
		}
		t.Run(dialect, func(t *testing.T) {
			for _, scenario := range []string{"private-owner", "private-foreign", "private-physical-selector", "superuser-private", "configured-local-authenticated", "legacy-hidden", "foreign-tenant", "inactive", "project-read", "project-revoked", "revoke-during-provider", "revoke-empty-provider", "revoke-preview", "revoke-during-embed", "callback-after-release", "cancel-before-callback", "canonical-selector", "trusted-local-foreign"} {
				t.Run(scenario, func(t *testing.T) {
					base, ctx := memoryCommitEvidenceFixture(t, pg)
					ctx, cancel := context.WithCancel(ctx)
					defer cancel()
					distillAuthorityPolicySchema(t, base)
					if err := chatimport.EnsureSchema(ctx, base.DB(), base.Q); err != nil {
						t.Fatal(err)
					}
					scope := distillAuthorityScope(base)
					run, err := chatimport.StartScopedRun(ctx, base.DB(), base.Q, scope, chatimport.RunInfo{ID: "authority-run", Platform: chatimport.PlatformCodex, StartedAt: time.Now().UTC().Format(time.RFC3339)})
					if err != nil {
						t.Fatal(err)
					}
					chat, _, err := chatimport.InsertScopedConversation(ctx, base.DB(), base.Q, scope, run, &chatimport.Conversation{Platform: chatimport.PlatformCodex, SessionID: "authority-source", Title: "Personal source", Messages: []chatimport.Message{{ExternalID: "m1", Role: "user", Kind: chatimport.KindText, Content: "Choose private storage."}}}, nil, time.Now().UTC())
					if err != nil {
						t.Fatal(err)
					}
					if _, err := base.DB().Exec(base.Q("INSERT INTO memories(id,key,value,type,owner_id,collection_name,room,hall,is_pinned,pin_priority,verification_status,source_task_id,source_receipt_ids,created_at,updated_at) VALUES('foreign-control','authority-result','untouched','project','owner-b','levara','memory','fact',FALSE,0,'verified','control-task','[]','2026-10-05','2026-10-05')")); err != nil {
						t.Fatal(err)
					}
					args := map[string]any{"platform": "codex", "session_id": "authority-source", "collection": "levara"}
					denied := false
					shared := strings.HasPrefix(scenario, "project-") || strings.HasPrefix(scenario, "revoke-")
					if shared {
						for _, sql := range []string{"INSERT INTO datasets(id,owner_id) VALUES('project','owner-a')", "INSERT INTO dataset_shares(dataset_id,user_id,role) VALUES('project','owner-b','viewer')", "UPDATE chat_import_sessions SET project_id='project'"} {
							if _, err := base.DB().Exec(sql); err != nil {
								t.Fatal(err)
							}
						}
						base.actor.UserID = "owner-b"
					}
					revoke := func() {
						t.Helper()
						if _, err := base.DB().Exec("DELETE FROM dataset_shares"); err != nil {
							t.Fatal(err)
						}
					}
					switch scenario {
					case "private-foreign":
						base.actor.UserID = "owner-b"
						denied = true
					case "private-physical-selector":
						base.actor.UserID = "owner-b"
						args["session_id"] = chat.ID
						denied = true
					case "superuser-private":
						base.actor.UserID = "owner-b"
						if _, err := base.DB().Exec("UPDATE users SET is_superuser=TRUE WHERE id='owner-b'"); err != nil {
							t.Fatal(err)
						}
						denied = true
					case "configured-local-authenticated":
						base.actor.UserID = "owner-b"
						base.actor.TrustedLocal = true
						denied = true
					case "foreign-tenant":
						base.actor.TenantID = "foreign"
						if _, err := base.DB().Exec("INSERT INTO user_tenant(user_id,tenant_id) VALUES('owner-a','foreign')"); err != nil {
							t.Fatal(err)
						}
						denied = true
					case "inactive":
						if _, err := base.DB().Exec("UPDATE users SET is_active=FALSE WHERE id='owner-a'"); err != nil {
							t.Fatal(err)
						}
						denied = true
					case "project-revoked":
						revoke()
						denied = true
					case "canonical-selector":
						args = map[string]any{"chat_id": chat.ID, "platform": "codex", "collection": "levara"}
					case "trusted-local-foreign":
						base.actor = access.MetadataActor{TrustedLocal: true}
						args = map[string]any{"chat_id": chat.ID, "platform": "codex", "collection": "levara"}
						denied = true
					case "legacy-hidden":
						args["session_id"] = "legacy"
						if _, err := base.DB().Exec("INSERT INTO chat_import_runs(id,platform,started_at) VALUES('legacy-run','codex','2026-10-06')"); err != nil {
							t.Fatal(err)
						}
						if _, err := base.DB().Exec("INSERT INTO chat_import_messages(id,run_id,platform,session_id,external_id,ordinal,role,kind,content,imported_at) VALUES('legacy-message','legacy-run','codex','legacy','m',0,'user','text','SECRET LEGACY','2026-10-06')"); err != nil {
							t.Fatal(err)
						}
						denied = true
					}
					calls := 0
					d := &distillAuthorityDeps{distillContextDeps: &distillContextDeps{Deps: base}}
					d.provider = &distillContextProvider{call: func(_ context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
						calls++
						if !strings.Contains(req.Messages[0].Content, "Choose private storage") {
							t.Fatal("wrong transcript")
						}
						if scenario == "revoke-during-provider" || scenario == "revoke-empty-provider" || scenario == "revoke-preview" {
							revoke()
						}
						content := `[{"key":"authority-result","value":"Private decision."}]`
						if scenario == "revoke-empty-provider" {
							content = "[]"
						}
						return &llm.CompletionResponse{Content: content}, nil
					}}
					if scenario == "revoke-preview" {
						args["dry_run"] = true
					}
					hookCalled := false
					if scenario == "revoke-during-embed" || scenario == "callback-after-release" || scenario == "project-read" || scenario == "cancel-before-callback" {
						d.embed = func(context.Context, string) ([]float32, error) {
							if scenario == "revoke-during-embed" {
								revoke()
							}
							return []float32{1, 0}, nil
						}
						if scenario == "cancel-before-callback" {
							d.onInsert = cancel
						}
						d.hook = func() {
							hookCalled = true
							check, cancel := context.WithTimeout(ctx, time.Second)
							defer cancel()
							if err := base.DB().PingContext(check); err != nil {
								t.Fatalf("callback held SQL fence: %v", err)
							}
						}
					}
					before := distillEvidenceSnapshot(t, d)
					result := ToolChatDistill(ctx, d, args)
					shouldError := denied || strings.HasPrefix(scenario, "revoke-") || scenario == "cancel-before-callback"
					if result.IsError != shouldError {
						t.Fatalf("error=%v want=%v: %s", result.IsError, shouldError, toolResultText(result))
					}
					if denied && calls != 0 {
						t.Fatalf("denied source reached provider %d times", calls)
					}
					if scenario == "revoke-empty-provider" && calls != 1 {
						t.Fatalf("revoked source retried provider %d", calls)
					}
					after := distillEvidenceSnapshot(t, d)
					if shouldError && scenario != "revoke-during-embed" && scenario != "cancel-before-callback" {
						if !reflect.DeepEqual(before, after) {
							t.Fatal("denied source changed authoritative rows")
						}
					} else {
						delete(before, "memories")
						delete(after, "memories")
						if !reflect.DeepEqual(before, after) {
							t.Fatal("distillation changed source or task rows")
						}
					}
					var count int
					if err := base.DB().QueryRow(base.Q("SELECT COUNT(*) FROM memories WHERE key='authority-result' AND owner_id=$1"), base.actor.UserID).Scan(&count); err != nil {
						t.Fatal(err)
					}
					// One foreign control is present for owner-b; only embedding revocation follows a committed save.
					control := 0
					if base.actor.UserID == "owner-b" {
						control = 1
					}
					expected := control
					if !shouldError || scenario == "revoke-during-embed" || scenario == "cancel-before-callback" {
						expected++
					}
					// Shared owner-b save upserts the same canonical control row instead of adding another.
					if control == 1 && expected > 1 {
						expected = 1
					}
					if count != expected {
						t.Fatalf("memory rows=%d want=%d", count, expected)
					}
					var value string
					if err := base.DB().QueryRow("SELECT value FROM memories WHERE id='foreign-control'").Scan(&value); err != nil {
						t.Fatal(err)
					}
					if denied || strings.HasPrefix(scenario, "revoke-") && scenario != "revoke-during-embed" || base.actor.UserID != "owner-b" {
						if value != "untouched" {
							t.Fatalf("control changed: %s", value)
						}
					}
					if scenario == "revoke-during-embed" && len(d.inserted) != 0 {
						t.Fatal("revoked source published vector")
					}
					if (scenario == "callback-after-release" || scenario == "project-read") && d.metadata["owner_id"] != base.actor.UserID {
						t.Fatalf("vector owner=%q", d.metadata["owner_id"])
					}
					if (scenario == "callback-after-release" || scenario == "project-read") && !hookCalled {
						t.Fatal("successful publication omitted callback")
					}
					if scenario == "cancel-before-callback" && hookCalled {
						t.Fatal("canceled insert started callback")
					}
					if !shouldError && !strings.Contains(toolResultText(result), chat.ID) {
						t.Fatal("canonical provenance absent")
					}
				})
			}
		})
	}
}

func publicationHeldResult(t *testing.T, done <-chan ToolResult) ToolResult {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(10 * time.Second):
		t.Fatal("held publication did not finish")
		return ToolResult{}
	}
}

func TestChatDistillMemoryPublicationAuthority(t *testing.T) {
	for _, pg := range []bool{false, true} {
		name := "sqlite"
		if pg {
			name = "postgres"
		}
		t.Run(name, func(t *testing.T) {
			for _, scenario := range []string{"retired", "key-changed", "type-changed", "preserved-type"} {
				t.Run(scenario, func(t *testing.T) {
					base, parent := distillContextFixture(t, pg, [][3]string{{"user", "text", "Publication source"}})
					ctx, cancel := context.WithTimeout(parent, 15*time.Second)
					defer cancel()
					if scenario == "preserved-type" {
						if _, err := base.DB().Exec("UPDATE memories SET type='feedback' WHERE id='target-id'"); err != nil {
							t.Fatal(err)
						}
					}
					started, release := make(chan struct{}), make(chan struct{})
					var once sync.Once
					unblock := func() { once.Do(func() { close(release) }) }
					defer unblock()
					d := &distillAuthorityDeps{distillContextDeps: &distillContextDeps{Deps: base, provider: &stubDistillProvider{content: `[{"key":"target","value":"Current proposed text"}]`}}}
					d.embed = func(context.Context, string) ([]float32, error) {
						close(started)
						<-release
						return []float32{1, 0}, nil
					}
					done := make(chan ToolResult, 1)
					go func() { done <- ToolChatDistill(ctx, d, distillContextArgs()) }()
					select {
					case <-started:
					case <-ctx.Done():
						t.Fatal("embedding was not reached")
					}
					var query string
					switch scenario {
					case "retired":
						query = "UPDATE memories SET superseded_by='successor' WHERE id='target-id'"
					case "key-changed":
						query = "UPDATE memories SET key='renamed-target' WHERE id='target-id'"
					case "type-changed":
						query = "UPDATE memories SET type='feedback' WHERE id='target-id'"
					}
					if query != "" {
						if _, err := base.DB().ExecContext(ctx, query); err != nil {
							t.Fatal(err)
						}
					}
					before := distillEvidenceSnapshot(t, d)
					unblock()
					result := publicationHeldResult(t, done)
					if result.IsError != (scenario != "preserved-type") {
						t.Fatalf("publication error=%v scenario=%s: %s", result.IsError, scenario, toolResultText(result))
					}
					if !reflect.DeepEqual(before, distillEvidenceSnapshot(t, d)) {
						t.Fatal("publication changed authoritative rows after held embedding")
					}
					if scenario == "preserved-type" {
						if len(d.inserted) != 1 || d.metadata["type"] != "feedback" || d.metadata["key"] != "target" || d.metadata["owner_id"] != "owner-a" || d.metadata["memory_id"] != "target-id" {
							t.Fatalf("canonical metadata lost: %+v / %v", d.metadata, d.inserted)
						}
					} else if len(d.inserted) != 0 {
						t.Fatal("retired/changed memory published a vector")
					}
				})
			}
		})
	}
}

// A source/row fence must outlive caller cancellation once native insertion starts.
func TestChatDistillLocalStartedPublicationFence(t *testing.T) {
	for _, pg := range []bool{false, true} {
		name := "sqlite"
		if pg {
			name = "postgres"
		}
		t.Run(name, func(t *testing.T) {
			base, parent := distillContextFixture(t, pg, [][3]string{{"user", "text", "Local publication"}})
			memoryCommitParallelPool(t, base, pg)
			base.actor = access.MetadataActor{TrustedLocal: true}
			if err := chatimport.StartRun(parent, base.DB(), base.Q, chatimport.RunInfo{ID: "local-publication-run", Platform: chatimport.PlatformCodex, StartedAt: "2026-10-06"}); err != nil {
				t.Fatal(err)
			}
			if _, err := chatimport.InsertConversation(parent, base.DB(), base.Q, "local-publication-run", &chatimport.Conversation{Platform: chatimport.PlatformCodex, SessionID: "local-publication", Messages: []chatimport.Message{{ExternalID: "local-message", Role: "user", Kind: chatimport.KindText, Content: "Local publication"}}}, nil, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			// Local publication requires no authentication tables.
			if _, err := base.DB().Exec("DROP TABLE users"); err != nil {
				t.Fatal(err)
			}
			base.DB().SetMaxOpenConns(2)
			ctx, cancel := context.WithCancel(parent)
			defer cancel()
			started, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			d := &distillAuthorityDeps{distillContextDeps: &distillContextDeps{Deps: base, provider: &stubDistillProvider{content: `[{"key":"local-target","value":"Local proposal"}]`}, embed: func(context.Context, string) ([]float32, error) { return []float32{1, 0}, nil }}}
			d.onInsert = func() { close(started); <-release }
			args := map[string]any{"platform": "codex", "session_id": "local-publication", "collection": "levara"}
			done := make(chan ToolResult, 1)
			go func() { done <- ToolChatDistill(ctx, d, args) }()
			select {
			case <-started:
			case <-time.After(10 * time.Second):
				t.Fatal("native insert did not start")
			}
			cancel()
			writerCtx, stopWriter := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer stopWriter()
			_, err := base.DB().ExecContext(writerCtx, base.Q("UPDATE memories SET value='Concurrent writer' WHERE key=$1 AND owner_id='' AND collection_name='levara'"), "local-target")
			if err == nil {
				t.Error("caller cancellation released fence while native insertion was held")
			}
			unblock()
			result := publicationHeldResult(t, done)
			if !result.IsError {
				t.Fatal("canceled native operation reported success")
			}
			afterCtx, stopAfter := context.WithTimeout(context.Background(), 3*time.Second)
			defer stopAfter()
			if _, err := base.DB().ExecContext(afterCtx, base.Q("UPDATE memories SET value='After native return' WHERE key=$1 AND owner_id='' AND collection_name='levara'"), "local-target"); err != nil {
				t.Fatalf("fence did not release after native return: %v", err)
			}
		})
	}
}

func TestChatDistillPublicationCredentialExpiry(t *testing.T) {
	for _, pg := range []bool{false, true} {
		name := "sqlite"
		if pg {
			name = "postgres"
		}
		t.Run(name, func(t *testing.T) {
			base, parent := distillContextFixture(t, pg, [][3]string{{"user", "text", "Expiry boundary"}})
			ctx, cancel := context.WithTimeout(parent, 12*time.Second)
			defer cancel()
			memoryCommitParallelPool(t, base, pg)
			base.DB().SetMaxOpenConns(3)
			base.actor.Credential.ExpiresAt = time.Now().Unix() + 3
			started, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			d := &distillAuthorityDeps{distillContextDeps: &distillContextDeps{Deps: base, provider: &stubDistillProvider{content: `[{"key":"target","value":"Expiry proposal"}]`}}}
			d.embed = func(context.Context, string) ([]float32, error) {
				close(started)
				<-release
				return []float32{1, 0}, nil
			}
			done := make(chan ToolResult, 1)
			go func() { done <- ToolChatDistill(ctx, d, distillContextArgs()) }()
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal("embedding was not reached before expiry")
			}
			var held *sql.Tx
			var heldConn *sql.Conn
			var err error
			waits := base.DB().Stats().WaitCount
			if pg {
				held, err = base.DB().BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer held.Rollback()
				if _, err := held.ExecContext(ctx, "UPDATE memories SET value=value WHERE id='target-id'"); err != nil {
					t.Fatal(err)
				}
			} else {
				base.DB().SetMaxOpenConns(1)
				heldConn, err = base.DB().Conn(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer heldConn.Close()
			}
			unblock()
			for {
				waiting := base.DB().Stats().WaitCount > waits
				if pg {
					var count int
					err := base.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE 'SELECT% FROM memories WHERE id=$1 AND owner_id=$2 AND collection_name=$3%'").Scan(&count)
					if err != nil {
						t.Fatal(err)
					}
					waiting = count > 0
				}
				if waiting {
					break
				}
				select {
				case result := <-done:
					t.Fatalf("publication completed before held acquisition/row wait: %s", toolResultText(result))
				case <-ctx.Done():
					t.Fatal("acquisition/row wait not observed")
				case <-time.After(5 * time.Millisecond):
				}
			}
			for time.Now().Unix() < base.actor.Credential.ExpiresAt {
				select {
				case <-ctx.Done():
					t.Fatal("expiry clock wait exceeded caller budget")
				case <-time.After(5 * time.Millisecond):
				}
			}
			if pg {
				err = held.Rollback()
			} else {
				err = heldConn.Close()
			}
			if err != nil {
				t.Fatal(err)
			}
			result := publicationHeldResult(t, done)
			if !result.IsError || len(d.inserted) != 0 {
				t.Fatalf("expired credential published vector: %s / %v", toolResultText(result), d.inserted)
			}
		})
	}
}
