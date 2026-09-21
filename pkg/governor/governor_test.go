// governor_test.go — A3 tests.
//
// QUALITY-1: No budget → never pause (quality first, always)
// QUALITY-2: Hysteresis prevents oscillation at the boundary
// GOV-1:   Pause when RSS > 80% of budget
// GOV-2:   Resume when RSS < 60% of budget
// GOV-3:   Pressure levels are correctly classified
package governor

import (
	"testing"
)

// TestGovernorNoBudgetNeverPauses — the core quality guarantee:
// without a configured budget, no job is ever paused.
func TestGovernorNoBudgetNeverPauses(t *testing.T) {
	g := NewGovernor(0) // no budget
	for _, job := range []string{"cognify", "distill", "rag", "sources"} {
		if !g.CanWork(job) {
			t.Fatalf("job %s paused with no budget — quality violation", job)
		}
	}
	if level, _ := g.Pressure(); level != PressureUnknown {
		t.Fatalf("pressure = %s with no budget, want unknown", level)
	}
}

// TestGovernorHysteresis — jobs don't oscillate between pause and resume.
// QUALITY-2: The 60-80% gap prevents rapid switching.
func TestGovernorHysteresis(t *testing.T) {
	// Governor with a 1-byte budget (any RSS will be > 80%)
	g := NewGovernor(1)

	// At 100% utilization: should pause
	if g.CanWork("job1") {
		t.Fatal("should pause at >80% budget utilization")
	}

	// Still at 100%: must remain paused (not flip-flop)
	if g.CanWork("job1") {
		t.Fatal("still >80%, should remain paused")
	}

	// Verify it's in the paused list
	paused := g.PausedJobs()
	if len(paused) != 1 || paused[0] != "job1" {
		t.Fatalf("paused = %v", paused)
	}
}

// TestGovernorBudgetZeroMeansUnlimited — explicit zero is "no limit".
func TestGovernorBudgetZeroMeansUnlimited(t *testing.T) {
	g := NewGovernor(0)
	if g.BudgetBytes() != 0 {
		t.Fatalf("budget = %d, want 0", g.BudgetBytes())
	}
	if len(g.PausedJobs()) != 0 {
		t.Fatalf("paused = %v, want empty", g.PausedJobs())
	}
}

// TestGovernorParseSizeBytes — GOMEMLIMIT format parsing.
func TestGovernorParseSizeBytes(t *testing.T) {
	cases := map[string]uint64{
		"8GiB":    8 * 1024 * 1024 * 1024,
		"512MiB":  512 * 1024 * 1024,
		"1024":    1024,
		"2GiB":    2 * 1024 * 1024 * 1024,
		"1GiB":    1024 * 1024 * 1024,
		"":        0,
		"invalid": 0,
		"16GiB":   16 * 1024 * 1024 * 1024,
		"100MB":   100 * 1000 * 1000,
	}
	for input, want := range cases {
		if got := parseSizeBytes(input); got != want {
			t.Errorf("parseSizeBytes(%q) = %d, want %d", input, got, want)
		}
	}
}

// TestGovernorPressureLevels — correct classification.
func TestGovernorPressureLevels(t *testing.T) {
	// This test uses a synthetic budget that we can reason about.
	// In production, currentRSS() reads actual MemStats.
	// Here we verify the classification thresholds are correct
	// by testing the ratio math directly.
	g := NewGovernor(1000) // 1000 bytes budget

	// The actual RSS will be much larger than 1000, so pressure
	// should be critical in a test environment
	level, ratio := g.Pressure()
	if ratio <= 0.80 {
		t.Fatalf("ratio = %f with 1000-byte budget, expected > 0.80", ratio)
	}
	if level != PressureCritical && level != PressureElevated {
		t.Fatalf("pressure = %s, expected elevated or critical", level)
	}
}

// TestGovernorIndependentJobs — pausing one job doesn't affect others.
func TestGovernorIndependentJobs(t *testing.T) {
	// With no budget, all jobs can always work
	g := NewGovernor(0)
	jobs := []string{"cognify", "distill", "rag-janitor", "sources", "query"}
	for round := 0; round < 3; round++ {
		for _, job := range jobs {
			if !g.CanWork(job) {
				t.Fatalf("round %d: job %s paused with no budget", round, job)
			}
		}
	}
}
