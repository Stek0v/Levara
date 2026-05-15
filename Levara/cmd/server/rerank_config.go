package main

import (
	"os"
	"strconv"
)

type rerankConfig struct {
	Endpoint           string
	Model              string
	TimeoutMs          int
	BudgetMs           int
	ScoreGapThreshold  float32
}

func rerankConfigFromEnv() rerankConfig {
	cfg := rerankConfig{
		Endpoint: os.Getenv("RERANK_ENDPOINT"),
		Model:    os.Getenv("RERANK_MODEL"),
		BudgetMs: 1500,
	}
	if raw := os.Getenv("RERANK_TIMEOUT_MS"); raw != "" {
		if timeoutMs, err := strconv.Atoi(raw); err == nil {
			cfg.TimeoutMs = timeoutMs
		}
	}
	if raw := os.Getenv("RERANK_BUDGET_MS"); raw != "" {
		if budgetMs, err := strconv.Atoi(raw); err == nil && budgetMs > 0 {
			cfg.BudgetMs = budgetMs
		}
	}
	if raw := os.Getenv("RERANK_SCORE_GAP_THRESHOLD"); raw != "" {
		if v, err := strconv.ParseFloat(raw, 32); err == nil && v > 0 {
			cfg.ScoreGapThreshold = float32(v)
		}
	}
	return cfg
}
