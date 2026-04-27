package main

import (
	"os"
	"strconv"
)

type rerankConfig struct {
	Endpoint  string
	Model     string
	TimeoutMs int
}

func rerankConfigFromEnv() rerankConfig {
	cfg := rerankConfig{
		Endpoint: os.Getenv("RERANK_ENDPOINT"),
		Model:    os.Getenv("RERANK_MODEL"),
	}
	if raw := os.Getenv("RERANK_TIMEOUT_MS"); raw != "" {
		if timeoutMs, err := strconv.Atoi(raw); err == nil {
			cfg.TimeoutMs = timeoutMs
		}
	}
	return cfg
}
