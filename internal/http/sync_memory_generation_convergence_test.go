package http

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/stek0v/levara/pkg/mcp"
)

// Real native independent creations produce different UUIDs for the same
// logical slot. Exact-ID tombstones alone cannot establish convergence.
func TestSyncMemoryNativeDivergentGeneration(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			for _, operation := range []string{"unseen_uuid_after_native_delete", "supersede_original_logical_slot"} {
				t.Run(operation, func(t *testing.T) {
					source, target := syncMemoryConflictConfig(t, dialect), syncMemoryConflictConfig(t, dialect)
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					ctx = context.WithValue(ctx, mcp.UserIDKey, "generation-owner")
					const key = "generation-key"
					const collection = "generation-collection"
					save := func(cfg APIConfig) syncMemory {
						t.Helper()
						deps := NewMCPDeps(cfg)
						if actor := deps.MetadataActor(ctx); !actor.TrustedLocal || actor.UserID != "generation-owner" || actor.TenantID != "" || actor.Credential.Kind != "" {
							t.Fatalf("native authority=%+v", actor)
						}
						result := mcp.ToolSaveMemory(ctx, deps, map[string]any{"key": key, "value": "independent native original", "collection": collection, "room": "memory", "hall": "fact"})
						if result.IsError {
							t.Fatalf("native save=%+v", result)
						}
						records, err := exportSyncMemories(ctx, cfg, "")
						if err != nil || len(records) != 1 {
							t.Fatalf("native save export=%+v err=%v", records, err)
						}
						return records[0]
					}
					remote, local := save(source), save(target)
					if remote.ID == local.ID || remote.OwnerID != local.OwnerID || remote.CollectionName != local.CollectionName || remote.Key != local.Key {
						t.Fatalf("fixture needs independent UUIDs for exact same logical slot: remote=%+v local=%+v", remote, local)
					}
					switch operation {
					case "unseen_uuid_after_native_delete":
						deleted, err := mcp.DeleteMemory(ctx, NewMCPDeps(target), mcp.DeleteMemoryRequest{MemoryID: local.ID})
						if err != nil || deleted.ID != local.ID {
							t.Fatalf("native delete=%+v err=%v", deleted, err)
						}
						beforeJobs := syncMemoryConflictJobs(t, ctx, target)
						raw, batch := syncLifecycleWireExport(t, syncLifecycleWireApp(source), "?since=2099-01-01T00:00:00Z")
						if len(batch.Memories) != 1 || batch.Memories[0].ID != remote.ID {
							t.Fatalf("genuine stale peer export=%+v", batch)
						}
						status, ack := syncLifecycleWireImport(t, syncLifecycleWireApp(target), raw)
						if status != 200 || ack["imported"] != float64(0) || ack["failed"] != float64(0) || ack["skipped"] != float64(1) {
							t.Errorf("stale independent generation admission=%d %v", status, ack)
						}
						if rows := syncLifecycleWireSQLSnapshot(t, target); len(rows) != 0 {
							t.Errorf("unseen peer UUID resurrected terminal generation: %+v", rows)
						}
						if after := syncMemoryConflictJobs(t, ctx, target); !reflect.DeepEqual(beforeJobs, after) {
							t.Errorf("stale generation changed canonical index intent: before=%+v after=%+v", beforeJobs, after)
						}
					case "supersede_original_logical_slot":
						result := mcp.ToolSupersedeMemory(ctx, NewMCPDeps(source), map[string]any{"old_memory_id": remote.ID, "new_value": "native generation successor", "reason": "independent identity"})
						if result.IsError {
							t.Fatalf("native supersede=%+v", result)
						}
						var successor string
						if err := source.DB.QueryRowContext(ctx, Q("SELECT superseded_by FROM memories WHERE id=$1"), remote.ID).Scan(&successor); err != nil || successor == "" {
							t.Fatalf("native successor=%q err=%v", successor, err)
						}
						raw, batch := syncLifecycleWireExport(t, syncLifecycleWireApp(source), "")
						if len(batch.Memories) != 2 {
							t.Fatalf("genuine supersession export=%+v", batch)
						}
						status, ack := syncLifecycleWireImport(t, syncLifecycleWireApp(target), raw)
						if status != 200 || ack["failed"] != float64(0) {
							t.Fatalf("native supersession transfer=%d %v", status, ack)
						}
						var localSuccessor, until, successorID, predecessor string
						if err := target.DB.QueryRowContext(ctx, Q("SELECT superseded_by,COALESCE(CAST(valid_until AS TEXT),'') FROM memories WHERE id=$1"), local.ID).Scan(&localSuccessor, &until); err != nil {
							t.Fatal(err)
						}
						if localSuccessor == "" || until == "" {
							t.Errorf("existing canonical predecessor became active successor: successor=%q until=%q", localSuccessor, until)
						}
						if err := target.DB.QueryRowContext(ctx, Q("SELECT id,supersedes_memory_id FROM memories WHERE key=$1 AND owner_id=$2 AND collection_name=$3 AND superseded_by='' AND valid_until IS NULL"), key, local.OwnerID, collection).Scan(&successorID, &predecessor); err != nil {
							t.Fatal(err)
						}
						if successorID == local.ID || successorID != localSuccessor || predecessor != local.ID {
							t.Errorf("generation/canonical links: successor=%q old successor=%q predecessor=%q original=%q", successorID, localSuccessor, predecessor, local.ID)
						}
						jobs := syncMemoryConflictJobs(t, ctx, target)
						retiredIntent := false
						for _, job := range jobs {
							if job.MemoryID == local.ID && job.Operation == "delete_vector" {
								retiredIntent = true
							}
						}
						if !retiredIntent {
							encoded, _ := json.Marshal(jobs)
							t.Errorf("canonical predecessor has no retirement intent: %s", encoded)
						}
					}
					if source.DB.Stats().InUse != 0 || target.DB.Stats().InUse != 0 {
						t.Fatal("native lifecycle leaked pool-one connection")
					}
				})
			}
		})
	}
}
