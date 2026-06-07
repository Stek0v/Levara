// qwen3rerank — tiny HTTP front that translates Cohere-compat rerank
// requests into Qwen3-Reranker chat completions.
//
// Runs as a sidecar in docker-compose.qwen3.yml next to the llama-server
// that actually hosts the reranker model. Levara (via pkg/rerank.Client)
// posts {query, documents, top_n} here; we fan out to N yes/no chat
// completions and aggregate logprobs into scores. See
// pkg/rerank/qwen3_adapter.go for the adapter itself.
//
// Env vars:
//
//	QWEN3_UPSTREAM    = http://qwen3-rerank-llm:9002   (required)
//	QWEN3_MODEL       = qwen3-reranker-0.6b            (required)
//	QWEN3_TIMEOUT_MS  = 5000                           (per-pair request timeout)
//	QWEN3_CONCURRENCY = 4                              (parallel pairs)
//	PORT              = 9003                           (listen port)
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"

	"github.com/stek0v/levara/pkg/rerank"
)

func main() {
	upstream := mustEnv("QWEN3_UPSTREAM")
	model := envOr("QWEN3_MODEL", "qwen3-reranker-0.6b")
	timeoutMs := envInt("QWEN3_TIMEOUT_MS", 5000)
	concurrency := envInt("QWEN3_CONCURRENCY", 4)
	port := envOr("PORT", "9003")

	// topN=0 → keep all documents, caller supplies top_n in body. The
	// adapter respects the request-level top_n.
	client := rerank.NewQwen3Client(upstream, model, 0, timeoutMs, concurrency)

	mux := http.NewServeMux()
	mux.Handle("/rerank", rerank.QwenRerankHTTPHandler(client))
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		// Pragma: liveness only. Doesn't probe the upstream — that
		// check lives on the llama-server healthcheck. Keeps this
		// handler cheap so Docker doesn't spam the upstream on every
		// 15s tick.
		_, _ = w.Write([]byte(`{"status":"ok","upstream":"` + upstream + `","model":"` + model + `"}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Informational root so a curl without a path still gives a
		// useful response instead of 404.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"service":  "qwen3rerank",
			"upstream": upstream,
			"model":    model,
			"endpoint": "/rerank (Cohere-compatible)",
		})
	})

	log.Printf("qwen3rerank listening on :%s (upstream=%s, model=%s, concurrency=%d)",
		port, upstream, model, concurrency)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatalf("http serve: %v", err)
	}
}

func mustEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		log.Fatalf("required env %s is empty", k)
	}
	return v
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
