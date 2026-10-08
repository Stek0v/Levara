package mcp

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stek0v/levara/pkg/access"
)

func TestMemoryWriteEvidence(t *testing.T) {
	for _, pg := range []bool{false, true} {
		t.Run(fmt.Sprint(pg), func(t *testing.T) {
			for _, operation := range []string{"save", "supersede"} {
				t.Run(operation, func(t *testing.T) {
					for _, proof := range []string{"none", "valid", "missing task", "missing receipt", "foreign task", "foreign receipt", "wrong collection", "failed command", "missing exit", "stale revision", "duplicate", "malformed", "artifact", "artifact valid"} {
						t.Run(proof, func(t *testing.T) {
							d, ctx := memoryCommitEvidenceFixture(t, pg)
							task, receipt := memoryCommitOwnedTaskReceipt(t, d, ctx)
							seed := map[string]any{"key": "claim", "value": "old", "collection": "levara", "room": "memory", "hall": "fact"}
							if result := ToolSaveMemory(ctx, d, seed); result.IsError {
								t.Fatal(toolResultText(result))
							}
							var oldID string
							if err := d.DB().QueryRow("SELECT id FROM memories WHERE key='claim'").Scan(&oldID); err != nil {
								t.Fatal(err)
							}
							args := map[string]any{"key": "claim", "value": "new", "new_value": "new", "old_memory_id": oldID, "reason": "new proof", "collection": "levara", "room": "memory", "hall": "fact", "verification_status": "verified"}
							if proof != "none" {
								args["source_task_id"], args["source_receipt_ids"] = task, []any{receipt}
							}
							query := ""
							id := receipt
							switch proof {
							case "missing task":
								args["source_task_id"] = "missing"
							case "missing receipt":
								args["source_receipt_ids"] = []any{"missing"}
							case "foreign task":
								query, id = "UPDATE tasks SET owner_id='owner-b' WHERE id=$1", task
							case "foreign receipt":
								query = "UPDATE task_receipts SET owner_id='owner-b' WHERE id=$1"
							case "wrong collection":
								query, id = "UPDATE tasks SET collection_name='other' WHERE id=$1", task
							case "failed command":
								query = "UPDATE task_receipts SET exit_code=7 WHERE id=$1"
							case "missing exit":
								query = "UPDATE task_receipts SET exit_code=NULL WHERE id=$1"
							case "stale revision":
								query, id = "UPDATE tasks SET current_workspace_revision='rev-2' WHERE id=$1", task
							case "duplicate":
								args["source_receipt_ids"] = []any{receipt, receipt}
							case "malformed":
								args["source_task_id"], args["source_receipt_ids"] = 7, []any{nil}
							case "artifact", "artifact valid":
								query = "UPDATE task_receipts SET receipt_type='artifact',evidence_uri='file:///proof',artifact_digest='" + strings.Repeat("a", 64) + "' WHERE id=$1"
								d.verify = func(artifactCtx context.Context, _, _ string) error {
									if proof == "artifact valid" {
										if _, ok := ArtifactReadPolicy(artifactCtx); !ok {
											t.Error("artifact verifier is outside publication transaction")
										}
										return nil
									}
									return fmt.Errorf("artifact changed")
								}
							}
							if query != "" {
								if _, err := d.DB().Exec(d.Q(query), id); err != nil {
									t.Fatal(err)
								}
							}
							var result ToolResult
							if operation == "save" {
								result = ToolSaveMemory(ctx, d, args)
							} else {
								result = ToolSupersedeMemory(ctx, d, args)
							}
							accepted := proof == "none" || proof == "valid" || proof == "artifact valid"
							if result.IsError == accepted {
								t.Errorf("accepted=%v result=%s", accepted, toolResultText(result))
							}
							var value, status, activeID string
							if err := d.DB().QueryRow("SELECT id,value,verification_status FROM memories WHERE key='claim' AND superseded_by=''").Scan(&activeID, &value, &status); err != nil {
								t.Fatal(err)
							}
							if !accepted {
								if activeID != oldID || value != "old" {
									t.Errorf("rejected proof changed memory: id=%s value=%s", activeID, value)
								}
								return
							}
							wantStatus := "unverified"
							if proof == "valid" || proof == "artifact valid" {
								wantStatus = "receipt-validated"
							}
							if value != "new" || status != wantStatus {
								t.Errorf("value=%q status=%q; want new / %s", value, status, wantStatus)
							}
						})
					}
				})
			}
		})
	}
}

func TestTaskTrustFailedProofDoesNotPromote(t *testing.T) {
	for _, pg := range []bool{false, true} {
		t.Run(fmt.Sprint(pg), func(t *testing.T) {
			for _, proof := range []string{"valid", "exit7", "missing exit", "foreign receipt", "artifact", "artifact valid"} {
				t.Run(proof, func(t *testing.T) {
					d, ctx := memoryCommitEvidenceFixture(t, pg)
					task, receipt := memoryCommitOwnedTaskReceipt(t, d, ctx)
					query := ""
					switch proof {
					case "exit7":
						query = "UPDATE task_receipts SET exit_code=7 WHERE id=$1"
					case "missing exit":
						query = "UPDATE task_receipts SET exit_code=NULL WHERE id=$1"
					case "foreign receipt":
						query = "UPDATE task_receipts SET owner_id='owner-b' WHERE id=$1"
					case "artifact", "artifact valid":
						query = "UPDATE task_receipts SET receipt_type='artifact',evidence_uri='file:///proof',artifact_digest='" + strings.Repeat("a", 64) + "' WHERE id=$1"
						d.verify = func(artifactCtx context.Context, _, _ string) error {
							if proof == "artifact valid" {
								if _, ok := ArtifactReadPolicy(artifactCtx); !ok {
									t.Error("artifact verifier is outside publication transaction")
								}
								return nil
							}
							return fmt.Errorf("artifact changed")
						}
					}
					if query != "" {
						if _, err := d.DB().Exec(d.Q(query), receipt); err != nil {
							t.Fatal(err)
						}
					}
					var version int
					if err := d.DB().QueryRow(d.Q("SELECT version FROM tasks WHERE id=$1"), task).Scan(&version); err != nil {
						t.Fatal(err)
					}
					good := taskPayload(t, ToolTaskReceipt(ctx, d, map[string]any{"task_id": task, "base_version": version, "idempotency_key": "good", "receipt_type": "command", "status": "pass", "exit_code": float64(0), "criterion_ids": []any{"check"}, "workspace_revision": "rev-1"}))
					checkpoint := taskPayload(t, ToolTaskCheckpoint(ctx, d, map[string]any{"task_id": task, "base_version": good["version"], "idempotency_key": "candidate", "summary": "checked", "workspace_revision": "rev-1", "memory_candidates": []any{map[string]any{"key": "claim", "value": "durable fact", "room": "memory", "hall": "fact", "evidence_receipt_ids": []any{receipt}}}}))
					completed := taskPayload(t, ToolTaskComplete(ctx, d, map[string]any{"task_id": task, "expected_version": checkpoint["version"]}))
					wantPromoted, wantRejected := float64(0), float64(1)
					if proof == "valid" || proof == "artifact valid" {
						wantPromoted, wantRejected = 1, 0
					}
					if completed["promoted_memories"] != wantPromoted || completed["rejected_memories"] != wantRejected {
						t.Errorf("completion=%v", completed)
					}
					var count int
					if err := d.DB().QueryRow("SELECT COUNT(*) FROM memories WHERE key='claim'").Scan(&count); err != nil || count != int(wantPromoted) {
						t.Errorf("published=%d want=%v error=%v", count, wantPromoted, err)
					}
					if proof == "valid" || proof == "artifact valid" {
						var status string
						if err := d.DB().QueryRow("SELECT verification_status FROM memories WHERE key='claim'").Scan(&status); err != nil || status != "receipt-validated" {
							t.Errorf("status=%s error=%v", status, err)
						}
					}
				})
			}
		})
	}
}

func TestTaskTrustCommandNeedsExplicitZero(t *testing.T) {
	for _, pg := range []bool{false, true} {
		t.Run(fmt.Sprint(pg), func(t *testing.T) {
			for _, exit := range []any{"missing", float64(7), nil, "0", float64(0.5), float64(0)} {
				t.Run(fmt.Sprint(exit), func(t *testing.T) {
					d, ctx := memoryCommitEvidenceFixture(t, pg)
					opened := taskPayload(t, ToolTaskOpen(ctx, d, map[string]any{"collection": "levara", "room": "memory", "objective": "command proof", "idempotency_key": "open", "definition_of_done": []any{map[string]any{"criterion_id": "check", "description": "passed"}}}))
					args := map[string]any{"task_id": opened["task_id"], "base_version": opened["version"], "idempotency_key": "proof", "receipt_type": "command", "status": "pass", "criterion_ids": []any{"check"}, "workspace_revision": "rev-1"}
					if exit != "missing" {
						args["exit_code"] = exit
					}
					result := ToolTaskReceipt(ctx, d, args)
					malformed := exit == nil || exit == "0" || exit == float64(0.5)
					if malformed {
						if !result.IsError {
							t.Fatal("malformed exit code accepted as success")
						}
						return
					}
					receipt := taskPayload(t, result)
					validation := taskPayload(t, ToolTaskValidate(ctx, d, map[string]any{"task_id": opened["task_id"]}))
					valid := exit == float64(0)
					if validation["valid"] != valid {
						t.Errorf("validation=%v, valid=%v", validation, valid)
					}
					completion := taskPayload(t, ToolTaskComplete(ctx, d, map[string]any{"task_id": opened["task_id"], "expected_version": receipt["version"]}))
					if completion["ok"] != valid {
						t.Errorf("completion=%v, valid=%v", completion, valid)
					}
				})
			}
		})
	}
}

func TestSupersedeTrustSharedAuthority(t *testing.T) {
	for _, pg := range []bool{false, true} {
		t.Run(fmt.Sprint(pg), func(t *testing.T) {
			for _, mode := range []string{"user", "claimed admin", "admin", "revoked admin", "revoked credential", "foreign", "trusted local"} {
				t.Run(mode, func(t *testing.T) {
					d, ctx := memoryCommitEvidenceFixture(t, pg)
					task, receipt := memoryCommitOwnedTaskReceipt(t, d, ctx)
					if _, err := d.DB().Exec("INSERT INTO memories(id,key,value,type,owner_id,collection_name,room,hall) VALUES ('target','claim','old','project','','levara','memory','fact')"); err != nil {
						t.Fatal(err)
					}
					if mode == "admin" || mode == "revoked credential" {
						if _, err := d.DB().Exec("UPDATE users SET is_superuser=true WHERE id='owner-a'"); err != nil {
							t.Fatal(err)
						}
					}
					if mode == "claimed admin" || mode == "revoked admin" {
						d.actor.Superuser = true
					}
					if mode == "revoked credential" {
						if _, err := d.DB().Exec("UPDATE users SET is_active=false WHERE id='owner-a'"); err != nil {
							t.Fatal(err)
						}
					}
					if mode == "foreign" {
						if _, err := d.DB().Exec("UPDATE memories SET owner_id='owner-b' WHERE id='target'"); err != nil {
							t.Fatal(err)
						}
					}
					if mode == "trusted local" {
						d.actor.TrustedLocal = true
						d.actor.Credential = access.MetadataCredential{}
					}
					result := ToolSupersedeMemory(ctx, d, map[string]any{"old_memory_id": "target", "new_value": "new", "reason": "new proof", "source_task_id": task, "source_receipt_ids": []any{receipt}})
					allowed := mode == "admin" || mode == "trusted local"
					if result.IsError == allowed {
						t.Errorf("allowed=%v result=%s", allowed, toolResultText(result))
					}
					var value, owner string
					if err := d.DB().QueryRow("SELECT value,owner_id FROM memories WHERE key='claim'").Scan(&value, &owner); err != nil {
						t.Fatal(err)
					}
					if allowed {
						if value != "new" || owner != "" {
							t.Errorf("shared scope changed: value=%q owner=%q", value, owner)
						}
					} else if value != "old" {
						t.Errorf("denied write mutated target: %q", value)
					}
				})
			}
		})
	}
}

func TestTaskTrustPromotionDoesNotMutateSharedFact(t *testing.T) {
	for _, pg := range []bool{false, true} {
		t.Run(fmt.Sprint(pg), func(t *testing.T) {
			d, ctx := memoryCommitEvidenceFixture(t, pg)
			task, receipt := memoryCommitOwnedTaskReceipt(t, d, ctx)
			if _, err := d.DB().Exec("INSERT INTO memories(id,key,value,type,owner_id,collection_name,room,hall) VALUES ('shared','claim','public fact','project','','levara','memory','fact')"); err != nil {
				t.Fatal(err)
			}
			var version int
			if err := d.DB().QueryRow(d.Q("SELECT version FROM tasks WHERE id=$1"), task).Scan(&version); err != nil {
				t.Fatal(err)
			}
			checkpoint := taskPayload(t, ToolTaskCheckpoint(ctx, d, map[string]any{"task_id": task, "base_version": version, "idempotency_key": "candidate", "summary": "checked", "workspace_revision": "rev-1", "memory_candidates": []any{map[string]any{"key": "claim", "value": "private fact", "room": "memory", "hall": "fact", "evidence_receipt_ids": []any{receipt}}}}))
			completed := taskPayload(t, ToolTaskComplete(ctx, d, map[string]any{"task_id": task, "expected_version": checkpoint["version"]}))
			if completed["promoted_memories"] != float64(1) {
				t.Fatal(completed)
			}
			var value, superseded string
			if err := d.DB().QueryRow("SELECT value,superseded_by FROM memories WHERE id='shared'").Scan(&value, &superseded); err != nil || value != "public fact" || superseded != "" {
				t.Fatalf("shared value=%q superseded=%q error=%v", value, superseded, err)
			}
			var status string
			if err := d.DB().QueryRow("SELECT value,verification_status FROM memories WHERE key='claim' AND owner_id='owner-a'").Scan(&value, &status); err != nil || value != "private fact" || status != "receipt-validated" {
				t.Fatalf("private value=%q status=%q error=%v", value, status, err)
			}
		})
	}
}

// Prepared commits lock memories before reading task evidence. Every writer
// must take the same order, otherwise completing that task can deadlock it.
func TestMemoryTrustPostgresPublicationLockOrder(t *testing.T) {
	for _, operation := range []string{"save", "completion"} {
		t.Run(operation, func(t *testing.T) {
			d, ctx := memoryCommitEvidenceFixture(t, true)
			memoryCommitParallelPool(t, d, true)
			ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			task, receipt := memoryCommitOwnedTaskReceipt(t, d, ctx)
			var version int
			if err := d.DB().QueryRow(d.Q("SELECT version FROM tasks WHERE id=$1"), task).Scan(&version); err != nil {
				t.Fatal(err)
			}
			checkpoint := taskPayload(t, ToolTaskCheckpoint(ctx, d, map[string]any{"task_id": task, "base_version": version, "idempotency_key": "candidate", "summary": "checked", "workspace_revision": "rev-1", "memory_candidates": []any{map[string]any{"key": "claim", "value": "fact", "room": "memory", "hall": "fact", "evidence_receipt_ids": []any{receipt}}}}))
			fence, err := d.DB().BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer fence.Rollback()
			if _, err := fence.ExecContext(ctx, "LOCK TABLE memories IN SHARE ROW EXCLUSIVE MODE"); err != nil {
				t.Fatal(err)
			}
			done := make(chan ToolResult, 1)
			go func() {
				if operation == "save" {
					done <- ToolSaveMemory(ctx, d, map[string]any{"key": "claim", "value": "fact", "room": "memory", "hall": "fact", "collection": "levara", "source_task_id": task, "source_receipt_ids": []any{receipt}})
				} else {
					done <- ToolTaskComplete(ctx, d, map[string]any{"task_id": task, "expected_version": checkpoint["version"]})
				}
			}()
			mode := "RowExclusiveLock"
			if operation == "completion" {
				mode = "ShareRowExclusiveLock"
			}
			for {
				var waiting bool
				if err := d.DB().QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_locks WHERE relation='memories'::regclass AND mode=$1 AND NOT granted)", mode).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			var taskID string
			lockErr := fence.QueryRowContext(ctx, "SELECT id FROM tasks WHERE id=$1 FOR UPDATE NOWAIT", task).Scan(&taskID)
			_ = fence.Rollback()
			select {
			case result := <-done:
				if result.IsError {
					t.Fatal(toolResultText(result))
				}
			case <-ctx.Done():
				t.Fatal("publication did not finish after releasing the fence")
			}
			if lockErr != nil {
				t.Fatalf("writer locked task before memories, reversing the prepared-commit order: %v", lockErr)
			}
		})
	}
}
