package workspace

import (
	"errors"
	"reflect"
	"testing"

	"github.com/stek0v/levara/pkg/vectorstore"
)

func TestGCGenerationsDropsExclusiveCollections(t *testing.T) {
	m := NewManifest("payments", "main")
	if err := m.UpsertChunk(ChunkRecord{Generation: "gen-1", Path: "docs/a.md", ChunkID: "a1", VectorID: "vec-a1", Collection: "kb_gen1"}); err != nil {
		t.Fatal(err)
	}
	if err := m.UpsertChunk(ChunkRecord{Generation: "gen-2", Path: "docs/a.md", ChunkID: "a1", VectorID: "vec-a1-g2", Collection: "kb_gen2"}); err != nil {
		t.Fatal(err)
	}
	if err := m.ActivateGeneration("gen-1"); err != nil {
		t.Fatal(err)
	}
	if err := m.ActivateGeneration("gen-2"); err != nil {
		t.Fatal(err)
	}

	store := newFakeVectorStore()
	if err := store.Create("kb_gen1"); err != nil {
		t.Fatal(err)
	}
	if err := store.Create("kb_gen2"); err != nil {
		t.Fatal(err)
	}
	result, err := GCGenerations(m, store)
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(result.Generations, []string{"gen-1"}) {
		t.Fatalf("generations=%v, want [gen-1]", result.Generations)
	}
	if !reflect.DeepEqual(result.DroppedCollections, []string{"kb_gen1"}) {
		t.Fatalf("dropped=%v, want [kb_gen1]", result.DroppedCollections)
	}
	if store.Has("kb_gen1") {
		t.Fatal("kb_gen1 should be dropped")
	}
	if !store.Has("kb_gen2") {
		t.Fatal("kb_gen2 should survive")
	}
	if _, ok := m.Generations["gen-1"]; ok {
		t.Fatal("gen-1 should be removed from manifest")
	}
	if ids := m.VectorIDs(ChunkFilter{Generation: "gen-1"}); len(ids) != 0 {
		t.Fatalf("gen-1 chunks still present: %v", ids)
	}
}

func TestGCGenerationsDeletesIDsForSharedCollection(t *testing.T) {
	m := NewManifest("payments", "main")
	if err := m.UpsertChunk(ChunkRecord{Generation: "gen-1", Path: "docs/a.md", ChunkID: "a1", VectorID: "vec-old", Collection: "kb_shared"}); err != nil {
		t.Fatal(err)
	}
	if err := m.UpsertChunk(ChunkRecord{Generation: "gen-2", Path: "docs/a.md", ChunkID: "a1", VectorID: "vec-new", Collection: "kb_shared"}); err != nil {
		t.Fatal(err)
	}
	if err := m.ActivateGeneration("gen-1"); err != nil {
		t.Fatal(err)
	}
	if err := m.ActivateGeneration("gen-2"); err != nil {
		t.Fatal(err)
	}
	store := newFakeVectorStore()
	if errs := store.BatchUpsert("kb_shared", []vectorstore.UpsertRecord{
		{ID: "vec-old", Vector: []float32{1, 0}},
		{ID: "vec-new", Vector: []float32{0, 1}},
	}); len(errs) != 0 {
		t.Fatal(errs)
	}

	result, err := GCGenerations(m, store)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.DroppedCollections) != 0 {
		t.Fatalf("shared collection should not be dropped: %v", result.DroppedCollections)
	}
	if !reflect.DeepEqual(result.DeletedVectorIDs, []string{"vec-old"}) {
		t.Fatalf("deleted IDs=%v, want [vec-old]", result.DeletedVectorIDs)
	}
	if _, ok := store.records["kb_shared"]["vec-old"]; ok {
		t.Fatal("vec-old should be deleted")
	}
	if _, ok := store.records["kb_shared"]["vec-new"]; !ok {
		t.Fatal("vec-new should survive")
	}
}

func TestPlanGCGenerationsDoesNotMutateManifest(t *testing.T) {
	m := NewManifest("payments", "main")
	if err := m.UpsertChunk(ChunkRecord{Generation: "gen-1", Path: "docs/a.md", ChunkID: "a1", VectorID: "vec-old", Collection: "kb_shared"}); err != nil {
		t.Fatal(err)
	}
	if err := m.UpsertChunk(ChunkRecord{Generation: "gen-2", Path: "docs/a.md", ChunkID: "a2", VectorID: "vec-new", Collection: "kb_shared"}); err != nil {
		t.Fatal(err)
	}
	if err := m.ActivateGeneration("gen-1"); err != nil {
		t.Fatal(err)
	}
	if err := m.ActivateGeneration("gen-2"); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanGCGenerations(m)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.DryRun {
		t.Fatalf("dry_run=false, want true")
	}
	if !reflect.DeepEqual(plan.Generations, []string{"gen-1"}) {
		t.Fatalf("generations=%v, want [gen-1]", plan.Generations)
	}
	if !reflect.DeepEqual(plan.SharedCollections, []string{"kb_shared"}) || !reflect.DeepEqual(plan.DeletedVectorIDs, []string{"vec-old"}) {
		t.Fatalf("plan=%+v, want shared collection exact delete", plan)
	}
	if _, ok := m.Generations["gen-1"]; !ok {
		t.Fatal("plan mutated manifest generation")
	}
	if ids := m.VectorIDs(ChunkFilter{Generation: "gen-1"}); len(ids) != 1 {
		t.Fatalf("plan mutated chunks: %v", ids)
	}
}

func TestGCGenerationsRefusesActiveGeneration(t *testing.T) {
	m := NewManifest("payments", "main")
	if err := m.UpsertChunk(ChunkRecord{Generation: "gen-1", Path: "docs/a.md", ChunkID: "a1", VectorID: "vec-a1", Collection: "kb"}); err != nil {
		t.Fatal(err)
	}
	m.ActiveGeneration = "gen-1"
	m.Generations["gen-1"] = Generation{ID: "gen-1", Status: GenerationGCPending}

	_, err := GCGenerations(m, newFakeVectorStore())
	if !errors.Is(err, ErrCannotGCActiveGeneration) {
		t.Fatalf("err=%v, want ErrCannotGCActiveGeneration", err)
	}
}
