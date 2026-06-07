package vectorstore

import (
	"errors"
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
		_ = os.RemoveAll(dir)
		t.Fatal(err)
	}
	s := NewHNSWStore(cm)
	return s, func() {
		_ = s.Close()
		_ = os.RemoveAll(dir)
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

func TestHNSWStore_BatchUpsertGetScanMetadata(t *testing.T) {
	s, cleanup := newStore(t, 2)
	defer cleanup()

	errs := s.BatchUpsert("docs", []UpsertRecord{
		{ID: "b", Vector: []float32{0, 1}, Metadata: map[string]any{"document_id": "doc-2", "index": 2}},
		{ID: "a", Vector: []float32{1, 0}, Metadata: map[string]any{"document_id": "doc-1", "index": 1}},
	})
	if len(errs) != 0 {
		t.Fatalf("BatchUpsert errors: %v", errs)
	}
	if got := s.Count("docs"); got != 2 {
		t.Fatalf("Count=%d, want 2", got)
	}

	rec, ok, err := s.Get("docs", "a")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("Get(a) ok=false, want true")
	}
	if rec.ID != "a" || len(rec.Vector) != 2 || !containsBytes(rec.Metadata, `"doc-1"`) {
		t.Fatalf("unexpected record: %+v metadata=%s", rec, string(rec.Metadata))
	}

	records, err := s.Scan("docs")
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("Scan len=%d, want 2", len(records))
	}
	if records[0].ID != "a" || records[1].ID != "b" {
		t.Fatalf("Scan should return deterministic ID order, got %q %q", records[0].ID, records[1].ID)
	}

	meta, ok := s.Metadata("docs")
	if !ok {
		t.Fatal("Metadata(docs) ok=false, want true")
	}
	if meta.Name != "docs" || meta.RecordCount != 2 || meta.Dimension != 2 {
		t.Fatalf("metadata=%+v, want docs count=2 dim=2", meta)
	}
}

func TestHNSWStore_BatchUpsertReplacesSameID(t *testing.T) {
	s, cleanup := newStore(t, 2)
	defer cleanup()

	if errs := s.BatchUpsert("docs", []UpsertRecord{
		{ID: "chunk-1", Vector: []float32{1, 0}, Metadata: map[string]any{"version": 1}},
	}); len(errs) != 0 {
		t.Fatalf("initial BatchUpsert errors: %v", errs)
	}
	if errs := s.BatchUpsert("docs", []UpsertRecord{
		{ID: "chunk-1", Vector: []float32{0, 1}, Metadata: map[string]any{"version": 2}},
	}); len(errs) != 0 {
		t.Fatalf("replacement BatchUpsert errors: %v", errs)
	}
	if got := s.Count("docs"); got != 1 {
		t.Fatalf("Count after replacement=%d, want 1", got)
	}
	rec, ok, err := s.Get("docs", "chunk-1")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || !containsBytes(rec.Metadata, `"version":2`) {
		t.Fatalf("replacement not visible: ok=%v metadata=%s", ok, string(rec.Metadata))
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

func TestHNSWStore_DeleteMany(t *testing.T) {
	s, cleanup := newStore(t, 2)
	defer cleanup()

	if errs := s.BatchUpsert("docs", []UpsertRecord{
		{ID: "a", Vector: []float32{1, 0}, Metadata: map[string]any{"keep": false}},
		{ID: "b", Vector: []float32{0, 1}, Metadata: map[string]any{"keep": false}},
		{ID: "c", Vector: []float32{1, 1}, Metadata: map[string]any{"keep": true}},
	}); len(errs) != 0 {
		t.Fatalf("BatchUpsert errors: %v", errs)
	}

	if errs := s.DeleteMany("docs", []string{"a", "b"}); len(errs) != 0 {
		t.Fatalf("DeleteMany errors: %v", errs)
	}
	if got := s.Count("docs"); got != 1 {
		t.Fatalf("Count=%d, want 1", got)
	}
	if _, ok, err := s.Get("docs", "c"); err != nil || !ok {
		t.Fatalf("survivor c missing: ok=%v err=%v", ok, err)
	}
}

func TestHNSWStore_Drop(t *testing.T) {
	s, cleanup := newStore(t, 2)
	defer cleanup()

	if err := s.Create("docs"); err != nil {
		t.Fatal(err)
	}
	if !s.Has("docs") {
		t.Fatal("docs should exist before Drop")
	}
	if err := s.Drop("docs"); err != nil {
		t.Fatal(err)
	}
	if s.Has("docs") {
		t.Fatal("docs should not exist after Drop")
	}
}

func TestHNSWStore_DeleteByFilter(t *testing.T) {
	s, cleanup := newStore(t, 2)
	defer cleanup()

	if errs := s.BatchUpsert("docs", []UpsertRecord{
		{
			ID:     "chunk-a1",
			Vector: []float32{1, 0},
			Metadata: map[string]any{
				"document_id": "adr-a",
				"file_digest": "digest-a",
				"tags":        []string{"payments", "incident"},
				"index":       1,
			},
		},
		{
			ID:     "chunk-a2",
			Vector: []float32{0.9, 0.1},
			Metadata: map[string]any{
				"document_id": "adr-a",
				"file_digest": "digest-b",
				"tags":        []string{"payments"},
				"index":       2,
			},
		},
		{
			ID:     "chunk-b1",
			Vector: []float32{0, 1},
			Metadata: map[string]any{
				"document_id": "adr-b",
				"file_digest": "digest-a",
				"tags":        []string{"auth"},
				"index":       3,
			},
		},
	}); len(errs) != 0 {
		t.Fatalf("BatchUpsert errors: %v", errs)
	}

	deleted, errs := s.DeleteByFilter("docs", MetadataFilter{
		"document_id": "adr-a",
		"tags":        "payments",
	})
	if len(errs) != 0 {
		t.Fatalf("DeleteByFilter errors: %v", errs)
	}
	if len(deleted) != 2 {
		t.Fatalf("deleted=%v, want two adr-a chunks", deleted)
	}
	if got := s.Count("docs"); got != 1 {
		t.Fatalf("Count=%d, want 1", got)
	}
	if _, ok, err := s.Get("docs", "chunk-b1"); err != nil || !ok {
		t.Fatalf("chunk-b1 should survive: ok=%v err=%v", ok, err)
	}
}

func TestHNSWStore_DeleteByFilterEmptyFilterErrors(t *testing.T) {
	s, cleanup := newStore(t, 2)
	defer cleanup()

	deleted, errs := s.DeleteByFilter("docs", MetadataFilter{})
	if len(deleted) != 0 {
		t.Fatalf("deleted=%v, want none", deleted)
	}
	if len(errs) != 1 || !errors.Is(errs[0], ErrEmptyFilter) {
		t.Fatalf("errs=%v, want ErrEmptyFilter", errs)
	}
}

func TestHNSWStore_Checkpoint(t *testing.T) {
	s, cleanup := newStore(t, 2)
	defer cleanup()

	if err := s.Create("docs"); err != nil {
		t.Fatal(err)
	}
	if err := s.Checkpoint(); err != nil {
		t.Fatalf("Checkpoint: %v", err)
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

func containsBytes(haystack []byte, needle string) bool {
	return containsString(string(haystack), needle)
}

func containsString(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
