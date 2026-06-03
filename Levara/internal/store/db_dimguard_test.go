package store

import (
	"os"
	"testing"
)

// TestDBSearchDimMismatchNoPanic guards P2.4: the raw sharded search path
// (Cluster.Search -> ShardHandler.Search -> Levara.Search) reaches dist()/
// vek32.Dot with no dim check, so a query whose dimension differs from the
// collection's panics "slices must be of equal length" and takes down the
// process. CollectionManager.Search (Fix #1) guards the memory path; this is
// defense in depth for the sharded path. Levara.Search returns no error, so the
// guard degrades to an empty result rather than crashing.
func TestDBSearchDimMismatchNoPanic(t *testing.T) {
	dir, _ := os.MkdirTemp("", "levara-db-dimguard-*")
	defer os.RemoveAll(dir)

	const dim = 64
	db, err := NewLevara(dim, dir+"/meta.bin")
	if err != nil {
		t.Fatalf("NewLevara: %v", err)
	}
	defer func() { _ = db.Close() }()

	if err := db.Insert("r1", randomVec(dim), nil); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// A mismatched-dim query must not panic (exercises both the pending
	// brute-force scan and the HNSW path).
	got := db.Search(randomVec(32), 5)
	if len(got) != 0 {
		t.Fatalf("Search with mismatched query dim returned %d results, want 0", len(got))
	}
}
