package http

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stek0v/levara/internal/store"
	"github.com/stek0v/levara/pkg/bm25"
	"github.com/stek0v/levara/pkg/embed"
)

func TestT10SearchACLBeforeFusionAndDedup(t *testing.T) {
	t.Setenv("LEVARA_TENANT_ENFORCED", "0")
	for _, dialect := range []string{"sqlite", "postgres"} {
		for _, strategy := range []string{"CHUNKS", "HYBRID"} {
			t.Run(dialect+"/"+strategy, func(t *testing.T) {
				app, db := documentACLHTTPFixture(t, dialect)
				if _, err := db.Exec("DELETE FROM dataset_shares WHERE id='share-b'"); err != nil {
					t.Fatal(err)
				}
				cm, err := store.NewCollectionManager(2, t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { cm.Close() })
				endpoint := newEmbedServer(t, []float32{1, 0})
				cfg := APIConfig{DB: db, RequireAuth: true, Collections: cm, EmbedEndpoint: endpoint.URL,
					EmbedClient: embed.NewClient(endpoint.URL, "fixture", 1, 1), BM25Indexes: bm25.NewIndexRegistry()}
				for _, candidate := range []struct {
					dataset, id, collection string
					vector                  []float32
					text                    string
				}{
					{"b", "denied", "docs", []float32{1, 0}, "source source source"},
					{"a", "allowed", "docs", []float32{0.8, 0.6}, "source"},
				} {
					collection, id := candidate.collection, candidate.id
					if strategy == "CHUNKS" {
						id = "same"
						collection = "a-denied"
						if candidate.dataset == "a" {
							collection = "z-allowed"
						}
					}
					meta := map[string]any{"dataset_id": candidate.dataset, "text": candidate.text}
					if err := cm.CreateWithDim(collection, 2, "fixture", "cosine"); err != nil {
						t.Fatal(err)
					}
					if err := cm.Insert(collection, id, candidate.vector, meta); err != nil {
						t.Fatal(err)
					}
					encoded, _ := json.Marshal(meta)
					cfg.BM25Indexes.GetOrCreate(collection, nil).Add(id, candidate.text, string(encoded))
				}
				app.Post("/search/text", searchHandler(cfg))
				req := httptest.NewRequest("POST", "/search/text", strings.NewReader(fmt.Sprintf(`{"query_text":"source","query_type":%q,"top_k":1,"rerank":false}`, strategy)))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", "Bearer "+createJWT("alice", "alice@example.test", "rbac-regression-secret"))
				resp, err := app.Test(req, -1)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				var rows []map[string]any
				if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
					t.Fatalf("HTTP %d: %v", resp.StatusCode, err)
				}
				if resp.StatusCode != 200 || len(rows) != 1 {
					t.Fatalf("authorized lower candidate lost: HTTP %d %v", resp.StatusCode, rows)
				}
				if strategy == "CHUNKS" {
					if rows[0]["id"] != "same" || rows[0]["collection"] != "z-allowed" {
						t.Fatalf("denied same-ID row consumed dedup slot: %v", rows)
					}
				} else if rows[0]["id"] != "allowed" {
					t.Fatalf("denied head consumed fusion budget: %v", rows)
				}
			})
		}
	}
}
