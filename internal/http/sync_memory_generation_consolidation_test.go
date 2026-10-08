package http

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stek0v/levara/internal/store"
	"github.com/stek0v/levara/pkg/consolidate"
	"github.com/stek0v/levara/pkg/embed"
	"github.com/stek0v/levara/pkg/mcp"
	"github.com/stek0v/levara/pkg/memoryindex"
)

// Exercise actual native Apply/Revert journals, then public v3 exchange.
// Only the source can revert: imported run IDs carry provenance, not its journal.
func TestSyncMemoryGenerationNativeConsolidationExchange(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			source, target := syncMemoryConflictConfig(t, dialect), syncMemoryConflictConfig(t, dialect)
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			ctx = context.WithValue(ctx, mcp.UserIDKey, "consolidation-sync-owner")
			const collection = "consolidation-sync"
			values := []string{"Alpha server listens on port 8080.", "Beta server listens on port 9090."}
			cm, err := store.NewCollectionManager(2, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cm.Close() })
			embedding := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/health" {
					w.WriteHeader(http.StatusOK)
					return
				}
				var req struct {
					Input []string `json:"input"`
				}
				if r.URL.Path != "/v1/embeddings" || json.NewDecoder(r.Body).Decode(&req) != nil {
					http.Error(w, "invalid embedding request", 400)
					return
				}
				data := make([]map[string]any, len(req.Input))
				for n, input := range req.Input {
					vec := []float32{1, 0}
					if input == "beta "+values[1] {
						vec = []float32{.94, float32(math.Sqrt(1 - .94*.94))}
					}
					data[n] = map[string]any{"index": n, "embedding": vec}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
			}))
			t.Cleanup(embedding.Close)
			preflight, err := embedding.Client().Get(embedding.URL + "/health")
			if err != nil {
				t.Fatal(err)
			}
			_ = preflight.Body.Close()
			if preflight.StatusCode != http.StatusOK {
				t.Fatalf("embedding preflight=%d", preflight.StatusCode)
			}
			provider := &consolidationIndexProvider{values: values}
			source.Collections = cm
			source.EmbedEndpoint, source.EmbedModel = embedding.URL+"/v1/embeddings", "local-index-test"
			source.EmbedClient = embed.NewClient(source.EmbedEndpoint, source.EmbedModel, 1, 1)
			source.LLMProvider = provider
			sourceDeps, targetDeps := NewMCPDeps(source), NewMCPDeps(target)
			for _, cfg := range []APIConfig{source, target} {
				actor := NewMCPDeps(cfg).MetadataActor(ctx)
				if !actor.TrustedLocal || actor.UserID != "consolidation-sync-owner" || actor.TenantID != "" || actor.Credential.Kind != "" || cfg.DB.Stats().MaxOpenConnections != 1 {
					t.Fatalf("requires scoped native pool-one fixture: %+v", actor)
				}
			}
			sourceIDs, targetIDs := map[string]string{}, map[string]string{}
			for n, key := range []string{"alpha", "beta"} {
				for _, node := range []struct {
					deps mcp.Deps
					cfg  APIConfig
					ids  map[string]string
				}{{sourceDeps, source, sourceIDs}, {targetDeps, target, targetIDs}} {
					result := mcp.ToolSaveMemory(ctx, node.deps, map[string]any{"key": key, "value": values[n], "type": "user", "collection": collection, "room": "memory", "hall": "fact"})
					if result.IsError {
						t.Fatalf("native save=%+v", result)
					}
					var id string
					if err := node.cfg.DB.QueryRowContext(ctx, Q("SELECT id FROM memories WHERE key=$1 AND owner_id=$2 AND collection_name=$3"), key, "consolidation-sync-owner", collection).Scan(&id); err != nil {
						t.Fatal(err)
					}
					node.ids[key] = id
				}
				if sourceIDs[key] == targetIDs[key] {
					t.Fatal("source and receiver must have independent native UUIDs")
				}
			}
			drain := func(want int) {
				t.Helper()
				n := 0
				for n < 16 && runMemoryIndexJob(ctx, source) {
					n++
				}
				counts, e := source.MemoryIndexOutbox.Counts(ctx)
				if e != nil || n != want || counts[memoryindex.Pending]+counts[memoryindex.Running]+counts[memoryindex.Failed]+counts[memoryindex.DeadLetter] != 0 {
					t.Fatalf("native worker drain=%d want=%d counts=%v err=%v", n, want, counts, e)
				}
			}
			drain(2)
			hits, err := sourceDeps.CollectionSearch(memoryCollectionNameHTTP(collection), []float32{1, 0}, 10)
			if err != nil {
				t.Fatal(err)
			}
			var cosine float64
			for _, hit := range hits {
				if hit.ID == sourceIDs["beta"] {
					cosine = float64(hit.Score)
				}
			}
			defaults := consolidate.DefaultConfig()
			if math.Abs(cosine-.94) > .0001 || cosine <= defaults.TauLow || cosine >= defaults.TauHigh {
				t.Fatalf("native vectors do not force abstraction: cosine=%f", cosine)
			}
			initial := map[string]syncGenerationFixtureRow{}
			for _, key := range []string{"alpha", "beta"} {
				for _, node := range []struct {
					cfg APIConfig
					id  string
				}{{source, sourceIDs[key]}, {target, targetIDs[key]}} {
					i := syncGenerationRow(t, ctx, node.cfg.DB, node.id)
					if i.State != "active" || i.Generation != 0 || i.Revision != 0 || i.Key != key {
						t.Fatalf("native initial identity=%+v", i)
					}
					initial[node.id] = i
				}
			}
			applied := mcp.ToolConsolidate(ctx, sourceDeps, map[string]any{"collection": collection, "room": "memory", "hall": "fact", "dry_run": false, "wait": true})
			if applied.IsError || len(applied.Content) == 0 || !strings.Contains(applied.Content[0].Text, "candidates=2 clusters=1 actions=1 skipped=0") || provider.calls.Load() != 1 {
				t.Fatalf("native consolidation did not create abstract: %+v calls=%d", applied, provider.calls.Load())
			}
			var summaryID, runID, summaryValue string
			if err := source.DB.QueryRowContext(ctx, Q("SELECT id,consolidation_run_id,value FROM memories WHERE owner_id=$1 AND collection_name=$2 AND tier='semantic'"), "consolidation-sync-owner", collection).Scan(&summaryID, &runID, &summaryValue); err != nil || summaryID == "" || runID == "" || summaryValue != strings.Join(values, " ") {
				t.Fatalf("native summary=%q run=%q value=%q err=%v", summaryID, runID, summaryValue, err)
			}
			summaryBefore := syncGenerationRow(t, ctx, source.DB, summaryID)
			if summaryBefore.State != "active" || summaryBefore.Generation != 0 {
				t.Fatalf("native summary incarnation=%+v", summaryBefore)
			}
			// Publishing vectors does not change journal-bound memory rows.
			drain(3)
			appliedRaw, appliedBatch := syncLifecycleWireExport(t, syncLifecycleWireApp(source), "")
			if len(appliedBatch.Memories) != 3 || len(appliedBatch.Incarnations) != 3 {
				t.Fatalf("native applied snapshot incomplete: %+v", appliedBatch)
			}
			importRaw := func(raw []byte) map[string]any {
				t.Helper()
				status, ack := syncLifecycleWireImport(t, syncLifecycleWireApp(target), raw)
				if status != 200 || ack["failed"] != float64(0) {
					t.Fatalf("native consolidation exchange=%d %v", status, ack)
				}
				return ack
			}
			if ack := importRaw(appliedRaw); ack["imported"] != float64(3) {
				t.Fatalf("applied snapshot not accepted: %v", ack)
			}
			var targetSummary, targetRun, targetLineage, targetSummaryKey, targetSummaryTask, targetSummaryReceipts, targetSummaryVerification string
			if err := target.DB.QueryRowContext(ctx, Q("SELECT id,consolidation_run_id,consolidated_from,key,source_task_id,source_receipt_ids,verification_status FROM memories WHERE tier='semantic' AND owner_id=$1 AND collection_name=$2"), "consolidation-sync-owner", collection).Scan(&targetSummary, &targetRun, &targetLineage, &targetSummaryKey, &targetSummaryTask, &targetSummaryReceipts, &targetSummaryVerification); err != nil || targetRun != runID {
				t.Fatalf("receiver summary=%q run=%q lineage=%q err=%v", targetSummary, targetRun, targetLineage, err)
			}
			if targetSummaryTask != "" || targetSummaryReceipts != "[]" || targetSummaryVerification != "unverified" {
				t.Fatal("summary imported foreign Task proof")
			}
			var lineage []string
			if err := json.Unmarshal([]byte(targetLineage), &lineage); err != nil {
				t.Fatal(err)
			}
			sort.Strings(lineage)
			wantLineage := []string{targetIDs["alpha"], targetIDs["beta"]}
			sort.Strings(wantLineage)
			if !reflect.DeepEqual(lineage, wantLineage) {
				t.Fatalf("receiver lineage used foreign physical IDs: %v want %v", lineage, wantLineage)
			}
			retired := map[string]syncGenerationFixtureRow{}
			assertSourceState := func(cfg APIConfig, ids map[string]string, state, summary string) {
				t.Helper()
				for _, key := range []string{"alpha", "beta"} {
					id := ids[key]
					i := syncGenerationRow(t, ctx, cfg.DB, id)
					if i.State != state || i.Generation != initial[id].Generation || i.Key != key || i.Revision <= initial[id].Revision {
						t.Fatalf("native source lifecycle=%+v initial=%+v", i, initial[id])
					}
					var forward, until, run, task, receipts, verification string
					if err := cfg.DB.QueryRowContext(ctx, Q("SELECT superseded_by,COALESCE(CAST(valid_until AS TEXT),''),consolidation_run_id,source_task_id,source_receipt_ids,verification_status FROM memories WHERE id=$1"), id).Scan(&forward, &until, &run, &task, &receipts, &verification); err != nil {
						t.Fatal(err)
					}
					if state == "retired" && (forward != summary || until == "" || run != runID) {
						t.Fatalf("retirement lineage=%q until=%q run=%q", forward, until, run)
					}
					if state == "active" && (forward != "" || until != "" || run != "") {
						t.Fatalf("restoration retained retirement: forward=%q until=%q run=%q", forward, until, run)
					}
					if task != "" || receipts != "[]" || verification != "unverified" {
						t.Fatalf("foreign task proof crossed sync: task=%q receipts=%q verification=%q", task, receipts, verification)
					}
					if state == "retired" {
						retired[id] = i
					} else if i.Revision <= retired[id].Revision {
						t.Fatalf("restore failed to advance revision: %+v prior=%+v", i, retired[id])
					}
				}
			}
			assertSourceState(source, sourceIDs, "retired", summaryID)
			assertSourceState(target, targetIDs, "retired", targetSummary)
			assertAliases := func() {
				t.Helper()
				for _, key := range []string{"alpha", "beta"} {
					var canonical string
					if err := target.DB.QueryRowContext(ctx, Q("SELECT memory_id FROM memory_sync_aliases WHERE alias_id=$1"), sourceIDs[key]).Scan(&canonical); err != nil || canonical != targetIDs[key] {
						t.Fatalf("immutable source alias=%q want %q err=%v", canonical, targetIDs[key], err)
					}
				}
				var canonical string
				if err := target.DB.QueryRowContext(ctx, Q("SELECT memory_id FROM memory_sync_aliases WHERE alias_id=$1"), summaryID).Scan(&canonical); err != nil || canonical != targetSummary {
					t.Fatalf("summary alias=%q want %q err=%v", canonical, targetSummary, err)
				}
			}
			assertAliases()
			checkIntent := func(id, operation, digest string) {
				t.Helper()
				found := false
				for _, job := range syncMemoryConflictJobs(t, ctx, target) {
					if job.MemoryID == id && job.Operation == operation && job.Digest == digest {
						model := target.EmbedModel
						if operation == "delete_vector" {
							model = ""
						}
						if job.ID == "" || job.OwnerID != "consolidation-sync-owner" || job.Collection != collection || job.Model != model {
							t.Fatalf("foreign canonical index intent=%+v", job)
						}
						found = true
					}
				}
				if !found {
					t.Fatalf("canonical intent absent: id=%q operation=%q digest=%q", id, operation, digest)
				}
			}
			for _, key := range []string{"alpha", "beta"} {
				checkIntent(targetIDs[key], "delete_vector", "delete:"+targetIDs[key])
			}
			checkIntent(targetSummary, "upsert_vector", fmt.Sprintf("%x", sha256.Sum256([]byte(targetSummaryKey+"\x00"+summaryValue))))
			// Importing a run's provenance must not import its local revert authority.
			beforeDenied := syncGenerationSnapshot(t, ctx, target.DB)
			denied := mcp.ToolConsolidationRevert(ctx, targetDeps, map[string]any{"run_id": runID})
			if !denied.IsError || len(denied.Content) == 0 || !strings.Contains(denied.Content[0].Text, "unavailable or lacks safe revert journal") {
				t.Fatalf("receiver obtained foreign revert authority: %+v", denied)
			}
			if after := syncGenerationSnapshot(t, ctx, target.DB); !reflect.DeepEqual(beforeDenied, after) {
				t.Fatal("receiver denied revert changed six authoritative tables")
			}
			var journals int
			if err := target.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM consolidation_runs").Scan(&journals); err != nil || journals != 0 {
				t.Fatalf("receiver imported safe revert journal: count=%d err=%v", journals, err)
			}
			// Do not sync receiver rows back into source: its full-row journal is local.
			reverted := mcp.ToolConsolidationRevert(ctx, sourceDeps, map[string]any{"run_id": runID})
			if reverted.IsError {
				t.Fatalf("actual native revert=%+v", reverted)
			}
			assertSourceState(source, sourceIDs, "active", "")
			if summary := syncGenerationRow(t, ctx, source.DB, summaryID); summary.State != "deleted" || summary.Generation != summaryBefore.Generation || summary.Revision <= summaryBefore.Revision {
				t.Fatalf("native revert summary not terminal: %+v", summary)
			}
			var sourceRows int
			if err := source.DB.QueryRowContext(ctx, Q("SELECT COUNT(*) FROM memories WHERE id=$1"), summaryID).Scan(&sourceRows); err != nil || sourceRows != 0 {
				t.Fatalf("native revert left summary row: %d err=%v", sourceRows, err)
			}
			drain(3)
			revertedRaw, revertedBatch := syncLifecycleWireExport(t, syncLifecycleWireApp(source), "")
			if len(revertedBatch.Memories) != 2 || len(revertedBatch.Deletions) != 1 {
				t.Fatalf("native revert snapshot incomplete: %+v", revertedBatch)
			}
			if ack := importRaw(revertedRaw); ack["imported"] != float64(3) {
				t.Fatalf("reverted snapshot not accepted: %v", ack)
			}
			assertSourceState(target, targetIDs, "active", "")
			if summary := syncGenerationRow(t, ctx, target.DB, targetSummary); summary.State != "deleted" || summary.Generation != summaryBefore.Generation || summary.Revision <= summaryBefore.Revision {
				t.Fatalf("receiver summary not terminal: %+v", summary)
			}
			var targetRows int
			if err := target.DB.QueryRowContext(ctx, Q("SELECT COUNT(*) FROM memories WHERE id=$1"), targetSummary).Scan(&targetRows); err != nil || targetRows != 0 {
				t.Fatalf("receiver restored deleted summary: %d err=%v", targetRows, err)
			}
			for n, key := range []string{"alpha", "beta"} {
				digest := fmt.Sprintf("%x", sha256.Sum256([]byte(key+"\x00"+values[n])))
				checkIntent(targetIDs[key], "upsert_vector", digest)
			}
			checkIntent(targetSummary, "delete_vector", "delete:"+targetSummary)
			assertAliases()
			fixed := syncGenerationSnapshot(t, ctx, target.DB)
			for _, raw := range [][]byte{appliedRaw, revertedRaw, appliedRaw, revertedRaw} {
				ack := importRaw(raw)
				if ack["imported"] != float64(0) || ack["skipped"] != ack["total"] {
					t.Fatalf("stale/replayed native lifecycle was accepted: %v", ack)
				}
				if after := syncGenerationSnapshot(t, ctx, target.DB); !reflect.DeepEqual(fixed, after) {
					t.Fatal("stale applied snapshot reretired sources/resurrected summary or replay changed SQL/outbox")
				}
				assertAliases()
			}
			if source.DB.Stats().InUse != 0 || target.DB.Stats().InUse != 0 {
				t.Fatal("native consolidation exchange leaked pool-one connection")
			}
		})
	}
}
