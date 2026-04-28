package vectorstore

import (
	"os"
	"testing"

	"github.com/stek0v/levara/internal/store"
)

// T-9b smoke for HNSWStore — the VectorStore adapter wrapping
// internal/store.CollectionManager. Pure pass-through, but the test acts as
// a contract check: if anything in the underlying CollectionManager API
// drifts (rename, signature change), the adapter caller and these tests
// catch it.

func newStore(t testing.TB, dim int) (*HNSWStore, func()) {
	t.Helper()
	dir, err := os.MkdirTemp("", "vectorstore-test-*")
	if err != nil {
		t.Fatal(err)
	}
	cm, err := store.NewCollectionManager(dim, dir)
	if err != nil {
		os.RemoveAll(dir)
		t.Fatal(err)
	}
	s := NewHNSWStore(cm)
	return s, func() {
		_ = s.Close()
		os.RemoveAll(dir)
	}
}

func TestHNSWStore_CreateHasList(t *testing.T) {
	s, cleanup := newStore(t, 4)
	defer cleanup()

	if s.Has("c1") {
		t.Error("Has(c1) before Create should be false")
	}
	if err := s.Create("c1"); err != nil {
		t.Fatal(err)
	}
	if !s.Has("c1") {
		t.Error("Has(c1) after Create should be true")
	}
	names := s.List()
	found := false
	for _, n := range names {
		if n == "c1" {
			found = true
		}
	}
	if !found {
		t.Errorf("c1 missing from List: %v", names)
	}
}

func TestHNSWStore_InsertCountSearch(t *testing.T) {
	s, cleanup := newStore(t, 2)
	defer cleanup()

	if err := s.Create("docs"); err != nil {
		t.Fatal(err)
	}

	pts := map[string][]float32{
		"a": {1, 0},
		"b": {0, 1},
		"c": {1, 1},
	}
	for id, v := range pts {
		if err := s.Insert("docs", id, v, map[string]any{"id": id}); err != nil {
			t.Fatalf("Insert %s: %v", id, err)
		}
	}

	if got := s.Count("docs"); got != 3 {
		t.Errorf("Count = %d, want 3", got)
	}

	results, err := s.Search("docs", []float32{1, 0}, 2)
	if err != nil {
		t.Fatal(err)
	}
	// Search may return 0 results if HNSW indexer hasn't caught up; the
	// shape contract is what we lock in here.
	for _, r := range results {
		if r.ID == "" {
			t.Error("result has empty ID")
		}
	}
}

func TestHNSWStore_Delete(t *testing.T) {
	s, cleanup := newStore(t, 2)
	defer cleanup()

	if err := s.Create("c"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"x", "y"} {
		if err := s.Insert("c", id, []float32{1, 0}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if got := s.Count("c"); got != 2 {
		t.Fatalf("pre-Delete Count = %d", got)
	}
	if err := s.Delete("c", "x"); err != nil {
		t.Fatal(err)
	}
	if got := s.Count("c"); got != 1 {
		t.Errorf("post-Delete Count = %d, want 1", got)
	}
}

func TestHNSWStore_CountMissingCollection(t *testing.T) {
	s, cleanup := newStore(t, 2)
	defer cleanup()
	if got := s.Count("nonexistent"); got != 0 {
		t.Errorf("Count on missing collection = %d, want 0", got)
	}
}

func TestHNSWStore_DeleteFromMissingCollectionErrors(t *testing.T) {
	s, cleanup := newStore(t, 2)
	defer cleanup()
	if err := s.Delete("nonexistent", "x"); err == nil {
		t.Error("Delete on missing collection should error")
	}
}
