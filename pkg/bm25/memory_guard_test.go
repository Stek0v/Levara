package bm25

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestIndexAddCapDropsNewDocsBeyondMax — the in-memory doc cap keeps a
// giant collection's BM25 index bounded (regression for the prod incident:
// the chat-imports index grew to 12.2 GB of heap, past the snapshot
// threshold, and put the GC into a death spiral).
func TestIndexAddCapDropsNewDocsBeyondMax(t *testing.T) {
	idx := NewIndex()
	idx.SetMaxDocs(3)
	for i := 0; i < 5; i++ {
		idx.Add(fmt.Sprintf("doc-%d", i), fmt.Sprintf("text number %d alpha beta", i), "")
	}
	if got := idx.Len(); got != 3 {
		t.Fatalf("Len = %d, want 3 (capped)", got)
	}

	// Replacements of existing IDs always pass — derivative re-renders
	// update in place and must never be dropped.
	idx.Add("doc-0", "replacement text gamma delta", "")
	if got := idx.Len(); got != 3 {
		t.Fatalf("Len after replacement = %d, want 3", got)
	}
	res := idx.Search("replacement", 5)
	if len(res) != 1 || res[0].ID != "doc-0" {
		t.Fatalf("replacement not searchable: %+v", res)
	}

	// After removals bring the index under the cap, new docs are accepted again.
	idx.Remove("doc-1")
	idx.Remove("doc-2")
	idx.Add("doc-new", "fresh text epsilon", "")
	if got := idx.Len(); got != 2 {
		t.Fatalf("Len after shrink+add = %d, want 2", got)
	}
}

// TestLoadAllSkipsOversizedSnapshot — snapshots past the doc threshold are
// not loaded at boot: the line-count precheck skips them before a single
// document is unmarshaled or tokenized (the pre-guard chat-imports
// snapshot was 3.6 GB on disk → 12.2 GB heap).
func TestLoadAllSkipsOversizedSnapshot(t *testing.T) {
	oldMax := bm25SnapshotMaxDocs
	bm25SnapshotMaxDocs = 10
	defer func() { bm25SnapshotMaxDocs = oldMax }()

	dir := t.TempDir()
	store := NewSnapshotStore(dir)
	writeSnapshotFile(t, dir, "big", 50)
	writeSnapshotFile(t, dir, "small", 3)

	loaded, err := store.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if _, ok := loaded["big"]; ok {
		t.Fatal("oversized snapshot was loaded — memory guard failed")
	}
	small, ok := loaded["small"]
	if !ok {
		t.Fatal("small snapshot missing from LoadAll result")
	}
	if small.Len() != 3 {
		t.Fatalf("small Len = %d, want 3", small.Len())
	}
}

// writeSnapshotFile writes a snapshot JSONL directly, bypassing SaveAll
// (whose own guard would refuse to persist an oversized index).
func writeSnapshotFile(t *testing.T, dir, collection string, docs int) {
	t.Helper()
	f, err := os.Create(filepath.Join(dir, encodeCollectionName(collection)+snapshotExt))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for i := 0; i < docs; i++ {
		if err := enc.Encode(diskDoc{Op: "put", ID: fmt.Sprintf("d-%d", i), Text: fmt.Sprintf("text %d alpha", i)}); err != nil {
			t.Fatal(err)
		}
	}
}

// TestSnapshotGuardsTreatCapAsOversize — a capped index sits at exactly the
// threshold; both the save and load guards must refuse it (> would let a
// ~GB snapshot be rewritten every autosave tick and reloaded at boot).
func TestSnapshotGuardsTreatCapAsOversize(t *testing.T) {
	dir := t.TempDir()
	old := bm25SnapshotMaxDocs
	bm25SnapshotMaxDocs = 3
	defer func() { bm25SnapshotMaxDocs = old }()

	idx := NewIndex()
	idx.SetMaxDocs(3)
	for i := 0; i < 5; i++ {
		idx.Add(fmt.Sprintf("doc-%d", i), fmt.Sprintf("capped text %d alpha", i), "")
	}
	if idx.Len() != 3 {
		t.Fatalf("Len = %d, want 3", idx.Len())
	}
	store := NewSnapshotStore(dir)
	if err := store.SaveAll(map[string]*Index{"capped": idx}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, encodeCollectionName("capped")+snapshotExt)); !os.IsNotExist(err) {
		t.Fatal("snapshot written for an index sitting exactly at the cap")
	}

	writeSnapshotFile(t, dir, "at-cap", bm25SnapshotMaxDocs)
	loaded, err := store.LoadAll()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := loaded["at-cap"]; ok {
		t.Fatal("snapshot with exactly threshold lines was loaded")
	}
}
