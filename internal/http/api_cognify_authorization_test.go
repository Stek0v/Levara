package http

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/internal/store"
	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/bm25"
	"github.com/stek0v/levara/pkg/ingest"
	"github.com/stek0v/levara/pkg/mcp"
	"github.com/stek0v/levara/pkg/runreg"
)

type documentACLCountingStorage struct {
	*memStorage
	loads int
}

type failingCognifyStorage struct{ *memStorage }

type observedMCPDeps struct {
	mcp.Deps
	done chan struct{}
	once sync.Once
}

func (d *observedMCPDeps) LogHeartbeat(string, any) {
	d.once.Do(func() { close(d.done) })
}

func (s *failingCognifyStorage) Save(context.Context, string, io.Reader) error {
	return errors.New("storage unavailable")
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
		if _, err := db.Exec(Q(`INSERT INTO data(id,name,raw_data_location,pipeline_status,raw_content_hash) VALUES($1,$2,$3,'{}',$4)`), doc.id, doc.id, "file://"+path, fmt.Sprintf("%x", sha256.Sum256([]byte(doc.text)))); err != nil {
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
		wantLoads  int
	}{
		{"invalid-json", `{`, 400, 0},
		{"ambiguous-inline", `{"datasets":["a","b"],"texts":["text"],"mode":"rag"}`, 400, 0},
		{"malformed-source-hash", `{"datasets":["a"],"mode":"rag"}`, 409, 0},
		{"missing-file", `{"datasets":["a"],"mode":"rag"}`, 422, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, db := documentACLHTTPFixture(t)
			if _, err := db.Exec(`UPDATE dataset_shares SET role='editor' WHERE id='share-b'`); err != nil {
				t.Fatal(err)
			}
			hash := fmt.Sprintf("%x", sha256.Sum256([]byte("missing source bytes")))
			if tc.name == "malformed-source-hash" {
				hash = ""
			}
			if _, err := db.Exec(Q(`INSERT INTO data(id,name,raw_data_location,raw_content_hash) VALUES('missing','must not index filename','storage://absent',$1)`), hash); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO dataset_data(dataset_id,data_id) VALUES('a','missing')`); err != nil {
				t.Fatal(err)
			}
			runs := runreg.New()
			storage := &documentACLCountingStorage{memStorage: newMemStorage()}
			app.Post("/cognify", cognifyHandler(APIConfig{DB: db, Runs: runs, FileStorage: storage}))
			req := httptest.NewRequest("POST", "/cognify", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Test-User", "alice")
			resp, err := app.Test(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != tc.want || len(runs.Snapshot()) != 0 || storage.loads != tc.wantLoads {
				t.Fatalf("status=%d runs=%d loads=%d want status=%d loads=%d without a pipeline", resp.StatusCode, len(runs.Snapshot()), storage.loads, tc.want, tc.wantLoads)
			}
		})
	}
}

func TestMCPInlineCognifyPersistsServerSourceBeforeRun(t *testing.T) {
	checkMCPInlineCognifyPersistsServerSourceBeforeRun(t)
}

func TestMCPInlineCognifyPersistsServerSourceBeforeRunPostgres(t *testing.T) {
	checkMCPInlineCognifyPersistsServerSourceBeforeRun(t, "postgres")
}

func TestMCPInlineCognifyTransportsPersistServerSource(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		for _, latest := range []bool{false, true} {
			name := "legacy"
			if latest {
				name = "latest"
			}
			t.Run(dialect+"/"+name, func(t *testing.T) {
				ingest.SetSQLiteMode(dialect == "sqlite")
				t.Cleanup(func() { ingest.SetSQLiteMode(false) })
				_, db := documentACLHTTPFixture(t, dialect)
				t.Setenv("BACKGROUND_TASK_TIMEOUT_MS", "500")
				runs := runreg.New()
				h := &mcpHandler{cfg: APIConfig{
					DB: db, Runs: runs, RequireAuth: true, JWTSecret: "test-inline-cognify",
					FileStorage: newMemStorage(), StoragePath: t.TempDir(), EmbedEndpoint: "http://127.0.0.1:1",
				}, sessions: mcp.NewSessionStore()}
				app := fiber.New()
				path := "/mcp"
				if latest {
					path = latestMCPPath
					app.Post(path, h.handleLatestRPC)
				} else {
					app.Post(path, h.handleRPC)
				}
				meta := ""
				if latest {
					meta = "," + latestMCPMetaParams()
				}
				body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"cognify","arguments":{"data":"transport inline source","collection":"docs","mode":"rag","document_id":"forged"}` + meta + `}}`
				req := httptest.NewRequest("POST", path, strings.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Accept", "application/json, text/event-stream")
				req.Header.Set("Authorization", "Bearer "+createJWT("alice", "alice@example.test", "test-inline-cognify"))
				if latest {
					for k, v := range latestMCPHeaders("tools/call") {
						req.Header.Set(k, v)
					}
					req.Header.Set("Mcp-Name", "cognify")
				}
				resp, err := app.Test(req, -1)
				if err != nil {
					t.Fatal(err)
				}
				raw, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
				var rpc struct {
					Result mcp.ToolResult `json:"result"`
				}
				if resp.StatusCode != 200 || json.Unmarshal(raw, &rpc) != nil || rpc.Result.IsError {
					t.Fatalf("HTTP=%d body=%s", resp.StatusCode, raw)
				}
				var datasetID, documentID string
				if err := db.QueryRow(`SELECT dd.dataset_id,dd.data_id FROM dataset_data dd JOIN datasets d ON d.id=dd.dataset_id WHERE d.name='__cognify__:alice:docs'`).Scan(&datasetID, &documentID); err != nil {
					t.Fatal(err)
				}
				if documentID == "" || documentID == "forged" || len(runs.Snapshot()) != 1 {
					t.Fatalf("dataset=%q document=%q runs=%+v", datasetID, documentID, runs.Snapshot())
				}
				deadline := time.Now().Add(3 * time.Second)
				for time.Now().Before(deadline) {
					var n int
					if err := db.QueryRow(`SELECT COUNT(*) FROM heartbeats WHERE event_type='cognify'`).Scan(&n); err == nil && n == 1 {
						return
					}
					time.Sleep(10 * time.Millisecond)
				}
				t.Fatal("cognify background run did not finish")
			})
		}
	}
}

func TestHTTPInlineCognifyPublishesServerSource(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			ingest.SetSQLiteMode(dialect == "sqlite")
			t.Cleanup(func() { ingest.SetSQLiteMode(false) })
			app, db := documentACLHTTPFixture(t, dialect)
			runs := runreg.New()
			cfg := APIConfig{DB: db, Runs: runs, FileStorage: newMemStorage(), StoragePath: t.TempDir(), EmbedEndpoint: "http://unused"}
			app.Post("/cognify", cognifyHandler(cfg))
			texts := []string{
				"HTTP inline source one contains enough immutable text to produce a complete document generation for publication.",
				strings.Repeat("HTTP inline source two has enough paragraphs to produce its own larger chunk count.\n\n", 80),
			}
			body, _ := json.Marshal(map[string]any{"texts": texts, "collection": "docs", "mode": "rag"})
			req := httptest.NewRequest("POST", "/cognify", strings.NewReader(string(body)))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Test-User", "alice")
			resp, err := app.Test(req, -1)
			if err != nil {
				t.Fatal(err)
			}
			var started struct {
				RunID string `json:"pipeline_run_id"`
			}
			err = json.NewDecoder(resp.Body).Decode(&started)
			resp.Body.Close()
			if err != nil || resp.StatusCode != 200 || started.RunID == "" {
				t.Fatalf("HTTP=%d run=%q err=%v", resp.StatusCode, started.RunID, err)
			}
			var datasetID string
			var documents int
			if err := db.QueryRow(`SELECT MIN(dd.dataset_id),COUNT(*) FROM dataset_data dd JOIN datasets d ON d.id=dd.dataset_id WHERE d.name='__cognify__:alice:docs'`).Scan(&datasetID, &documents); err != nil {
				t.Fatal(err)
			}
			status, ok := runs.Load(started.RunID)
			var sources []searchDocumentSource
			if !ok || json.Unmarshal([]byte(status.SourcesJSON), &sources) != nil || documents != 2 || len(sources) != 2 {
				t.Fatalf("sources not committed before run visibility: dataset=%q documents=%d sources=%+v status=%+v", datasetID, documents, sources, status)
			}
			complete := false
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				var publications, statuses int
				publicationErr := db.QueryRow(Q(`SELECT COUNT(*) FROM document_index_publications WHERE dataset_id=$1 AND collection_name='docs'`), datasetID).Scan(&publications)
				statusErr := db.QueryRow(Q(`SELECT COUNT(*) FROM document_pipeline_statuses WHERE dataset_id=$1 AND collection_name='docs' AND pipeline_state='COMPLETED'`), datasetID).Scan(&statuses)
				if publicationErr == nil && statusErr == nil && publications == 2 && statuses == 2 {
					complete = true
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if !complete {
				t.Fatal("inline sources were not both published with terminal status")
			}
			counts := make([]int, len(sources))
			for i, source := range sources {
				var raw string
				if err := db.QueryRow(Q(`SELECT status_json FROM document_pipeline_statuses
					WHERE dataset_id=$1 AND data_id=$2 AND collection_name=$3`), source.DatasetID, source.DocumentID, "docs").Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var exact struct {
					Chunks int `json:"chunks"`
				}
				if json.Unmarshal([]byte(raw), &exact) != nil {
					t.Fatalf("status=%s", raw)
				}
				counts[i] = exact.Chunks
			}
			if counts[0] <= 0 || counts[1] <= counts[0] {
				t.Fatalf("per-source counters overwritten: %v", counts)
			}
			for _, source := range sources {
				var raw string
				if err := db.QueryRow(Q(`SELECT sources_json FROM document_index_publications
					WHERE dataset_id=$1 AND data_id=$2 AND collection_name=$3`), source.DatasetID, source.DocumentID, "docs").Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var lineage []searchDocumentSource
				if json.Unmarshal([]byte(raw), &lineage) != nil || len(lineage) != 1 || lineage[0].DocumentID != source.DocumentID {
					t.Fatalf("cross-document lineage for %s: %s", source.DocumentID, raw)
				}
			}
		})
	}
}

func TestHTTPBatchCognifyKeepsPerSourceTerminalTruth(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			ingest.SetSQLiteMode(dialect == "sqlite")
			t.Cleanup(func() { ingest.SetSQLiteMode(false) })
			app, db := documentACLHTTPFixture(t, dialect)
			cm, err := store.NewCollectionManager(2, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer cm.Close()
			embed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Input []string `json:"input"`
				}
				if json.NewDecoder(r.Body).Decode(&req) != nil {
					http.Error(w, "invalid", 400)
					return
				}
				for _, input := range req.Input {
					if strings.Contains(input, "fail-second") {
						http.Error(w, "forced second-source failure", http.StatusServiceUnavailable)
						return
					}
				}
				rows := make([]any, len(req.Input))
				for i := range rows {
					rows[i] = map[string]any{"index": i, "embedding": []float32{1, 0}}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": rows})
			}))
			defer embed.Close()
			runs := runreg.New()
			cfg := APIConfig{DB: db, Runs: runs, FileStorage: newMemStorage(), StoragePath: t.TempDir(), EmbedEndpoint: embed.URL, Collections: cm}
			app.Post("/cognify", cognifyHandler(cfg))
			body, _ := json.Marshal(map[string]any{"texts": []string{
				"first source completes and remains the readable publication after a later source fails",
				"fail-second source forces the batch to stop before publication",
				"third source is claimed but never attempted after the preceding failure",
			}, "collection": "docs", "mode": "rag"})
			req := httptest.NewRequest("POST", "/cognify", strings.NewReader(string(body)))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Test-User", "alice")
			resp, err := app.Test(req, -1)
			if err != nil {
				t.Fatal(err)
			}
			var started struct {
				RunID string `json:"pipeline_run_id"`
			}
			if json.NewDecoder(resp.Body).Decode(&started) != nil || resp.StatusCode != 200 {
				t.Fatalf("HTTP=%d", resp.StatusCode)
			}
			resp.Body.Close()
			var status runreg.Status
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				if current, ok := runs.Load(started.RunID); ok && current.Status != "RUNNING" {
					status = *current
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if status.Status != "FAILED" {
				t.Fatalf("batch status=%+v", status)
			}
			var sources []searchDocumentSource
			if json.Unmarshal([]byte(status.SourcesJSON), &sources) != nil || len(sources) != 3 {
				t.Fatalf("sources=%+v", sources)
			}
			want := []string{"COMPLETED", "FAILED", "FAILED"}
			for i, source := range sources {
				var got string
				if err := db.QueryRow(Q(`SELECT pipeline_state FROM document_pipeline_statuses
					WHERE dataset_id=$1 AND data_id=$2 AND collection_name=$3`), source.DatasetID, source.DocumentID, "docs").Scan(&got); err != nil || got != want[i] {
					t.Fatalf("source[%d] state=%s want=%s err=%v", i, got, want[i], err)
				}
			}
			var publications int
			if err := db.QueryRow(Q(`SELECT COUNT(*) FROM document_index_publications WHERE dataset_id=$1 AND collection_name=$2`), sources[0].DatasetID, "docs").Scan(&publications); err != nil || publications != 1 {
				t.Fatalf("publications=%d err=%v", publications, err)
			}
		})
	}
}

func TestCognifyFailureFinalizationIsAtomicAndRetryable(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			_, db := documentACLHTTPFixture(t, dialect)
			ids := []string{"first", "second", "completed", "stale"}
			sources := make([]cognifySource, 0, len(ids))
			claims := make([]pipelineAttemptSource, 0, len(ids))
			for i, id := range ids {
				hash := fmt.Sprintf("%064x", i+1)
				if _, err := db.Exec(Q(`INSERT INTO data(id,name,source_revision,raw_content_hash) VALUES($1,$2,1,$3)`), id, id, hash); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(Q(`INSERT INTO dataset_data(dataset_id,data_id) VALUES('a',$1)`), id); err != nil {
					t.Fatal(err)
				}
				sources = append(sources, cognifySource{datasetID: "a", documentID: id, sourceRevision: 1, rawContentHash: hash})
				claims = append(claims, pipelineAttemptSource{datasetID: "a", dataID: id, sourceRevision: 1, rawContentHash: hash})
			}
			if err := claimPipelineAttempts(context.Background(), db, claims, "docs", "attempt"); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE document_pipeline_statuses SET pipeline_state='COMPLETED' WHERE data_id='completed'`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE document_pipeline_statuses SET attempt_id='newer' WHERE data_id='stale'`); err != nil {
				t.Fatal(err)
			}

			if dialect == "postgres" {
				if _, err := db.Exec(`CREATE FUNCTION fail_second_cognify_status() RETURNS trigger LANGUAGE plpgsql AS $$
				BEGIN IF NEW.data_id='second' AND NEW.pipeline_state='FAILED' THEN RAISE EXCEPTION 'forced second source failure'; END IF; RETURN NEW; END $$`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`CREATE TRIGGER fail_second_cognify_status BEFORE UPDATE ON document_pipeline_statuses FOR EACH ROW EXECUTE FUNCTION fail_second_cognify_status()`); err != nil {
					t.Fatal(err)
				}
			} else if _, err := db.Exec(`CREATE TRIGGER fail_second_cognify_status BEFORE UPDATE ON document_pipeline_statuses
				WHEN NEW.data_id='second' AND NEW.pipeline_state='FAILED'
				BEGIN SELECT RAISE(ABORT,'forced second source failure'); END`); err != nil {
				t.Fatal(err)
			}

			runs := runreg.New()
			status := &runreg.Status{RunID: "attempt", Status: "FAILED", Stage: "failed", Message: "pipeline failed", StartedAt: time.Now()}
			runs.Store("attempt", &runreg.Status{RunID: "attempt", Status: "RUNNING", Stage: "running", StartedAt: status.StartedAt})
			if publishCognifyFailure(db, runs, sources, "docs", "attempt", status) {
				t.Fatal("terminal status published despite failed atomic finalization")
			}
			if visible, _ := runs.Load("attempt"); visible.Status != "RUNNING" || visible.Stage != "finalizing" {
				t.Fatalf("visible status=%+v, want RUNNING/finalizing", visible)
			}
			assertState := func(id, wantState, wantAttempt string) {
				t.Helper()
				var state, attempt string
				if err := db.QueryRow(Q(`SELECT pipeline_state,attempt_id FROM document_pipeline_statuses WHERE data_id=$1`), id).Scan(&state, &attempt); err != nil || state != wantState || attempt != wantAttempt {
					t.Fatalf("%s state=%s attempt=%s err=%v, want %s/%s", id, state, attempt, err, wantState, wantAttempt)
				}
			}
			assertState("first", "RUNNING", "attempt")
			assertState("second", "RUNNING", "attempt")
			assertState("completed", "COMPLETED", "attempt")
			assertState("stale", "RUNNING", "newer")

			dropTrigger := "DROP TRIGGER fail_second_cognify_status"
			if dialect == "postgres" {
				dropTrigger += " ON document_pipeline_statuses"
			}
			if _, err := db.Exec(dropTrigger); err != nil {
				t.Fatal(err)
			}
			if !publishCognifyFailure(db, runs, sources, "docs", "attempt", status) {
				t.Fatal("retry did not publish terminal status")
			}
			if visible, _ := runs.Load("attempt"); visible.Status != "FAILED" {
				t.Fatalf("visible status=%+v, want FAILED", visible)
			}
			assertState("first", "FAILED", "attempt")
			assertState("second", "FAILED", "attempt")
			assertState("completed", "COMPLETED", "attempt")
			assertState("stale", "RUNNING", "newer")
		})
	}
}

func checkMCPInlineCognifyPersistsServerSourceBeforeRun(t *testing.T, dialect ...string) {
	ingest.SetSQLiteMode(len(dialect) == 0)
	t.Cleanup(func() { ingest.SetSQLiteMode(false) })
	app, db := documentACLHTTPFixture(t, dialect...)
	_ = app
	runs := runreg.New()
	deps := &observedMCPDeps{
		Deps: NewMCPDeps(APIConfig{DB: db, Runs: runs, FileStorage: newMemStorage(), StoragePath: t.TempDir(), EmbedEndpoint: "http://unused"}),
		done: make(chan struct{}),
	}
	ctx := context.WithValue(context.Background(), mcp.UserIDKey, "owner")
	result := mcp.ToolCognify(ctx, deps, map[string]any{"data": "inline source bytes", "collection": "docs", "mode": "rag", "document_id": "forged"})
	if result.IsError {
		t.Fatalf("cognify failed: %+v", result)
	}
	var datasetID, documentID string
	if err := db.QueryRow(`SELECT dd.dataset_id,dd.data_id FROM dataset_data dd JOIN datasets d ON d.id=dd.dataset_id WHERE d.name='__cognify__:owner:docs'`).Scan(&datasetID, &documentID); err != nil {
		t.Fatal(err)
	}
	if documentID == "" || documentID == "forged" {
		t.Fatalf("server document id=%q", documentID)
	}
	version, hash, err := (documentSQLPolicy(APIConfig{DB: db})).SourceVersion(ctx, accesspkg.DocumentRef{DatasetID: datasetID, DataID: documentID})
	if err != nil || version <= 0 || hash != fmt.Sprintf("%x", sha256.Sum256([]byte("inline source bytes"))) {
		t.Fatalf("source version=%d hash=%q err=%v", version, hash, err)
	}
	snapshot := runs.Snapshot()
	if len(snapshot) != 1 {
		t.Fatalf("runs=%+v", snapshot)
	}
	var proof []searchDocumentSource
	if err := json.Unmarshal([]byte(snapshot[0].SourcesJSON), &proof); err != nil || len(proof) != 1 || proof[0].DatasetID != datasetID || proof[0].DocumentID != documentID || proof[0].SourceRevision != version || proof[0].RawContentHash != hash {
		t.Fatalf("proof=%+v err=%v", proof, err)
	}
	select {
	case <-deps.done:
	case <-time.After(2 * time.Second):
		t.Fatal("cognify run did not finish")
	}
}

func TestMCPInlineCognifyPreparationFailureLeavesNoState(t *testing.T) {
	checkMCPInlineCognifyPreparationFailureLeavesNoState(t)
}

func TestMCPInlineCognifyPreparationFailureLeavesNoStatePostgres(t *testing.T) {
	checkMCPInlineCognifyPreparationFailureLeavesNoState(t, "postgres")
}

func checkMCPInlineCognifyPreparationFailureLeavesNoState(t *testing.T, dialect ...string) {
	ingest.SetSQLiteMode(len(dialect) == 0)
	t.Cleanup(func() { ingest.SetSQLiteMode(false) })
	app, db := documentACLHTTPFixture(t, dialect...)
	_ = app
	runs := runreg.New()
	deps := NewMCPDeps(APIConfig{DB: db, Runs: runs, FileStorage: &failingCognifyStorage{newMemStorage()}, StoragePath: t.TempDir(), EmbedEndpoint: "http://unused"})
	ctx := context.WithValue(context.Background(), mcp.UserIDKey, "owner")
	if result := mcp.ToolCognify(ctx, deps, map[string]any{"data": "inline source bytes", "collection": "docs", "mode": "rag"}); !result.IsError {
		t.Fatalf("result=%+v", result)
	}
	if len(runs.Snapshot()) != 0 {
		t.Fatalf("failed source left runs=%+v", runs.Snapshot())
	}
	var datasets, data, pending int
	for query, target := range map[string]*int{
		`SELECT COUNT(*) FROM datasets WHERE name='__cognify__:owner:docs'`: &datasets,
		`SELECT COUNT(*) FROM data WHERE name='inline source bytes'`:        &data,
		`SELECT COUNT(*) FROM ingest_pending_uploads`:                       &pending,
	} {
		if err := db.QueryRow(query).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	if datasets != 0 || data != 0 || pending != 0 {
		t.Fatalf("datasets=%d data=%d pending=%d", datasets, data, pending)
	}
}

func TestPersistPipelineStatusScopesExactSource(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		storedHash := strings.Repeat("AB", 32)
		hash := strings.ToLower(storedHash)
		f.exec("UPDATE data SET source_revision=7,raw_content_hash=$1,pipeline_status=$2 WHERE id='blob'", storedHash, `{"other":{"status":"FAILED"}}`)
		f.exec("UPDATE data SET source_revision=7,raw_content_hash=$1,pipeline_status=$2 WHERE id='visible'", storedHash, `{"docs":{"status":"FAILED"}}`)
		PersistPipelineStatus(f.db, "beta", "blob", "docs", "FAILED", 7, hash, 0, 0, 0, 1)
		PersistPipelineStatus(f.db, "alpha", "blob", "docs", "COMPLETED", 7, hash, 3, 2, 1, 9)

		var blob, visible string
		if err := f.db.QueryRow(Q("SELECT pipeline_status FROM data WHERE id=$1"), "blob").Scan(&blob); err != nil {
			t.Fatal(err)
		}
		if err := f.db.QueryRow(Q("SELECT pipeline_status FROM data WHERE id=$1"), "visible").Scan(&visible); err != nil {
			t.Fatal(err)
		}
		if blob != `{"other":{"status":"FAILED"}}` {
			t.Fatalf("shared data compatibility status changed: %s", blob)
		}
		if visible != `{"docs":{"status":"FAILED"}}` {
			t.Fatalf("sibling status changed: %s", visible)
		}
		alpha, err := pipelineStatusForDocument(context.Background(), f.db, "alpha", "blob", blob)
		if err != nil {
			t.Fatal(err)
		}
		beta, err := pipelineStatusForDocument(context.Background(), f.db, "beta", "blob", blob)
		if err != nil {
			t.Fatal(err)
		}
		var alphaState, betaState map[string]map[string]any
		if json.Unmarshal([]byte(alpha), &alphaState) != nil || alphaState["docs"]["status"] != "COMPLETED" {
			t.Fatalf("alpha status=%s", alpha)
		}
		if json.Unmarshal([]byte(beta), &betaState) != nil || betaState["docs"]["status"] != "FAILED" {
			t.Fatalf("beta status=%s", beta)
		}
		claim := []pipelineAttemptSource{{datasetID: "alpha", dataID: "blob", sourceRevision: 7, rawContentHash: hash}}
		if err := claimPipelineAttempts(context.Background(), f.db, claim, "docs", "attempt-a"); err != nil {
			t.Fatal(err)
		}
		if err := claimPipelineAttempts(context.Background(), f.db, claim, "docs", "attempt-b"); err != nil {
			t.Fatal(err)
		}
		PersistPipelineStatus(f.db, "alpha", "blob", "docs", "COMPLETED", 7, hash, 8, 2, 1, 12, "attempt-b")
		PersistPipelineStatus(f.db, "alpha", "blob", "docs", "COMPLETED", 7, hash, 1, 0, 0, 2, "attempt-a")
		var attemptID, pipelineState, exact string
		if err := f.db.QueryRow(Q(`SELECT attempt_id,pipeline_state,status_json FROM document_pipeline_statuses
			WHERE dataset_id=$1 AND data_id=$2 AND collection_name=$3`), "alpha", "blob", "docs").Scan(&attemptID, &pipelineState, &exact); err != nil {
			t.Fatal(err)
		}
		var exactState map[string]any
		if json.Unmarshal([]byte(exact), &exactState) != nil || attemptID != "attempt-b" || pipelineState != "COMPLETED" || exactState["chunks"] != float64(8) {
			t.Fatalf("late worker replaced exact status: attempt=%s state=%s status=%s", attemptID, pipelineState, exact)
		}
		if err := claimPipelineAttempts(context.Background(), f.db, claim, "docs", "attempt-c"); err != nil {
			t.Fatal(err)
		}
		PersistPipelineStatus(f.db, "alpha", "blob", "docs", "FAILED", 7, hash, 0, 0, 0, 1, "attempt-c")
		if err := f.db.QueryRow(Q(`SELECT attempt_id,pipeline_state,status_json FROM document_pipeline_statuses
			WHERE dataset_id=$1 AND data_id=$2 AND collection_name=$3`), "alpha", "blob", "docs").Scan(&attemptID, &pipelineState, &exact); err != nil {
			t.Fatal(err)
		}
		if attemptID != "attempt-c" || pipelineState != "COMPLETED" || json.Unmarshal([]byte(exact), &exactState) != nil || exactState["chunks"] != float64(8) {
			t.Fatalf("failed rerun erased successful status: attempt=%s state=%s status=%s", attemptID, pipelineState, exact)
		}
		rollbackClaims := append(claim, pipelineAttemptSource{datasetID: "alpha", dataID: "visible", sourceRevision: 8, rawContentHash: hash})
		if err := claimPipelineAttempts(context.Background(), f.db, rollbackClaims, "docs", "attempt-rollback"); err == nil {
			t.Fatal("batch claim accepted stale source")
		}
		if err := f.db.QueryRow(Q(`SELECT attempt_id FROM document_pipeline_statuses
			WHERE dataset_id=$1 AND data_id=$2 AND collection_name=$3`), "alpha", "blob", "docs").Scan(&attemptID); err != nil || attemptID != "attempt-c" {
			t.Fatalf("batch claim did not roll back: attempt=%s err=%v", attemptID, err)
		}
		before, err := pipelineStatusForDocument(context.Background(), f.db, "alpha", "blob", blob)
		if err != nil {
			t.Fatal(err)
		}
		PersistPipelineStatus(f.db, "alpha", "blob", "docs", "FAILED", 6, hash, 0, 0, 0, 0)
		PersistPipelineStatus(f.db, "alpha", "blob", "docs", "FAILED", 7, hash, 0, 0, 0, 0)
		alpha, err = pipelineStatusForDocument(context.Background(), f.db, "alpha", "blob", blob)
		if err != nil || alpha != before {
			t.Fatalf("stale/failed worker changed completed status: before=%s after=%s err=%v", before, alpha, err)
		}
		PersistPipelineStatus(f.db, "beta", "blob", "docs", "COMPLETED", 7, hash, 4, 0, 0, 10)
		if !CheckPipelineStatus(f.db, "beta", "docs") {
			t.Fatal("current beta source should be complete")
		}
		newHash := strings.Repeat("b", 64)
		f.exec("UPDATE data SET source_revision=8,raw_content_hash=$1 WHERE id='blob'", newHash)
		for _, datasetID := range []string{"alpha", "beta"} {
			got, err := pipelineStatusForDocument(context.Background(), f.db, datasetID, "blob", blob)
			if err != nil || got != "{}" {
				t.Fatalf("dataset=%s stale source status=%s err=%v", datasetID, got, err)
			}
		}
		if CheckPipelineStatus(f.db, "beta", "docs") {
			t.Fatal("source replacement revived old completed status")
		}

		transferHash := strings.Repeat("c", 64)
		f.exec("INSERT INTO data(id,name,source_revision,raw_content_hash) VALUES('transferred','transferred',1,$1)", transferHash)
		f.exec("INSERT INTO dataset_data(dataset_id,data_id) VALUES('alpha','transferred')")
		PersistPipelineStatus(f.db, "alpha", "transferred", "docs", "COMPLETED", 1, transferHash, 1, 0, 0, 1)
		f.exec("INSERT INTO dataset_data(dataset_id,data_id) VALUES('beta','transferred')")
		f.exec("DELETE FROM dataset_data WHERE dataset_id='alpha' AND data_id='transferred'")
		var legacy string
		if err := f.db.QueryRow("SELECT pipeline_status FROM data WHERE id='transferred'").Scan(&legacy); err != nil {
			t.Fatal(err)
		}
		transferred, err := pipelineStatusForDocument(context.Background(), f.db, "beta", "transferred", legacy)
		if err != nil || transferred != "{}" {
			t.Fatalf("new alias inherited legacy status=%s err=%v", transferred, err)
		}
	})
}
