package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stek0v/levara/pkg/chatimport"
	"github.com/stek0v/levara/pkg/llm"
)

type distillContextKey struct{}

type distillContextProvider struct {
	call func(context.Context, llm.CompletionRequest) (*llm.CompletionResponse, error)
}

func (*distillContextProvider) Name() string { return "local-context-fixture" }
func (p *distillContextProvider) ChatCompletion(ctx context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	return p.call(ctx, req)
}

// Embedding Deps preserves each native driver's placeholder rewrite.
type distillContextDeps struct {
	Deps
	provider llm.Provider
	embed    func(context.Context, string) ([]float32, error)
	inserted []string
	onInsert func()
	checks   int
}

func (d *distillContextDeps) LLMProvider() llm.Provider { return d.provider }
func (d *distillContextDeps) EmbedAvailable() bool      { return d.embed != nil }
func (d *distillContextDeps) Embed(ctx context.Context, text string) ([]float32, error) {
	return d.embed(ctx, text)
}
func (d *distillContextDeps) CollectionInsert(collection, id string, _ []float32, _ any) error {
	d.inserted = append(d.inserted, collection+"/"+id)
	if d.onInsert != nil {
		d.onInsert()
	}
	return nil
}
func (d *distillContextDeps) CollectionInsertDeferredHook(collection, id string, vec []float32, meta any) (func(), error) {
	return nil, d.CollectionInsert(collection, id, vec, meta)
}
func (d *distillContextDeps) CollectionHasRecord(string, string) bool {
	d.checks++
	return true
}

func distillContextFixture(t *testing.T, pg bool, messages [][3]string) (*memoryCommitEvidenceDeps, context.Context) {
	t.Helper()
	d, ctx := memoryCommitEvidenceFixture(t, pg)
	distillAuthorityPolicySchema(t, d)
	if pg {
		for _, column := range []string{"created_at", "updated_at"} {
			if _, err := d.DB().Exec("ALTER TABLE memories ALTER COLUMN " + column + " TYPE TIMESTAMPTZ USING " + column + "::timestamptz"); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := chatimport.EnsureSchema(ctx, d.DB(), d.Q); err != nil {
		t.Fatal(err)
	}
	scope := distillAuthorityScope(d)
	run, err := chatimport.StartScopedRun(ctx, d.DB(), d.Q, scope, chatimport.RunInfo{ID: "context-import", Platform: chatimport.PlatformCodex, StartedAt: "2026-10-06T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	conv := &chatimport.Conversation{Platform: chatimport.PlatformCodex, SessionID: "context-session", Title: "Context check"}
	for i, message := range messages {
		conv.Messages = append(conv.Messages, chatimport.Message{ExternalID: fmt.Sprintf("external-%d", i), Ordinal: i, Role: message[0], Kind: chatimport.Kind(message[1]), Content: message[2]})
	}
	if _, _, err := chatimport.InsertScopedConversation(ctx, d.DB(), d.Q, scope, run, conv, nil, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct{ id, key, owner, collection string }{
		{"target-id", "target", "owner-a", "levara"},
		{"foreign", "target", "owner-b", "levara"},
		{"shared", "target", "", "levara"},
		{"sibling", "target", "owner-a", "sibling"},
		{"other-key", "control", "owner-a", "levara"},
	} {
		_, err := d.DB().Exec(d.Q(`INSERT INTO memories
			(id,key,value,type,owner_id,collection_name,room,hall,is_pinned,pin_priority,verification_status,source_task_id,source_receipt_ids,created_at,updated_at)
			VALUES($1,$2,'Original control text','project',$3,$4,'memory','fact',FALSE,0,'unverified','','[]','2026-10-05T00:00:00Z','2026-10-05T00:00:00Z')`), row.id, row.key, row.owner, row.collection)
		if err != nil {
			t.Fatal(err)
		}
	}
	return d, context.WithValue(ctx, distillContextKey{}, "request-metadata")
}

func distillContextArgs() map[string]any {
	return map[string]any{"platform": "codex", "session_id": "context-session", "collection": "levara", "hall": "discovery"}
}

func TestChatDistillContext(t *testing.T) {
	for _, dialect := range []struct {
		name string
		pg   bool
	}{{"sqlite", false}, {"postgres", true}} {
		t.Run(dialect.name, func(t *testing.T) {
			for _, scenario := range []string{"positive", "caller-cancel", "earlier-deadline", "late-preview", "late-empty", "index-cancel", "started-insert-cancel"} {
				t.Run(scenario, func(t *testing.T) {
					base, caller := distillContextFixture(t, dialect.pg, [][3]string{{"user", "text", "A new proposal."}, {"assistant", "text", "The proposal remains unverified."}})
					before := distillEvidenceSnapshot(t, base)
					deadline := time.Now().Add(5 * time.Second)
					if scenario == "earlier-deadline" {
						deadline = time.Now().Add(time.Second)
					}
					ctx, cancel := context.WithDeadline(caller, deadline)
					defer cancel()
					calls, observedCancel := 0, false
					deps := &distillContextDeps{Deps: base}
					deps.provider = &distillContextProvider{call: func(modelCtx context.Context, _ llm.CompletionRequest) (*llm.CompletionResponse, error) {
						calls++
						if scenario == "positive" || scenario == "earlier-deadline" {
							gotDeadline, ok := modelCtx.Deadline()
							if modelCtx.Value(distillContextKey{}) != "request-metadata" || !ok || !gotDeadline.Equal(deadline) {
								t.Errorf("model lost caller values/earlier deadline: value=%v deadline=%v want=%v", modelCtx.Value(distillContextKey{}), gotDeadline, deadline)
								// Bound the original RED: do not wait four minutes on its detached context.
								if scenario == "earlier-deadline" {
									return &llm.CompletionResponse{Content: `[{"key":"target","value":"Late model text"}]`}, nil
								}
							}
						}
						if scenario == "earlier-deadline" {
							<-modelCtx.Done()
							observedCancel = modelCtx.Err() == context.DeadlineExceeded
							return nil, modelCtx.Err()
						}
						if scenario == "caller-cancel" || scenario == "late-preview" || scenario == "late-empty" {
							cancel()
							observedCancel = modelCtx.Err() == context.Canceled
							if scenario == "caller-cancel" && observedCancel {
								return nil, modelCtx.Err()
							}
						}
						if scenario == "late-empty" {
							return &llm.CompletionResponse{Content: `[]`}, nil
						}
						return &llm.CompletionResponse{Content: `[{"key":"target","value":"New model text"},{"key":"later","value":"Second candidate"}]`}, nil
					}}
					if scenario == "positive" || scenario == "index-cancel" || scenario == "started-insert-cancel" {
						deps.embed = func(embedCtx context.Context, _ string) ([]float32, error) {
							gotDeadline, ok := embedCtx.Deadline()
							if embedCtx.Value(distillContextKey{}) != "request-metadata" || !ok || !gotDeadline.Equal(deadline) {
								t.Errorf("embedding lost caller context: value=%v deadline=%v want=%v", embedCtx.Value(distillContextKey{}), gotDeadline, deadline)
							}
							if scenario == "index-cancel" {
								cancel()
								observedCancel = embedCtx.Err() == context.Canceled
								// A late successful embed must still not publish a vector.
							}
							return []float32{1, 0}, nil
						}
						if scenario == "started-insert-cancel" {
							deps.onInsert = cancel
						}
					}
					args := distillContextArgs()
					args["dry_run"] = scenario == "late-preview"
					result := ToolChatDistill(ctx, deps, args)
					if scenario != "positive" && !result.IsError {
						t.Errorf("canceled operation returned success: %s", toolResultText(result))
					}
					if scenario == "positive" && result.IsError {
						t.Fatal(toolResultText(result))
					}
					if calls != 1 {
						t.Errorf("provider calls=%d want 1; cancellation must stop retry", calls)
					}
					if scenario != "positive" && scenario != "started-insert-cancel" && !observedCancel {
						t.Error("model/embed operation did not observe caller cancellation")
					}
					after := distillEvidenceSnapshot(t, deps)
					committed := scenario == "positive" || scenario == "index-cancel" || scenario == "started-insert-cancel"
					if committed {
						var id, value, status, task, receipts string
						if err := deps.DB().QueryRow(deps.Q(`SELECT id,value,verification_status,source_task_id,source_receipt_ids FROM memories
							WHERE key='target' AND owner_id='owner-a' AND collection_name='levara'`)).Scan(&id, &value, &status, &task, &receipts); err != nil {
							t.Fatal(err)
						}
						if id != "target-id" || !strings.Contains(value, "New model text [источник: codex session context-sessi") || status != "unverified" || task != "" || receipts != "[]" {
							t.Errorf("committed canonical result/evidence incorrect: %q/%q/%q/%q/%q", id, value, status, task, receipts)
						}
						for _, column := range []string{"value", "room", "hall", "updated_at", "verification_status", "source_task_id", "source_receipt_ids"} {
							delete(before["memories"][id], column)
							delete(after["memories"][id], column)
						}
						if scenario == "positive" {
							var laterID string
							if err := deps.DB().QueryRow("SELECT id FROM memories WHERE key='later'").Scan(&laterID); err != nil {
								t.Fatal(err)
							}
							delete(after["memories"], laterID)
							if len(deps.inserted) != 2 || deps.inserted[0] != "_memories_levara/target-id" {
								t.Errorf("positive indexing lacks canonical scope: %v", deps.inserted)
							}
						} else if scenario == "started-insert-cancel" {
							if len(deps.inserted) != 1 || deps.checks != 0 {
								t.Errorf("insert already started may finish, but no later insert/verification may start: inserted=%v checks=%d", deps.inserted, deps.checks)
							}
						} else if len(deps.inserted) != 0 {
							t.Errorf("late embedding published vectors after cancellation: %v", deps.inserted)
						}
					}
					if !reflect.DeepEqual(before, after) {
						t.Error("canceled work changed later candidates or unrelated memory/import/Task state")
					}
				})
			}
		})
	}
}

func TestChatDistillContextEmptyInput(t *testing.T) {
	for _, pg := range []bool{false, true} {
		t.Run(fmt.Sprint(pg), func(t *testing.T) {
			for _, scenario := range []string{"missing", "zero-message", "whitespace", "developer-only", "nonempty-tools"} {
				t.Run(scenario, func(t *testing.T) {
					messages := [][3]string{}
					switch scenario {
					case "whitespace":
						messages = [][3]string{{"user", "text", " \t\n "}, {"assistant", "text", " "}, {"assistant", "reasoning", "\n"}, {"tool", "tool_result", "\t"}, {"system", "system", " "}}
					case "developer-only":
						messages = [][3]string{{"developer", "system", "Harness instructions"}}
					case "nonempty-tools":
						messages = [][3]string{{"system", "system", "System context"}, {"assistant", "tool_call", "Tool invocation"}, {"tool", "tool_result", "Tool output"}}
					}
					base, ctx := distillContextFixture(t, pg, messages)
					calls := 0
					deps := &distillContextDeps{Deps: base, provider: &distillContextProvider{call: func(_ context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
						calls++
						if scenario == "nonempty-tools" {
							for _, expected := range []string{"[system] System context", "[tool_call] Tool invocation", "[tool_result] Tool output"} {
								if !strings.Contains(req.Messages[0].Content, expected) {
									t.Errorf("existing nonempty tool rendering lost %q", expected)
								}
							}
						}
						return &llm.CompletionResponse{Content: `[{"key":"target","value":"Preview text"}]`}, nil
					}}}
					args := distillContextArgs()
					args["dry_run"] = true
					if scenario == "missing" {
						args["session_id"] = "missing-session"
					}
					before := distillEvidenceSnapshot(t, deps)
					result := ToolChatDistill(ctx, deps, args)
					if scenario == "nonempty-tools" {
						var payload struct {
							DryRun     bool               `json:"dry_run"`
							Candidates []DistillCandidate `json:"candidates"`
						}
						if result.IsError || calls != 1 || json.Unmarshal([]byte(toolResultText(result)), &payload) != nil || !payload.DryRun || len(payload.Candidates) != 1 {
							t.Errorf("nonempty tool/system compatibility failed: calls=%d result=%s", calls, toolResultText(result))
						}
					} else if !result.IsError || calls != 0 {
						t.Errorf("empty dialogue must fail before model: calls=%d result=%s", calls, toolResultText(result))
					}
					if !reflect.DeepEqual(before, distillEvidenceSnapshot(t, deps)) {
						t.Error("empty input or preview changed SQL state")
					}
				})
			}
		})
	}
}

func TestChatDistillContextOrdinarySaveDetachedIndex(t *testing.T) {
	for _, pg := range []bool{false, true} {
		t.Run(fmt.Sprint(pg), func(t *testing.T) {
			base, caller := distillContextFixture(t, pg, nil)
			ctx, cancel := context.WithCancel(caller)
			defer cancel()
			deps := &distillContextDeps{Deps: base}
			deps.embed = func(embedCtx context.Context, _ string) ([]float32, error) {
				cancel() // SQL has committed; ordinary save's indexing retains its existing detached lifetime.
				if embedCtx.Err() != nil || embedCtx.Value(distillContextKey{}) != nil {
					t.Error("ordinary save wrapper inherited caller cancellation/metadata")
				}
				return []float32{1, 0}, nil
			}
			result := ToolSaveMemory(ctx, deps, map[string]any{"key": "target", "value": "Ordinary save", "collection": "levara", "room": "memory", "hall": "fact"})
			if result.IsError || len(deps.inserted) != 1 || deps.inserted[0] != "_memories_levara/target-id" || deps.checks != 1 {
				t.Fatalf("ordinary detached wrapper changed: result=%s inserted=%v checks=%d", toolResultText(result), deps.inserted, deps.checks)
			}
		})
	}
}
