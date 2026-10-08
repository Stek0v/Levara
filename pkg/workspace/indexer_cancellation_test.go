package workspace

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type cancelingEmbedder struct {
	fakeEmbedder
	cancel context.CancelFunc
}

func (e *cancelingEmbedder) EmbedTexts(ctx context.Context, texts []string) ([][]float32, error) {
	e.cancel()
	return e.fakeEmbedder.EmbedTexts(ctx, texts)
}

func TestIndexerCanceledProviderResultDoesNotPublish(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	embedder := &cancelingEmbedder{cancel: cancel}
	store := newFakeVectorStore()
	manifest := NewManifest("payments", "main")
	indexer := &Indexer{Store: store, Embedder: embedder, Manifest: manifest}
	_, err := indexer.IndexMarkdown(ctx, MarkdownFile{Path: "note.md", Text: "# Note\n\nSuccessful late embedding.", FileDigest: "sha256:new"}, IndexOptions{Generation: "g1", Collection: "kb", ChunkStrategy: "paragraph", MinChunkChars: 1, ActivateGeneration: true})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v, want canceled", err)
	}
	if len(embedder.texts) == 0 {
		t.Fatal("embedder was never reached")
	}
	if len(store.records) != 0 || len(manifest.Chunks) != 0 || manifest.ActiveGeneration != "" {
		t.Fatal("canceled provider result published")
	}
}

func TestIndexerCanceledEmptyFilePreservesExistingVectors(t *testing.T) {
	store := newFakeVectorStore()
	manifest := NewManifest("payments", "main")
	indexer := &Indexer{Store: store, Embedder: &fakeEmbedder{}, Manifest: manifest}
	opts := IndexOptions{Generation: "g1", Collection: "kb", ChunkStrategy: "paragraph", MinChunkChars: 1, ActivateGeneration: true}
	file := MarkdownFile{Path: "note.md", Text: "# Note\n\nPreviously published embedding.", FileDigest: "sha256:old"}
	if _, err := indexer.IndexMarkdown(context.Background(), file, opts); err != nil {
		t.Fatal(err)
	}
	filter := ChunkFilter{ProjectID: "payments", Branch: "main", Generation: "g1", Path: "note.md", Collection: "kb"}
	before := manifest.VectorIDs(filter)
	if len(before) == 0 {
		t.Fatal("seed index did not publish vectors")
	}
	count := len(store.records["kb"])
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	file.Text = ""
	if _, err := indexer.IndexMarkdown(ctx, file, opts); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v, want canceled", err)
	}
	if !reflect.DeepEqual(before, manifest.VectorIDs(filter)) || len(store.records["kb"]) != count || len(store.deleted) != 0 || manifest.ActiveGeneration != "g1" {
		t.Fatal("cancelled empty-file index removed previous truth")
	}
}
