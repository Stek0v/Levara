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
