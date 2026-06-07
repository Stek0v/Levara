package workspace

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestManifestUpsertListVectorIDs(t *testing.T) {
	m := NewManifest("payments", "main")
	if err := m.UpsertChunk(ChunkRecord{
		Generation:  "gen-1",
		Path:        "docs/adr/payment-timeout.md",
		FileDigest:  "sha256:a",
		DocumentID:  "adr-payment-timeout",
		HeadingPath: []string{"Payment timeout", "Mitigation"},
		ChunkID:     "chunk-2",
		VectorID:    "vec-2",
		Collection:  "kb_payments_main_gen1",
	}); err != nil {
		t.Fatal(err)
	}
	if err := m.UpsertChunk(ChunkRecord{
		Generation: "gen-1",
		Path:       "docs/adr/payment-timeout.md",
		FileDigest: "sha256:a",
		DocumentID: "adr-payment-timeout",
		ChunkID:    "chunk-1",
		VectorID:   "vec-1",
		Collection: "kb_payments_main_gen1",
	}); err != nil {
		t.Fatal(err)
	}

	got := m.ListChunks(ChunkFilter{Path: "docs/adr/payment-timeout.md"})
	if len(got) != 2 {
		t.Fatalf("ListChunks len=%d, want 2", len(got))
	}
	if got[0].ChunkID != "chunk-1" || got[1].ChunkID != "chunk-2" {
		t.Fatalf("chunks not sorted by generation/path/chunk/vector: %+v", got)
	}

	ids := m.VectorIDs(ChunkFilter{FileDigest: "sha256:a"})
	if len(ids) != 2 || ids[0] != "vec-1" || ids[1] != "vec-2" {
		t.Fatalf("VectorIDs=%v, want [vec-1 vec-2]", ids)
	}
}

func TestManifestUpsertReplacesByVectorID(t *testing.T) {
	m := NewManifest("payments", "main")
	rec := ChunkRecord{
		Generation: "gen-1",
		Path:       "docs/a.md",
		FileDigest: "sha256:old",
		ChunkID:    "chunk-1",
		VectorID:   "vec-1",
	}
	if err := m.UpsertChunk(rec); err != nil {
		t.Fatal(err)
	}
	rec.FileDigest = "sha256:new"
	if err := m.UpsertChunk(rec); err != nil {
		t.Fatal(err)
	}

	chunks := m.ListChunks(ChunkFilter{VectorID: "vec-1"})
	if len(chunks) != 1 {
		t.Fatalf("chunks len=%d, want 1", len(chunks))
	}
	if chunks[0].FileDigest != "sha256:new" {
		t.Fatalf("FileDigest=%q, want sha256:new", chunks[0].FileDigest)
	}
}

func TestManifestDeleteChunksByPathAndGeneration(t *testing.T) {
	m := NewManifest("payments", "main")
	records := []ChunkRecord{
		{Generation: "gen-1", Path: "docs/a.md", FileDigest: "sha256:a", ChunkID: "a1", VectorID: "vec-a1"},
		{Generation: "gen-1", Path: "docs/a.md", FileDigest: "sha256:a", ChunkID: "a2", VectorID: "vec-a2"},
		{Generation: "gen-1", Path: "docs/b.md", FileDigest: "sha256:b", ChunkID: "b1", VectorID: "vec-b1"},
		{Generation: "gen-2", Path: "docs/a.md", FileDigest: "sha256:c", ChunkID: "a1", VectorID: "vec-a1-g2"},
	}
	for _, rec := range records {
		if err := m.UpsertChunk(rec); err != nil {
			t.Fatal(err)
		}
	}

	deleted := m.DeleteChunks(ChunkFilter{Generation: "gen-1", Path: "docs/a.md"})
	if len(deleted) != 2 {
		t.Fatalf("deleted len=%d, want 2", len(deleted))
	}

	remaining := m.VectorIDs(ChunkFilter{})
	want := map[string]bool{"vec-b1": true, "vec-a1-g2": true}
	if len(remaining) != len(want) {
		t.Fatalf("remaining=%v, want two survivors", remaining)
	}
	for _, id := range remaining {
		if !want[id] {
			t.Fatalf("unexpected survivor %q in %v", id, remaining)
		}
	}
}

func TestManifestGenerationActivation(t *testing.T) {
	m := NewManifest("payments", "main")
	if err := m.ActivateGeneration("gen-1"); err != nil {
		t.Fatal(err)
	}
	if m.ActiveGeneration != "gen-1" {
		t.Fatalf("ActiveGeneration=%q, want gen-1", m.ActiveGeneration)
	}
	if err := m.ActivateGeneration("gen-2"); err != nil {
		t.Fatal(err)
	}
	if m.Generations["gen-1"].Status != GenerationGCPending {
		t.Fatalf("gen-1 status=%q, want gc_pending", m.Generations["gen-1"].Status)
	}
	if m.Generations["gen-2"].Status != GenerationActive {
		t.Fatalf("gen-2 status=%q, want active", m.Generations["gen-2"].Status)
	}
}

func TestManifestActiveOnlyFilter(t *testing.T) {
	m := NewManifest("payments", "main")
	if err := m.UpsertChunk(ChunkRecord{Generation: "gen-1", Path: "docs/a.md", ChunkID: "a1", VectorID: "vec-old"}); err != nil {
		t.Fatal(err)
	}
	if err := m.UpsertChunk(ChunkRecord{Generation: "gen-2", Path: "docs/a.md", ChunkID: "a1", VectorID: "vec-new"}); err != nil {
		t.Fatal(err)
	}
	if err := m.ActivateGeneration("gen-2"); err != nil {
		t.Fatal(err)
	}

	ids := m.VectorIDs(ChunkFilter{Path: "docs/a.md", ActiveOnly: true})
	if len(ids) != 1 || ids[0] != "vec-new" {
		t.Fatalf("active IDs=%v, want [vec-new]", ids)
	}
}

func TestManifestSaveLoadRoundTrip(t *testing.T) {
	m := NewManifest("payments", "main")
	if err := m.UpsertChunk(ChunkRecord{
		Generation: "gen-1",
		Path:       "docs/a.md",
		FileDigest: "sha256:a",
		ChunkID:    "a1",
		VectorID:   "vec-a1",
	}); err != nil {
		t.Fatal(err)
	}
	if err := m.ActivateGeneration("gen-1"); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), ".kb", "manifests", "payments-main.json")
	if err := m.Save(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadManifest(path)
	if err != nil {
		t.Fatal(err)
	}

	if loaded.ProjectID != "payments" || loaded.Branch != "main" || loaded.ActiveGeneration != "gen-1" {
		t.Fatalf("loaded manifest identity wrong: %+v", loaded)
	}
	ids := loaded.VectorIDs(ChunkFilter{FileDigest: "sha256:a", ActiveOnly: true})
	if len(ids) != 1 || ids[0] != "vec-a1" {
		t.Fatalf("loaded IDs=%v, want [vec-a1]", ids)
	}
}

func TestManifestValidateRequiredFields(t *testing.T) {
	m := NewManifest("payments", "main")
	err := m.UpsertChunk(ChunkRecord{Generation: "gen-1", Path: "docs/a.md", VectorID: "vec-a1"})
	if !errors.Is(err, ErrMissingChunkID) {
		t.Fatalf("err=%v, want ErrMissingChunkID", err)
	}
	err = m.UpsertChunk(ChunkRecord{Generation: "gen-1", Path: "docs/a.md", ChunkID: "a1"})
	if !errors.Is(err, ErrMissingVectorID) {
		t.Fatalf("err=%v, want ErrMissingVectorID", err)
	}
}
