package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/pkg/bm25"
)

func TestT10SearchTagsFailClosedAndMatchAny(t *testing.T) {
	rows := []fiber.Map{
		{"id": "missing"},
		{"id": "malformed", "metadata": json.RawMessage(`{`)},
		{"id": "wrong-type", "metadata": map[string]any{"tags": "wanted"}},
		{"id": "any", "metadata": json.RawMessage(`{"tags":["other","WANTED"]}`)},
		{"id": "typed", "metadata": map[string]any{"tags": []string{"wanted"}}},
		{"id": "key", "metadata": json.RawMessage(`{"key":"wanted"}`)},
	}
	if got := filterByTags(rows, []string{"absent"}); len(got) != 0 {
		t.Fatalf("zero matches returned unfiltered rows: %v", got)
	}
	if got := filterByTags(rows, []string{"absent", "wanted"}); len(got) != 3 {
		t.Fatalf("tags must use case-insensitive ANY semantics: %v", got)
	}
	if got := filterByTags(rows, nil); len(got) != len(rows) {
		t.Fatal("absent filter changed results")
	}
}

func TestT10SearchTagsBeforeSourceTextEgress(t *testing.T) {
	for _, strategy := range []string{"CHUNKS", "HYBRID", "RAG_COMPLETION"} {
		for _, tag := range []string{"wanted", "absent"} {
			t.Run(strategy+"/"+tag, func(t *testing.T) {
				env := newSearchTestEnv(t)
				env.cfg.DB = nil
				const wanted = "wanted source text sufficiently long for generation"
				const other = "unrequested source text must never reach the provider"
				for id, text := range map[string]string{"wanted": wanted, "other": other} {
					env.insertVector("entities", id, []float32{1, 0, 0, 0}, map[string]any{"text": text, "tags": []string{id}})
				}
				var mu sync.Mutex
				var texts []string
				reranker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var request struct {
						Documents []string `json:"documents"`
					}
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Error(err)
					}
					mu.Lock()
					texts = append(texts, request.Documents...)
					mu.Unlock()
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"results":[{"index":0,"relevance_score":1}]}`))
				}))
				t.Cleanup(reranker.Close)
				env.cfg.RerankEndpoint = reranker.URL
				env.cfg.RerankModel = "fixture"
				model := &recordingLLM{responses: []string{"answer"}}
				env.cfg.LLMProvider = model
				t.Setenv("LLM_ENDPOINT", "http://unused.test")
				t.Setenv("LLM_MODEL", "fixture")
				t.Setenv("LEVARA_RAG_ABSTAIN_THRESHOLD_RAG_COMPLETION", "0")
				env.start()
				status, result := postSearchAny(t, env, map[string]any{
					"query_text": "source", "query_type": strategy, "collection": "entities", "top_k": 1,
					"tags": []string{tag}, "rerank": true,
				})
				if status != 200 {
					t.Fatalf("HTTP %d: %v", status, result)
				}
				mu.Lock()
				observed := append([]string(nil), texts...)
				mu.Unlock()
				if strategy == "RAG_COMPLETION" {
					observed = model.promptsSnapshot()
				}
				if tag == "absent" {
					if len(observed) != 0 {
						t.Fatalf("zero matching rows reached provider: %v", observed)
					}
				} else {
					if len(observed) == 0 {
						t.Fatal("matching text never reached provider")
					}
					for _, text := range observed {
						if strings.Contains(text, other) || !strings.Contains(text, wanted) {
							t.Fatalf("provider received wrong source: %s", text)
						}
					}
				}
			})
		}
	}
}

func TestT10SearchLexicalHonorsDomainAndTags(t *testing.T) {
	env := newSearchTestEnv(t)
	env.cfg.DB = nil
	env.cfg.BM25Indexes = bm25.NewIndexRegistry()
	for _, collection := range []string{"medical", "legal"} {
		env.insertVector(collection, collection, []float32{1, 0, 0, 0}, map[string]any{"text": "source"})
		env.cm.SetDomain(collection, collection)
		idx := env.cfg.BM25Indexes.GetOrCreate(collection, nil)
		idx.Add(collection+"-wrong", "source source source", `{"tags":["wrong"]}`)
		idx.Add(collection+"-wanted", "source", `{"tags":["wanted"]}`)
	}
	env.start()
	status, result := postSearchAny(t, env, map[string]any{
		"query_text": "source", "query_type": "CHUNKS_LEXICAL", "domain": "medical", "tags": []string{"wanted"}, "top_k": 1,
	})
	rows, ok := result.([]any)
	if status != 200 || !ok || len(rows) != 1 || rows[0].(map[string]any)["id"] != "medical-wanted" {
		t.Fatalf("domain/tags must apply before top_k: HTTP %d %v", status, result)
	}
}

func TestT10SearchMissingEmbedClientDoesNotAdvertiseOrPanic(t *testing.T) {
	for _, strategy := range []string{"CHUNKS", "HYBRID", "RAG_COMPLETION", "SUMMARIES", "AUTO"} {
		t.Run(strategy, func(t *testing.T) {
			env := newSearchTestEnv(t)
			env.insertVector("entities", "source", []float32{1, 0, 0, 0}, map[string]any{"text": "source"})
			env.cfg.EmbedClient = nil
			if capabilitiesFromConfig(env.cfg, "").HasEmbedding {
				t.Error("nil embedding client advertised")
			}
			env.start()
			status, result := postSearchAny(t, env, map[string]any{"query_text": "source", "query_type": strategy})
			if status != 200 {
				t.Fatalf("HTTP %d: %v", status, result)
			}
		})
	}
}
