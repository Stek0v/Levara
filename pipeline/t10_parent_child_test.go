package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stek0v/levara/internal/store"
	"github.com/stek0v/levara/pkg/embed"
	"github.com/stek0v/levara/pkg/llm"
)

func TestT10ParentChildExactResolutionAndRanking(t *testing.T) {
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
	insert := func(coll, id string, v []float32, m map[string]any) {
		t.Helper()
		if err := cm.Insert(coll, id, v, m); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 15; i++ {
		insert("kb", fmt.Sprintf("distractor-%d", i), []float32{1, 0, 0}, map[string]any{"text": "distractor"})
	}
	insert("kb", "first", []float32{-1, 0, 0}, map[string]any{"text": "parent first"})
	insert("kb", "second", []float32{0, 1, 0}, map[string]any{"text": "parent second"})
	insert("kb_child", "missing", []float32{1, 0, 0}, map[string]any{"parent_id": "missing"})
	insert("kb_child", "a", []float32{1, .1, 0}, map[string]any{"parent_id": "first"})
	insert("kb_child", "a-duplicate", []float32{1, .2, 0}, map[string]any{"parent_id": "first"})
	insert("kb_child", "b", []float32{1, .4, 0}, map[string]any{"parent_id": "second"})
	p := NewSearchPipeline(embed.NewClient(srv.URL, "encoder", 16, 1), cm, nil)
	rows, err := p.SearchByTextParentChild(context.Background(), "kb", "q", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ID != "first" || rows[1].ID != "second" || rows[0].Score <= rows[1].Score || rows[1].Score <= 0 {
		t.Fatalf("parents=%+v", rows)
	}
	p = p.WithResultFilter(func(_ context.Context, in []ScoredResult) ([]ScoredResult, error) {
		out := in[:0]
		for _, r := range in {
			if r.ID != "first" {
				out = append(out, r)
			}
		}
		return out, nil
	})
	rows, err = p.SearchByTextParentChild(context.Background(), "kb", "q", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != "second" {
		t.Fatalf("revoked parent results=%+v", rows)
	}
}

type t10VariantProvider struct{}

func (t10VariantProvider) Name() string { return "test" }
func (t10VariantProvider) ChatCompletion(context.Context, llm.CompletionRequest) (*llm.CompletionResponse, error) {
	return &llm.CompletionResponse{Content: `["variant","tail"]`}, nil
}

func TestT10MultiQueryDiscardsPartialOnTerminalError(t *testing.T) {
	for _, failure := range []string{"cancel", "embed-guard", "result-filter", "provider"} {
		t.Run(failure, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var httpCalls, guardCalls, filterCalls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := httpCalls.Add(1)
				if failure == "provider" && n == 2 {
					http.Error(w, "unavailable", http.StatusServiceUnavailable)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": [][]float32{{1, 0, 0}}})
			}))
			defer srv.Close()
			cm, err := store.NewCollectionManager(3, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer cm.Close()
			if err := cm.Create("kb"); err != nil {
				t.Fatal(err)
			}
			if err := cm.Insert("kb", "answer", []float32{1, 0, 0}, map[string]any{"text": "answer"}); err != nil {
				t.Fatal(err)
			}
			client := embed.NewClient(srv.URL, "encoder", 16, 1).WithGuard(func(context.Context) (func(), error) {
				n := guardCalls.Add(1)
				if n == 2 {
					if failure == "cancel" {
						cancel()
						return nil, ctx.Err()
					}
					if failure == "embed-guard" {
						return nil, errors.New("revoked")
					}
				}
				return func() {}, nil
			})
			p := NewSearchPipeline(client, cm, nil).WithResultFilter(func(_ context.Context, in []ScoredResult) ([]ScoredResult, error) {
				if filterCalls.Add(1) == 2 && failure == "result-filter" {
					return nil, errors.New("policy unavailable")
				}
				return in, nil
			})
			rows, err := p.SearchByTextMultiQuery(ctx, "kb", "original", 2, t10VariantProvider{}, "test", 2)
			if failure == "provider" {
				if err != nil || len(rows) != 1 || rows[0].ID != "answer" || httpCalls.Load() != 3 {
					t.Fatalf("provider fallback rows=%v error=%v HTTPcalls=%d", rows, err, httpCalls.Load())
				}
				return
			}
			want := embed.ErrGuardRejected
			wantCalls := int32(1)
			if failure == "cancel" {
				want = context.Canceled
			}
			if failure == "result-filter" {
				want = ErrResultFilterRejected
				wantCalls = 2
			}
			if len(rows) != 0 || !errors.Is(err, want) || httpCalls.Load() != wantCalls || guardCalls.Load() != 2 {
				t.Fatalf("partial rows=%v error=%v HTTPcalls=%d guardcalls=%d", rows, err, httpCalls.Load(), guardCalls.Load())
			}
		})
	}
}
