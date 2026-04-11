package router

import (
	"context"
	"database/sql"
	"log"
	"sync"
)

// AdaptiveWeights tracks feedback-driven weight adjustments per search type.
// Weights multiply the base signal score from the heuristic router.
type AdaptiveWeights struct {
	mu           sync.RWMutex
	weights      map[string]float64 // search_type → weight multiplier (default 1.0)
	successCount map[string]int     // search_type → count of rating >= 4
	totalCount   map[string]int     // search_type → total rated queries
	learningRate float64            // how fast weights change, default 0.1
	db           *sql.DB            // persistence (nil = in-memory only)
	dirtyCount   int                // number of updates since last persist
}

// NewAdaptiveWeights creates an adaptive weight tracker.
// If db is non-nil, loads existing weights from routing_weights table.
// learningRate controls sensitivity: 0.1 = moderate, 0.01 = slow, 0.5 = fast.
func NewAdaptiveWeights(db *sql.DB, learningRate float64) *AdaptiveWeights {
	if learningRate <= 0 {
		learningRate = 0.1
	}
	aw := &AdaptiveWeights{
		weights:      make(map[string]float64),
		successCount: make(map[string]int),
		totalCount:   make(map[string]int),
		learningRate: learningRate,
		db:           db,
	}
	if db != nil {
		aw.Load(context.Background())
	}
	return aw
}

// RecordFeedback updates weights based on new feedback.
// rating >= 4 = success, < 4 = failure.
func (aw *AdaptiveWeights) RecordFeedback(searchType string, rating int) {
	if searchType == "" {
		return
	}
	aw.mu.Lock()
	defer aw.mu.Unlock()

	aw.totalCount[searchType]++
	if rating >= 4 {
		aw.successCount[searchType]++
	}

	// Recompute weight for this type
	total := aw.totalCount[searchType]
	success := aw.successCount[searchType]
	if total > 0 {
		successRate := float64(success) / float64(total)
		baseline := 0.5
		weight := 1.0 + aw.learningRate*float64(total)*(successRate-baseline)
		// Clamp to [0.5, 1.5]
		if weight < 0.5 {
			weight = 0.5
		}
		if weight > 1.5 {
			weight = 1.5
		}
		aw.weights[searchType] = weight
	}

	aw.dirtyCount++
	if aw.dirtyCount >= 10 && aw.db != nil {
		aw.persistLocked(context.Background())
		aw.dirtyCount = 0
	}
}

// AdjustScore multiplies a base score by the adaptive weight for this search type.
// If no feedback exists for this type, returns the base score unchanged (weight = 1.0).
func (aw *AdaptiveWeights) AdjustScore(searchType string, baseScore float64) float64 {
	if aw == nil {
		return baseScore
	}
	aw.mu.RLock()
	w, ok := aw.weights[searchType]
	aw.mu.RUnlock()
	if !ok {
		return baseScore
	}
	return baseScore * w
}

// GetWeight returns the current adaptive weight for a search type.
func (aw *AdaptiveWeights) GetWeight(searchType string) float64 {
	if aw == nil {
		return 1.0
	}
	aw.mu.RLock()
	defer aw.mu.RUnlock()
	if w, ok := aw.weights[searchType]; ok {
		return w
	}
	return 1.0
}

// AllWeights returns a copy of all current weights.
func (aw *AdaptiveWeights) AllWeights() map[string]float64 {
	if aw == nil {
		return nil
	}
	aw.mu.RLock()
	defer aw.mu.RUnlock()
	cp := make(map[string]float64, len(aw.weights))
	for k, v := range aw.weights {
		cp[k] = v
	}
	return cp
}

// Persist writes current weights to DB.
func (aw *AdaptiveWeights) Persist(ctx context.Context) error {
	aw.mu.Lock()
	defer aw.mu.Unlock()
	return aw.persistLocked(ctx)
}

func (aw *AdaptiveWeights) persistLocked(ctx context.Context) error {
	if aw.db == nil {
		return nil
	}
	for st, w := range aw.weights {
		_, err := aw.db.ExecContext(ctx,
			`INSERT INTO routing_weights (search_type, weight, success_count, total_count, updated_at)
			 VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)
			 ON CONFLICT(search_type) DO UPDATE SET weight=excluded.weight, success_count=excluded.success_count, total_count=excluded.total_count, updated_at=CURRENT_TIMESTAMP`,
			st, w, aw.successCount[st], aw.totalCount[st])
		if err != nil {
			log.Printf("[adaptive] persist %s: %v", st, err)
		}
	}
	return nil
}

// Load reads weights from DB.
func (aw *AdaptiveWeights) Load(ctx context.Context) error {
	if aw.db == nil {
		return nil
	}
	rows, err := aw.db.QueryContext(ctx,
		"SELECT search_type, weight, success_count, total_count FROM routing_weights")
	if err != nil {
		// Table may not exist yet
		return nil
	}
	defer rows.Close()

	for rows.Next() {
		var st string
		var w float64
		var sc, tc int
		if rows.Scan(&st, &w, &sc, &tc) == nil {
			aw.weights[st] = w
			aw.successCount[st] = sc
			aw.totalCount[st] = tc
		}
	}
	return nil
}
