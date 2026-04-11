package router

import (
	"context"
	"database/sql"
	"os"
	"testing"

	_ "github.com/ncruces/go-sqlite3/driver"
)

func setupAdaptiveDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	f, _ := os.CreateTemp("", "adaptive-test-*.db")
	path := f.Name()
	f.Close()

	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.Exec(`CREATE TABLE routing_weights (
		search_type TEXT PRIMARY KEY,
		weight REAL NOT NULL DEFAULT 1.0,
		success_count INTEGER NOT NULL DEFAULT 0,
		total_count INTEGER NOT NULL DEFAULT 0,
		updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`)

	return db, func() { db.Close(); os.Remove(path) }
}

func TestAdaptiveWeights_Default(t *testing.T) {
	aw := NewAdaptiveWeights(nil, 0.1)
	// No feedbacks → all weights = 1.0
	w := aw.GetWeight("HYBRID")
	if w != 1.0 {
		t.Errorf("Default weight: expected 1.0, got %f", w)
	}
	s := aw.AdjustScore("HYBRID", 0.85)
	if s != 0.85 {
		t.Errorf("AdjustScore with no feedback: expected 0.85, got %f", s)
	}
}

func TestAdaptiveWeights_PositiveFeedback(t *testing.T) {
	aw := NewAdaptiveWeights(nil, 0.1)
	for i := 0; i < 10; i++ {
		aw.RecordFeedback("HYBRID", 5) // all positive
	}
	w := aw.GetWeight("HYBRID")
	if w <= 1.0 {
		t.Errorf("10 positive feedbacks: expected weight > 1.0, got %f", w)
	}
	t.Logf("After 10 positive: weight=%f", w)
}

func TestAdaptiveWeights_NegativeFeedback(t *testing.T) {
	aw := NewAdaptiveWeights(nil, 0.1)
	for i := 0; i < 10; i++ {
		aw.RecordFeedback("HYBRID", 1) // all negative
	}
	w := aw.GetWeight("HYBRID")
	if w >= 1.0 {
		t.Errorf("10 negative feedbacks: expected weight < 1.0, got %f", w)
	}
	if w < 0.5 {
		t.Errorf("Weight should be clamped >= 0.5, got %f", w)
	}
	t.Logf("After 10 negative: weight=%f", w)
}

func TestAdaptiveWeights_Clamp(t *testing.T) {
	aw := NewAdaptiveWeights(nil, 0.1)
	for i := 0; i < 100; i++ {
		aw.RecordFeedback("CHUNKS", 1) // extreme negative
	}
	w := aw.GetWeight("CHUNKS")
	if w < 0.5 {
		t.Errorf("Clamp lower: expected >= 0.5, got %f", w)
	}

	aw2 := NewAdaptiveWeights(nil, 0.1)
	for i := 0; i < 100; i++ {
		aw2.RecordFeedback("RAG_COMPLETION", 5) // extreme positive
	}
	w2 := aw2.GetWeight("RAG_COMPLETION")
	if w2 > 1.5 {
		t.Errorf("Clamp upper: expected <= 1.5, got %f", w2)
	}
}

func TestAdaptiveWeights_MixedFeedback(t *testing.T) {
	aw := NewAdaptiveWeights(nil, 0.1)
	for i := 0; i < 5; i++ {
		aw.RecordFeedback("HYBRID", 5)
	}
	for i := 0; i < 5; i++ {
		aw.RecordFeedback("HYBRID", 1)
	}
	w := aw.GetWeight("HYBRID")
	// 5 success / 10 total = 0.5 success rate = baseline → weight ≈ 1.0
	if w < 0.9 || w > 1.1 {
		t.Errorf("Mixed feedback: expected ~1.0, got %f", w)
	}
	t.Logf("Mixed: weight=%f", w)
}

func TestAdaptiveWeights_UnknownType(t *testing.T) {
	aw := NewAdaptiveWeights(nil, 0.1)
	aw.RecordFeedback("NONEXISTENT_TYPE", 5)
	// Should not panic, weight recorded
	w := aw.GetWeight("NONEXISTENT_TYPE")
	if w <= 0 {
		t.Errorf("Unknown type should still get weight, got %f", w)
	}
}

func TestAdaptiveWeights_Persistence(t *testing.T) {
	db, cleanup := setupAdaptiveDB(t)
	defer cleanup()

	// Write
	aw1 := NewAdaptiveWeights(db, 0.1)
	for i := 0; i < 10; i++ {
		aw1.RecordFeedback("HYBRID", 5)
	}
	aw1.Persist(context.Background())

	// Read in new instance
	aw2 := NewAdaptiveWeights(db, 0.1)
	w := aw2.GetWeight("HYBRID")
	if w <= 1.0 {
		t.Errorf("Persisted weight should be > 1.0 after positive feedback, got %f", w)
	}
	t.Logf("Persisted + restored: weight=%f", w)
}

func TestAdaptiveWeights_AdjustScore(t *testing.T) {
	aw := NewAdaptiveWeights(nil, 0.1)
	for i := 0; i < 10; i++ {
		aw.RecordFeedback("HYBRID", 5)
	}
	w := aw.GetWeight("HYBRID")
	adjusted := aw.AdjustScore("HYBRID", 0.80)
	expected := 0.80 * w
	if adjusted < expected-0.01 || adjusted > expected+0.01 {
		t.Errorf("AdjustScore: expected %f*%f=%f, got %f", 0.80, w, expected, adjusted)
	}
}

func TestAdaptiveWeights_NilSafe(t *testing.T) {
	var aw *AdaptiveWeights
	s := aw.AdjustScore("HYBRID", 0.85)
	if s != 0.85 {
		t.Errorf("Nil adaptive: expected 0.85, got %f", s)
	}
	w := aw.GetWeight("HYBRID")
	if w != 1.0 {
		t.Errorf("Nil adaptive weight: expected 1.0, got %f", w)
	}
}
