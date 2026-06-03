package store

import (
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

func randVecForTest(dim int) []float32 {
	v := make([]float32, dim)
	for i := range v {
		v[i] = rand.Float32()*2 - 1
	}
	return v
}

// TestCollectionSearchDimMismatchReturnsError guards the crash where a query
// vector whose dimension differs from the collection's reaches dist()/vek32.Dot
// and panics "slices must be of equal length", taking down the whole process.
// This happens in prod when a 768-dim embedder queries a 256-dim memory sidecar
// (consolidate, recall). Search must return an error instead.
func TestCollectionSearchDimMismatchReturnsError(t *testing.T) {
	dir, _ := os.MkdirTemp("", "levara-dimguard-*")
	defer os.RemoveAll(dir)

	cm, err := NewCollectionManager(64, dir)
	if err != nil {
		t.Fatalf("NewCollectionManager: %v", err)
	}
	defer func() { _ = cm.Close() }()

	if err := cm.Create("c"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := cm.Insert("c", "r1", randVecForTest(64), map[string]any{"text": "x"}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	_, err = cm.Search("c", randVecForTest(32), 5)
	if err == nil {
		t.Fatal("Search with mismatched query dim should return an error, got nil")
	}
	// Callers (consolidate) must be able to recognize a dim mismatch
	// distinctly from other errors to surface "collection incompatible"
	// instead of a silent clusters=0.
	if !errors.Is(err, ErrDimMismatch) {
		t.Fatalf("Search dim-mismatch error not detectable via errors.Is(ErrDimMismatch): %v", err)
	}
}

func TestCollectionCRUD(t *testing.T) {
	dir, _ := os.MkdirTemp("", "levara-col-test-*")
	defer os.RemoveAll(dir)

	cm, err := NewCollectionManager(64, dir)
	if err != nil {
		t.Fatalf("NewCollectionManager: %v", err)
	}
	defer func() { _ = cm.Close() }()

	// Create
	if err := cm.Create("test_col"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !cm.Has("test_col") {
		t.Fatal("Has should return true after Create")
	}

	// Idempotent create
	if err := cm.Create("test_col"); err != nil {
		t.Fatalf("Idempotent Create: %v", err)
	}

	// List
	names := cm.List()
	if len(names) != 1 || names[0] != "test_col" {
		t.Fatalf("List: got %v, want [test_col]", names)
	}

	// Create second
	_ = cm.Create("another_col")
	if cm.Count() != 2 {
		t.Fatalf("Count: got %d, want 2", cm.Count())
	}

	// Drop
	if err := cm.Drop("test_col"); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	if cm.Has("test_col") {
		t.Fatal("Has should return false after Drop")
	}
	if cm.Count() != 1 {
		t.Fatalf("Count after drop: got %d, want 1", cm.Count())
	}

	// Drop non-existent
	if err := cm.Drop("nonexistent"); err == nil {
		t.Fatal("Drop non-existent should return error")
	}
}

func TestCollectionIsolation(t *testing.T) {
	dir, _ := os.MkdirTemp("", "levara-isolation-test-*")
	defer os.RemoveAll(dir)

	cm, err := NewCollectionManager(64, dir)
	if err != nil {
		t.Fatalf("NewCollectionManager: %v", err)
	}
	defer func() { _ = cm.Close() }()

	// Insert into two collections with DIFFERENT vectors
	_ = cm.Create("books")
	_ = cm.Create("movies")

	bookVec := randVecForTest(64)
	movieVec := randVecForTest(64)

	_ = cm.Insert("books", "book-1", bookVec, map[string]any{"text": "a book"})
	_ = cm.Insert("movies", "movie-1", movieVec, map[string]any{"text": "a movie"})

	// Search books — should NOT find movie
	bookResults, err := cm.Search("books", bookVec, 10)
	if err != nil {
		t.Fatalf("Search books: %v", err)
	}

	for _, r := range bookResults {
		if r.ID == "movie-1" {
			t.Fatal("Cross-collection leakage: found movie-1 in books search")
		}
	}

	// Search movies — should NOT find book
	movieResults, err := cm.Search("movies", movieVec, 10)
	if err != nil {
		t.Fatalf("Search movies: %v", err)
	}

	for _, r := range movieResults {
		if r.ID == "book-1" {
			t.Fatal("Cross-collection leakage: found book-1 in movies search")
		}
	}

	// Verify correct results
	if len(bookResults) != 1 || bookResults[0].ID != "book-1" {
		t.Fatalf("books search: got %v, want [book-1]", bookResults)
	}
	if len(movieResults) != 1 || movieResults[0].ID != "movie-1" {
		t.Fatalf("movies search: got %v, want [movie-1]", movieResults)
	}
}

func TestCollectionRename(t *testing.T) {
	dir, _ := os.MkdirTemp("", "levara-rename-test-*")
	defer os.RemoveAll(dir)

	cm, err := NewCollectionManager(64, dir)
	if err != nil {
		t.Fatalf("NewCollectionManager: %v", err)
	}
	defer func() { _ = cm.Close() }()

	if err := cm.Create("orig"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	vec := randVecForTest(64)
	if err := cm.Insert("orig", "r1", vec, map[string]any{"text": "hello"}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// Happy path
	if err := cm.Rename("orig", "renamed"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if cm.Has("orig") {
		t.Fatal("old name should be gone after rename")
	}
	if !cm.Has("renamed") {
		t.Fatal("new name should exist after rename")
	}
	results, err := cm.Search("renamed", vec, 10)
	if err != nil {
		t.Fatalf("Search renamed: %v", err)
	}
	if len(results) != 1 || results[0].ID != "r1" {
		t.Fatalf("Search after rename: got %v, want [r1]", results)
	}
	// On-disk directory moved (collections live under <dir>/collections/<name>)
	if _, err := os.Stat(filepath.Join(dir, "collections", "orig")); !os.IsNotExist(err) {
		t.Fatalf("old dir should be gone: err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "collections", "renamed")); err != nil {
		t.Fatalf("new dir should exist: %v", err)
	}
	if meta := cm.GetMeta("renamed"); meta == nil || meta.Name != "renamed" {
		t.Fatalf("meta.Name not updated: %+v", meta)
	}

	// Source not found
	if err := cm.Rename("missing", "whatever"); err == nil {
		t.Fatal("Rename of missing source should error")
	}

	// Target already exists (in-memory)
	_ = cm.Create("other")
	if err := cm.Rename("renamed", "other"); err == nil {
		t.Fatal("Rename to existing target should error")
	}

	// Identical names
	if err := cm.Rename("renamed", "renamed"); err == nil {
		t.Fatal("Rename to identical name should error")
	}

	// Empty names
	if err := cm.Rename("", "foo"); err == nil {
		t.Fatal("Rename with empty source should error")
	}
	if err := cm.Rename("renamed", ""); err == nil {
		t.Fatal("Rename with empty target should error")
	}

	// Path traversal
	for _, bad := range []string{"../escape", "..", ".", "a/b", "a\\b"} {
		if err := cm.Rename("renamed", bad); err == nil {
			t.Fatalf("Rename to %q should error (path traversal guard)", bad)
		}
	}
}

func TestCollectionRenameSurvivesRestart(t *testing.T) {
	dir, _ := os.MkdirTemp("", "levara-rename-restart-*")
	defer os.RemoveAll(dir)

	dim := 64

	// Phase 1: create, insert, rename, close.
	cm, err := NewCollectionManager(dim, dir)
	if err != nil {
		t.Fatalf("NewCollectionManager: %v", err)
	}
	if err := cm.Create("orig"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	vecs := make([][]float32, 10)
	for i := 0; i < 10; i++ {
		vecs[i] = randVecForTest(dim)
		if err := cm.Insert("orig", fmt.Sprintf("rec-%d", i), vecs[i],
			map[string]any{"index": i, "src": "orig"}); err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}
	if err := cm.Rename("orig", "renamed"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if err := cm.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Phase 2: reopen — simulates Levara restart after a rename.
	cm2, err := NewCollectionManager(dim, dir)
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	defer cm2.Close()

	if !cm2.Has("renamed") {
		t.Fatal("renamed collection should be loaded after restart")
	}
	if cm2.Has("orig") {
		t.Fatal("orig collection should NOT come back after rename + restart")
	}
	db, err := cm2.Get("renamed")
	if err != nil {
		t.Fatalf("Get(renamed) failed after restart: %v", err)
	}
	if len(db.index) != 10 {
		t.Fatalf("record count after restart: got %d, want 10", len(db.index))
	}
	// Every original vector should still be retrievable by ID via search.
	for i := 0; i < 10; i++ {
		results, err := cm2.Search("renamed", vecs[i], 1)
		if err != nil {
			t.Fatalf("Search rec-%d: %v", i, err)
		}
		if len(results) == 0 || results[0].ID != fmt.Sprintf("rec-%d", i) {
			t.Fatalf("rec-%d: got %+v", i, results)
		}
	}
	// Meta survived under the new name.
	meta := cm2.GetMeta("renamed")
	if meta == nil || meta.Name != "renamed" {
		t.Fatalf("meta after restart: %+v", meta)
	}
}

func TestCollectionRenameTargetDirOnDisk(t *testing.T) {
	dir, _ := os.MkdirTemp("", "levara-rename-disk-test-*")
	defer os.RemoveAll(dir)

	cm, err := NewCollectionManager(64, dir)
	if err != nil {
		t.Fatalf("NewCollectionManager: %v", err)
	}
	defer func() { _ = cm.Close() }()

	_ = cm.Create("src")
	// Pre-existing target directory not tracked by the manager
	if err := os.MkdirAll(filepath.Join(dir, "collections", "stale"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := cm.Rename("src", "stale"); err == nil {
		t.Fatal("Rename should refuse to clobber existing on-disk directory")
	}
	// Source must remain functional after the refusal
	if !cm.Has("src") {
		t.Fatal("source should still exist after refused rename")
	}
}

func TestCollectionPersistence(t *testing.T) {
	dir, _ := os.MkdirTemp("", "levara-persist-test-*")
	defer os.RemoveAll(dir)

	dim := 64

	// Phase 1: Create and populate
	cm, _ := NewCollectionManager(dim, dir)
	cm.Create("persist_test")
	for i := 0; i < 10; i++ {
		_ = cm.Insert("persist_test", fmt.Sprintf("id-%d", i), randVecForTest(dim),
			map[string]any{"index": i})
	}
	_ = cm.Close()

	// Phase 2: Reopen and verify data survived
	cm2, err := NewCollectionManager(dim, dir)
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	defer cm2.Close()

	if !cm2.Has("persist_test") {
		t.Fatal("Collection should persist across restarts")
	}

	db, _ := cm2.Get("persist_test")
	if len(db.index) != 10 {
		t.Fatalf("Expected 10 records after restart, got %d", len(db.index))
	}
}
