package http

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/stek0v/levara/pkg/mcp"
	"github.com/stek0v/levara/pkg/memoryindex"
)

func TestSyncMemoryGenerationMetadataConvergence(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			for _, field := range []string{"tier", "consolidated_from", "competing_logical_lineage"} {
				t.Run(field, func(t *testing.T) {
					left, right := syncMemoryConflictConfig(t, dialect), syncMemoryConflictConfig(t, dialect)
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					ctx = context.WithValue(ctx, mcp.UserIDKey, "payload-owner")
					const collection = "payload-collection"
					save := func(cfg APIConfig, key string) string {
						t.Helper()
						deps := NewMCPDeps(cfg)
						actor := deps.MetadataActor(ctx)
						if !actor.TrustedLocal || actor.UserID != "payload-owner" || actor.TenantID != "" || actor.Credential.Kind != "" {
							t.Fatalf("native authority=%+v", actor)
						}
						result := mcp.ToolSaveMemory(ctx, deps, map[string]any{"key": key, "value": "same native content", "collection": collection, "room": "memory", "hall": "fact"})
						if result.IsError {
							t.Fatalf("native save=%+v", result)
						}
						var id string
						if err := cfg.DB.QueryRowContext(ctx, Q("SELECT id FROM memories WHERE key=$1 AND owner_id=$2 AND collection_name=$3"), key, "payload-owner", collection).Scan(&id); err != nil {
							t.Fatal(err)
						}
						return id
					}
					leftID, rightID := save(left, "payload"), save(right, "payload")
					if leftID == rightID {
						t.Fatal("fixture requires independently generated UUIDs")
					}
					refs := map[string]string{}
					switch field {
					case "competing_logical_lineage":
						// Reverse physical UUID ordering for the same logical p/q slots.
						insert := func(cfg APIConfig, id, key string) {
							t.Helper()
							query, args := QArgs("INSERT INTO memories(id,key,value,type,owner_id,collection_name,room,hall,created_at,updated_at) VALUES($1,$2,'same native source','project','payload-owner',$3,'memory','fact',$4,$4)", id, key, collection, "2026-10-07T01:02:03.123456Z")
							if _, err := cfg.DB.ExecContext(ctx, query, args...); err != nil {
								t.Fatal(err)
							}
						}
						const leftP = "00000000-0000-4000-8000-000000000001"
						const leftQ = "ffffffff-ffff-4fff-8fff-fffffffffff1"
						const rightP = "ffffffff-ffff-4fff-8fff-fffffffffff2"
						const rightQ = "00000000-0000-4000-8000-000000000002"
						insert(left, leftP, "p")
						insert(left, leftQ, "q")
						insert(right, rightP, "p")
						insert(right, rightQ, "q")
						refs[leftID], refs[rightID] = leftQ, rightQ
						for _, record := range []struct {
							cfg     APIConfig
							id, ref string
						}{{left, leftID, leftP}, {right, rightID, rightQ}} {
							raw, _ := json.Marshal([]string{record.ref})
							if _, err := record.cfg.DB.ExecContext(ctx, Q("UPDATE memories SET tier='semantic',consolidated_from=$1 WHERE id=$2"), string(raw), record.id); err != nil {
								t.Fatal(err)
							}
						}
					case "consolidated_from":
						refs[leftID], refs[rightID] = save(left, "lineage-source"), save(right, "lineage-source")
						if refs[leftID] == refs[rightID] {
							t.Fatal("lineage source roots must have independent native UUIDs")
						}
						raw, _ := json.Marshal([]string{refs[rightID]})
						if _, err := right.DB.ExecContext(ctx, Q("UPDATE memories SET consolidated_from=$1 WHERE id=$2"), string(raw), rightID); err != nil {
							t.Fatal(err)
						}
					default:
						if _, err := right.DB.ExecContext(ctx, Q("UPDATE memories SET tier='semantic' WHERE id=$1"), rightID); err != nil {
							t.Fatal(err)
						}
					}
					stamp := "2026-10-07T01:02:03.123456Z"
					for _, cfg := range []APIConfig{left, right} {
						if _, err := cfg.DB.ExecContext(ctx, Q("UPDATE memories SET updated_at=$1"), stamp); err != nil {
							t.Fatal(err)
						}
					}
					// This SQL fixture has no embedding provider, so native Save
					// does not publish an index job. Seed real pending canonical
					// publications before asserting metadata-only deduplication.
					for _, cfg := range []APIConfig{left, right} {
						records, err := exportSyncMemories(ctx, cfg, "")
						if err != nil {
							t.Fatal(err)
						}
						for _, m := range records {
							digest := fmt.Sprintf("%x", sha256.Sum256([]byte(m.Key+"\x00"+m.Value)))
							job, err := cfg.MemoryIndexOutbox.Enqueue(ctx, memoryindex.Job{MemoryID: m.ID, Operation: "upsert_vector", OwnerID: m.OwnerID, Collection: m.CollectionName, Digest: digest, Model: cfg.EmbedModel})
							if err != nil {
								t.Fatal(err)
							}
							if job.ID == "" || job.MemoryID != m.ID || job.Operation != "upsert_vector" || job.OwnerID != m.OwnerID || job.Collection != m.CollectionName || job.Digest != digest || job.Model != cfg.EmbedModel || job.Status != memoryindex.Pending {
								t.Fatalf("native canonical publication fixture=%+v row=%+v", job, m)
							}
						}
						if jobs := syncMemoryConflictJobs(t, ctx, cfg); len(jobs) != len(records) {
							t.Fatalf("publication baseline jobs=%d rows=%d", len(jobs), len(records))
						}
					}
					leftJobs, rightJobs := syncMemoryConflictJobs(t, ctx, left), syncMemoryConflictJobs(t, ctx, right)
					transfer := func(from, to APIConfig) map[string]any {
						t.Helper()
						raw, b := syncLifecycleWireExport(t, syncLifecycleWireApp(from), "")
						if b.ProtocolVersion != syncMemoryProtocolVersion {
							t.Fatal("export did not use current generation protocol")
						}
						status, ack := syncLifecycleWireImport(t, syncLifecycleWireApp(to), raw)
						if status != 200 || ack["failed"] != float64(0) {
							t.Fatalf("metadata transfer=%d %v", status, ack)
						}
						return ack
					}
					// Send the losing snapshot first, then the winner, then reverse again.
					if ack := transfer(left, right); ack["imported"] != float64(0) {
						t.Fatalf("losing metadata unexpectedly changed content: %v", ack)
					}
					if ack := transfer(right, left); ack["imported"] != float64(1) {
						t.Fatalf("metadata-only winner was discarded: %v", ack)
					}
					transfer(left, right)
					check := func(cfg APIConfig, id, otherID string) {
						t.Helper()
						var storedID, tier, lineage, run string
						var generation, revision int64
						if err := cfg.DB.QueryRowContext(ctx, Q("SELECT id,tier,consolidated_from,consolidation_run_id FROM memories WHERE key=$1 AND owner_id=$2 AND collection_name=$3"), "payload", "payload-owner", collection).Scan(&storedID, &tier, &lineage, &run); err != nil {
							t.Fatal(err)
						}
						if storedID != id {
							t.Fatalf("metadata changed canonical identity: %q want %q", storedID, id)
						}
						if (field == "tier" || field == "competing_logical_lineage") && tier != "semantic" {
							t.Fatalf("metadata convergence lost semantic tier: %q", tier)
						}
						if field == "consolidated_from" || field == "competing_logical_lineage" {
							var ids []string
							if err := json.Unmarshal([]byte(lineage), &ids); err != nil || len(ids) != 1 || ids[0] != refs[id] {
								t.Fatalf("lineage not remapped to native canonical source: %q err=%v", lineage, err)
							}
						}
						if run != "" {
							t.Fatalf("unrelated run provenance changed: %q", run)
						}
						if err := cfg.DB.QueryRowContext(ctx, Q("SELECT generation,state_revision FROM memory_sync_incarnations WHERE memory_id=$1"), id).Scan(&generation, &revision); err != nil {
							t.Fatal(err)
						}
						if generation != 0 || revision != 0 {
							t.Fatalf("metadata changed lifecycle: generation=%d revision=%d", generation, revision)
						}
						var alias string
						if err := cfg.DB.QueryRowContext(ctx, Q("SELECT memory_id FROM memory_sync_aliases WHERE alias_id=$1"), otherID).Scan(&alias); err != nil || alias != id {
							t.Fatalf("losing/equal peer alias missing: %q err=%v", alias, err)
						}
					}
					check(left, leftID, rightID)
					check(right, rightID, leftID)
					beforeLeft, beforeRight := syncLifecycleWireSQLSnapshot(t, left), syncLifecycleWireSQLSnapshot(t, right)
					for n := 0; n < 2; n++ {
						for _, pair := range [][2]APIConfig{{left, right}, {right, left}} {
							if ack := transfer(pair[0], pair[1]); ack["imported"] != float64(0) || ack["skipped"] != ack["total"] {
								t.Fatalf("fixed point replay changed content: %v", ack)
							}
						}
					}
					if !reflect.DeepEqual(beforeLeft, syncLifecycleWireSQLSnapshot(t, left)) || !reflect.DeepEqual(beforeRight, syncLifecycleWireSQLSnapshot(t, right)) {
						t.Fatal("fixed point replay changed authoritative rows")
					}
					if !reflect.DeepEqual(leftJobs, syncMemoryConflictJobs(t, ctx, left)) || !reflect.DeepEqual(rightJobs, syncMemoryConflictJobs(t, ctx, right)) {
						t.Fatal("metadata-only convergence/replay changed canonical index intents")
					}
					check(left, leftID, rightID)
					check(right, rightID, leftID)
					if left.DB.Stats().InUse != 0 || right.DB.Stats().InUse != 0 {
						t.Fatal("metadata convergence leaked pool-one connection")
					}
				})
			}
		})
	}
}
