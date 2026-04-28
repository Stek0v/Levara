package embed

import (
	"strings"

	"github.com/stek0v/levara/internal/store"
)

// DriftCheckResult reports an embedding configuration mismatch for one collection.
type DriftCheckResult struct {
	Collection    string `json:"collection"`
	ExpectedModel string `json:"expected_model"`
	ExpectedDim   int    `json:"expected_dim"`
	ActualModel   string `json:"actual_model"`
	ActualDim     int    `json:"actual_dim"`
	IsDrifted     bool   `json:"is_drifted"`
	RecordCount   int    `json:"record_count"`
}

// CheckDrift compares current embedding config against all collections.
// Returns list of collections where model or dimension doesn't match.
// Skips internal collections (prefixed with "_") and empty collections.
func CheckDrift(collections *store.CollectionManager, currentModel string, currentDim int) []DriftCheckResult {
	if collections == nil {
		return nil
	}

	var results []DriftCheckResult
	for _, name := range collections.List() {
		// Skip internal collections
		if strings.HasPrefix(name, "_") {
			continue
		}

		meta := collections.GetMeta(name)
		if meta.RecordCount == 0 {
			continue // empty collection — not drifted
		}

		isDrifted := false
		if meta.EmbeddingModel != "" && meta.EmbeddingModel != currentModel {
			isDrifted = true
		}
		if meta.EmbeddingDim > 0 && meta.EmbeddingDim != currentDim {
			isDrifted = true
		}

		if isDrifted {
			results = append(results, DriftCheckResult{
				Collection:    name,
				ExpectedModel: currentModel,
				ExpectedDim:   currentDim,
				ActualModel:   meta.EmbeddingModel,
				ActualDim:     meta.EmbeddingDim,
				IsDrifted:     true,
				RecordCount:   meta.RecordCount,
			})
		}
	}
	return results
}
