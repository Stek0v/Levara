package http

import (
	"math"
	"testing"
)

func TestSyncCanonicalNativeBoundaries(t *testing.T) {
	for _, raw := range []string{"", " ", "{}"} {
		rank, err := syncJSONRank(raw)
		if err != nil || rank != "{}" {
			t.Fatalf("legacy empty properties: %q => %q %v", raw, rank, err)
		}
	}
	edge, err := normalizeSyncEdge(syncGraphEdge{Confidence: .7})
	if err != nil || edge.Confidence != float64(float32(.7)) {
		t.Fatalf("native REAL precision: %#v %v", edge, err)
	}
	for _, value := range []float64{1e-100, math.Inf(1), math.NaN()} {
		if _, err := normalizeSyncEdge(syncGraphEdge{Confidence: value}); err == nil {
			t.Fatalf("unrepresentable native confidence accepted: %v", value)
		}
	}
	zero, err := normalizeSyncEdge(syncGraphEdge{})
	if err != nil || zero.Confidence != 1 {
		t.Fatal("legacy zero confidence default lost")
	}
}
