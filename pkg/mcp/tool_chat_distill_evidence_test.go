package mcp

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/stek0v/levara/pkg/chatimport"
	"github.com/stek0v/levara/pkg/llm"
)

// Embed Deps rather than fakeDeps so PostgreSQL keeps its native placeholders.
type distillEvidenceDeps struct {
	Deps
	provider llm.Provider
}

func (d *distillEvidenceDeps) LLMProvider() llm.Provider { return d.provider }

func TestChatDistillEvidenceReset(t *testing.T) {
	for _, dialect := range []struct {
		name string
		pg   bool
	}{{"sqlite", false}, {"postgres", true}} {
		t.Run(dialect.name, func(t *testing.T) {
			for _, scenario := range []string{"receipt-validated-overwrite", "legacy-verified-overwrite", "fresh-insert", "dry-run"} {
				t.Run(scenario, func(t *testing.T) {
					base, ctx := memoryCommitEvidenceFixture(t, dialect.pg)
					distillAuthorityPolicySchema(t, base)
					if dialect.pg {
						for _, column := range []string{"created_at", "updated_at"} {
							if _, err := base.DB().Exec("ALTER TABLE memories ALTER COLUMN " + column + " TYPE TIMESTAMPTZ USING " + column + "::timestamptz"); err != nil {
								t.Fatal(err)
							}
						}
					}
					taskID, receiptID := memoryCommitOwnedTaskReceipt(t, base, ctx)
					receipts, err := json.Marshal([]string{receiptID})
					if err != nil {
						t.Fatal(err)
					}

					// Use the actual importer schema and writer, with a local model stub.
					const sessionID, title = "distill-proof", "Evidence replacement"
					now := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
					if err := chatimport.EnsureSchema(ctx, base.DB(), base.Q); err != nil {
						t.Fatal(err)
					}
					scope := distillAuthorityScope(base)
					runID, err := chatimport.StartScopedRun(ctx, base.DB(), base.Q, scope, chatimport.RunInfo{ID: "import-run", Platform: chatimport.PlatformCodex, StartedAt: now.Format(time.RFC3339)})
					if err != nil {
						t.Fatal(err)
					}
					var warnings []string
					chat, inserted, err := chatimport.InsertScopedConversation(ctx, base.DB(), base.Q, scope, runID, &chatimport.Conversation{
						Platform: chatimport.PlatformCodex, SessionID: sessionID, Title: title,
						Messages: []chatimport.Message{
							{ExternalID: "user-1", Ordinal: 0, Role: "user", Kind: chatimport.KindText, Content: "Discuss a different proposal.", CreatedAt: now.Format(time.RFC3339)},
							{ExternalID: "assistant-1", Ordinal: 1, Role: "assistant", Kind: chatimport.KindText, Content: "This proposal has not been checked.", CreatedAt: now.Format(time.RFC3339)},
						},
					}, &warnings, now)
					if err != nil || inserted != 2 {
						t.Fatalf("actual import: inserted=%d error=%v", inserted, err)
					}
					finishTx, err := base.DB().BeginTx(ctx, nil)
					if err != nil {
						t.Fatal(err)
					}
					defer finishTx.Rollback()
					var runOwner, runTenant string
					if err := finishTx.QueryRowContext(ctx, base.Q("SELECT owner_id,tenant_id FROM chat_import_run_scopes WHERE run_id=$1"), runID).Scan(&runOwner, &runTenant); err != nil || runOwner != scope.OwnerID || runTenant != scope.TenantID {
						t.Fatalf("finish scope: %q/%q/%v", runOwner, runTenant, err)
					}
					if err := chatimport.FinishRunTx(ctx, finishTx, base.Q, runID, "ok", inserted, 0, len(warnings), warnings, now.Add(time.Second).Format(time.RFC3339)); err != nil {
						t.Fatal(err)
					}

					if err := finishTx.Commit(); err != nil {
						t.Fatal(err)
					}

					const key = "owned-claim"
					selectedID := ""
					if scenario != "fresh-insert" {
						result := ToolSaveMemory(ctx, base, map[string]any{
							"key": key, "value": "Previously checked text.", "collection": "levara", "room": "memory", "hall": "decision",
							"source_task_id": taskID, "source_receipt_ids": []any{receiptID}, "verification_status": "verified",
						})
						if result.IsError {
							t.Fatal(toolResultText(result))
						}
						var status, sourceTask, sourceReceipts string
						if err := base.DB().QueryRow(base.Q("SELECT id,verification_status,source_task_id,source_receipt_ids FROM memories WHERE key=$1 AND owner_id=$2 AND collection_name=$3"), key, "owner-a", "levara").Scan(&selectedID, &status, &sourceTask, &sourceReceipts); err != nil {
							t.Fatal(err)
						}
						if status != "receipt-validated" || sourceTask != taskID || sourceReceipts != string(receipts) {
							t.Fatalf("actual save lacks validated evidence: %q / %q / %q", status, sourceTask, sourceReceipts)
						}
						if _, err := base.DB().Exec(base.Q("UPDATE memories SET is_pinned=true,pin_priority=8,created_at=$1,updated_at=$2 WHERE id=$3"), now.Add(-time.Hour).Format(time.RFC3339), now.Format(time.RFC3339), selectedID); err != nil {
							t.Fatal(err)
						}
						if scenario == "legacy-verified-overwrite" {
							if _, err := base.DB().Exec(base.Q("UPDATE memories SET verification_status='verified' WHERE id=$1"), selectedID); err != nil {
								t.Fatal(err)
							}
						}
					}
					for i, control := range []struct{ key, owner, collection string }{
						{key, "owner-b", "levara"}, {key, "", "levara"}, {key, "owner-a", "other"}, {"other-key", "owner-a", "levara"},
					} {
						if _, err := base.DB().Exec(base.Q(`INSERT INTO memories
							(id,key,value,type,owner_id,collection_name,room,hall,is_pinned,pin_priority,verification_status,source_task_id,source_receipt_ids,created_at,updated_at)
							VALUES ($1,$2,$3,'project',$4,$5,'memory','decision',true,5,'verified',$6,$7,$8,$9)`),
							fmt.Sprintf("control-%d", i), control.key, "Control text.", control.owner, control.collection, taskID, string(receipts), now.Format(time.RFC3339), now.Format(time.RFC3339)); err != nil {
							t.Fatal(err)
						}
					}

					// Both model fields and caller fields are hints, not proof for new text.
					const newText = "Unsupported replacement proposed by the model."
					modelJSON, err := json.Marshal([]map[string]any{{"key": key, "value": newText,
						"verification_status": "receipt-validated", "source_task_id": taskID, "source_receipt_ids": []string{receiptID}}})
					if err != nil {
						t.Fatal(err)
					}
					deps := &distillEvidenceDeps{Deps: base, provider: &stubDistillProvider{content: string(modelJSON)}}
					before := distillEvidenceSnapshot(t, deps)
					result := ToolChatDistill(ctx, deps, map[string]any{
						"platform": "codex", "session_id": sessionID, "collection": "levara", "room": "chat-import", "hall": "discovery",
						"dry_run": scenario == "dry-run", "verification_status": "verified", "source_task_id": taskID, "source_receipt_ids": []any{receiptID},
					})
					if result.IsError {
						t.Fatal(toolResultText(result))
					}
					var payload struct {
						SessionID  string             `json:"session_id"`
						Platform   string             `json:"platform"`
						Hall       string             `json:"hall"`
						Saved      int                `json:"saved"`
						DryRun     bool               `json:"dry_run"`
						Candidates []DistillCandidate `json:"candidates"`
					}
					if err := json.Unmarshal([]byte(toolResultText(result)), &payload); err != nil {
						t.Fatal(err)
					}
					wantValue := newText + " [источник: codex session " + sessionID + ", " + title + "] [chat_id: " + chat.ID + "]"
					if payload.SessionID != sessionID || payload.Platform != "codex" || payload.Hall != "discovery" || len(payload.Candidates) != 1 || payload.Candidates[0].Key != key || payload.Candidates[0].Value != wantValue {
						t.Fatalf("result/provenance changed: %+v", payload)
					}
					after := distillEvidenceSnapshot(t, deps)
					if scenario == "dry-run" {
						if !payload.DryRun || payload.Saved != 0 || !reflect.DeepEqual(before, after) {
							t.Fatalf("dry-run changed SQL state or result: %+v", payload)
						}
						return
					}
					if payload.DryRun || payload.Saved != 1 {
						t.Fatalf("save result changed: %+v", payload)
					}
					var id, value, room, hall, status, sourceTask, sourceReceipts string
					if err := deps.DB().QueryRow(deps.Q("SELECT id,value,room,hall,verification_status,source_task_id,source_receipt_ids FROM memories WHERE key=$1 AND owner_id=$2 AND collection_name=$3"), key, "owner-a", "levara").Scan(&id, &value, &room, &hall, &status, &sourceTask, &sourceReceipts); err != nil {
						t.Fatal(err)
					}
					if selectedID != "" && id != selectedID {
						t.Errorf("canonical ID changed: got %q want %q", id, selectedID)
					}
					if value != wantValue || room != "chat-import" || hall != "discovery" {
						t.Errorf("replacement/provenance not stored: %q / %q / %q", value, room, hall)
					}
					if status != "unverified" || sourceTask != "" || sourceReceipts != "[]" {
						t.Errorf("new LLM text inherited evidence: status=%q task=%q receipts=%q; want unverified / empty / []", status, sourceTask, sourceReceipts)
					}
					if scenario == "fresh-insert" {
						delete(after["memories"], id)
					} else {
						// Only text/classification/freshness and its evidence may change.
						for _, column := range []string{"value", "room", "hall", "updated_at", "verification_status", "source_task_id", "source_receipt_ids"} {
							delete(before["memories"][id], column)
							delete(after["memories"][id], column)
						}
					}
					if !reflect.DeepEqual(before, after) {
						t.Error("distillation changed control rows, canonical fields, task evidence, or import ledger")
					}
				})
			}
		})
	}
}

// Capture every column so unchanged controls also include timestamps and proof.
func distillEvidenceSnapshot(t *testing.T, deps Deps) map[string]map[string]map[string]any {
	t.Helper()
	snapshot := make(map[string]map[string]map[string]any)
	for _, table := range []string{"memories", "tasks", "task_criteria", "task_steps", "task_receipts", "task_checkpoints", "task_blockers", "task_events", "task_memory_candidates", "chat_import_runs", "chat_import_messages", "chat_import_sessions"} {
		rows, err := deps.DB().Query("SELECT * FROM " + table + " ORDER BY id")
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		snapshot[table] = make(map[string]map[string]any)
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range pointers {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			row := make(map[string]any)
			for i, column := range columns {
				if bytes, ok := values[i].([]byte); ok {
					values[i] = string(bytes)
				}
				row[column] = values[i]
			}
			snapshot[table][fmt.Sprint(row["id"])] = row
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return snapshot
}
