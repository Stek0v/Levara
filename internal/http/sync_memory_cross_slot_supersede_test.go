package http

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/stek0v/levara/pkg/mcp"
)

// Native MCP mutations and public JSON handlers in explicit trusted-local mode.
// Independent SQL stores use pool one; this does not claim JWT or OS-process coverage.
func TestSyncMemoryNativeCrossSlotSupersede(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			source, target := syncMemoryConflictConfig(t, dialect), syncMemoryConflictConfig(t, dialect)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			ctx = context.WithValue(ctx, mcp.UserIDKey, "cross-slot-owner")
			const owner, collection = "cross-slot-owner", "cross-slot-collection"
			const oldKey, newKey = "original-slot", "successor-slot"
			save := func(cfg APIConfig) string {
				t.Helper()
				deps := NewMCPDeps(cfg)
				if actor := deps.MetadataActor(ctx); !actor.TrustedLocal || actor.UserID != owner || actor.TenantID != "" || actor.Credential.Kind != "" {
					t.Fatalf("native authority=%+v", actor)
				}
				if result := mcp.ToolSaveMemory(ctx, deps, map[string]any{"key": oldKey, "value": "independent original", "collection": collection, "room": "memory", "hall": "fact"}); result.IsError {
					t.Fatalf("native save=%+v", result)
				}
				var id string
				if err := cfg.DB.QueryRowContext(ctx, Q("SELECT id FROM memories WHERE key=$1 AND owner_id=$2 AND collection_name=$3"), oldKey, owner, collection).Scan(&id); err != nil {
					t.Fatal(err)
				}
				return id
			}
			remoteOld, localOld := save(source), save(target)
			if remoteOld == localOld {
				t.Fatal("fixture requires independent old-slot canonical UUIDs")
			}
			result := mcp.ToolSupersedeMemory(ctx, NewMCPDeps(source), map[string]any{"old_memory_id": remoteOld, "key": newKey, "new_value": "cross-slot replacement", "reason": "native rename"})
			if result.IsError {
				t.Fatalf("native renamed supersede=%+v", result)
			}
			sourceApp, targetApp := syncLifecycleWireApp(source), syncLifecycleWireApp(target)
			raw, batch := syncLifecycleWireExport(t, sourceApp, "")
			if len(batch.Memories) != 2 {
				t.Fatalf("native lifecycle export=%+v", batch)
			}
			var remoteNew string
			for _, row := range batch.Memories {
				if row.ID == remoteOld {
					if row.Key != fmt.Sprintf("%s#superseded:%s", oldKey, remoteOld) || row.SupersededBy == "" || row.ValidUntil == "" {
						t.Fatalf("predecessor archive changed original key: %+v", row)
					}
					remoteNew = row.SupersededBy
				}
			}
			if remoteNew == "" {
				t.Fatal("native export lost successor")
			}
			for _, cfg := range []APIConfig{source} {
				old := syncGenerationRow(t, ctx, cfg.DB, remoteOld)
				fresh := syncGenerationRow(t, ctx, cfg.DB, remoteNew)
				if old.Key != oldKey || old.Generation != 0 || old.State != "retired" || fresh.Key != newKey || fresh.Generation != 0 || fresh.State != "active" {
					t.Fatalf("native cross-slot ledger old=%+v successor=%+v", old, fresh)
				}
			}
			status, ack := syncLifecycleWireImport(t, targetApp, raw)
			if status != 200 || ack["failed"] != float64(0) {
				t.Fatalf("native cross-slot transfer=%d %v", status, ack)
			}
			var localNew, archiveKey, until, reason string
			if err := target.DB.QueryRowContext(ctx, Q("SELECT key,superseded_by,COALESCE(CAST(valid_until AS TEXT),''),supersession_reason FROM memories WHERE id=$1"), localOld).Scan(&archiveKey, &localNew, &until, &reason); err != nil {
				t.Fatal(err)
			}
			if archiveKey != fmt.Sprintf("%s#superseded:%s", oldKey, localOld) || localNew == "" || localNew == localOld || until == "" || reason != "native rename" {
				t.Fatalf("receiver predecessor key=%q successor=%q until=%q reason=%q", archiveKey, localNew, until, reason)
			}
			var successorKey, predecessor string
			if err := target.DB.QueryRowContext(ctx, Q("SELECT key,supersedes_memory_id FROM memories WHERE id=$1 AND owner_id=$2 AND collection_name=$3 AND superseded_by='' AND valid_until IS NULL"), localNew, owner, collection).Scan(&successorKey, &predecessor); err != nil {
				t.Fatal(err)
			}
			old, fresh := syncGenerationRow(t, ctx, target.DB, localOld), syncGenerationRow(t, ctx, target.DB, localNew)
			if successorKey != newKey || predecessor != localOld || old.Key != oldKey || old.Generation != 0 || old.State != "retired" || fresh.Key != newKey || fresh.Generation != 0 || fresh.State != "active" {
				t.Fatalf("receiver links/generations key=%q predecessor=%q old=%+v successor=%+v", successorKey, predecessor, old, fresh)
			}
			jobs := syncMemoryConflictJobs(t, ctx, target)
			deleteCount, upsertCount := 0, 0
			for _, job := range jobs {
				if job.OwnerID != owner || job.Collection != collection || job.MemoryID == remoteOld {
					t.Fatalf("noncanonical receiver intent=%+v", job)
				}
				if job.Operation == "delete_vector" && job.MemoryID == localOld {
					deleteCount++
				} else if job.Operation == "upsert_vector" && job.MemoryID == localNew {
					upsertCount++
				} else {
					t.Fatalf("unexpected receiver intent=%+v", job)
				}
			}
			if deleteCount != 1 || upsertCount != 1 {
				t.Fatalf("canonical intents delete=%d upsert=%d", deleteCount, upsertCount)
			}
			// Exchange both canonical alias inventories once, then require exact
			// fixed-point snapshots and skip-only replays in both directions.
			exchange := func(from, to APIConfig, replay bool) {
				t.Helper()
				before := syncGenerationSnapshot(t, ctx, to.DB)
				raw, exported := syncLifecycleWireExport(t, syncLifecycleWireApp(from), "")
				for _, incarnation := range exported.Incarnations {
					if incarnation.LogicalKey != oldKey && incarnation.LogicalKey != newKey {
						t.Fatalf("logical key drift=%+v", incarnation)
					}
				}
				status, ack := syncLifecycleWireImport(t, syncLifecycleWireApp(to), raw)
				if status != 200 || ack["failed"] != float64(0) {
					t.Fatalf("bidirectional exchange=%d %v", status, ack)
				}
				if replay && (ack["imported"] != float64(0) || ack["skipped"] != float64(len(exported.Memories)) || !reflect.DeepEqual(before, syncGenerationSnapshot(t, ctx, to.DB))) {
					t.Fatalf("replay did not reach fixed point: ack=%v", ack)
				}
			}
			exchange(target, source, false)
			exchange(source, target, false)
			for i := 0; i < 2; i++ {
				exchange(target, source, true)
				exchange(source, target, true)
			}
			if source.DB.Stats().InUse != 0 || target.DB.Stats().InUse != 0 {
				t.Fatal("native cross-slot transfer leaked pool-one connection")
			}
		})
	}
}
