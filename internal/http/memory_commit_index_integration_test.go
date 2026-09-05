package http

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stek0v/levara/pkg/embed"
	"github.com/stek0v/levara/pkg/mcp"
	"github.com/stek0v/levara/pkg/memoryindex"
)

func TestMemoryCommitApplyIndexesThroughWorker(t *testing.T) {
	previousProvider := GetDBProvider()
	t.Cleanup(func() { SetDBProvider(previousProvider) })
	db := newMCPMemoryBehaviorDB(t)
	cfg, cleanup := newWorkspaceTestConfig(t)
	t.Cleanup(cleanup)
	cfg.DB = db
	cfg.EmbedClient = embed.NewClient(cfg.EmbedEndpoint, cfg.EmbedModel, 1, 1)
	var err error
	cfg.MemoryIndexOutbox, err = memoryindex.NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	deps := &mcpHandler{cfg: cfg}
	ctx := context.WithValue(context.Background(), mcp.UserIDKey, "owner-a")
	const collection = "commit-index-test"

	for _, method := range []string{"save_memory_control", "memory_commit"} {
		t.Run(method, func(t *testing.T) {
			decode := func(result mcp.ToolResult) map[string]any {
				t.Helper()
				if result.IsError || len(result.Content) == 0 {
					t.Fatalf("tool failed: %+v", result)
				}
				var payload map[string]any
				if err := json.Unmarshal([]byte(result.Content[0].Text), &payload); err != nil {
					t.Fatal(err)
				}
				return payload
			}
			candidate := map[string]any{
				"key": method, "value": "Use temporary fixtures for integration checks.",
				"room": "memory", "hall": "advice",
			}
			if method == "save_memory_control" {
				candidate["collection"] = collection
				decode(mcp.ToolSaveMemory(ctx, deps, candidate))
			} else {
				candidate["candidate_id"] = "candidate-1"
				preview := decode(mcp.ToolMemoryCommitPreview(ctx, deps, map[string]any{
					"collection": collection, "idempotency_key": "commit-1",
					"candidates": []any{candidate},
				}))
				applied := decode(mcp.ToolMemoryCommitApply(ctx, deps, map[string]any{
					"commit_id": preview["commit_id"], "plan_digest": preview["plan_digest"],
				}))
				if applied["added"] != float64(1) {
					t.Fatalf("apply did not add one memory: %+v", applied)
				}
			}

			// ponytail: run one real worker iteration directly; no timers or startup reconciliation.
			if !runMemoryIndexJob(ctx, cfg) {
				t.Fatal("worker did not claim the queued memory")
			}
			var memoryID, status, digest, lastError string
			if err := db.QueryRow(`SELECT m.id,j.status,j.digest,j.last_error
				FROM memories m JOIN memory_index_jobs j ON j.memory_id=m.id
				WHERE m.key=? AND m.owner_id=? AND m.collection_name=?`,
				method, "owner-a", collection).Scan(&memoryID, &status, &digest, &lastError); err != nil {
				t.Fatal(err)
			}
			present := cfg.Collections.HasRecord(memoryCollectionNameHTTP(collection), memoryID)
			t.Logf("job_status=%s vector_present=%t digest=%s last_error=%q", status, present, digest, lastError)
			if status != string(memoryindex.Completed) {
				t.Fatalf("worker status=%s error=%q, want completed", status, lastError)
			}
			if !present {
				t.Fatal("completed indexing job has no vector for the persisted memory")
			}
		})
	}
}
