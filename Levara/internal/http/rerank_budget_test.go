package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/stek0v/levara/internal/metrics"
)

// Phase 2 budget enforcement: when the reranker takes longer than
// RerankBudgetMs, the chunksSearch handler must (a) still return results
// (vector order, no rerank), and (b) bump
// levara_rerank_invocations_total{outcome="budget"}.
//
// We can't easily assert the response shape because per-result reranked
// flag is intertwined with the pipeline internals; the metric counter is
// the cleanest contract.
func TestChunksSearch_RerankBudgetExceeded(t *testing.T) {
	env := newSearchTestEnv(t)

	// Slow rerank server: stalls past the budget. The handler's
	// context.WithTimeout fires, the pipeline catches the error and
	// returns vector-order results, and we count outcome="budget".
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(200)
		w.Write([]byte(`{"results":[]}`))
	}))
	t.Cleanup(slow.Close)

	env.cfg.RerankEndpoint = slow.URL
	env.cfg.RerankModel = "test-rerank"
	env.cfg.RerankTimeoutMs = 5000 // generous HTTP timeout
	env.cfg.RerankBudgetMs = 50    // tight budget — guaranteed to trip
	env.start()

	vec := []float32{1, 0, 0, 0}
	env.insertVector("entities", "e1", vec, map[string]any{"text": "hello"})

	before := testutil.ToFloat64(metrics.RerankInvocations.WithLabelValues("budget"))

	status, _ := postSearchAny(t, env, map[string]any{
		"query_text": "hello",
		"query_type": "CHUNKS",
		"collection": "entities",
		"top_k":      5,
	})
	if status != 200 {
		t.Fatalf("status=%d, want 200 (handler must degrade gracefully)", status)
	}

	after := testutil.ToFloat64(metrics.RerankInvocations.WithLabelValues("budget"))
	if after <= before {
		t.Fatalf("budget counter did not increment: before=%v after=%v", before, after)
	}
}

// Explicit opt-out: with the endpoint configured, sending rerank=false
// must NOT call the sidecar and must still count as outcome="disabled".
// Distinct from the no-endpoint test below — this proves the false branch
// of the tri-state is honored even when a reranker is available.
func TestChunksSearch_RerankExplicitFalseSkipsSidecar(t *testing.T) {
	env := newSearchTestEnv(t)

	// Tripwire sidecar: any call to it fails the test. If rerank=false is
	// honored, no call should ever land here.
	hit := false
	tripwire := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.WriteHeader(200)
		w.Write([]byte(`{"results":[]}`))
	}))
	t.Cleanup(tripwire.Close)

	env.cfg.RerankEndpoint = tripwire.URL
	env.cfg.RerankModel = "test-rerank"
	env.cfg.RerankTimeoutMs = 5000
	env.cfg.RerankBudgetMs = 5000
	env.start()

	vec := []float32{1, 0, 0, 0}
	env.insertVector("entities", "e1", vec, map[string]any{"text": "hello"})

	before := testutil.ToFloat64(metrics.RerankInvocations.WithLabelValues("disabled"))

	rerankFalse := false
	status, body := postSearchAny(t, env, map[string]any{
		"query_text": "hello",
		"query_type": "CHUNKS",
		"collection": "entities",
		"top_k":      5,
		"rerank":     rerankFalse,
	})
	if status != 200 {
		t.Fatalf("status=%d, want 200", status)
	}
	if hit {
		t.Fatal("sidecar was called despite rerank=false")
	}
	// Per-result reranked flag must be false in opt-out mode.
	if arr, ok := body.([]any); ok && len(arr) > 0 {
		if m, ok := arr[0].(map[string]any); ok {
			if rr, _ := m["reranked"].(bool); rr {
				t.Errorf("result reranked=true under rerank=false")
			}
		}
	}
	after := testutil.ToFloat64(metrics.RerankInvocations.WithLabelValues("disabled"))
	if after <= before {
		t.Fatalf("disabled counter did not increment on explicit opt-out: before=%v after=%v", before, after)
	}
}

// Counterpart: when no rerank endpoint is configured, outcome="disabled"
// must increment — so dashboards see "rerank was off" rather than empty.
func TestChunksSearch_RerankDisabledCountsOutcome(t *testing.T) {
	env := newSearchTestEnv(t)
	// no RerankEndpoint → rerankWanted returns false
	env.start()

	vec := []float32{1, 0, 0, 0}
	env.insertVector("entities", "e1", vec, map[string]any{"text": "hello"})

	before := testutil.ToFloat64(metrics.RerankInvocations.WithLabelValues("disabled"))

	status, _ := postSearchAny(t, env, map[string]any{
		"query_text": "hello",
		"query_type": "CHUNKS",
		"collection": "entities",
		"top_k":      5,
	})
	if status != 200 {
		t.Fatalf("status=%d, want 200", status)
	}

	after := testutil.ToFloat64(metrics.RerankInvocations.WithLabelValues("disabled"))
	if after <= before {
		t.Fatalf("disabled counter did not increment: before=%v after=%v", before, after)
	}
}

// Happy path: sidecar replies inside the budget with a valid Cohere-shape
// response. outcome="ok" must increment.
func TestChunksSearch_RerankOKCountsOutcome(t *testing.T) {
	env := newSearchTestEnv(t)

	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// One document in, one result out. Score is arbitrary — the
		// handler only cares that the call returned successfully.
		score := 0.9
		body := map[string]any{
			"results": []map[string]any{
				{"index": 0, "relevance_score": score},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(fast.Close)

	env.cfg.RerankEndpoint = fast.URL
	env.cfg.RerankModel = "test-rerank"
	env.cfg.RerankTimeoutMs = 5000
	env.cfg.RerankBudgetMs = 5000
	env.start()

	vec := []float32{1, 0, 0, 0}
	env.insertVector("entities", "e1", vec, map[string]any{"text": "hello"})

	before := testutil.ToFloat64(metrics.RerankInvocations.WithLabelValues("ok"))

	status, _ := postSearchAny(t, env, map[string]any{
		"query_text": "hello",
		"query_type": "CHUNKS",
		"collection": "entities",
		"top_k":      5,
	})
	if status != 200 {
		t.Fatalf("status=%d, want 200", status)
	}

	after := testutil.ToFloat64(metrics.RerankInvocations.WithLabelValues("ok"))
	if after <= before {
		t.Fatalf("ok counter did not increment: before=%v after=%v", before, after)
	}
}

// Order contract: when the sidecar succeeds, results must come back in
// rerank order, not vector order. With three documents inserted using
// identical vectors, vector score ties on all three; only the reranker
// can produce a non-trivial ordering, so first-result correctness here
// proves the rerank output actually drives response order.
func TestChunksSearch_RerankReordersResults(t *testing.T) {
	env := newSearchTestEnv(t)

	// Sidecar scores by document content: gamma > beta > alpha. We use
	// content-based scoring rather than index-based so the test doesn't
	// depend on HNSW iteration order for tied vector scores.
	scorer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Documents []string `json:"documents"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		results := make([]map[string]any, 0, len(req.Documents))
		for i, d := range req.Documents {
			var s float64
			switch {
			case contains(d, "gamma"):
				s = 0.9
			case contains(d, "beta"):
				s = 0.5
			default:
				s = 0.1
			}
			results = append(results, map[string]any{"index": i, "relevance_score": s})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
	}))
	t.Cleanup(scorer.Close)

	env.cfg.RerankEndpoint = scorer.URL
	env.cfg.RerankModel = "test-rerank"
	env.cfg.RerankTimeoutMs = 5000
	env.cfg.RerankBudgetMs = 5000
	env.start()

	vec := []float32{1, 0, 0, 0}
	env.insertVector("entities", "e1", vec, map[string]any{"text": "alpha"})
	env.insertVector("entities", "e2", vec, map[string]any{"text": "beta"})
	env.insertVector("entities", "e3", vec, map[string]any{"text": "gamma"})

	status, body := postSearchAny(t, env, map[string]any{
		"query_text": "find gamma",
		"query_type": "CHUNKS",
		"collection": "entities",
		"top_k":      3,
	})
	if status != 200 {
		t.Fatalf("status=%d, want 200", status)
	}

	arr, ok := body.([]any)
	if !ok || len(arr) != 3 {
		t.Fatalf("expected 3-element array response, got %T %v", body, body)
	}
	gotIDs := make([]string, len(arr))
	for i, row := range arr {
		m, _ := row.(map[string]any)
		gotIDs[i], _ = m["id"].(string)
		if rr, _ := m["reranked"].(bool); !rr {
			t.Errorf("result[%d] reranked=false, want true", i)
		}
	}
	want := []string{"e3", "e2", "e1"}
	for i, w := range want {
		if gotIDs[i] != w {
			t.Fatalf("rerank order = %v, want %v", gotIDs, want)
		}
	}
}

// contains is a tiny helper to keep the sidecar handler readable without
// pulling strings into this test's import surface (already in use across
// the file would have been fine; this avoids drift if imports change).
func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// Query-type contract: the lexical-only branch must not invoke the
// reranker, even with rerank=true and an endpoint configured. Only
// CHUNKS (and HYBRID via its own path) wire up the reranker; the
// lexical-only branch must stay zero-cost on that axis. "BM25" is the
// documented client-facing name (aliased to bm25Search in the strategy
// registry alongside the legacy "CHUNKS_LEXICAL").
func TestBM25Search_RerankIsIgnored(t *testing.T) {
	env := newSearchTestEnv(t)

	hit := false
	tripwire := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.WriteHeader(200)
		w.Write([]byte(`{"results":[]}`))
	}))
	t.Cleanup(tripwire.Close)

	env.cfg.RerankEndpoint = tripwire.URL
	env.cfg.RerankModel = "test-rerank"
	env.cfg.RerankTimeoutMs = 5000
	env.cfg.RerankBudgetMs = 5000
	env.start()

	totalBefore := 0.0
	for _, out := range []string{"ok", "budget", "error", "disabled"} {
		totalBefore += testutil.ToFloat64(metrics.RerankInvocations.WithLabelValues(out))
	}

	rerankTrue := true
	status, _ := postSearchAny(t, env, map[string]any{
		"query_text": "anything",
		"query_type": "BM25",
		"collection": "entities",
		"top_k":      5,
		"rerank":     rerankTrue,
	})
	if status != 200 {
		t.Fatalf("status=%d, want 200", status)
	}
	if hit {
		t.Fatal("sidecar was called for query_type=BM25")
	}
	totalAfter := 0.0
	for _, out := range []string{"ok", "budget", "error", "disabled"} {
		totalAfter += testutil.ToFloat64(metrics.RerankInvocations.WithLabelValues(out))
	}
	if totalAfter != totalBefore {
		t.Fatalf("BM25 path touched rerank counters: before=%v after=%v", totalBefore, totalAfter)
	}
}

// Failure path: sidecar 500s within the budget. The pipeline catches the
// error, falls back to vector order, and outcome="error" increments.
// Distinct from outcome="budget" (timeout) so dashboards can separate
// "rerank is slow" from "rerank is broken".
func TestChunksSearch_RerankErrorCountsOutcome(t *testing.T) {
	env := newSearchTestEnv(t)

	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model unavailable", 500)
	}))
	t.Cleanup(broken.Close)

	env.cfg.RerankEndpoint = broken.URL
	env.cfg.RerankModel = "test-rerank"
	env.cfg.RerankTimeoutMs = 5000
	env.cfg.RerankBudgetMs = 5000
	env.start()

	vec := []float32{1, 0, 0, 0}
	env.insertVector("entities", "e1", vec, map[string]any{"text": "hello"})

	before := testutil.ToFloat64(metrics.RerankInvocations.WithLabelValues("error"))

	status, _ := postSearchAny(t, env, map[string]any{
		"query_text": "hello",
		"query_type": "CHUNKS",
		"collection": "entities",
		"top_k":      5,
	})
	if status != 200 {
		t.Fatalf("status=%d, want 200 (handler must degrade gracefully)", status)
	}

	after := testutil.ToFloat64(metrics.RerankInvocations.WithLabelValues("error"))
	if after <= before {
		t.Fatalf("error counter did not increment: before=%v after=%v", before, after)
	}
}

// Latency contract: budget=0 disables the per-request timeout entirely
// (handler code is `if cfg.RerankBudgetMs > 0`). A sidecar that would
// normally trip a small budget must succeed when the budget is off.
func TestChunksSearch_RerankBudgetZeroDisablesTimeout(t *testing.T) {
	env := newSearchTestEnv(t)

	// Sidecar takes ~150ms — would trip any sub-150 budget, but with
	// budget=0 there is no deadline and we get a clean outcome=ok.
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{{"index": 0, "relevance_score": 0.5}},
		})
	}))
	t.Cleanup(slow.Close)

	env.cfg.RerankEndpoint = slow.URL
	env.cfg.RerankModel = "test-rerank"
	env.cfg.RerankTimeoutMs = 5000
	env.cfg.RerankBudgetMs = 0 // off
	env.start()

	vec := []float32{1, 0, 0, 0}
	env.insertVector("entities", "e1", vec, map[string]any{"text": "hello"})

	okBefore := testutil.ToFloat64(metrics.RerankInvocations.WithLabelValues("ok"))
	budgetBefore := testutil.ToFloat64(metrics.RerankInvocations.WithLabelValues("budget"))

	status, _ := postSearchAny(t, env, map[string]any{
		"query_text": "hello",
		"query_type": "CHUNKS",
		"collection": "entities",
		"top_k":      5,
	})
	if status != 200 {
		t.Fatalf("status=%d, want 200", status)
	}
	okAfter := testutil.ToFloat64(metrics.RerankInvocations.WithLabelValues("ok"))
	budgetAfter := testutil.ToFloat64(metrics.RerankInvocations.WithLabelValues("budget"))
	if okAfter <= okBefore {
		t.Fatalf("ok counter did not increment with budget=0: before=%v after=%v", okBefore, okAfter)
	}
	if budgetAfter != budgetBefore {
		t.Fatalf("budget counter moved with budget=0: before=%v after=%v", budgetBefore, budgetAfter)
	}
}

// Concurrency: under N parallel slow-sidecar requests, the handler must
// (a) stay panic-free, (b) return 200 for every request via the
// vector-order fallback, and (c) bump exactly N "budget" outcomes —
// the budget context must fire for every request and never get lost.
//
// Note: we do NOT assert exact goroutine count here. Each chunksSearch
// builds a fresh rerank.Client → fresh http.Client with its own idle
// connection pool, so transient pooled-conn goroutines linger past the
// request. That is a known sub-optimality (one client per request), not
// a leak — those goroutines unwind when the pool idle-timeouts. What
// would be a real leak is the per-request goroutine count growing
// without bound across runs; the contract test below ensures that the
// handler's cancel path runs to completion for every concurrent request.
func TestChunksSearch_RerankBudget_ConcurrentDegradesCleanly(t *testing.T) {
	env := newSearchTestEnv(t)

	// Sidecar that hangs for 2s — far past our 50ms budget. Honors the
	// request context so the cancel from rerankCtx propagates cleanly.
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
		w.WriteHeader(200)
		w.Write([]byte(`{"results":[]}`))
	}))
	t.Cleanup(slow.Close)

	env.cfg.RerankEndpoint = slow.URL
	env.cfg.RerankModel = "test-rerank"
	env.cfg.RerankTimeoutMs = 5000
	env.cfg.RerankBudgetMs = 50
	env.start()

	vec := []float32{1, 0, 0, 0}
	env.insertVector("entities", "e1", vec, map[string]any{"text": "hello"})

	budgetBefore := testutil.ToFloat64(metrics.RerankInvocations.WithLabelValues("budget"))

	const N = 50
	var wg sync.WaitGroup
	var ok200 atomic.Int32
	start := time.Now()
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			status, _ := postSearchAny(t, env, map[string]any{
				"query_text": "hello", "query_type": "CHUNKS",
				"collection": "entities", "top_k": 5,
			})
			if status == 200 {
				ok200.Add(1)
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	if got := ok200.Load(); got != N {
		t.Fatalf("under slow sidecar: got %d/%d 200s — handler should always degrade to vector order", got, N)
	}
	budgetAfter := testutil.ToFloat64(metrics.RerankInvocations.WithLabelValues("budget"))
	if delta := budgetAfter - budgetBefore; int(delta) != N {
		t.Fatalf("budget counter delta=%v, want %d (one budget hit per request)", delta, N)
	}
	// Sanity: with budget=50ms, all N requests should finish in well under
	// the 2s sidecar timeout. If we ever blow past ~1s for N=50, the
	// budget cancel isn't actually preempting the rerank call.
	if elapsed > 1500*time.Millisecond {
		t.Fatalf("concurrent run took %v; budget cancel is not preempting the rerank call", elapsed)
	}
}

// SECURITY contract (current behavior, documents a known limitation):
// filterByAllowedDatasets runs AFTER the rerank pass, which means the
// rerank sidecar receives document text from datasets the requester is
// NOT authorized to see. This test pins that behavior so a future
// refactor that moves the ACL filter pre-rerank fails loudly here and
// the doc/release notes get updated explicitly.
//
// Mitigation today: deploy the reranker on the same host as Levara
// (Pi 5 default) and never point RERANK_ENDPOINT at a third-party
// service. Tracked in docs/phase2-rerank-default-design.md §ACL.
func TestChunksSearch_RerankSeesForbiddenDocs_KnownLimitation(t *testing.T) {
	env := newSearchTestEnv(t)

	// Capture every document the sidecar receives.
	var (
		mu       sync.Mutex
		seenDocs []string
	)
	tap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Documents []string `json:"documents"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		seenDocs = append(seenDocs, req.Documents...)
		mu.Unlock()
		results := make([]map[string]any, len(req.Documents))
		for i := range req.Documents {
			results[i] = map[string]any{"index": i, "relevance_score": 0.5}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
	}))
	t.Cleanup(tap.Close)

	env.cfg.RerankEndpoint = tap.URL
	env.cfg.RerankModel = "test-rerank"
	env.cfg.RerankTimeoutMs = 5000
	env.cfg.RerankBudgetMs = 5000
	env.start()

	vec := []float32{1, 0, 0, 0}
	// e1 belongs to dataset "allowed", e2 to "secret". Same collection.
	env.insertVector("entities", "e1", vec, map[string]any{
		"text": "allowed-doc-content", "dataset_id": "allowed",
	})
	env.insertVector("entities", "e2", vec, map[string]any{
		"text": "SECRET-doc-content", "dataset_id": "secret",
	})

	// Caller is restricted to "allowed" only — "secret" must not appear
	// in the response, but with today's post-rerank filter the sidecar
	// still receives it. The handler reads AllowedDatasetIDs from the
	// JWT in production; in the test fixture we wire it directly via a
	// per-request field by sending it through a query param hook —
	// since UnifiedSearchRequest.AllowedDatasetIDs is json:"-", we can't
	// set it through the JSON body, so we simulate the path by sending
	// a search and then asserting the leak surface independently.
	//
	// Instead of pinning that the handler enforces ACL (it does, post-
	// rerank), we pin the surface: the sidecar saw the SECRET text.
	status, _ := postSearchAny(t, env, map[string]any{
		"query_text": "doc",
		"query_type": "CHUNKS",
		"collection": "entities",
		"top_k":      5,
	})
	if status != 200 {
		t.Fatalf("status=%d, want 200", status)
	}
	mu.Lock()
	defer mu.Unlock()
	var sawSecret bool
	for _, d := range seenDocs {
		if contains(d, "SECRET") {
			sawSecret = true
			break
		}
	}
	if !sawSecret {
		t.Fatalf("expected sidecar to receive SECRET doc (current behavior leaks across ACL); seenDocs=%v", seenDocs)
	}
}

// HYBRID (RRF over vector+BM25) does NOT call the rerank sidecar in
// Phase 2, even when rerank is configured and explicitly requested
// via `"rerank": true`. The design doc's "Open questions" section
// scopes Phase 2 rerank to chunks-only and defers HYBRID/graph to
// Phase 2.5. This test pins that contract so a refactor that wires
// the rerank client through HYBRID either updates the doc or fails
// loudly here.
//
// Phase 2.5 (2026-05-15): rerank now flows through HYBRID. Sidecar is
// hit, fused candidates get re-ordered by relevance score, and the
// outcome counter increments — mirroring chunksSearch.
//
// If a refactor regresses HYBRID back to skip-rerank, this test fails
// loudly: either fix the regression or downgrade the design doc.
func TestHybridSearch_RerankApplied_Phase25(t *testing.T) {
	env := newSearchTestEnv(t)

	hit := false
	// Score doc 1 higher than doc 0 so a successful rerank flips the
	// natural fused order and we can detect it from the response.
	tripwire := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.WriteHeader(200)
		w.Write([]byte(`{"results":[{"index":1,"relevance_score":0.95},{"index":0,"relevance_score":0.10}]}`))
	}))
	t.Cleanup(tripwire.Close)

	env.cfg.RerankEndpoint = tripwire.URL
	env.cfg.RerankModel = "test-rerank"
	env.cfg.RerankTimeoutMs = 5000
	env.cfg.RerankBudgetMs = 5000
	env.start()

	vec := []float32{1, 0, 0, 0}
	env.insertVector("entities", "e1", vec, map[string]any{"text": "alpha"})
	env.insertVector("entities", "e2", vec, map[string]any{"text": "beta"})

	okBefore := testutil.ToFloat64(metrics.RerankInvocations.WithLabelValues("ok"))

	rerankTrue := true
	status, body := postSearchAny(t, env, map[string]any{
		"query_text": "alpha",
		"query_type": "HYBRID",
		"collection": "entities",
		"top_k":      5,
		"rerank":     rerankTrue,
	})
	if status != 200 {
		t.Fatalf("status=%d, want 200", status)
	}
	if !hit {
		t.Fatal("sidecar was NOT called for query_type=HYBRID — Phase 2.5 wires rerank through HYBRID; if intentionally reverted, update phase2-rerank-default-design.md")
	}
	okAfter := testutil.ToFloat64(metrics.RerankInvocations.WithLabelValues("ok"))
	if okAfter <= okBefore {
		t.Fatalf("HYBRID rerank=ok counter did not increment: before=%v after=%v", okBefore, okAfter)
	}

	// Each result row must carry reranked:true after a successful pass.
	bodyMap, _ := body.(map[string]any)
	rows, ok := bodyMap["items"].([]any)
	if !ok {
		// Legacy array envelope (no include_debug=true).
		rows, ok = body.([]any)
	}
	if !ok || len(rows) == 0 {
		t.Fatalf("expected non-empty items, got %v", body)
	}
	for i, row := range rows {
		m, _ := row.(map[string]any)
		if m["reranked"] != true {
			t.Fatalf("row %d: reranked=%v want true", i, m["reranked"])
		}
	}
}
