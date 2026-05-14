package http

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/stek0v/levara/internal/metrics"
)

// Phase 2 concurrency contract for the chunks-search rerank path:
//   - N parallel requests must all return 200 (handler always degrades cleanly).
//   - The sum of outcome-counter deltas (ok+budget+error+disabled+no_text) must
//     equal N — every request must produce exactly one accounted outcome.
//   - Per-request p95 latency must stay within 2× RerankBudgetMs. The budget
//     cancel context is what bounds latency under a wobbly sidecar; if p95
//     blew past 2× budget it would mean the cancel isn't preempting in flight
//     reranks under contention.
//
// The sidecar replies with a valid Cohere-shape body after a random 50–200ms
// delay. With RerankBudgetMs=2000 every request should land cleanly in the
// "ok" bucket; the test also tolerates other outcomes (budget/error/no_text)
// as long as the conservation law (sum delta == N) holds — that way a future
// pipeline change that legitimately shifts the mix doesn't make this test
// flake.
//
// Run with -race; the goal is to surface any data race on the shared rerank
// client state, prometheus label maps, or pipeline reused buffers.
func TestRerankConcurrency_OutcomesSumAndLatency(t *testing.T) {
	env := newSearchTestEnv(t)

	// Sidecar returns a valid Cohere response after random 50–200ms delay.
	// Latency variance is the whole point — exercises the budget cancel path
	// and the happy path simultaneously under contention.
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	var rngMu sync.Mutex
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rngMu.Lock()
		d := 50 + rng.Intn(151) // [50,200] ms
		rngMu.Unlock()

		select {
		case <-r.Context().Done():
			// Honor cancel so the budget path unwinds cleanly.
			return
		case <-time.After(time.Duration(d) * time.Millisecond):
		}

		var req struct {
			Documents []string `json:"documents"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		results := make([]map[string]any, len(req.Documents))
		for i := range req.Documents {
			results[i] = map[string]any{"index": i, "relevance_score": 0.5}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
	}))
	t.Cleanup(sidecar.Close)

	const budgetMs = 2000
	env.cfg.RerankEndpoint = sidecar.URL
	env.cfg.RerankModel = "test-rerank"
	env.cfg.RerankTimeoutMs = 5000
	env.cfg.RerankBudgetMs = budgetMs
	env.start()

	// Seed ~10 records with proper text metadata so candidates carry a
	// rerank-eligible field; otherwise the handler short-circuits to
	// outcome="no_text" and we're not actually exercising the rerank path.
	vec := []float32{1, 0, 0, 0}
	for i := 0; i < 10; i++ {
		env.insertVector(
			"entities",
			fmt.Sprintf("e%d", i),
			vec,
			map[string]any{"text": fmt.Sprintf("document number %d about concurrency rerank load", i)},
		)
	}

	outcomes := []string{"ok", "budget", "error", "disabled", "no_text"}
	before := make(map[string]float64, len(outcomes))
	for _, o := range outcomes {
		before[o] = testutil.ToFloat64(metrics.RerankInvocations.WithLabelValues(o))
	}

	const N = 50
	var (
		wg       sync.WaitGroup
		ok200    atomic.Int32
		latMu    sync.Mutex
		latency  = make([]time.Duration, 0, N)
	)
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			t0 := time.Now()
			status, _ := postSearchAny(t, env, map[string]any{
				"query_text": "concurrency rerank load",
				"query_type": "CHUNKS",
				"collection": "entities",
				"top_k":      5,
			})
			elapsed := time.Since(t0)
			if status == 200 {
				ok200.Add(1)
			}
			latMu.Lock()
			latency = append(latency, elapsed)
			latMu.Unlock()
		}()
	}
	wg.Wait()

	if got := ok200.Load(); got != N {
		t.Fatalf("ok200=%d/%d — handler must always degrade to 200", got, N)
	}

	after := make(map[string]float64, len(outcomes))
	var deltaSum float64
	for _, o := range outcomes {
		after[o] = testutil.ToFloat64(metrics.RerankInvocations.WithLabelValues(o))
		deltaSum += after[o] - before[o]
	}
	if int(deltaSum) != N {
		t.Fatalf("outcome conservation broken: sum(delta)=%v, want %d (per-outcome: %v -> %v)",
			deltaSum, N, before, after)
	}
	t.Logf("rerank outcomes after %d requests: ok=%.0f budget=%.0f error=%.0f disabled=%.0f no_text=%.0f",
		N,
		after["ok"]-before["ok"],
		after["budget"]-before["budget"],
		after["error"]-before["error"],
		after["disabled"]-before["disabled"],
		after["no_text"]-before["no_text"],
	)

	// p95 latency — sort ascending, pick the ceil(0.95*N)-1 index. With
	// N=50 that's index 47 (the 48th value).
	sort.Slice(latency, func(i, j int) bool { return latency[i] < latency[j] })
	p95Idx := int(float64(len(latency))*0.95) - 1
	if p95Idx < 0 {
		p95Idx = 0
	}
	p95 := latency[p95Idx]
	cap := 2 * time.Duration(budgetMs) * time.Millisecond
	if p95 > cap {
		t.Fatalf("p95 latency %v exceeds 2× budget (%v); budget cancel is not bounding tail latency", p95, cap)
	}
	t.Logf("latency p50=%v p95=%v cap=%v", latency[len(latency)/2], p95, cap)
}
