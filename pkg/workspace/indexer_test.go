package workspace

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/stek0v/levara/pkg/vectorstore"
)

type fakeEmbedder struct {
	texts []string
	err   error
}

func (f *fakeEmbedder) EmbedTexts(_ context.Context, texts []string) ([][]float32, error) {
	f.texts = append([]string(nil), texts...)
	if f.err != nil {
		return nil, f.err
	}
	vecs := make([][]float32, len(texts))
	for i, text := range texts {
		vecs[i] = []float32{float32(len(text)), float32(i + 1)}
	}
	return vecs, nil
}

type fakeVectorStore struct {
	records map[string]map[string]vectorstore.UpsertRecord
	deleted []string
	dropped []string
}

func newFakeVectorStore() *fakeVectorStore {
	return &fakeVectorStore{records: make(map[string]map[string]vectorstore.UpsertRecord)}
}

func (f *fakeVectorStore) Insert(collection, id string, vector []float32, metadata interface{}) error {
	errs := f.BatchUpsert(collection, []vectorstore.UpsertRecord{{ID: id, Vector: vector, Metadata: metadata}})
	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

func (f *fakeVectorStore) BatchUpsert(collection string, records []vectorstore.UpsertRecord) []error {
	if f.records[collection] == nil {
		f.records[collection] = make(map[string]vectorstore.UpsertRecord)
	}
	for _, r := range records {
		f.records[collection][r.ID] = r
	}
	return nil
}

func (f *fakeVectorStore) Search(string, []float32, int) ([]vectorstore.SearchResult, error) {
	return nil, nil
}

func (f *fakeVectorStore) Get(collection, id string) (vectorstore.StoredRecord, bool, error) {
	rec, ok := f.records[collection][id]
	if !ok {
		return vectorstore.StoredRecord{}, false, nil
	}
	return vectorstore.StoredRecord{ID: id, Vector: rec.Vector}, true, nil
}

func (f *fakeVectorStore) Scan(collection string) ([]vectorstore.StoredRecord, error) {
	var out []vectorstore.StoredRecord
	for id, rec := range f.records[collection] {
		out = append(out, vectorstore.StoredRecord{ID: id, Vector: rec.Vector})
	}
	return out, nil
}

func (f *fakeVectorStore) Delete(collection, id string) error {
	delete(f.records[collection], id)
	f.deleted = append(f.deleted, id)
	return nil
}

func (f *fakeVectorStore) DeleteMany(collection string, ids []string) []error {
	for _, id := range ids {
		delete(f.records[collection], id)
		f.deleted = append(f.deleted, id)
	}
	return nil
}

func (f *fakeVectorStore) DeleteByFilter(string, vectorstore.MetadataFilter) ([]string, []error) {
	return nil, nil
}

func (f *fakeVectorStore) Has(collection string) bool { return f.records[collection] != nil }
func (f *fakeVectorStore) Create(collection string) error {
	if f.records[collection] == nil {
		f.records[collection] = make(map[string]vectorstore.UpsertRecord)
	}
	return nil
}
func (f *fakeVectorStore) Drop(collection string) error {
	delete(f.records, collection)
	f.dropped = append(f.dropped, collection)
	return nil
}
func (f *fakeVectorStore) List() []string { return nil }
func (f *fakeVectorStore) Count(collection string) int {
	return len(f.records[collection])
}
func (f *fakeVectorStore) Metadata(collection string) (vectorstore.CollectionMeta, bool) {
	return vectorstore.CollectionMeta{Name: collection, RecordCount: len(f.records[collection])}, true
}
func (f *fakeVectorStore) Checkpoint() error { return nil }
func (f *fakeVectorStore) Close() error      { return nil }

type fakeLexicalIndex struct {
	added   map[string]string
	removed []string
}

func newFakeLexicalIndex() *fakeLexicalIndex {
	return &fakeLexicalIndex{added: make(map[string]string)}
}

func (f *fakeLexicalIndex) Add(id, text, metadata string) {
	f.added[id] = text + "\n" + metadata
}

func (f *fakeLexicalIndex) Remove(id string) {
	delete(f.added, id)
	f.removed = append(f.removed, id)
}

func TestIndexerIndexMarkdownCreatesManifestAndVectors(t *testing.T) {
	manifest := NewManifest("payments", "main")
	store := newFakeVectorStore()
	embedder := &fakeEmbedder{}
	indexer := &Indexer{Store: store, Embedder: embedder, Manifest: manifest}

	file := MarkdownFile{
		Path:       "docs/adr/payment-timeout.md",
		Text:       "# Payment Timeout\n\nIntro paragraph about payment latency.\n\n## Mitigation\n\nRetry with bounded timeout and record incident notes.",
		FileDigest: "sha256:abc",
		Room:       "payments",
		Tags:       []string{"incident", "latency"},
	}
	result, err := indexer.IndexMarkdown(context.Background(), file, IndexOptions{
		Generation:         "gen-1",
		Collection:         "kb_payments_main_gen1",
		ChunkStrategy:      "paragraph",
		MinChunkChars:      1,
		ActivateGeneration: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ChunksCreated == 0 {
		t.Fatal("expected chunks")
	}
	if manifest.ActiveGeneration != "gen-1" {
		t.Fatalf("ActiveGeneration=%q, want gen-1", manifest.ActiveGeneration)
	}
	if len(embedder.texts) != result.ChunksCreated {
		t.Fatalf("embedded texts=%d, chunks=%d", len(embedder.texts), result.ChunksCreated)
	}
	if len(store.records["kb_payments_main_gen1"]) != result.ChunksCreated {
		t.Fatalf("store records=%d, chunks=%d", len(store.records["kb_payments_main_gen1"]), result.ChunksCreated)
	}

	activeIDs := manifest.VectorIDs(ChunkFilter{ActiveOnly: true})
	if !reflect.DeepEqual(activeIDs, sortedCopy(result.VectorIDs)) {
		t.Fatalf("active IDs=%v, result IDs=%v", activeIDs, result.VectorIDs)
	}

	var mitigationFound bool
	for _, rec := range store.records["kb_payments_main_gen1"] {
		meta := rec.Metadata.(map[string]any)
		text, _ := meta["text"].(string)
		if strings.Contains(text, "Retry with bounded timeout") {
			mitigationFound = true
			path := meta["heading_path"].([]string)
			if !reflect.DeepEqual(path, []string{"Payment Timeout", "Mitigation"}) {
				t.Fatalf("heading_path=%v, want Payment Timeout > Mitigation", path)
			}
			if meta["room"] != "payments" {
				t.Fatalf("room=%v, want payments", meta["room"])
			}
		}
	}
	if !mitigationFound {
		t.Fatal("mitigation chunk metadata not found")
	}
}

func TestIndexerKeepsLexicalIndexInSync(t *testing.T) {
	manifest := NewManifest("payments", "main")
	store := newFakeVectorStore()
	lexical := newFakeLexicalIndex()
	indexer := &Indexer{Store: store, Embedder: &fakeEmbedder{}, Manifest: manifest, Lexical: lexical}
	opts := IndexOptions{Generation: "gen-1", Collection: "kb", ChunkStrategy: "paragraph", MinChunkChars: 1}

	first, err := indexer.IndexMarkdown(context.Background(), MarkdownFile{
		Path:       "docs/a.md",
		Text:       "# A\n\nOld bounded timeout content.",
		FileDigest: "sha256:old",
	}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(lexical.added) != len(first.VectorIDs) {
		t.Fatalf("lexical added=%d, want %d", len(lexical.added), len(first.VectorIDs))
	}
	var oldContentIndexed bool
	for _, indexed := range lexical.added {
		if strings.Contains(indexed, "Old bounded timeout") {
			oldContentIndexed = true
			break
		}
	}
	if !oldContentIndexed {
		t.Fatalf("lexical index missing original body content: %+v", lexical.added)
	}

	second, err := indexer.IndexMarkdown(context.Background(), MarkdownFile{
		Path:       "docs/a.md",
		Text:       "# A\n\nNew reconciliation content.",
		FileDigest: "sha256:new",
	}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sortedCopy(lexical.removed), sortedCopy(first.VectorIDs)) {
		t.Fatalf("lexical removed=%v, want %v", lexical.removed, first.VectorIDs)
	}
	var newContentIndexed bool
	for _, id := range second.VectorIDs {
		if strings.Contains(lexical.added[id], "New reconciliation") {
			newContentIndexed = true
			break
		}
	}
	if !newContentIndexed {
		t.Fatalf("lexical index missing new body content: %+v", lexical.added)
	}
}

func TestIndexerReindexSamePathDeletesOnlyStaleVectorIDs(t *testing.T) {
	manifest := NewManifest("payments", "main")
	store := newFakeVectorStore()
	indexer := &Indexer{Store: store, Embedder: &fakeEmbedder{}, Manifest: manifest}
	opts := IndexOptions{Generation: "gen-1", Collection: "kb", ChunkStrategy: "paragraph", MinChunkChars: 1}

	first, err := indexer.IndexMarkdown(context.Background(), MarkdownFile{
		Path:       "docs/a.md",
		Text:       "# A\n\nOld content.",
		FileDigest: "sha256:old",
	}, opts)
	if err != nil {
		t.Fatal(err)
	}
	second, err := indexer.IndexMarkdown(context.Background(), MarkdownFile{
		Path:       "docs/a.md",
		Text:       "# A\n\nNew content.\n\nMore detail.",
		FileDigest: "sha256:new",
	}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.DeletedVectorIDs) != len(first.VectorIDs) {
		t.Fatalf("deleted=%v, want old IDs %v", second.DeletedVectorIDs, first.VectorIDs)
	}
	for _, oldID := range first.VectorIDs {
		if _, ok := store.records["kb"][oldID]; ok {
			t.Fatalf("old vector %q still in store", oldID)
		}
	}
	manifestIDs := manifest.VectorIDs(ChunkFilter{Path: "docs/a.md", Generation: "gen-1"})
	if !reflect.DeepEqual(manifestIDs, sortedCopy(second.VectorIDs)) {
		t.Fatalf("manifest IDs=%v, want second IDs=%v", manifestIDs, second.VectorIDs)
	}
}

func TestIndexerReindexEmbedFailureKeepsOldManifestAndVectors(t *testing.T) {
	manifest := NewManifest("payments", "main")
	store := newFakeVectorStore()
	indexer := &Indexer{Store: store, Embedder: &fakeEmbedder{}, Manifest: manifest}
	opts := IndexOptions{Generation: "gen-1", Collection: "kb", ChunkStrategy: "paragraph", MinChunkChars: 1}

	first, err := indexer.IndexMarkdown(context.Background(), MarkdownFile{
		Path:       "docs/a.md",
		Text:       "# A\n\nOld content.",
		FileDigest: "sha256:old",
	}, opts)
	if err != nil {
		t.Fatal(err)
	}

	indexer.Embedder = &fakeEmbedder{err: errors.New("embed down")}
	if _, err := indexer.IndexMarkdown(context.Background(), MarkdownFile{
		Path:       "docs/a.md",
		Text:       "# A\n\nNew content.",
		FileDigest: "sha256:new",
	}, opts); err == nil {
		t.Fatal("expected embed error")
	}
	if len(store.deleted) != 0 {
		t.Fatalf("deleted=%v, want no deletes before successful upsert", store.deleted)
	}
	manifestIDs := manifest.VectorIDs(ChunkFilter{Path: "docs/a.md", Generation: "gen-1"})
	if !reflect.DeepEqual(manifestIDs, sortedCopy(first.VectorIDs)) {
		t.Fatalf("manifest changed after failed reindex: got %v want %v", manifestIDs, first.VectorIDs)
	}
	if manifest.Generations["gen-1"].Status != GenerationFailed {
		t.Fatalf("generation status=%q, want failed", manifest.Generations["gen-1"].Status)
	}
}

func TestIndexerDeleteMarkdownUsesManifestExactVectorIDs(t *testing.T) {
	manifest := NewManifest("payments", "main")
	store := newFakeVectorStore()
	indexer := &Indexer{Store: store, Embedder: &fakeEmbedder{}, Manifest: manifest}
	opts := IndexOptions{Generation: "gen-1", Collection: "kb", ChunkStrategy: "paragraph", MinChunkChars: 1}

	result, err := indexer.IndexMarkdown(context.Background(), MarkdownFile{
		Path:       "docs/delete-me.md",
		Text:       "# Delete\n\nTemporary note.",
		FileDigest: "sha256:tmp",
	}, opts)
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := indexer.DeleteMarkdown("docs/delete-me.md", opts)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sortedCopy(deleted), sortedCopy(result.VectorIDs)) {
		t.Fatalf("deleted=%v, want %v", deleted, result.VectorIDs)
	}
	if got := manifest.VectorIDs(ChunkFilter{Path: "docs/delete-me.md"}); len(got) != 0 {
		t.Fatalf("manifest still has IDs: %v", got)
	}
	if got := store.Count("kb"); got != 0 {
		t.Fatalf("store count=%d, want 0", got)
	}
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	for i := 1; i < len(out); i++ {
		key := out[i]
		j := i - 1
		for j >= 0 && out[j] > key {
			out[j+1] = out[j]
			j--
		}
		out[j+1] = key
	}
	return out
}
