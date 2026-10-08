package http

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/stek0v/levara/pkg/mcp"
)

// These fixtures use explicitly trusted local native MCP authority, independent
// native SQL stores and pool size one. They do not claim HTTP authentication,
// independent processes, embedding publication or legacy-peer negotiation.
func TestSyncMemoryNativeLifecycleIndependentStores(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			for _, operation := range []string{"supersede_into_empty_target", "delete_then_replay_predelete_export"} {
				t.Run(operation, func(t *testing.T) {
					source := syncMemoryConflictConfig(t, dialect)
					target := syncMemoryConflictConfig(t, dialect)
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					ctx = context.WithValue(ctx, mcp.UserIDKey, "lifecycle-owner")
					const key = "lifecycle-key"
					const collection = "lifecycle-collection"
					sourceDeps, targetDeps := NewMCPDeps(source), NewMCPDeps(target)
					// Explicit local mode is the authority here. Collection is stored
					// by Save; exact-ID Supersede/Delete resolve it from the native row.
					// No selected tenant, session collection, owner hint or fake credential.
					for _, deps := range []mcp.Deps{sourceDeps, targetDeps} {
						actor := deps.MetadataActor(ctx)
						if !actor.TrustedLocal || actor.UserID != "lifecycle-owner" || actor.TenantID != "" || actor.Credential.Kind != "" {
							t.Fatalf("fixture lacks explicit trusted-local owner authority: %+v", actor)
						}
					}
					checkTool := func(result mcp.ToolResult) {
						t.Helper()
						if result.IsError || len(result.Content) == 0 {
							t.Fatalf("native mutation failed: %+v", result)
						}
					}
					checkTool(mcp.ToolSaveMemory(ctx, sourceDeps, map[string]any{
						"key": key, "value": "original native memory", "type": "project",
						"collection": collection, "room": "memory", "hall": "fact",
					}))
					predelete, err := exportSyncMemories(ctx, source, "")
					if err != nil || len(predelete) != 1 {
						t.Fatalf("native source export=%+v err=%v", predelete, err)
					}
					oldID := predelete[0].ID
					if predelete[0].OwnerID != "lifecycle-owner" || predelete[0].CollectionName != collection || predelete[0].Key != key {
						t.Fatalf("source identity changed: %+v", predelete[0])
					}
					// Cross the actual existing JSON contract rather than hand-building
					// lifecycle metadata that the native source did not produce.
					wire := func(records []syncMemory) []syncMemory {
						t.Helper()
						body, err := json.Marshal(records)
						if err != nil {
							t.Fatal(err)
						}
						var received []syncMemory
						if err := json.Unmarshal(body, &received); err != nil {
							t.Fatal(err)
						}
						return received
					}
					switch operation {
					case "supersede_into_empty_target":
						checkTool(mcp.ToolSupersedeMemory(ctx, sourceDeps, map[string]any{
							"old_memory_id": oldID, "new_value": "replacement native memory", "reason": "native lifecycle regression",
						}))
						var newID, validUntil, reason string
						if err := source.DB.QueryRowContext(ctx, Q(`SELECT superseded_by,COALESCE(CAST(valid_until AS TEXT),''),supersession_reason FROM memories WHERE id=$1 AND owner_id=$2 AND collection_name=$3`), oldID, "lifecycle-owner", collection).Scan(&newID, &validUntil, &reason); err != nil {
							t.Fatal(err)
						}
						if newID == "" || validUntil == "" || reason != "native lifecycle regression" {
							t.Fatalf("source was not actually retired: successor=%q until=%q reason=%q", newID, validUntil, reason)
						}
						var sourceSuccessors int
						if err := source.DB.QueryRowContext(ctx, Q(`SELECT COUNT(*) FROM memories WHERE id=$1 AND key=$2 AND owner_id=$3 AND collection_name=$4 AND superseded_by='' AND valid_until IS NULL`), newID, key, "lifecycle-owner", collection).Scan(&sourceSuccessors); err != nil || sourceSuccessors != 1 {
							t.Fatalf("native successor did not retain exact source scope: count=%d err=%v", sourceSuccessors, err)
						}
						retiredExport, err := exportSyncMemories(ctx, source, "")
						if err != nil || len(retiredExport) != 2 {
							t.Fatalf("retired export=%+v err=%v", retiredExport, err)
						}
						counts, _, err := importSyncMemories(ctx, target, wire(retiredExport))
						if err != nil || counts["failed"] != 0 {
							t.Fatalf("native retirement import=%v err=%v", counts, err)
						}
						var child, until, importedReason string
						if err := target.DB.QueryRowContext(ctx, Q(`SELECT superseded_by,COALESCE(CAST(valid_until AS TEXT),''),supersession_reason FROM memories WHERE id=$1 AND owner_id=$2 AND collection_name=$3`), oldID, "lifecycle-owner", collection).Scan(&child, &until, &importedReason); err != nil {
							t.Fatal(err)
						}
						if child != newID || until == "" || importedReason != reason {
							t.Errorf("retired source became active/lost lineage: successor=%q until=%q reason=%q, want successor=%q and native reason=%q", child, until, importedReason, newID, reason)
						}
						var active int
						if err := target.DB.QueryRowContext(ctx, Q(`SELECT COUNT(*) FROM memories WHERE owner_id=$1 AND collection_name=$2 AND superseded_by='' AND valid_until IS NULL`), "lifecycle-owner", collection).Scan(&active); err != nil {
							t.Fatal(err)
						}
						if active != 1 {
							t.Errorf("active memories=%d, want only the successor", active)
						}
						jobs := syncMemoryConflictJobs(t, ctx, target)
						var retiredDelete, successorUpsert int
						for _, job := range jobs {
							if job.OwnerID != "lifecycle-owner" || job.Collection != collection {
								t.Errorf("outbox scope changed: %+v", job)
							}
							if job.MemoryID == oldID && job.Operation == "upsert_vector" {
								t.Errorf("retired source queued active indexing: %+v", job)
							}
							if job.MemoryID == oldID && job.Operation == "delete_vector" {
								retiredDelete++
							}
							if job.MemoryID == newID && job.Operation == "upsert_vector" {
								successorUpsert++
							}
						}
						if retiredDelete != 1 || successorUpsert != 1 {
							t.Errorf("canonical lifecycle outbox: old delete=%d successor upsert=%d, want 1/1", retiredDelete, successorUpsert)
						}
					case "delete_then_replay_predelete_export":
						counts, _, err := importSyncMemories(ctx, target, wire(predelete))
						if err != nil || counts["imported"] != 1 {
							t.Fatalf("initial import=%v err=%v", counts, err)
						}
						deleted, err := mcp.DeleteMemory(ctx, targetDeps, mcp.DeleteMemoryRequest{MemoryID: oldID})
						if err != nil || deleted.ID != oldID || deleted.OwnerID != "lifecycle-owner" || deleted.Collection != collection {
							t.Fatalf("native delete=%+v err=%v", deleted, err)
						}
						var before int
						if err := target.DB.QueryRowContext(ctx, Q(`SELECT COUNT(*) FROM memories WHERE id=$1`), oldID).Scan(&before); err != nil || before != 0 {
							t.Fatalf("native delete not committed: count=%d err=%v", before, err)
						}
						jobs := syncMemoryConflictJobs(t, ctx, target)
						var nativeDeleteJobs int
						for _, job := range jobs {
							if job.MemoryID == oldID && job.OwnerID == "lifecycle-owner" && job.Collection == collection && job.Operation == "delete_vector" {
								nativeDeleteJobs++
							}
						}
						if nativeDeleteJobs != 1 {
							t.Fatalf("native delete lacked exact committed retirement intent: jobs=%+v", jobs)
						}
						counts, accepted, replayErr := importSyncMemories(ctx, target, wire(predelete))
						var after int
						if err := target.DB.QueryRowContext(ctx, Q(`SELECT COUNT(*) FROM memories WHERE owner_id=$1 AND collection_name=$2 AND key=$3`), "lifecycle-owner", collection, key).Scan(&after); err != nil {
							t.Fatal(err)
						}
						if after != 0 || counts["imported"] != 0 || len(accepted) != 0 {
							t.Errorf("predelete native export resurrected deleted memory: count=%d counts=%v accepted=%+v err=%v", after, counts, accepted, replayErr)
						}
						if afterJobs := syncMemoryConflictJobs(t, ctx, target); !reflect.DeepEqual(jobs, afterJobs) {
							t.Errorf("stale replay changed index intent: before=%+v after=%+v", jobs, afterJobs)
						}
					}
					for _, cfg := range []APIConfig{source, target} {
						if cfg.DB.Stats().InUse != 0 {
							t.Errorf("lifecycle transaction leaked pool-one connection: %+v", cfg.DB.Stats())
						}
					}
				})
			}
		})
	}
}
