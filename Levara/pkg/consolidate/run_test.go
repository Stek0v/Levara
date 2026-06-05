package consolidate

import (
	"context"
	"strings"
	"testing"
	"time"
)

// spySummarizer records whether Summarize was invoked.
type spySummarizer struct {
	calls int
	out   string
	err   error
}

func (s *spySummarizer) Summarize(_ context.Context, _ []string) (string, error) {
	s.calls++
	return s.out, s.err
}

type fakeStore struct {
	recs    []MemoryRecord
	applied []Action
	runID   string
}

func (f *fakeStore) Candidates(_ context.Context, _, _, _ string) ([]MemoryRecord, error) {
	return f.recs, nil
}
func (f *fakeStore) Apply(_ context.Context, runID string, actions []Action) error {
	f.runID = runID
	f.applied = append(f.applied, actions...)
	return nil
}

type fakeNeighbors struct{ edges []SimEdge }

func (f fakeNeighbors) Edges(_ context.Context, _ []MemoryRecord, _ Config) ([]SimEdge, error) {
	return f.edges, nil
}

func TestRun_DryRunDoesNotApply(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store := &fakeStore{recs: []MemoryRecord{
		{ID: "a", Value: "x", CreatedAt: t0},
		{ID: "b", Value: "x", CreatedAt: t0.Add(time.Hour)},
	}}
	neigh := fakeNeighbors{edges: []SimEdge{{A: "a", B: "b", Score: 0.99}}}

	res, err := Run(context.Background(), Params{
		Store: store, Neighbors: neigh, Summarizer: fakeSummarizer{out: "x"},
		Cfg: DefaultConfig(), RunID: "run1", DryRun: true,
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(res.Actions) != 1 || res.Actions[0].Kind != ActionMerge {
		t.Fatalf("planned actions = %+v, want one merge", res.Actions)
	}
	if len(store.applied) != 0 {
		t.Fatalf("dry run applied %d actions, want 0", len(store.applied))
	}
}

func TestRun_AppliesWhenNotDryRun(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store := &fakeStore{recs: []MemoryRecord{
		{ID: "a", Value: "x", CreatedAt: t0},
		{ID: "b", Value: "x", CreatedAt: t0.Add(time.Hour)},
	}}
	neigh := fakeNeighbors{edges: []SimEdge{{A: "a", B: "b", Score: 0.99}}}

	_, err := Run(context.Background(), Params{
		Store: store, Neighbors: neigh, Summarizer: fakeSummarizer{out: "x"},
		Cfg: DefaultConfig(), RunID: "run2", DryRun: false,
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if store.runID != "run2" || len(store.applied) != 1 {
		t.Fatalf("applied=%d runID=%q, want 1 action under run2", len(store.applied), store.runID)
	}
}

// An abstract cluster larger than Cfg.MaxAbstractSize is skipped BEFORE the LLM
// is called (oversized clusters always overflow the token budget and degrade
// into truncation-induced guard rejections — see findings P2.5).
func TestRun_SkipsOversizedAbstractCluster(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ids := []string{"a", "b", "c", "d", "e", "f", "g"} // 7 > MaxAbstractSize 6
	var recs []MemoryRecord
	for i, id := range ids {
		recs = append(recs, MemoryRecord{ID: id, Value: "note " + id, CreatedAt: t0.Add(time.Duration(i) * time.Hour)})
	}
	// All-pairs edges at 0.92: one connected component, between TauLow (0.90)
	// and TauHigh (0.97) → classified abstract.
	var edges []SimEdge
	for i := 0; i < len(ids); i++ {
		for j := i + 1; j < len(ids); j++ {
			edges = append(edges, SimEdge{A: ids[i], B: ids[j], Score: 0.92})
		}
	}
	spy := &spySummarizer{out: "x"}
	res, err := Run(context.Background(), Params{
		Store: &fakeStore{recs: recs}, Neighbors: fakeNeighbors{edges: edges}, Summarizer: spy,
		Cfg: DefaultConfig(), RunID: "run", DryRun: true,
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if spy.calls != 0 {
		t.Errorf("summarizer called %d times, want 0 (skip must precede the LLM)", spy.calls)
	}
	if res.Skipped != 1 || len(res.Skips) != 1 {
		t.Fatalf("Skipped=%d Skips=%+v, want exactly 1", res.Skipped, res.Skips)
	}
	if !strings.Contains(res.Skips[0].Reason, "too large") {
		t.Errorf("reason = %q, want it to mention 'too large'", res.Skips[0].Reason)
	}
	if len(res.Actions) != 0 {
		t.Errorf("actions = %d, want 0", len(res.Actions))
	}
}

// A per-run LLM-call budget caps how many abstract clusters reach the
// Summarizer (DeepSeek) in one sweep. Once the budget is spent, remaining
// abstract clusters are skipped with an explicit reason instead of running up
// unbounded LLM cost on a large collection (findings P3.3).
func TestRun_LLMCallBudgetCapsAbstractions(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// Three independent 2-record clusters, all classified abstract (edges at
	// 0.92, between TauLow and TauHigh). Lowercase, number-free values so the
	// coverage guard never rejects — this isolates the budget logic.
	recs := []MemoryRecord{
		{ID: "a", Value: "alpha apple", CreatedAt: t0},
		{ID: "b", Value: "alpha apricot", CreatedAt: t0.Add(time.Hour)},
		{ID: "c", Value: "beta banana", CreatedAt: t0.Add(2 * time.Hour)},
		{ID: "d", Value: "beta berry", CreatedAt: t0.Add(3 * time.Hour)},
		{ID: "e", Value: "gamma grape", CreatedAt: t0.Add(4 * time.Hour)},
		{ID: "f", Value: "gamma guava", CreatedAt: t0.Add(5 * time.Hour)},
	}
	edges := []SimEdge{
		{A: "a", B: "b", Score: 0.92},
		{A: "c", B: "d", Score: 0.92},
		{A: "e", B: "f", Score: 0.92},
	}
	cfg := DefaultConfig()
	cfg.MaxLLMCalls = 2
	spy := &spySummarizer{out: "consolidated"}
	res, err := Run(context.Background(), Params{
		Store: &fakeStore{recs: recs}, Neighbors: fakeNeighbors{edges: edges}, Summarizer: spy,
		Cfg: cfg, RunID: "run", DryRun: true,
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if spy.calls != 2 {
		t.Errorf("summarizer called %d times, want 2 (budget cap)", spy.calls)
	}
	if len(res.Actions) != 2 {
		t.Errorf("actions = %d, want 2 (budget cap)", len(res.Actions))
	}
	if res.Skipped != 1 || len(res.Skips) != 1 {
		t.Fatalf("Skipped=%d Skips=%+v, want exactly 1", res.Skipped, res.Skips)
	}
	if !strings.Contains(res.Skips[0].Reason, "budget") {
		t.Errorf("reason = %q, want it to mention 'budget'", res.Skips[0].Reason)
	}
}

// MaxLLMCalls == 0 means unbounded: every abstract cluster reaches the
// Summarizer (back-compat for callers that build Config by hand).
func TestRun_LLMBudgetZeroIsUnbounded(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	recs := []MemoryRecord{
		{ID: "a", Value: "alpha apple", CreatedAt: t0},
		{ID: "b", Value: "alpha apricot", CreatedAt: t0.Add(time.Hour)},
		{ID: "c", Value: "beta banana", CreatedAt: t0.Add(2 * time.Hour)},
		{ID: "d", Value: "beta berry", CreatedAt: t0.Add(3 * time.Hour)},
		{ID: "e", Value: "gamma grape", CreatedAt: t0.Add(4 * time.Hour)},
		{ID: "f", Value: "gamma guava", CreatedAt: t0.Add(5 * time.Hour)},
	}
	edges := []SimEdge{{A: "a", B: "b", Score: 0.92}, {A: "c", B: "d", Score: 0.92}, {A: "e", B: "f", Score: 0.92}}
	cfg := DefaultConfig()
	cfg.MaxLLMCalls = 0 // unbounded
	spy := &spySummarizer{out: "consolidated"}
	res, err := Run(context.Background(), Params{
		Store: &fakeStore{recs: recs}, Neighbors: fakeNeighbors{edges: edges}, Summarizer: spy,
		Cfg: cfg, RunID: "run", DryRun: true,
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if spy.calls != 3 {
		t.Errorf("summarizer called %d times, want 3 (0 = unbounded)", spy.calls)
	}
	if res.Skipped != 0 {
		t.Errorf("Skipped = %d, want 0 (no budget cap)", res.Skipped)
	}
}

// Merge clusters never touch the Summarizer, so they must not consume the LLM
// budget: with a mix of merges and abstractions, the cap applies only to the
// abstractions.
func TestRun_LLMBudgetIgnoresMerges(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	recs := []MemoryRecord{
		{ID: "m1", Value: "identical", CreatedAt: t0}, // tight pair → merge
		{ID: "m2", Value: "identical", CreatedAt: t0.Add(time.Hour)},
		{ID: "a", Value: "alpha apple", CreatedAt: t0.Add(2 * time.Hour)}, // abstract
		{ID: "b", Value: "alpha apricot", CreatedAt: t0.Add(3 * time.Hour)},
		{ID: "c", Value: "beta banana", CreatedAt: t0.Add(4 * time.Hour)}, // abstract
		{ID: "d", Value: "beta berry", CreatedAt: t0.Add(5 * time.Hour)},
	}
	edges := []SimEdge{
		{A: "m1", B: "m2", Score: 0.99}, // ≥ TauHigh → merge
		{A: "a", B: "b", Score: 0.92},
		{A: "c", B: "d", Score: 0.92},
	}
	cfg := DefaultConfig()
	cfg.MaxLLMCalls = 2 // enough for both abstractions; the merge must not eat into it
	spy := &spySummarizer{out: "consolidated"}
	res, err := Run(context.Background(), Params{
		Store: &fakeStore{recs: recs}, Neighbors: fakeNeighbors{edges: edges}, Summarizer: spy,
		Cfg: cfg, RunID: "run", DryRun: true,
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if spy.calls != 2 {
		t.Errorf("summarizer called %d times, want 2 (both abstractions; merge is free)", spy.calls)
	}
	if res.Skipped != 0 {
		t.Errorf("Skipped = %d, want 0 (budget covers both abstractions)", res.Skipped)
	}
	// 1 merge + 2 abstract = 3 actions, none skipped for budget.
	if len(res.Actions) != 3 {
		t.Errorf("actions = %d, want 3", len(res.Actions))
	}
}

// The budget counts LLM attempts, not successes: an abstraction the coverage
// guard rejects still made the DeepSeek call, so it must consume budget.
func TestRun_LLMBudgetCountsRejectedAttempts(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	recs := []MemoryRecord{
		{ID: "a", Value: "value 111", CreatedAt: t0}, // numbers → guard rejects a dropping summary
		{ID: "b", Value: "value 222", CreatedAt: t0.Add(time.Hour)},
		{ID: "c", Value: "value 333", CreatedAt: t0.Add(2 * time.Hour)},
		{ID: "d", Value: "value 444", CreatedAt: t0.Add(3 * time.Hour)},
	}
	edges := []SimEdge{{A: "a", B: "b", Score: 0.92}, {A: "c", B: "d", Score: 0.92}}
	cfg := DefaultConfig()
	cfg.MaxLLMCalls = 1
	spy := &spySummarizer{out: "lossy"} // drops every source number → guard rejects
	res, err := Run(context.Background(), Params{
		Store: &fakeStore{recs: recs}, Neighbors: fakeNeighbors{edges: edges}, Summarizer: spy,
		Cfg: cfg, RunID: "run", DryRun: true,
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if spy.calls != 1 {
		t.Errorf("summarizer called %d times, want 1 (budget caps attempts even when rejected)", spy.calls)
	}
	// One cluster: the rejected attempt (coverage_guard). Other: never attempted (budget).
	if res.Skipped != 2 || len(res.Skips) != 2 {
		t.Fatalf("Skipped=%d Skips=%+v, want 2", res.Skipped, res.Skips)
	}
	var guard, budget int
	for _, sk := range res.Skips {
		if strings.Contains(sk.Reason, "budget") {
			budget++
		} else {
			guard++
		}
	}
	if guard != 1 || budget != 1 {
		t.Errorf("skip reasons: guard=%d budget=%d, want 1 each", guard, budget)
	}
}

// actionCharDensity reports survivor chars / total source chars, the
// compression ratio used to flag over-aggressive consolidation (findings P3.3).
func TestActionCharDensity(t *testing.T) {
	byID := map[string]MemoryRecord{
		"a": {ID: "a", Value: "1234567890"}, // 10 chars
		"b": {ID: "b", Value: "abcdefghij"}, // 10 chars
		"c": {ID: "c", Value: "klmnopqrst"}, // 10 chars
	}
	// Merge: survivor "a" (10) kept, total = a+b = 20 → 0.5.
	merge := Action{Kind: ActionMerge, SurvivorID: "a", SourceIDs: []string{"b"}}
	if got := actionCharDensity(merge, byID); got != 0.5 {
		t.Errorf("merge density = %v, want 0.5", got)
	}
	// Abstract: NewValue 6 chars, sources b+c = 20 → 0.3.
	abs := Action{Kind: ActionAbstract, NewValue: "synced", SourceIDs: []string{"b", "c"}}
	if got := actionCharDensity(abs, byID); got != 0.3 {
		t.Errorf("abstract density = %v, want 0.3", got)
	}
	// Degenerate: no source chars → 0, no divide-by-zero.
	empty := Action{Kind: ActionAbstract, NewValue: "x", SourceIDs: []string{"missing"}}
	if got := actionCharDensity(empty, byID); got != 0 {
		t.Errorf("empty-source density = %v, want 0", got)
	}
}

// Run reports a per-action char-density aligned with Actions so the handler can
// observe the compression ratio without re-loading source values (findings P3.3).
func TestRun_ReportsCharDensities(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store := &fakeStore{recs: []MemoryRecord{
		{ID: "a", Value: "1234567890", CreatedAt: t0},
		{ID: "b", Value: "1234567890", CreatedAt: t0.Add(time.Hour)},
	}}
	neigh := fakeNeighbors{edges: []SimEdge{{A: "a", B: "b", Score: 0.99}}} // tight → merge
	res, err := Run(context.Background(), Params{
		Store: store, Neighbors: neigh, Summarizer: fakeSummarizer{out: "x"},
		Cfg: DefaultConfig(), RunID: "run", DryRun: true,
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(res.Densities) != len(res.Actions) {
		t.Fatalf("Densities=%d Actions=%d, want aligned", len(res.Densities), len(res.Actions))
	}
	// Identical 10-char records, newest survives: 10 / 20 = 0.5.
	if len(res.Densities) != 1 || res.Densities[0] != 0.5 {
		t.Errorf("Densities = %v, want [0.5]", res.Densities)
	}
}

// When the coverage guard rejects an abstraction, the run records the concrete
// reason instead of silently bumping a counter (findings P2.5).
func TestRun_RecordsGuardSkipReason(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store := &fakeStore{recs: []MemoryRecord{
		{ID: "a", Value: "potion is 256 dim", CreatedAt: t0},
		{ID: "b", Value: "potion runs on 9101", CreatedAt: t0.Add(time.Hour)},
	}}
	neigh := fakeNeighbors{edges: []SimEdge{{A: "a", B: "b", Score: 0.92}}}
	res, err := Run(context.Background(), Params{
		Store: store, Neighbors: neigh, Summarizer: fakeSummarizer{out: "potion thing"}, // drops 256 and 9101
		Cfg: DefaultConfig(), RunID: "run", DryRun: true,
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if res.Skipped != 1 || len(res.Skips) != 1 {
		t.Fatalf("Skipped=%d, want 1", res.Skipped)
	}
	if res.Skips[0].Reason == "" {
		t.Error("skip reason empty, want the guard detail (dropped number)")
	}
	if len(res.Actions) != 0 {
		t.Errorf("actions = %d, want 0", len(res.Actions))
	}
}
