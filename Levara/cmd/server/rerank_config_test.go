package main

import (
	"os"
	"testing"
)

func TestRerankConfigFromEnv(t *testing.T) {
	t.Setenv("RERANK_ENDPOINT", "http://rerank.local/rerank")
	t.Setenv("RERANK_MODEL", "qwen3-reranker-0.6b")
	t.Setenv("RERANK_TIMEOUT_MS", "1234")

	cfg := rerankConfigFromEnv()
	if cfg.Endpoint != "http://rerank.local/rerank" {
		t.Fatalf("Endpoint = %q", cfg.Endpoint)
	}
	if cfg.Model != "qwen3-reranker-0.6b" {
		t.Fatalf("Model = %q", cfg.Model)
	}
	if cfg.TimeoutMs != 1234 {
		t.Fatalf("TimeoutMs = %d, want 1234", cfg.TimeoutMs)
	}
}

func TestRerankConfigFromEnvIgnoresInvalidTimeout(t *testing.T) {
	t.Setenv("RERANK_ENDPOINT", "http://rerank.local/rerank")
	t.Setenv("RERANK_TIMEOUT_MS", "not-an-int")

	cfg := rerankConfigFromEnv()
	if cfg.TimeoutMs != 0 {
		t.Fatalf("TimeoutMs = %d, want 0 for invalid env", cfg.TimeoutMs)
	}
	if got := os.Getenv("RERANK_ENDPOINT"); got != cfg.Endpoint {
		t.Fatalf("Endpoint = %q, want env value %q", cfg.Endpoint, got)
	}
}
