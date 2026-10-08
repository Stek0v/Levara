package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/stek0v/levara/pkg/vectorstore"
)

// These faults deliberately leave native-like partial effects for exact-ID
// compensation; they do not turn a failed BatchUpsert into an atomic operation.
type publicationFaultStore struct {
	*fakeVectorStore
	failUpsert        bool
	failDelete        bool
	cancelAfterUpsert context.CancelFunc
}

func (s *publicationFaultStore) BatchUpsert(collection string, records []vectorstore.UpsertRecord) []error {
	if s.failUpsert {
		if len(records) > 0 {
			s.fakeVectorStore.BatchUpsert(collection, records[:1])
		}
		return []error{errors.New("partial upsert")}
	}
	errs := s.fakeVectorStore.BatchUpsert(collection, records)
	if s.cancelAfterUpsert != nil {
		s.cancelAfterUpsert()
	}
	return errs
}
func (s *publicationFaultStore) DeleteMany(collection string, ids []string) []error {
	if s.failDelete {
		if len(ids) > 0 {
			s.fakeVectorStore.DeleteMany(collection, ids[:1])
		}
		return []error{errors.New("partial cleanup")}
	}
	return s.fakeVectorStore.DeleteMany(collection, ids)
}

func TestWorkspaceCandidateSameGenerationRechunkPreservesPublishedIDs(t *testing.T) {
	store := newFakeVectorStore()
	active := NewManifest("project", "main")
	lexical := newFakeLexicalIndex()
	file := MarkdownFile{Path: "docs/a.md", Text: strings.Repeat("First sentence. Second sentence.\n\n", 30), FileDigest: "same-digest"}
	old, err := (&Indexer{Store: store, Embedder: &fakeEmbedder{}, Manifest: active, Lexical: lexical}).IndexMarkdown(context.Background(), file, IndexOptions{Generation: "g", Collection: "shared", AttemptID: "a", ActivateGeneration: true, MinChunkChars: 1, MaxChunkChars: 100000})
	if err != nil {
		t.Fatal(err)
	}
	before := active.Clone()
	if len(old.VectorIDs) == 0 {
		t.Fatal("fixture did not publish old vectors")
	}
	candidate := active.Clone()
	fresh, err := (&Indexer{Store: store, Embedder: &fakeEmbedder{}, Manifest: candidate, Lexical: lexical}).IndexMarkdown(context.Background(), file, IndexOptions{Generation: "g", Collection: "shared", AttemptID: "b", DeferRetirement: true, MinChunkChars: 1, MaxChunkChars: 80, ChunkStrategy: "sliding"})
	if err != nil {
		t.Fatal(err)
	}
	if fresh.ChunksCreated <= old.ChunksCreated {
		t.Fatalf("fixture did not rechunk: old=%d fresh=%d", old.ChunksCreated, fresh.ChunksCreated)
	}
	if !reflect.DeepEqual(active, before) {
		t.Fatal("candidate changed published manifest")
	}
	for _, id := range old.VectorIDs {
		if _, ok := store.records["shared"][id]; !ok {
			t.Fatalf("published vector %s retired before publication", id)
		}
		if _, ok := candidate.PendingRetirements[id]; !ok {
			t.Fatalf("old ID %s not queued", id)
		}
		if _, ok := lexical.added[id]; !ok {
			t.Fatalf("old lexical ID %s retired", id)
		}
	}
	if len(lexical.removed) != 0 {
		t.Fatalf("early lexical retirement=%v", lexical.removed)
	}
	for _, id := range fresh.VectorIDs {
		if _, oldID := active.Chunks[id]; oldID {
			t.Fatal("attempt overwrote published ID")
		}
		metadata := store.records["shared"][id].Metadata.(map[string]any)
		if metadata["attempt_id"] != "b" {
			t.Fatalf("attempt metadata=%v", metadata)
		}
	}
	if candidate.Files["g"][file.Path] != file.FileDigest {
		t.Fatal("successful candidate inventory missing")
	}
}

func TestWorkspacePreparedIDsSurvivePartialUpsertAndCancellation(t *testing.T) {
	for _, mode := range []string{"partial_upsert", "post_upsert_cancel"} {
		t.Run(mode, func(t *testing.T) {
			base := newFakeVectorStore()
			active := NewManifest("project", "main")
			file := MarkdownFile{Path: "a.md", Text: strings.Repeat("Enough chunk text.\n\n", 30), FileDigest: "digest"}
			old, err := (&Indexer{Store: base, Embedder: &fakeEmbedder{}, Manifest: active}).IndexMarkdown(context.Background(), file, IndexOptions{Generation: "g", Collection: "shared", AttemptID: "published", ActivateGeneration: true})
			if err != nil {
				t.Fatal(err)
			}
			if len(old.VectorIDs) == 0 {
				t.Fatal("fixture did not publish old vectors")
			}
			candidate := active.Clone()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			fault := &publicationFaultStore{fakeVectorStore: base, failUpsert: mode == "partial_upsert"}
			if mode == "post_upsert_cancel" {
				fault.cancelAfterUpsert = cancel
			}
			prepared, err := (&Indexer{Store: fault, Embedder: &fakeEmbedder{}, Manifest: candidate}).IndexMarkdown(ctx, file, IndexOptions{Generation: "g", Collection: "shared", AttemptID: "failed", DeferRetirement: true, ChunkStrategy: "sliding", MaxChunkChars: 160})
			if err == nil {
				t.Fatal("failed preparation reported success")
			}
			if mode == "post_upsert_cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("err=%v", err)
			}
			if len(prepared.VectorIDs) < 2 || len(prepared.Chunks) != len(prepared.VectorIDs) {
				t.Fatalf("missing prepared identities: %+v", prepared)
			}
			if !reflect.DeepEqual(candidate.Chunks, active.Chunks) {
				t.Fatal("failed preparation replaced manifest membership")
			}
			if errs := base.DeleteMany("shared", prepared.VectorIDs); len(errs) > 0 {
				t.Fatal(errs)
			}
			for _, id := range old.VectorIDs {
				if _, ok := base.records["shared"][id]; !ok {
					t.Fatal("compensation deleted published ID")
				}
			}
			if len(base.records["shared"]) != len(old.VectorIDs) {
				t.Fatal("prepared IDs did not cover partial effects")
			}
		})
	}
}

func TestWorkspaceDeferredEmptyAndDeletePreserveOldProjection(t *testing.T) {
	for _, text := range []string{"", " \n\t"} {
		active := NewManifest("project", "main")
		store := newFakeVectorStore()
		lexical := newFakeLexicalIndex()
		old, err := (&Indexer{Store: store, Embedder: &fakeEmbedder{}, Manifest: active, Lexical: lexical}).IndexMarkdown(context.Background(), MarkdownFile{Path: "a.md", Text: "A published paragraph.", FileDigest: "old"}, IndexOptions{Generation: "g", Collection: "shared", AttemptID: "a", ActivateGeneration: true, MinChunkChars: 1})
		if err != nil {
			t.Fatal(err)
		}
		if len(old.VectorIDs) == 0 {
			t.Fatal("fixture did not publish old vectors")
		}
		candidate := active.Clone()
		indexer := &Indexer{Store: store, Embedder: &fakeEmbedder{}, Manifest: candidate, Lexical: lexical}
		result, err := indexer.IndexMarkdown(context.Background(), MarkdownFile{Path: "a.md", Text: text, FileDigest: "empty"}, IndexOptions{Generation: "g", Collection: "shared", AttemptID: "b", DeferRetirement: true})
		if err != nil {
			t.Fatal(err)
		}
		if result.ChunksCreated != 0 || len(candidate.Chunks) != 0 {
			t.Fatal("empty candidate still has chunks")
		}
		if candidate.Files["g"]["a.md"] != "empty" {
			t.Fatal("zero-chunk file missing from inventory")
		}
		for _, id := range old.VectorIDs {
			if _, ok := store.records["shared"][id]; !ok {
				t.Fatal("zero-chunk preparation retired vector")
			}
			if _, ok := candidate.PendingRetirements[id]; !ok {
				t.Fatal("zero-chunk retirement not queued")
			}
		}
		if len(lexical.removed) != 0 {
			t.Fatal("zero-chunk preparation retired lexical IDs")
		}
		if _, err := indexer.DeleteMarkdown("a.md", IndexOptions{Generation: "g", Collection: "shared", DeferRetirement: true}); err != nil {
			t.Fatal(err)
		}
		if _, exists := candidate.Files["g"]["a.md"]; exists {
			t.Fatal("zero-chunk delete retained file inventory")
		}
	}
}

func TestWorkspaceAttemptEmptyPreservesLegacyVectorIdentity(t *testing.T) {
	m := NewManifest("project", "main")
	result, err := (&Indexer{Store: newFakeVectorStore(), Embedder: &fakeEmbedder{}, Manifest: m}).IndexMarkdown(context.Background(), MarkdownFile{Path: "a.md", Text: "One paragraph.", FileDigest: "digest"}, IndexOptions{Generation: "g", Collection: "shared", MinChunkChars: 1})
	if err != nil {
		t.Fatal(err)
	}
	want := stableID("vec", "project", "main", "g", "a.md", "digest", "0")
	if !reflect.DeepEqual(result.VectorIDs, []string{want}) {
		t.Fatalf("legacy IDs=%v want %s", result.VectorIDs, want)
	}
	if _, exists := result.Chunks[0].Metadata["attempt_id"]; exists {
		t.Fatal("empty attempt changed legacy metadata")
	}
}

func TestWorkspaceManifestCloneAndInventoryCompatibility(t *testing.T) {
	var legacy Manifest
	if err := json.Unmarshal([]byte(`{"project_id":"project","branch":"main","generations":{},"chunks":{}}`), &legacy); err != nil {
		t.Fatal(err)
	}
	clone := legacy.Clone()
	if _, known := clone.Files["legacy"]; known {
		t.Fatal("legacy unknown inventory invented")
	}
	m := NewManifest("project", "main")
	m.Files["empty"] = map[string]string{}
	m.Files["known"] = map[string]string{"a.md": "digest"}
	m.Chunks["live"] = ChunkRecord{HeadingPath: []string{"Live"}}
	m.PendingRetirements["old"] = ChunkRecord{HeadingPath: []string{"Old"}}
	m.Generations["known"] = Generation{ID: "known", Status: GenerationActive}
	copy := m.Clone()
	copy.Files["known"]["a.md"] = "changed"
	copy.Files["empty"]["new.md"] = "added"
	live := copy.Chunks["live"]
	live.HeadingPath[0] = "Changed"
	retired := copy.PendingRetirements["old"]
	retired.HeadingPath[0] = "Changed"
	copy.Generations["known"] = Generation{Status: GenerationFailed}
	if m.Files["known"]["a.md"] != "digest" || len(m.Files["empty"]) != 0 || m.Chunks["live"].HeadingPath[0] != "Live" || m.PendingRetirements["old"].HeadingPath[0] != "Old" || m.Generations["known"].Status != GenerationActive {
		t.Fatal("clone aliases mutable state")
	}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var restored Manifest
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	files, known := restored.Files["empty"]
	if !known || files == nil || len(files) != 0 {
		t.Fatalf("explicit empty inventory lost: %s", data)
	}
	if _, known := restored.Files["legacy"]; known {
		t.Fatal("unknown legacy inventory became certified")
	}
}

func TestWorkspacePendingRetirementRetryProtectsCurrentAndOtherProject(t *testing.T) {
	m := NewManifest("project", "main")
	old := ChunkRecord{Generation: "g", Path: "a.md", ChunkID: "old", VectorID: "old", Collection: "shared"}
	live := ChunkRecord{Generation: "g", Path: "a.md", ChunkID: "live", VectorID: "live", Collection: "shared"}
	if err := m.UpsertChunk(live); err != nil {
		t.Fatal(err)
	}
	if err := m.ActivateGeneration("g"); err != nil {
		t.Fatal(err)
	}
	m.PendingRetirements["old"] = old
	m.PendingRetirements["live"] = live
	m.Generations["previous"] = Generation{ID: "previous", Status: GenerationGCPending}
	m.Files["previous"] = map[string]string{}
	base := newFakeVectorStore()
	if errs := base.BatchUpsert("shared", []vectorstore.UpsertRecord{{ID: "old"}, {ID: "live"}, {ID: "other-project"}}); len(errs) > 0 {
		t.Fatal(errs)
	}
	store := &publicationFaultStore{fakeVectorStore: base, failDelete: true}
	if err := RetirePendingChunks(m, store); err == nil {
		t.Fatal("partial cleanup reported success")
	}
	if _, pending := m.PendingRetirements["old"]; !pending {
		t.Fatal("failed retirement forgotten")
	}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var restored Manifest
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	store.failDelete = false
	if err := RetirePendingChunks(&restored, store); err != nil {
		t.Fatal(err)
	}
	if _, pending := restored.PendingRetirements["old"]; pending {
		t.Fatal("successful retry still pending")
	}
	if _, pending := restored.PendingRetirements["live"]; !pending {
		t.Fatal("protected collision forgotten")
	}
	for _, id := range []string{"live", "other-project"} {
		if _, ok := base.records["shared"][id]; !ok {
			t.Fatalf("protected ID %s deleted", id)
		}
	}
	if _, exists := restored.Generations["previous"]; !exists {
		t.Fatal("publication cleanup implicitly GCed previous generation")
	}
	if len(base.dropped) > 0 {
		t.Fatal("shared collection dropped")
	}
}

func TestWorkspaceGCCleanupFailureRetainsRetryableGeneration(t *testing.T) {
	m := NewManifest("project", "main")
	if err := m.UpsertChunk(ChunkRecord{Generation: "old", Path: "a.md", ChunkID: "old", VectorID: "old", Collection: "shared"}); err != nil {
		t.Fatal(err)
	}
	if err := m.ActivateGeneration("old"); err != nil {
		t.Fatal(err)
	}
	if err := m.UpsertChunk(ChunkRecord{Generation: "new", Path: "a.md", ChunkID: "live", VectorID: "live", Collection: "shared"}); err != nil {
		t.Fatal(err)
	}
	if err := m.ActivateGeneration("new"); err != nil {
		t.Fatal(err)
	}
	m.Files["old"] = map[string]string{"a.md": "old"}
	base := newFakeVectorStore()
	base.BatchUpsert("shared", []vectorstore.UpsertRecord{{ID: "old"}, {ID: "live"}, {ID: "other"}})
	store := &publicationFaultStore{fakeVectorStore: base, failDelete: true}
	if _, err := GCGenerations(m, store); err == nil {
		t.Fatal("failed GC reported success")
	}
	if _, pending := m.PendingRetirements["old"]; !pending {
		t.Fatal("failed GC lost pending identity")
	}
	if _, exists := m.Chunks["old"]; !exists {
		t.Fatal("failed GC lost retry chunk")
	}
	if _, exists := m.Generations["old"]; !exists {
		t.Fatal("failed GC lost generation")
	}
	store.failDelete = false
	result, err := GCGenerations(m, store)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.DeletedVectorIDs, []string{"old"}) || !reflect.DeepEqual(result.Generations, []string{"old"}) {
		t.Fatalf("retry=%+v", result)
	}
	if _, exists := m.Files["old"]; exists {
		t.Fatal("retired inventory retained")
	}
	for _, id := range []string{"live", "other"} {
		if _, ok := base.records["shared"][id]; !ok {
			t.Fatalf("GC deleted protected %s", id)
		}
	}
}

func TestWorkspaceDeferredDeleteQueuesNonemptyFile(t *testing.T) {
	manifest := NewManifest("project", "main")
	store := newFakeVectorStore()
	lexical := newFakeLexicalIndex()
	indexer := &Indexer{Store: store, Embedder: &fakeEmbedder{}, Manifest: manifest, Lexical: lexical}
	initial, err := indexer.IndexMarkdown(context.Background(), MarkdownFile{Path: "a.md", Text: "Published file content.", FileDigest: "digest"}, IndexOptions{Generation: "g", Collection: "shared", AttemptID: "a", MinChunkChars: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(initial.VectorIDs) == 0 {
		t.Fatal("fixture did not publish initial vectors")
	}
	indexer.Manifest = manifest.Clone()
	deleted, err := indexer.DeleteMarkdown("a.md", IndexOptions{Generation: "g", Collection: "shared", DeferRetirement: true})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(deleted, initial.VectorIDs) {
		t.Fatalf("delete identities=%v", deleted)
	}
	if len(indexer.Manifest.Chunks) != 0 || len(indexer.Manifest.PendingRetirements) != len(initial.VectorIDs) {
		t.Fatal("delete candidate did not queue exact retirements")
	}
	if len(lexical.removed) != 0 || len(store.deleted) != 0 {
		t.Fatal("deferred delete changed published projection")
	}
	if len(manifest.Chunks) != len(initial.VectorIDs) {
		t.Fatal("deferred delete mutated original manifest")
	}
}

func TestWorkspacePendingRetirementUnknownCollectionFailsClosed(t *testing.T) {
	manifest := NewManifest("project", "main")
	manifest.PendingRetirements["old"] = ChunkRecord{VectorID: "old", Generation: "g"}
	store := newFakeVectorStore()
	store.BatchUpsert("unrecorded", []vectorstore.UpsertRecord{{ID: "old"}})
	if err := RetirePendingChunks(manifest, store); err == nil {
		t.Fatal("unknown collection was reported cleaned")
	}
	if len(manifest.PendingRetirements) != 1 || len(store.deleted) != 0 || len(store.dropped) != 0 {
		t.Fatal("unresolved identity changed store or pending state")
	}
}
