package store

import (
	"fmt"
	"testing"
	"time"
)

func TestDebugSearchRecovery(t *testing.T) {
	const dim = 64
	dir := t.TempDir()
	dbPath := dir + "/meta.bin"

	db, err := NewLevara(dim, dbPath)
	if err != nil {
		t.Fatal(err)
	}

	target := randomVec(dim)
	db.Insert("target", target, map[string]any{"label": "target"})
	for i := 0; i < 50; i++ {
		db.Insert(fmt.Sprintf("noise-%d", i), randomVec(dim), nil)
	}
	time.Sleep(200 * time.Millisecond)

	// Search before close
	r1 := db.Search(target, 1)
	t.Logf("Before close: %d results, first=%v", len(r1), r1)

	db.Close()

	// Reopen WITHOUT checkpoint
	db2, err := NewLevara(dim, dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()

	// How many records?
	t.Logf("Records after recovery: %d", len(db2.index))

	// Search after recovery
	r2 := db2.Search(target, 1)
	t.Logf("After recovery: %d results, first=%v", len(r2), r2)
}
