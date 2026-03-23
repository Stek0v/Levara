package store

import (
	"fmt"
	"math/rand"
	"os"
	"testing"
)

func randVecForTest(dim int) []float32 {
	v := make([]float32, dim)
	for i := range v {
		v[i] = rand.Float32()*2 - 1
	}
	return v
}

func TestCollectionCRUD(t *testing.T) {
	dir, _ := os.MkdirTemp("", "levara-col-test-*")
	defer os.RemoveAll(dir)

	cm, err := NewCollectionManager(64, dir)
	if err != nil {
		t.Fatalf("NewCollectionManager: %v", err)
	}
	defer cm.Close()

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
	cm.Create("another_col")
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
	defer cm.Close()

	// Insert into two collections with DIFFERENT vectors
	cm.Create("books")
	cm.Create("movies")

	bookVec := randVecForTest(64)
	movieVec := randVecForTest(64)

	cm.Insert("books", "book-1", bookVec, map[string]any{"text": "a book"})
	cm.Insert("movies", "movie-1", movieVec, map[string]any{"text": "a movie"})

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

func TestCollectionPersistence(t *testing.T) {
	dir, _ := os.MkdirTemp("", "levara-persist-test-*")
	defer os.RemoveAll(dir)

	dim := 64

	// Phase 1: Create and populate
	cm, _ := NewCollectionManager(dim, dir)
	cm.Create("persist_test")
	for i := 0; i < 10; i++ {
		cm.Insert("persist_test", fmt.Sprintf("id-%d", i), randVecForTest(dim),
			map[string]any{"index": i})
	}
	cm.Close()

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
