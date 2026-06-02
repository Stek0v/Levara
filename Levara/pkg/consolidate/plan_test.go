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
