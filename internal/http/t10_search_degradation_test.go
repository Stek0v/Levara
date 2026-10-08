package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stek0v/levara/internal/metrics"
	"github.com/stek0v/levara/pkg/bm25"
	"github.com/stek0v/levara/pkg/embcontract"
	"github.com/stek0v/levara/pkg/embed"
	"github.com/stek0v/levara/pkg/rerank"
)

func TestT10SearchHybridRetainsSurvivingLeg(t *testing.T) {
	for _, scenario := range []string{"provider-failure", "encoder-mismatch", "missing-client", "missing-lexical", "both-unavailable", "cancelled", "guard-denied", "opaque-guard-denied"} {
		t.Run(scenario, func(t *testing.T) {
			env := newSearchTestEnv(t)
			env.cfg.DB = nil
			env.insertVector("entities", "vector", []float32{1, 0, 0, 0}, map[string]any{"text": "source"})
			env.cfg.BM25Indexes = bm25.NewIndexRegistry()
			if scenario != "missing-lexical" && scenario != "both-unavailable" {
				env.cfg.BM25Indexes.GetOrCreate("entities", nil).Add("lexical", "source", `{"text":"source"}`)
			}
			if scenario == "provider-failure" || scenario == "both-unavailable" {
				failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
				t.Cleanup(failing.Close)
				env.cfg.EmbedClient = embed.NewClient(failing.URL, "test-model", 1, 1)
			}
			if scenario == "missing-client" {
				env.cfg.EmbedClient = nil
			}
			if scenario == "encoder-mismatch" {
				if err := env.cm.UpdateEmbeddingContract("entities", embcontract.FromEnv("other-model", 4, "cosine")); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "guard-denied" || scenario == "opaque-guard-denied" {
				env.cfg.EmbedClient = env.cfg.EmbedClient.WithGuard(func(context.Context) (func(), error) {
					if scenario == "opaque-guard-denied" {
						return nil, errors.New("fixture authority unavailable")
					}
					return nil, fiber.NewError(403, "fixture access revoked")
				})
			}
			env.app = fiber.New(fiber.Config{DisableStartupMessage: true, ErrorHandler: func(c *fiber.Ctx, err error) error {
				code := 500
				var httpErr *fiber.Error
				if errors.As(err, &httpErr) {
					code = httpErr.Code
				}
				return c.Status(code).JSON(fiber.Map{"error": err.Error()})
			}})
			if scenario == "cancelled" {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				env.app.Use(func(c *fiber.Ctx) error { c.SetUserContext(ctx); return c.Next() })
			}
			env.app.Post("/search/text", searchHandler(env.cfg))
			status, result := postSearchAny(t, env, map[string]any{"query_text": "source", "query_type": "HYBRID", "collection": "entities", "top_k": 2})
			if scenario == "cancelled" || scenario == "guard-denied" || scenario == "opaque-guard-denied" || scenario == "both-unavailable" {
				wantStatus := map[string]int{"cancelled": 504, "guard-denied": 403, "opaque-guard-denied": 500, "both-unavailable": 502}[scenario]
				if status != wantStatus {
					t.Fatalf("failed request returned HTTP %d, want %d: %v", status, wantStatus, result)
				}
				return
			}
			rows, ok := result.([]any)
			want := "lexical"
			if scenario == "missing-lexical" {
				want = "vector"
			}
			if status != 200 || !ok || len(rows) != 1 || rows[0].(map[string]any)["id"] != want {
				t.Fatalf("surviving %s leg lost: HTTP %d %v", want, status, result)
			}
		})
	}
}

func TestT10SearchHybridRerankBudgetRetainsOrderAndCancelsProvider(t *testing.T) {
	started, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		close(started)
		select {
		case <-r.Context().Done():
			close(cancelled)
		case <-release:
		}
	}))
	t.Cleanup(func() { close(release); server.Close() })
	rows := []fiber.Map{
		{"id": "first", "metadata": json.RawMessage(`{"text":"first source"}`), "reranked": false},
		{"id": "second", "metadata": json.RawMessage(`{"text":"second source"}`), "reranked": false},
	}
	hybridApplyRerankUnchecked(context.Background(), APIConfig{RerankBudgetMs: 50}, rerank.NewClient(server.URL, "fixture", 0, 5000), "source", &rows, true)
	if len(rows) != 2 || rows[0]["id"] != "first" || rows[1]["id"] != "second" || rows[0]["reranked"] != false || rows[1]["reranked"] != false {
		t.Fatalf("budget failure changed retrieval order/provenance: %v", rows)
	}
	select {
	case <-started:
	default:
		t.Fatal("fixture never received rerank request")
	}
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("budget did not cancel provider request")
	}
}

func TestT10SearchHybridRerankRejectsInvalidAndDuplicateIndices(t *testing.T) {
	for _, fixture := range []struct {
		response   string
		allInvalid bool
	}{
		{`{"results":[{"index":1,"relevance_score":1},{"index":1,"relevance_score":0.9},{"index":-1,"relevance_score":0.8},{"index":99,"relevance_score":0.7}]}`, false},
		{`{"results":[{"index":-1,"relevance_score":1},{"index":99,"relevance_score":0.9}]}`, true},
	} {
		response := fixture.response
		t.Run(response, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(response))
			}))
			t.Cleanup(server.Close)
			rows := []fiber.Map{
				{"id": "first", "metadata": json.RawMessage(`{"text":"first source"}`), "reranked": false},
				{"id": "second", "metadata": json.RawMessage(`{"text":"second source"}`), "reranked": false},
			}
			before := testutil.ToFloat64(metrics.RerankInvocations.WithLabelValues("ok"))
			hybridApplyRerankUnchecked(context.Background(), APIConfig{}, rerank.NewClient(server.URL, "fixture", 0, 5000), "source", &rows, true)
			if len(rows) != 2 || rows[0]["id"] == rows[1]["id"] {
				t.Fatalf("invalid provider indices duplicated rows: %v", rows)
			}
			if fixture.allInvalid { // preserve input and do not count success
				if rows[0]["id"] != "first" || rows[0]["reranked"] != false || rows[1]["reranked"] != false {
					t.Fatalf("all invalid response changed results: %v", rows)
				}
				if testutil.ToFloat64(metrics.RerankInvocations.WithLabelValues("ok")) != before {
					t.Fatal("all invalid indices counted as success")
				}
			} else if rows[0]["id"] != "second" || rows[0]["reranked"] != true {
				t.Fatalf("valid placement lost: %v", rows)
			}
		})
	}
}
