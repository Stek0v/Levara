package http

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	"github.com/stek0v/levara/internal/metrics"
)

// emit_rag_metrics_test.go — pins the metric contract that the verify-stack
// rollout depends on. Phase 1 threshold tuning reads `levara_rag_confidence`
// histograms and `levara_rag_abstain_total{reason}`; if a future refactor
// silently drops one of those Observe/Inc calls, dashboards go blind instead
// of failing loudly.

func histogramSampleCount(t *testing.T, st string) uint64 {
	t.Helper()
	obs := metrics.RAGConfidence.WithLabelValues(st)
	m, ok := obs.(prometheus.Metric)
	if !ok {
		t.Fatalf("observer is not a prometheus.Metric: %T", obs)
	}
	var pb dto.Metric
	if err := m.Write(&pb); err != nil {
		t.Fatalf("write histogram: %v", err)
	}
	if pb.Histogram == nil {
		return 0
	}
	return pb.Histogram.GetSampleCount()
}

func TestEmitRAGMetrics_RecordsConfidenceHistogram(t *testing.T) {
	const st = "TEST_RAG_CONFIDENCE_OBSERVES"

	before := histogramSampleCount(t, st)
	emitRAGMetrics(st, 0.42, false, "", resultVerification{})
	if got := histogramSampleCount(t, st) - before; got != 1 {
		t.Errorf("RAGConfidence sample delta = %d, want 1", got)
	}
}

func TestEmitRAGMetrics_AbstainIncrementsOnReason(t *testing.T) {
	const st = "TEST_RAG_ABSTAIN_LOW_CONF"

	before := testutil.ToFloat64(metrics.RAGAbstainTotal.WithLabelValues(st, "low_confidence"))
	emitRAGMetrics(st, 0.05, true, "low_confidence", resultVerification{})
	got := testutil.ToFloat64(metrics.RAGAbstainTotal.WithLabelValues(st, "low_confidence"))
	if got-before != 1 {
		t.Errorf("low_confidence delta = %v, want 1", got-before)
	}

	// abstained=true with empty reason must NOT increment — guards against a
	// caller forgetting to pass the reason and silently inflating alerts.
	emitRAGMetrics(st, 0.05, true, "", resultVerification{})
	again := testutil.ToFloat64(metrics.RAGAbstainTotal.WithLabelValues(st, "low_confidence"))
	if again-before != 1 {
		t.Errorf("after empty-reason call, delta = %v, want still 1", again-before)
	}
}

func TestEmitRAGMetrics_VerifyDropsCountByReason(t *testing.T) {
	const st = "TEST_RAG_VERIFY_DROPS"

	beforeLow := testutil.ToFloat64(metrics.RAGVerifyDroppedTotal.WithLabelValues(st, "low_score"))
	beforeMeta := testutil.ToFloat64(metrics.RAGVerifyDroppedTotal.WithLabelValues(st, "bad_metadata"))

	emitRAGMetrics(st, 0.7, false, "", resultVerification{
		Total:           5,
		Kept:            2,
		DroppedLowScore: 2,
		DroppedBadMeta:  1,
	})

	gotLow := testutil.ToFloat64(metrics.RAGVerifyDroppedTotal.WithLabelValues(st, "low_score"))
	gotMeta := testutil.ToFloat64(metrics.RAGVerifyDroppedTotal.WithLabelValues(st, "bad_metadata"))
	if gotLow-beforeLow != 2 {
		t.Errorf("low_score delta = %v, want 2", gotLow-beforeLow)
	}
	if gotMeta-beforeMeta != 1 {
		t.Errorf("bad_metadata delta = %v, want 1", gotMeta-beforeMeta)
	}
}

func TestEmitRAGMetrics_NoDropsNoIncrement(t *testing.T) {
	const st = "TEST_RAG_VERIFY_NO_DROPS"

	beforeLow := testutil.ToFloat64(metrics.RAGVerifyDroppedTotal.WithLabelValues(st, "low_score"))
	beforeMeta := testutil.ToFloat64(metrics.RAGVerifyDroppedTotal.WithLabelValues(st, "bad_metadata"))

	emitRAGMetrics(st, 0.9, false, "", resultVerification{Total: 3, Kept: 3})

	gotLow := testutil.ToFloat64(metrics.RAGVerifyDroppedTotal.WithLabelValues(st, "low_score"))
	gotMeta := testutil.ToFloat64(metrics.RAGVerifyDroppedTotal.WithLabelValues(st, "bad_metadata"))
	if gotLow-beforeLow != 0 {
		t.Errorf("low_score should not move, got delta %v", gotLow-beforeLow)
	}
	if gotMeta-beforeMeta != 0 {
		t.Errorf("bad_metadata should not move, got delta %v", gotMeta-beforeMeta)
	}
}
