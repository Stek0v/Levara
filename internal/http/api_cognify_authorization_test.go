package http

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/internal/store"
	"github.com/stek0v/levara/pkg/bm25"
	"github.com/stek0v/levara/pkg/runreg"
)

type documentACLCountingStorage struct {
	*memStorage
	loads int
}

func (s *documentACLCountingStorage) Load(ctx context.Context, key string) (io.ReadCloser, error) {
	s.loads++
	return s.memStorage.Load(ctx, key)
}

func TestDocumentACLCognifyRejectsForeignAndViewerSources(t *testing.T) {
	app, db := documentACLHTTPFixture(t)
	storage := &documentACLCountingStorage{memStorage: newMemStorage()}
	storage.objects["doc"] = []byte("private text must never be loaded before authorizing all source datasets")
	for _, query := range []string{`INSERT INTO data(id,name,raw_data_location) VALUES('source','private','storage://doc')`, `INSERT INTO dataset_data(dataset_id,data_id) VALUES('a','source'),('b','source')`} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	runs := runreg.New()
	app.Post("/cognify", cognifyHandler(APIConfig{DB: db, Runs: runs, FileStorage: storage}))
	for _, body := range []string{`{"datasets":["b"],"mode":"rag"}`, `{"datasets":["a","b"],"mode":"rag"}`} {
		req := httptest.NewRequest("POST", "/cognify", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Test-User", "alice")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		result, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 403 {
			t.Errorf("status=%d body=%s, want 403 before file read", resp.StatusCode, result)
		}
	}
	if storage.loads != 0 || len(runs.Snapshot()) != 0 {
		t.Fatalf("unauthorized source loads=%d pipeline runs=%d", storage.loads, len(runs.Snapshot()))
	}
}

func TestDocumentACLCognifyPreservesEachSource(t *testing.T) {
	app, db := documentACLHTTPFixture(t)
	checkDocumentACLCognifyPreservesEachSource(t, app, db)
}
func TestDocumentACLCognifyPostgres(t *testing.T) {
	app, db := documentACLHTTPFixture(t, "postgres")
	checkDocumentACLCognifyPreservesEachSource(t, app, db)
}
func checkDocumentACLCognifyPreservesEachSource(t *testing.T, app *fiber.App, db *sql.DB) {
	t.Helper()
	dir := t.TempDir()
	for _, doc := range []struct{ id, ds, text string }{{"doc-a", "a", "alpha private document with enough text to create an indexed chunk for this test."}, {"doc-b", "b", "bravo confidential document with enough text to create an indexed chunk for this test."}} {
		path := filepath.Join(dir, doc.id+".txt")
		if err := os.WriteFile(path, []byte(doc.text), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(Q(`INSERT INTO data(id,name,raw_data_location,pipeline_status) VALUES($1,$2,$3,'{}')`), doc.id, doc.id, "file://"+path); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(Q(`INSERT INTO dataset_data(dataset_id,data_id) VALUES($1,$2)`), doc.ds, doc.id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE dataset_shares SET role='editor' WHERE id='share-b'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO dataset_data(dataset_id,data_id) VALUES('b','doc-a')`); err != nil {
		t.Fatal(err)
	}
	cm, err := store.NewCollectionManager(2, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer cm.Close()
	bm := bm25.NewIndexRegistry()
	embedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		rows := []any{}
		for i := range req.Input {
			rows = append(rows, map[string]any{"index": i, "embedding": []float32{1, 0}})
		}
		json.NewEncoder(w).Encode(map[string]any{"data": rows})
	}))
	defer embedServer.Close()
	cfg := APIConfig{DB: db, Runs: runreg.New(), StoragePath: dir, Collections: cm, BM25Indexes: bm, EmbedEndpoint: embedServer.URL, EmbedModel: "test"}
	app.Post("/cognify", cognifyHandler(cfg))
	app.Delete("/datasets/:id/data/:dataId", datasetDataDeleteHandler(cfg))
	app.Get("/datasets/:id/data/:dataId/raw", datasetDataRawHandler(cfg))
	req := httptest.NewRequest("POST", "/cognify", strings.NewReader(`{"datasets":["a","b"],"collection":"docs","mode":"rag"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-User", "alice")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	result, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, result)
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		var status string
		if err := db.QueryRow(`SELECT pipeline_status FROM data WHERE id='doc-a'`).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(status, "COMPLETED") || strings.Contains(status, "FAILED") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	idx := bm.Get("docs")
	if idx == nil {
		t.Fatal("pipeline produced no lexical index")
	}
	for _, tc := range []struct {
		term, doc string
		datasets  []string
	}{{"alpha", "doc-a", []string{"a", "b"}}, {"bravo", "doc-b", []string{"b"}}} {
		hits := idx.Search(tc.term, 10)
		if len(hits) != len(tc.datasets) {
			t.Errorf("term=%s hits=%v", tc.term, hits)
			continue
		}
		got := map[string]bool{}
		for _, hit := range hits {
			var meta map[string]any
			if err := json.Unmarshal([]byte(hit.Metadata), &meta); err != nil {
				t.Fatal(err)
			}
			dataset, _ := meta["dataset_id"].(string)
			got[dataset] = true
			if meta["document_id"] != tc.doc {
				t.Errorf("term=%s document=%v want %s", tc.term, meta["document_id"], tc.doc)
			}
		}
		for _, dataset := range tc.datasets {
			if !got[dataset] {
				t.Errorf("term=%s missing dataset=%s hits=%v", tc.term, dataset, hits)
			}
		}
	}
	// Removing one dataset link must preserve the shared document and b's copy.
	req = httptest.NewRequest("DELETE", "/datasets/a/data/doc-a", nil)
	req.Header.Set("X-Test-User", "alice")
	resp, err = app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("delete link status=%d", resp.StatusCode)
	}
	req = httptest.NewRequest("GET", "/datasets/b/data/doc-a/raw", nil)
	req.Header.Set("X-Test-User", "bob")
	resp, err = app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "alpha") {
		t.Fatalf("remaining shared document unavailable: status=%d body=%s", resp.StatusCode, body)
	}
	preserved := false
	for _, hit := range idx.Search("alpha", 10) {
		var meta map[string]any
		if err := json.Unmarshal([]byte(hit.Metadata), &meta); err != nil {
			t.Fatal(err)
		}
		preserved = preserved || meta["dataset_id"] == "b"
	}
	if !preserved {
		t.Fatal("deleting dataset a's link removed dataset b's indexed document")
	}

}

func TestDocumentACLCognifyInvalidSourcesDoNotStart(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       int
	}{
		{"invalid-json", `{`, 400},
		{"ambiguous-inline", `{"datasets":["a","b"],"texts":["text"],"mode":"rag"}`, 400},
		{"missing-file", `{"datasets":["a"],"mode":"rag"}`, 422},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, db := documentACLHTTPFixture(t)
			if _, err := db.Exec(`UPDATE dataset_shares SET role='editor' WHERE id='share-b'`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO data(id,name,raw_data_location) VALUES('missing','must not index filename','storage://absent')`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO dataset_data(dataset_id,data_id) VALUES('a','missing')`); err != nil {
				t.Fatal(err)
			}
			runs := runreg.New()
			app.Post("/cognify", cognifyHandler(APIConfig{DB: db, Runs: runs, FileStorage: newMemStorage()}))
			req := httptest.NewRequest("POST", "/cognify", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Test-User", "alice")
			resp, err := app.Test(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != tc.want || len(runs.Snapshot()) != 0 {
				t.Fatalf("status=%d runs=%d want status=%d without a pipeline", resp.StatusCode, len(runs.Snapshot()), tc.want)
			}
		})
	}
}
