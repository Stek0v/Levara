package http

import (
	"errors"
	"strings"
	"testing"

	"github.com/stek0v/cognevra/internal/metrics"

	dto "github.com/prometheus/client_model/go"
)

// runWithPanicGuard: happy path returns nil, no counter bump.
func TestRunWithPanicGuard_NoError(t *testing.T) {
	before := counterValue(t, "extract")
	err := runWithPanicGuard("run-1", func() string { return "extract" }, func() error {
		return nil
	})
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if after := counterValue(t, "extract"); after != before {
		t.Fatalf("counter should not change on success: before=%f after=%f", before, after)
	}
}

// runWithPanicGuard: normal error passes through without counter bump.
func TestRunWithPanicGuard_NormalError(t *testing.T) {
	before := counterValue(t, "chunk")
	sentinel := errors.New("pipeline failure")
	err := runWithPanicGuard("run-2", func() string { return "chunk" }, func() error {
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got %v", err)
	}
	if after := counterValue(t, "chunk"); after != before {
		t.Fatalf("counter should not change on normal error: before=%f after=%f", before, after)
	}
}

// runWithPanicGuard: panic converted to error + counter bumped + stage captured.
func TestRunWithPanicGuard_Panic(t *testing.T) {
	before := counterValue(t, "embed")
	err := runWithPanicGuard("run-3", func() string { return "embed" }, func() error {
		panic("kaboom at embed")
	})
	if err == nil {
		t.Fatal("expected error from panic")
	}
	if !strings.Contains(err.Error(), "pipeline panic") || !strings.Contains(err.Error(), "kaboom") {
		t.Fatalf("error message should wrap panic value, got %q", err.Error())
	}
	after := counterValue(t, "embed")
	if after != before+1 {
		t.Fatalf("CognifyPanics{stage=embed} should increment by 1: before=%f after=%f", before, after)
	}
}

// runWithPanicGuard: counter uses the stage at recover-time, not at call-time.
// Pipelines advance stages as they run, so the panic should attribute to the
// stage the pipeline was actually in when it blew up.
func TestRunWithPanicGuard_StageResolvedLate(t *testing.T) {
	before := counterValue(t, "extract-late")
	stage := "start"
	_ = runWithPanicGuard("run-4", func() string { return stage }, func() error {
		stage = "extract-late"
		panic("late panic")
	})
	if after := counterValue(t, "extract-late"); after != before+1 {
		t.Fatalf("counter should attribute to stage at recover-time: before=%f after=%f", before, after)
	}
}

// counterValue reads the current float value of levara_cognify_panics_total{stage=<stage>}.
func counterValue(t *testing.T, stage string) float64 {
	t.Helper()
	c, err := metrics.CognifyPanics.GetMetricWithLabelValues(stage)
	if err != nil {
		t.Fatalf("GetMetricWithLabelValues: %v", err)
	}
	m := &dto.Metric{}
	if err := c.Write(m); err != nil {
		t.Fatalf("counter Write: %v", err)
	}
	return m.GetCounter().GetValue()
}
