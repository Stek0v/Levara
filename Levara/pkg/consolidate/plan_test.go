package consolidate

import (
	"testing"
	"time"
)

func recsByID() map[string]MemoryRecord {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return map[string]MemoryRecord{
		"a": {ID: "a", Value: "Pi runs potion sidecar on 9101", Room: "infra", Hall: "fact", CreatedAt: t0},
		"b": {ID: "b", Value: "Pi runs potion sidecar on 9101", Room: "infra", Hall: "fact", CreatedAt: t0.Add(time.Hour)},
		"c": {ID: "c", Value: "potion model is 256-dim", Room: "infra", Hall: "fact", CreatedAt: t0.Add(2 * time.Hour)},
	}
}

func TestPlan_TightClusterMerges_NewestSurvives(t *testing.T) {
	recs := recsByID()
	clusters := []Cluster{{
		IDs:   []string{"a", "b"},
		Edges: []SimEdge{{A: "a", B: "b", Score: 0.985}}, // >= TauHigh
	}}
	actions := Plan(recs, clusters, DefaultConfig())

	if len(actions) != 1 {
		t.Fatalf("got %d actions, want 1", len(actions))
	}
	act := actions[0]
	if act.Kind != ActionMerge {
		t.Fatalf("kind = %s, want merge", act.Kind)
	}
	if act.SurvivorID != "b" {
		t.Fatalf("survivor = %s, want b (newest)", act.SurvivorID)
	}
	if len(act.SourceIDs) != 1 || act.SourceIDs[0] != "a" {
		t.Fatalf("sources = %v, want [a]", act.SourceIDs)
	}
}

// P2.3: potion envelope-collapse makes records that share a long common header
// score >= TauHigh despite carrying distinct bodies. A mechanical merge keeps
// only the survivor and supersedes the rest, silently dropping those distinct
// bodies. Such a cluster must be downgraded to an abstraction (which preserves
// every source via the coverage guard), not merged.
func TestPlan_EnvelopeCollapseDowngradesToAbstract(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	recs := map[string]MemoryRecord{
		"a": {ID: "a", Value: "common envelope preamble. fact one is about nginx port 8080 deploy",
			Room: "infra", Hall: "fact", CreatedAt: t0},
		"b": {ID: "b", Value: "common envelope preamble. fact two is about postgres backup at 0300",
			Room: "infra", Hall: "fact", CreatedAt: t0.Add(time.Hour)},
	}
	clusters := []Cluster{{
		IDs:   []string{"a", "b"},
		Edges: []SimEdge{{A: "a", B: "b", Score: 0.9999}}, // envelope-collapse: tight cosine
	}}
	actions := Plan(recs, clusters, DefaultConfig())

	if len(actions) != 1 {
		t.Fatalf("got %d actions, want 1", len(actions))
	}
	if actions[0].Kind != ActionAbstract {
		t.Fatalf("kind = %s, want abstract (distinct bodies must not be mechanically merged)", actions[0].Kind)
	}
	if len(actions[0].SourceIDs) != 2 {
		t.Fatalf("abstract sources = %v, want both a,b preserved", actions[0].SourceIDs)
	}
}

// A tight cluster with only trivial wording differences (one added stopword)
// is a genuine near-duplicate and must still merge mechanically.
func TestPlan_NearDuplicateStillMerges(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	recs := map[string]MemoryRecord{
		"a": {ID: "a", Value: "Pi runs potion sidecar on 9101", CreatedAt: t0},
		"b": {ID: "b", Value: "Pi runs the potion sidecar on 9101", CreatedAt: t0.Add(time.Hour)},
	}
	clusters := []Cluster{{
		IDs:   []string{"a", "b"},
		Edges: []SimEdge{{A: "a", B: "b", Score: 0.985}},
	}}
	actions := Plan(recs, clusters, DefaultConfig())
	if len(actions) != 1 || actions[0].Kind != ActionMerge {
		t.Fatalf("actions = %+v, want one merge (near-duplicate)", actions)
	}
}

func TestPlan_LooseClusterAbstracts(t *testing.T) {
	recs := recsByID()
	clusters := []Cluster{{
		IDs:   []string{"a", "c"},
		Edges: []SimEdge{{A: "a", B: "c", Score: 0.88}}, // between TauLow and TauHigh
	}}
	actions := Plan(recs, clusters, DefaultConfig())

	if len(actions) != 1 || actions[0].Kind != ActionAbstract {
		t.Fatalf("actions = %+v, want one abstract action", actions)
	}
	if len(actions[0].SourceIDs) != 2 {
		t.Fatalf("abstract sources = %v, want both a,c", actions[0].SourceIDs)
	}
	if actions[0].NewValue != "" {
		t.Fatalf("NewValue should be empty until LLM fills it, got %q", actions[0].NewValue)
	}
}
