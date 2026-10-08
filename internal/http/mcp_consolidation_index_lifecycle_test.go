package http

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stek0v/levara/internal/store"
	"github.com/stek0v/levara/pkg/consolidate"
	"github.com/stek0v/levara/pkg/embed"
	"github.com/stek0v/levara/pkg/llm"
	"github.com/stek0v/levara/pkg/mcp"
	"github.com/stek0v/levara/pkg/memoryindex"
)

type consolidationIndexProvider struct {
	values []string
	calls  atomic.Int32
}

func (*consolidationIndexProvider) Name() string { return "local-consolidation-index-test" }
func (p *consolidationIndexProvider) ChatCompletion(ctx context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(req.Messages) != 1 {
		return nil, fmt.Errorf("unexpected messages: %+v", req.Messages)
	}
	prompt := req.Messages[0].Content
	for _, value := range p.values {
		if !strings.Contains(prompt, value) {
			return nil, fmt.Errorf("source absent from prompt: %q", value)
		}
	}
	if strings.Contains(prompt, "Foreign") || strings.Contains(prompt, "Sibling") {
		return nil, fmt.Errorf("control crossed provider namespace: %s", prompt)
	}
	p.calls.Add(1)
	return &llm.CompletionResponse{Content: strings.Join(p.values, " ")}, nil
}

// Include every persisted column so restoration cannot hide retirement,
// provenance, pin, classification, or timestamp changes behind a projection.
func consolidationIndexRows(t *testing.T, f *documentHTTPFixture) map[string]map[string]any {
	t.Helper()
	rows, err := f.db.Query(`SELECT * FROM memories`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]map[string]any{}
	for rows.Next() {
		values, ptrs := make([]any, len(cols)), make([]any, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		row := map[string]any{}
		for i, value := range values {
			switch value := value.(type) {
			case []byte:
				row[cols[i]] = string(value)
			case time.Time:
				row[cols[i]] = value.UTC().Format(time.RFC3339Nano)
			default:
				row[cols[i]] = value
			}
		}
		out[row["id"].(string)] = row
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestMCPConsolidationIndexedSemanticLifecycle(t *testing.T) {
	t.Setenv("LEVARA_MCP_TOOLSET", "full")
	t.Setenv("LEVARA_TENANT_ENFORCED", "1")
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		for _, path := range []string{"/mcp", latestMCPPath} {
			t.Run(path, func(t *testing.T) {
				const query = "semanticneedleoutsideallpersistedtext"
				values := []string{"Alpha server listens on port 8080.", "Beta server listens on port 9090."}
				h := asyncAuthorityHandler(f)
				cm, err := store.NewCollectionManager(2, t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = cm.Close() })
				var queryCalls atomic.Int32
				embedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/health" {
						w.WriteHeader(http.StatusOK)
						return
					}
					var req struct {
						Input []string `json:"input"`
					}
					if r.URL.Path != "/v1/embeddings" || json.NewDecoder(r.Body).Decode(&req) != nil {
						http.Error(w, "invalid embedding request", http.StatusBadRequest)
						return
					}
					data := make([]map[string]any, len(req.Input))
					for i, input := range req.Input {
						vec := []float32{1, 0}
						if input == "beta "+values[1] {
							vec = []float32{0.94, float32(math.Sqrt(1 - 0.94*0.94))}
						}
						if input == query {
							queryCalls.Add(1)
						}
						data[i] = map[string]any{"index": i, "embedding": vec}
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
				}))
				t.Cleanup(embedServer.Close)
				// Preflight the local dependency before any tool or worker execution.
				resp, err := embedServer.Client().Get(embedServer.URL + "/health")
				if err != nil {
					t.Fatal(err)
				}
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("embed preflight=%d", resp.StatusCode)
				}
				h.cfg.Collections = cm
				h.cfg.EmbedEndpoint, h.cfg.EmbedModel = embedServer.URL+"/v1/embeddings", "local-index-test"
				h.cfg.EmbedClient = embed.NewClient(h.cfg.EmbedEndpoint, h.cfg.EmbedModel, 1, 1)
				h.cfg.MemoryIndexOutbox, err = memoryindex.NewStore(f.db)
				if err != nil {
					t.Fatal(err)
				}
				f.exec(`DELETE FROM memories`)
				f.exec(`DELETE FROM memory_index_jobs`)
				provider := &consolidationIndexProvider{values: values}
				h.cfg.LLMProvider = provider
				app := asyncAuthorityApp(h, nil)
				rpc := func(user, tool string, args map[string]any) mcp.ToolResult {
					t.Helper()
					got := asyncAuthorityRPC(t, app, h, path, user, tool, args)
					if got.IsError {
						t.Fatalf("%s: %+v", tool, got)
					}
					return got
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				drain := func(want int) {
					t.Helper()
					processed := 0
					for processed < 32 && runMemoryIndexJob(ctx, h.cfg) {
						processed++
					}
					counts, err := h.cfg.MemoryIndexOutbox.Counts(ctx)
					if err != nil || processed != want || counts[memoryindex.Pending]+counts[memoryindex.Running]+counts[memoryindex.Failed]+counts[memoryindex.DeadLetter] != 0 {
						t.Fatalf("worker drain=%d want=%d counts=%v err=%v", processed, want, counts, err)
					}
				}
				ids := make([]string, 4)
				initialJobs := make([]string, 2)
				for i, row := range []struct{ user, collection, key, value string }{
					{"peer", "main", "alpha", values[0]}, {"peer", "main", "beta", values[1]},
					{"foreign", "main", "foreign-control", "Foreign control remains separate."},
					{"peer", "other", "alpha", "Sibling collection remains separate."},
				} {
					got := rpc(row.user, "save_memory", map[string]any{"key": row.key, "value": row.value, "type": "user", "collection": row.collection, "room": "memory", "hall": "fact"})
					var saved struct {
						JobID, Status string
					}
					var payload map[string]any
					if err := json.Unmarshal([]byte(got.Content[0].Text), &payload); err != nil {
						t.Fatal(err)
					}
					saved.JobID, _ = payload["index_job_id"].(string)
					saved.Status, _ = payload["index_status"].(string)
					if saved.JobID == "" || saved.Status != "pending" {
						t.Fatalf("save did not enqueue actual index effect: %+v", payload)
					}
					if i < 2 {
						initialJobs[i] = saved.JobID
					}
					if err := f.db.QueryRow(Q(`SELECT id FROM memories WHERE key=$1 AND owner_id=$2 AND collection_name=$3`), row.key, row.user, row.collection).Scan(&ids[i]); err != nil {
						t.Fatal(err)
					}
				}
				drain(4)
				before := consolidationIndexRows(t, f)
				for i, id := range ids {
					collection := "main"
					if i == 3 {
						collection = "other"
					}
					if !cm.HasRecord(memoryCollectionNameHTTP(collection), id) {
						t.Fatalf("initial physical vector absent: %s", id)
					}
				}
				hits, err := h.CollectionSearch("_memories_main", []float32{1, 0}, 10)
				if err != nil {
					t.Fatal(err)
				}
				var sourceCos float64
				for _, hit := range hits {
					if hit.ID == ids[1] {
						sourceCos = float64(hit.Score)
					}
				}
				defaults := consolidate.DefaultConfig()
				if math.Abs(sourceCos-0.94) > 0.0001 || sourceCos <= defaults.TauLow || sourceCos >= defaults.TauHigh {
					t.Fatalf("physical source cosine=%f does not force abstract", sourceCos)
				}
				recall := func(want ...string) {
					t.Helper()
					var literalMatches int
					if err := f.db.QueryRow(Q(`SELECT COUNT(*) FROM memories WHERE key LIKE $1 OR value LIKE $2`), "%"+query+"%", "%"+query+"%").Scan(&literalMatches); err != nil || literalMatches != 0 {
						t.Fatalf("semantic probe can be masked by SQL fallback: count=%d err=%v", literalMatches, err)
					}
					args := map[string]any{"query": query, "collection": "main", "room": "memory", "hall": "fact", "owner_id": "foreign"}
					decode := func(result mcp.ToolResult) []map[string]any {
						var response struct {
							Results []map[string]any `json:"results"`
						}
						if err := json.Unmarshal([]byte(result.Content[0].Text), &response); err != nil {
							t.Fatal(err)
						}
						return response.Results
					}
					endpoint := h.cfg.EmbedEndpoint
					h.cfg.EmbedEndpoint = ""
					fallback := decode(rpc("peer", "recall_memory", args))
					h.cfg.EmbedEndpoint = endpoint
					if len(fallback) != 0 {
						t.Fatalf("SQL-only negative control returned rows: %+v", fallback)
					}
					calls := queryCalls.Load()
					rows := decode(rpc("peer", "recall_memory", args))
					persisted := consolidationIndexRows(t, f)
					var got []string
					for _, row := range rows {
						id, _ := row["id"].(string)
						got = append(got, id)
						for _, key := range []string{"key", "value", "type", "owner_id", "room", "hall", "verification_status", "source_task_id", "supersedes_memory_id", "superseded_by", "supersession_reason"} {
							if row[key] != persisted[id][key] {
								t.Fatalf("recall did not hydrate SQL %s for %s: %+v", key, id, row)
							}
						}
						if row["supersession_state"] != "active" || row["superseded_at"] != "" || !reflect.DeepEqual(row["source_receipt_ids"], []any{}) {
							t.Fatalf("recall provenance/active state=%+v", row)
						}
					}
					sort.Strings(got)
					want = append([]string{}, want...)
					sort.Strings(want)
					if queryCalls.Load() != calls+1 || !reflect.DeepEqual(got, want) {
						t.Fatalf("physical semantic recall IDs=%v want=%v embed calls=%d→%d", got, want, calls, queryCalls.Load())
					}
				}
				recall(ids[0], ids[1])
				applied := rpc("peer", "consolidate", map[string]any{"collection": "main", "room": "memory", "hall": "fact", "wait": true, "dry_run": false, "owner_id": "foreign", "actor_id": "foreign"})
				if !strings.Contains(applied.Content[0].Text, "candidates=2 clusters=1 actions=1 skipped=0") || provider.calls.Load() != 1 {
					t.Fatalf("abstract pipeline was not applied: %+v provider calls=%d", applied, provider.calls.Load())
				}
				after := consolidationIndexRows(t, f)
				var abstractID, runID string
				for id, row := range after {
					if row["tier"] == "semantic" {
						abstractID, runID = id, row["consolidation_run_id"].(string)
						var sources []string
						if err := json.Unmarshal([]byte(row["consolidated_from"].(string)), &sources); err != nil {
							t.Fatal(err)
						}
						sort.Strings(sources)
						wantSources := append([]string{}, ids[:2]...)
						sort.Strings(wantSources)
						if !reflect.DeepEqual(sources, wantSources) || row["owner_id"] != "peer" || row["collection_name"] != "main" || row["type"] != "user" || row["room"] != "memory" || row["hall"] != "fact" || row["value"] != strings.Join(values, " ") {
							t.Fatalf("abstract classification/lineage=%+v", row)
						}
					}
				}
				if len(after) != len(before)+1 || abstractID == "" || runID == "" {
					t.Fatalf("generated abstract absent: %+v", after)
				}
				for _, id := range ids[:2] {
					if after[id]["superseded_by"] != abstractID || after[id]["valid_until"] == nil || after[id]["consolidation_run_id"] != runID {
						t.Fatalf("source retirement/lineage=%+v", after[id])
					}
				}
				for _, id := range ids[2:] {
					if !reflect.DeepEqual(after[id], before[id]) {
						t.Fatalf("consolidation changed control %s", id)
					}
				}
				drain(3)
				if !cm.HasRecord("_memories_main", abstractID) || cm.HasRecord("_memories_main", ids[0]) || cm.HasRecord("_memories_main", ids[1]) {
					t.Fatal("apply did not publish abstract/remove physical source vectors")
				}
				recall(abstractID)
				jobs, err := h.cfg.MemoryIndexOutbox.List(ctx, "peer", 100)
				if err != nil {
					t.Fatal(err)
				}
				var delayedDelete memoryindex.Job
				for _, job := range jobs {
					if job.MemoryID == ids[0] && job.Operation == "delete_vector" {
						delayedDelete = job
					}
				}
				if delayedDelete.ID == "" || delayedDelete.Status != memoryindex.Completed {
					t.Fatalf("source delete job absent: %+v", jobs)
				}
				rpc("peer", "consolidation_revert", map[string]any{"run_id": runID, "owner_id": "foreign"})
				restored := consolidationIndexRows(t, f)
				for _, id := range ids[:2] {
					stamp, ok := restored[id]["updated_at"].(string)
					appliedStamp, appliedOK := after[id]["updated_at"].(string)
					if !ok || !appliedOK {
						t.Fatal("source revision missing")
					}
					when, err := time.Parse(time.RFC3339Nano, stamp)
					if err != nil {
						t.Fatal(err)
					}
					prior, err := time.Parse(time.RFC3339Nano, appliedStamp)
					if err != nil || !when.After(prior) {
						t.Fatal("revert did not advance source revision")
					}
					before[id]["updated_at"] = stamp
				}
				if !reflect.DeepEqual(restored, before) {
					t.Fatalf("revert did not restore source contents/preserve controls: before=%+v restored=%+v", before, restored)
				}
				jobs, err = h.cfg.MemoryIndexOutbox.List(ctx, "peer", 100)
				if err != nil {
					t.Fatal(err)
				}
				for i, id := range ids[:2] {
					found := false
					for _, job := range jobs {
						if job.MemoryID == id && job.Operation == "upsert_vector" {
							found = job.Status == memoryindex.Pending && job.Attempts == 0 && job.ID != initialJobs[i]
						}
					}
					if !found {
						t.Fatalf("identical source digest not republished on revert: %+v", jobs)
					}
				}
				// Simulate process loss after a claim but before its effect, then use
				// the durable recovery path and real worker to finish restoration.
				lost, ok, err := h.cfg.MemoryIndexOutbox.Claim(ctx)
				if err != nil || !ok || lost.Operation != "upsert_vector" || (lost.MemoryID != ids[0] && lost.MemoryID != ids[1]) {
					t.Fatalf("recovery claim=%+v ok=%t err=%v", lost, ok, err)
				}
				if recovered, err := h.cfg.MemoryIndexOutbox.RecoverRunning(ctx); err != nil || recovered != 1 {
					t.Fatalf("bounded recovery=%d err=%v", recovered, err)
				}
				drain(3)
				if cm.HasRecord("_memories_main", abstractID) || !cm.HasRecord("_memories_main", ids[0]) || !cm.HasRecord("_memories_main", ids[1]) || !cm.HasRecord("_memories_main", ids[2]) || !cm.HasRecord("_memories_other", ids[3]) {
					t.Fatal("revert/recovery physical vector set differs from sources and controls")
				}
				recall(ids[0], ids[1])
				if err := executeMemoryIndexJob(ctx, h.cfg, delayedDelete); err != nil || !cm.HasRecord("_memories_main", ids[0]) {
					t.Fatalf("late historical source delete removed restored vector: %v", err)
				}
				beforeRepeat, err := h.cfg.MemoryIndexOutbox.List(ctx, "", 100)
				if err != nil {
					t.Fatal(err)
				}
				rpc("peer", "consolidation_revert", map[string]any{"run_id": runID})
				afterRepeat, err := h.cfg.MemoryIndexOutbox.List(ctx, "", 100)
				if err != nil || !reflect.DeepEqual(beforeRepeat, afterRepeat) || !reflect.DeepEqual(consolidationIndexRows(t, f), before) {
					t.Fatalf("repeated revert/late delete changed restored rows or jobs: err=%v", err)
				}
				drain(0)
				recall(ids[0], ids[1])
				t.Logf("sources→abstract→physical semantic recall→revert verified; cosine=%.4f provider_calls=%d semantic_query_calls=%d recovery_claim=%s", sourceCos, provider.calls.Load(), queryCalls.Load(), lost.ID)
			})
		}
	})
}
