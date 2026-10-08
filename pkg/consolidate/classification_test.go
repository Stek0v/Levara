package consolidate

import (
	"context"
	"strings"
	"testing"
)

func TestConsolidationClassificationEdges(t *testing.T) {
	for _, axis := range []string{"owner", "shared-owner", "collection", "type", "room", "hall", "unknown-a", "unknown-b"} {
		t.Run(axis, func(t *testing.T) {
			a := MemoryRecord{ID: "a", Value: "same", OwnerID: "owner-a", Collection: "levara", Type: "preference", Room: "memory", Hall: "fact"}
			b := a
			b.ID = "b"
			edge := SimEdge{A: "a", B: "b", Score: 0.92}
			switch axis {
			case "owner":
				b.OwnerID = "owner-b"
			case "shared-owner":
				b.OwnerID = ""
			case "collection":
				b.Collection = "other"
			case "type":
				b.Type = "user"
			case "room":
				b.Room = "deploy"
			case "hall":
				b.Hall = "decision"
			case "unknown-a":
				edge.A = "missing"
			case "unknown-b":
				edge.B = "missing"
			}
			store := &fakeStore{recs: []MemoryRecord{a, b}}
			spy := &spySummarizer{out: "same"}
			res, err := Run(context.Background(), Params{Store: store, Neighbors: fakeNeighbors{edges: []SimEdge{edge}}, Summarizer: spy, Cfg: DefaultConfig()})
			if err != nil {
				t.Fatal(err)
			}
			if res.Clusters != 0 || len(res.Actions) != 0 || len(store.applied) != 0 || spy.calls != 0 || res.LLMCalls != 0 {
				t.Fatalf("unsafe edge reached clustering/provider/apply: result=%+v applied=%+v calls=%d", res, store.applied, spy.calls)
			}
			if actions := Plan(map[string]MemoryRecord{"a": a, "b": b}, []Cluster{{IDs: []string{edge.A, edge.B}, Edges: []SimEdge{edge}}}, DefaultConfig()); len(actions) != 0 {
				t.Fatalf("direct planner accepted unsafe cluster: %+v", actions)
			}
		})
	}
}

func TestConsolidationClassificationPreserved(t *testing.T) {
	for _, hall := range []string{"fact", "event", "decision", "preference", "advice", "discovery"} {
		t.Run(hall, func(t *testing.T) {
			a := MemoryRecord{ID: "a", Value: "same", OwnerID: "owner-a", Collection: "levara", Type: "preference", Room: "memory", Hall: hall}
			b := a
			b.ID = "b"
			for _, score := range []float64{0.92, 0.99} {
				store := &fakeStore{recs: []MemoryRecord{a, b}}
				spy := &spySummarizer{out: "same"}
				res, err := Run(context.Background(), Params{Store: store, Neighbors: fakeNeighbors{edges: []SimEdge{{A: "a", B: "b", Score: score}}}, Summarizer: spy, Cfg: DefaultConfig()})
				if err != nil {
					t.Fatal(err)
				}
				wantCalls, wantKind := 1, ActionAbstract
				if score == 0.99 {
					wantCalls, wantKind = 0, ActionMerge
				}
				if len(res.Actions) != 1 || len(store.applied) != 1 || spy.calls != wantCalls || res.Actions[0].Kind != wantKind || res.Actions[0].OwnerID != a.OwnerID || res.Actions[0].Collection != a.Collection || res.Actions[0].Type != a.Type || res.Actions[0].Room != a.Room || res.Actions[0].Hall != a.Hall {
					t.Fatalf("classification lost: result=%+v applied=%+v calls=%d", res, store.applied, spy.calls)
				}
			}
		})
	}
}

func TestConsolidationClassificationPartitions(t *testing.T) {
	recs := []MemoryRecord{
		{ID: "a", Value: "same", OwnerID: "owner-a", Collection: "levara", Type: "project", Room: "memory", Hall: "fact"},
		{ID: "b", Value: "same", OwnerID: "owner-a", Collection: "levara", Type: "project", Room: "memory", Hall: "fact"},
		{ID: "c", Value: "same", OwnerID: "owner-b", Collection: "levara", Type: "project", Room: "memory", Hall: "fact"},
		{ID: "d", Value: "same", OwnerID: "owner-b", Collection: "levara", Type: "project", Room: "memory", Hall: "fact"},
	}
	edges := []SimEdge{
		{A: "a", B: "b", Score: 0.92},
		{A: "c", B: "d", Score: 0.92},
		{A: "b", B: "c", Score: 0.99},
		{A: "a", B: "missing", Score: 0.99},
	}
	store := &fakeStore{recs: recs}
	spy := &spySummarizer{out: "same"}
	res, err := Run(context.Background(), Params{Store: store, Neighbors: fakeNeighbors{edges: edges}, Summarizer: spy, Cfg: DefaultConfig()})
	if err != nil {
		t.Fatal(err)
	}
	if res.Clusters != 2 || len(res.Actions) != 2 || len(store.applied) != 2 || spy.calls != 2 || res.LLMCalls != 2 {
		t.Fatalf("safe partitions lost or joined by unsafe bridge: result=%+v calls=%d", res, spy.calls)
	}
	byID := map[string]MemoryRecord{}
	for _, r := range recs {
		byID[r.ID] = r
	}
	for _, a := range res.Actions {
		if len(a.SourceIDs) != 2 {
			t.Fatalf("partition sources = %v, want 2", a.SourceIDs)
		}
		for _, id := range a.SourceIDs {
			if byID[id].OwnerID != a.OwnerID {
				t.Fatalf("mixed-owner action: %+v", a)
			}
		}
	}
}

func TestConsolidationLegacyHallSkipsAbstractPreservesMerge(t *testing.T) {
	for _, hall := range []string{"", "unknown", "semantic"} {
		t.Run(hall, func(t *testing.T) {
			a := MemoryRecord{ID: "a", Value: "same", OwnerID: "owner-a", Collection: "levara", Type: "preference", Room: "memory", Hall: hall}
			b := a
			b.ID = "b"
			for _, score := range []float64{0.92, 0.99} {
				store := &fakeStore{recs: []MemoryRecord{a, b}}
				spy := &spySummarizer{out: "same"}
				res, err := Run(context.Background(), Params{Store: store, Neighbors: fakeNeighbors{edges: []SimEdge{{A: "a", B: "b", Score: score}}}, Summarizer: spy, Cfg: DefaultConfig()})
				if err != nil {
					t.Fatal(err)
				}
				if spy.calls != 0 || res.LLMCalls != 0 {
					t.Fatalf("legacy hall reached provider: calls=%d result=%+v", spy.calls, res)
				}
				if score == 0.92 {
					if len(store.applied) != 0 || len(res.Actions) != 0 || len(res.Skips) != 1 || res.Skipped != 1 || !strings.Contains(res.Skips[0].Reason, "invalid hall") {
						t.Fatalf("legacy abstract missing explicit no-effect skip: %+v", res)
					}
				} else if len(res.Actions) != 1 || len(store.applied) != 1 || res.Actions[0].Kind != ActionMerge || res.Actions[0].OwnerID != a.OwnerID || res.Actions[0].Collection != a.Collection || res.Actions[0].Type != a.Type || res.Actions[0].Room != a.Room || res.Actions[0].Hall != hall {
					t.Fatalf("legacy mechanical merge lost classification: %+v", res)
				}
			}
		})
	}
}
