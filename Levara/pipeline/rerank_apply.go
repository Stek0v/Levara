package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/stek0v/levara/internal/metrics"
	"github.com/stek0v/levara/pkg/rerank"
)

// FilterScoredByAllowedDatasets drops ScoredResult rows whose metadata names
// a dataset outside allowedIDs. Pass nil to disable filtering (dev mode /
// no JWT scopes). Apply BEFORE ApplyRerankToScored so forbidden chunks never
// reach the third-party reranker.
func FilterScoredByAllowedDatasets(results []ScoredResult, allowedIDs []string) []ScoredResult {
	if allowedIDs == nil {
		return results
	}
	allowed := make(map[string]bool, len(allowedIDs))
	for _, id := range allowedIDs {
		allowed[id] = true
	}
	filtered := results[:0]
	for _, r := range results {
		dsID := datasetIDFromMeta(r.Metadata)
		if dsID == "" || allowed[dsID] {
			filtered = append(filtered, r)
		}
	}
	return filtered
}

func datasetIDFromMeta(meta json.RawMessage) string {
	if len(meta) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(meta, &m); err != nil {
		return ""
	}
	if d, ok := m["dataset_id"].(string); ok && d != "" {
		return d
	}
	if d, ok := m["project_id"].(string); ok {
		return d
	}
	return ""
}

// ApplyRerankConfig configures ApplyRerankToScored.
//
// BudgetMs bounds the cross-encoder round-trip (0 = no timeout, falls back
// to client default). ScoreGapThreshold > 0 enables the Phase 2.5 adaptive
// gate: skip the sidecar when vector-score spread between the top and
// bottom candidate already exceeds it.
type ApplyRerankConfig struct {
	BudgetMs          int
	ScoreGapThreshold float32
}

// ApplyRerankToScored reranks an ACL-prefiltered slice of ScoredResult
// against `query`. Returns (reranked, ordered) — `reranked=true` only when
// the sidecar successfully scored at least one row. On budget/error/no_text
// the input order is preserved (graceful degradation) and the matching
// outcome counter (`ok|budget|error|no_text|skipped_gap`) is bumped.
//
// Caller is responsible for ACL filtering BEFORE calling this — passing
// unfiltered candidates leaks forbidden-dataset text to the third-party
// reranker (the bug that motivated retiring SearchByTextWithRerank).
//
// Mirrors internal/http.applyRerankToScored so gRPC + MCP can share the
// same post-Phase-2.5 path without duplicating the gate / outcome logic.
func ApplyRerankToScored(
	ctx context.Context,
	cfg ApplyRerankConfig,
	rerankClient *rerank.Client,
	query string,
	in []ScoredResult,
	topK int,
) (bool, []ScoredResult) {
	trim := func(s []ScoredResult) []ScoredResult {
		if topK > 0 && len(s) > topK {
			return s[:topK]
		}
		return s
	}
	if len(in) == 0 || rerankClient == nil || !rerankClient.Enabled() {
		return false, trim(in)
	}

	docs := make([]string, 0, len(in))
	idxMap := make([]int, 0, len(in))
	for i, r := range in {
		if t := ExtractText(r.Metadata); t != "" {
			docs = append(docs, t)
			idxMap = append(idxMap, i)
		}
	}
	if len(docs) == 0 {
		metrics.RerankInvocations.WithLabelValues("no_text").Inc()
		return false, trim(in)
	}

	if cfg.ScoreGapThreshold > 0 && len(in) >= 2 {
		if in[0].Score-in[len(in)-1].Score > cfg.ScoreGapThreshold {
			metrics.RerankInvocations.WithLabelValues("skipped_gap").Inc()
			return false, trim(in)
		}
	}

	rerankCtx := ctx
	var cancelRerank context.CancelFunc
	if cfg.BudgetMs > 0 {
		rerankCtx, cancelRerank = context.WithTimeout(ctx, time.Duration(cfg.BudgetMs)*time.Millisecond)
	}
	scored, err := rerankClient.Rerank(rerankCtx, query, docs)
	deadlineHit := cancelRerank != nil && errors.Is(rerankCtx.Err(), context.DeadlineExceeded)
	if cancelRerank != nil {
		cancelRerank()
	}
	switch {
	case err == nil && len(scored) > 0:
		metrics.RerankInvocations.WithLabelValues("ok").Inc()
		placed := make(map[int]bool, len(scored))
		out := make([]ScoredResult, 0, len(in))
		for _, s := range scored {
			if s.Index < 0 || s.Index >= len(idxMap) {
				continue
			}
			orig := idxMap[s.Index]
			if placed[orig] {
				continue
			}
			placed[orig] = true
			out = append(out, in[orig])
		}
		for i, r := range in {
			if !placed[i] {
				out = append(out, r)
			}
		}
		return true, trim(out)
	case deadlineHit:
		metrics.RerankInvocations.WithLabelValues("budget").Inc()
	default:
		metrics.RerankInvocations.WithLabelValues("error").Inc()
	}
	return false, trim(in)
}
