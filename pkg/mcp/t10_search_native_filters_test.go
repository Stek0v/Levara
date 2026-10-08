package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stek0v/levara/internal/store"
	"github.com/stek0v/levara/pipeline"
	"github.com/stek0v/levara/pkg/embed"
)

type t10NativeSearchAdapter struct{ *pipeline.SearchPipeline }

func (p t10NativeSearchAdapter) ApplyRerank(_ context.Context, _ string, in []pipeline.ScoredResult, _ int) (bool, []pipeline.ScoredResult) {
	return false, in
}
func (p t10NativeSearchAdapter) RerankEnabled() bool { return false }

func TestT10MetadataBeforeNativeSpecializedCaps(t *testing.T) {
	for _, tc := range []struct {
		name, typ string
		parents   bool
	}{{"parent", "PARENT_CHILD", true}, {"multi", "MULTI_QUERY", true}, {"children-without-parent-id", "PARENT_CHILD", false}} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": [][]float32{{1, 0, 0}}})
			}))
			defer srv.Close()
			cm, err := store.NewCollectionManager(3, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer cm.Close()
			for _, coll := range []string{"kb", "kb_child"} {
				if err := cm.Create(coll); err != nil {
					t.Fatal(err)
				}
			}
			insert := func(id, room string, v []float32) {
				t.Helper()
				if err := cm.Insert("kb", id, v, map[string]any{"text": id, "room": room, "tags": []string{"b"}}); err != nil {
					t.Fatal(err)
				}
				metadata := map[string]any{"parent_id": id}
				if !tc.parents {
					metadata = map[string]any{"text": id, "room": room, "tags": []string{"b"}}
				}
				if err := cm.Insert("kb_child", "child-"+id, v, metadata); err != nil {
					t.Fatal(err)
				}
			}
			insert("revoked", "wanted", []float32{1, 0, 0})
			for i := 1; i <= 4; i++ {
				insert(fmt.Sprintf("other-%d", i), "other", []float32{1, float32(i) / 10, 0})
			}
			insert("wanted", "wanted", []float32{1, .8, 0})
			sp := pipeline.NewSearchPipeline(embed.NewClient(srv.URL, "encoder", 16, 1), cm, nil).WithResultFilter(func(_ context.Context, in []pipeline.ScoredResult) ([]pipeline.ScoredResult, error) {
				out := in[:0]
				for _, r := range in {
					if r.ID != "revoked" && r.ID != "child-revoked" {
						out = append(out, r)
					}
				}
				return out, nil
			})
			deps := &fakeDeps{collections: []string{"kb"}, searchPipelineFn: func(bool) SearchPipeline { return t10NativeSearchAdapter{sp} }, llmProvider: &stubDistillProvider{content: `[]`}}
			body := decodeSearchResp(t, ToolSearch(context.Background(), deps, map[string]any{"search_query": "q", "search_type": tc.typ, "room": "wanted", "tags": []any{"a", "b"}, "top_k": 1}))
			rows := body["results"].([]any)
			want := "wanted"
			if !tc.parents {
				want = "child-wanted"
			}
			if len(rows) != 1 || rows[0].(map[string]any)["id"] != want {
				t.Fatalf("results=%v", rows)
			}
		})
	}
}
