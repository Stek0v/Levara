package consolidate

import (
	"context"
	"strings"
	"testing"
	"time"
)

type gateStore struct {
	recs   []MemoryRecord
	sort   bool
	lastID string
}

func (f *gateStore) Candidates(ctx context.Context, collection, room, hall string) ([]MemoryRecord, error) {
	return f.recs, nil
}

func (f *gateStore) Apply(ctx context.Context, runID string, actions []Action) error {
	f.lastID = runID
	return nil
}

type gateNeighbors struct{ edges []SimEdge }

func (f *gateNeighbors) Edges(ctx context.Context, recs []MemoryRecord, cfg Config) ([]SimEdge, error) {
	return f.edges, nil
}

type gateSummarizer struct{ calls int }

func (f *gateSummarizer) Summarize(ctx context.Context, sources []string) (string, error) {
	f.calls++
	if len(sources) == 0 {
		return "", nil
	}
	return sources[0], nil // identity synthesis: coverage guard keeps it
}

// gateFunc adapts a stub scoring function to FactGate.
type gateFunc func(oldRec, newRec MemoryRecord) (float64, error)

func (f gateFunc) Supersedes(ctx context.Context, oldRec, newRec MemoryRecord) (float64, error) {
	return f(oldRec, newRec)
}

func mergeClusterRecs() (map[string]MemoryRecord, []MemoryRecord, []SimEdge) {
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	recs := map[string]MemoryRecord{
		"a": {ID: "a", Value: "Gemma migration completed 2026-09-24, 320 collections", CreatedAt: base},
		"b": {ID: "b", Value: "Gemma migration completed 2026-09-24, 320 collections, WAL replay clean", CreatedAt: base.Add(time.Hour)},
	}
	return recs, []MemoryRecord{recs["a"], recs["b"]}, []SimEdge{{A: "a", B: "b", Score: 0.98}} // >= TauHigh → merge path
}

func abstractClusterRecs() (map[string]MemoryRecord, []MemoryRecord, []SimEdge) {
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	recs := map[string]MemoryRecord{
		// identical token sets so the identity synthesis passes the
		// coverage guard and the test isolates the fact-gate behaviour
		"a": {ID: "a", Value: "Deploy decision: use Nginx blue green rollout", CreatedAt: base},
		"b": {ID: "b", Value: "Use Nginx blue green rollout deploy decision", CreatedAt: base},
	}
	return recs, []MemoryRecord{recs["a"], recs["b"]}, []SimEdge{{A: "a", B: "b", Score: 0.91}} // < TauHigh → abstract path
}

func TestRunWithoutGateUnchanged(t *testing.T) {
	_, recList, edges := mergeClusterRecs()
	res, err := Run(context.Background(), Params{
		Store:      &gateStore{recs: recList},
		Neighbors:  &gateNeighbors{edges: edges},
		Summarizer: &gateSummarizer{},
		Cfg:        DefaultConfig(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Actions) != 1 || res.Actions[0].Kind != ActionMerge {
		t.Fatalf("expected 1 merge action, got %+v", res.Actions)
	}
	if res.GateChecked != 0 || res.GateRejected != 0 {
		t.Fatalf("nil gate must not touch gate counters: %+v", res)
	}
}

func TestRunGateAcceptsMergeAboveThreshold(t *testing.T) {
	_, recList, edges := mergeClusterRecs()
	res, err := Run(context.Background(), Params{
		Store:         &gateStore{recs: recList},
		Neighbors:     &gateNeighbors{edges: edges},
		Summarizer:    &gateSummarizer{},
		Cfg:           DefaultConfig(),
		Gate:          gateFunc(func(oldRec, newRec MemoryRecord) (float64, error) { return 0.44, nil }),
		GateThreshold: 0.05,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Actions) != 1 || res.GateChecked != 1 || res.GateRejected != 0 {
		t.Fatalf("gate should accept p=0.44 >= 0.05: actions=%d checked=%d rejected=%d",
			len(res.Actions), res.GateChecked, res.GateRejected)
	}
}

func TestRunGateRejectsMergeBelowThreshold(t *testing.T) {
	_, recList, edges := mergeClusterRecs()
	res, err := Run(context.Background(), Params{
		Store:         &gateStore{recs: recList},
		Neighbors:     &gateNeighbors{edges: edges},
		Summarizer:    &gateSummarizer{},
		Cfg:           DefaultConfig(),
		Gate:          gateFunc(func(oldRec, newRec MemoryRecord) (float64, error) { return 0.01, nil }),
		GateThreshold: 0.05,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Actions) != 0 || res.GateRejected != 1 || len(res.Skips) != 1 {
		t.Fatalf("gate should reject the cluster: actions=%d rejected=%d skips=%+v",
			len(res.Actions), res.GateRejected, res.Skips)
	}
	if !strings.Contains(res.Skips[0].Reason, "decision gate") ||
		!strings.Contains(res.Skips[0].Reason, "p=0.01") {
		t.Fatalf("unexpected skip reason: %q", res.Skips[0].Reason)
	}
}

func TestRunGateChecksSynthesisAgainstSources(t *testing.T) {
	recs, recList, edges := abstractClusterRecs()
	sum := &gateSummarizer{}
	var seenNew string
	res, err := Run(context.Background(), Params{
		Store:      &gateStore{recs: recList},
		Neighbors:  &gateNeighbors{edges: edges},
		Summarizer: sum,
		Cfg:        DefaultConfig(),
		Gate: gateFunc(func(oldRec, newRec MemoryRecord) (float64, error) {
			seenNew = newRec.Value
			return 0.9, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Actions) != 1 || res.Actions[0].Kind != ActionAbstract {
		t.Fatalf("expected abstraction to pass the gate, got actions=%+v skips=%+v checked=%d rejected=%d", res.Actions, res.Skips, res.GateChecked, res.GateRejected)
	}
	if seenNew != recs["a"].Value {
		t.Fatalf("gate must score the synthesized value, got %q", seenNew)
	}
	if sum.calls != 1 || res.LLMCalls != 1 {
		t.Fatalf("LLM call accounting broken: calls=%d res.LLMCalls=%d", sum.calls, res.LLMCalls)
	}

	// Rejected synthesis: sources stay, the spent LLM call is still counted.
	sum2 := &gateSummarizer{}
	res2, err := Run(context.Background(), Params{
		Store:      &gateStore{recs: recList},
		Neighbors:  &gateNeighbors{edges: edges},
		Summarizer: sum2,
		Cfg:        DefaultConfig(),
		Gate:       gateFunc(func(oldRec, newRec MemoryRecord) (float64, error) { return 0.01, nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res2.Actions) != 0 || res2.GateRejected != 1 || res2.LLMCalls != 1 || sum2.calls != 1 {
		t.Fatalf("rejected abstraction must keep sources and count the LLM call: %+v", res2)
	}
	if !strings.Contains(res2.Skips[0].Reason, "synthesis") {
		t.Fatalf("abstract rejection should name the synthesis: %q", res2.Skips[0].Reason)
	}
}

func TestRunGateErrorFailsOpen(t *testing.T) {
	_, recList, edges := mergeClusterRecs()
	res, err := Run(context.Background(), Params{
		Store:      &gateStore{recs: recList},
		Neighbors:  &gateNeighbors{edges: edges},
		Summarizer: &gateSummarizer{},
		Cfg:        DefaultConfig(),
		Gate: gateFunc(func(oldRec, newRec MemoryRecord) (float64, error) {
			return 0, context.DeadlineExceeded
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Actions) != 1 || res.GateErrors != 1 || res.GateRejected != 0 {
		t.Fatalf("gate error must fail open: actions=%d errors=%d rejected=%d",
			len(res.Actions), res.GateErrors, res.GateRejected)
	}
}
