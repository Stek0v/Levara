package bm25

import (
	"testing"
)

func TestSnapshotStoreSaveLoadAll(t *testing.T) {
	dir := t.TempDir()
	store := NewSnapshotStore(dir)

	idx := NewIndex()
	idx.Add("doc-1", "bounded timeout retry", `{"source":"test"}`)
	if err := store.SaveAll(map[string]*Index{"project/chunks": idx}); err != nil {
		t.Fatalf("SaveAll: %v", err)
	}

	loaded, err := store.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	got := loaded["project/chunks"]
	if got == nil {
		t.Fatal("missing loaded index for collection")
	}
	hits := got.Search("bounded timeout", 10)
	if len(hits) != 1 {
		t.Fatalf("hits=%d, want 1", len(hits))
	}
	if hits[0].ID != "doc-1" {
		t.Fatalf("hit ID=%q, want doc-1", hits[0].ID)
	}
}

func TestSaveSnapshotReplacesRemovedDocs(t *testing.T) {
	path := t.TempDir() + "/idx.jsonl"

	idx := NewIndex()
	idx.Add("old", "legacy keyword", `{}`)
	if err := SaveSnapshot(path, idx); err != nil {
		t.Fatalf("SaveSnapshot old: %v", err)
	}

	idx.Remove("old")
	idx.Add("new", "current keyword", `{}`)
	if err := SaveSnapshot(path, idx); err != nil {
		t.Fatalf("SaveSnapshot new: %v", err)
	}

	loaded, err := LoadSnapshot(path)
	if err != nil {
		t.Fatalf("LoadSnapshot: %v", err)
	}
	if hits := loaded.Search("legacy", 10); len(hits) != 0 {
		t.Fatalf("legacy hits=%d, want 0", len(hits))
	}
	if hits := loaded.Search("current", 10); len(hits) != 1 {
		t.Fatalf("current hits=%d, want 1", len(hits))
	}
}

func TestSnapshotStoreAttachPersistsMutationsImmediately(t *testing.T) {
	dir := t.TempDir()
	store := NewSnapshotStore(dir)
	idx := NewIndex()
	store.Attach("events", idx)

	idx.Add("doc-1", "exact panic message", `{}`)
	idx.Remove("doc-1")
	idx.Add("doc-2", "recovered panic message", `{}`)

	loaded, err := store.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	got := loaded["events"]
	if got == nil {
		t.Fatal("missing loaded events index")
	}
	if hits := got.Search("exact", 10); len(hits) != 0 {
		t.Fatalf("exact hits=%d, want 0", len(hits))
	}
	if hits := got.Search("recovered", 10); len(hits) != 1 {
		t.Fatalf("recovered hits=%d, want 1", len(hits))
	}
}

func TestSnapshotStoreRemoveDeletesSidecar(t *testing.T) {
	dir := t.TempDir()
	store := NewSnapshotStore(dir)
	idx := NewIndex()
	idx.Add("doc-1", "legacy keyword", `{}`)
	if err := store.SaveAll(map[string]*Index{"obsolete": idx}); err != nil {
		t.Fatalf("SaveAll: %v", err)
	}

	if err := store.Remove("obsolete"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	loaded, err := store.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if loaded["obsolete"] != nil {
		t.Fatal("obsolete sidecar was loaded after Remove")
	}
}
