package orchestrator

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stek0v/levara/internal/store"
	"github.com/stek0v/levara/pkg/bm25"
)

func TestDocumentACLChunkIdentity(t *testing.T) {
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		data := []any{}
		for i := range req.Input {
			data = append(data, map[string]any{"index": i, "embedding": []float32{1, 0}})
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer endpoint.Close()
	for _, tc := range []struct {
		name, document, dataset string
		parentChild             bool
	}{
		{"scoped", "doc", "a", false}, {"parents", "doc", "a", true}, {"legacy-document", "doc", "", false}, {"ephemeral", "", "a", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cm, err := store.NewCollectionManager(2, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer cm.Close()
			indexes := bm25.NewIndexRegistry()
			cfg := Config{Collection: "docs", Collections: cm, BM25Indexes: indexes, EmbedEndpoint: endpoint.URL, SkipGraph: true, DocumentID: tc.document, DatasetID: tc.dataset, ParentChild: tc.parentChild, ParentMaxChars: 1000, ChildMaxChars: 500}
			run := func() {
				t.Helper()
				updates := make(chan Progress, 100)
				if err := Run(context.Background(), []string{"A document shared between teams retains a stable source identity and independent dataset permissions."}, cfg, updates); err != nil {
					t.Fatal(err)
				}
			}
			run()
			collection := "docs"
			if tc.parentChild {
				collection += "_child"
			}
			idx := indexes.Get(collection)
			if idx == nil {
				t.Fatal("no chunks indexed")
			}
			initial := idx.Size()
			if initial == 0 {
				t.Fatal("empty chunk index")
			}
			if tc.dataset != "" {
				cfg.DatasetID = "b"
			}
			run()
			want := initial * 2
			if tc.name == "legacy-document" {
				want = initial
			}
			if idx.Size() != want {
				t.Fatalf("chunks=%d want=%d", idx.Size(), want)
			}
			if tc.parentChild {
				parents := map[string]string{}
				for _, doc := range idx.Documents() {
					// tags is an array, so decode only fields required by the contract.
					var fields struct {
						Dataset  string `json:"dataset_id"`
						Document string `json:"document_id"`
						Parent   string `json:"parent_id"`
					}
					if err := json.Unmarshal([]byte(doc.Metadata), &fields); err != nil {
						t.Fatal(err)
					}
					if fields.Document != tc.document || fields.Parent == "" {
						t.Fatalf("lost provenance: %+v", fields)
					}
					parents[fields.Dataset] = fields.Parent
				}
				if parents["a"] == parents["b"] {
					t.Fatal("datasets share a parent chunk ID")
				}
				hits, err := cm.Search("docs", []float32{1, 0}, 10)
				if err != nil || len(hits) != 2 {
					t.Fatalf("parent records=%d err=%v", len(hits), err)
				}
			}
		})
	}
}
