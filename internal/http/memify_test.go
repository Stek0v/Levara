package http

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	_ "github.com/ncruces/go-sqlite3/driver"
)

// memify_test.go — synchronous-path coverage for /memify. The async pipeline
// (entity_consolidation / triplet_embeddings / rule_associations / summary_generation)
// requires Neo4j + an LLM endpoint and is intentionally NOT exercised here.
// These tests pin the request-validation contract and the run-registry
// surfaces (status / stream 404), plus the pure extractJSON helper.

func newMemifyApp(t *testing.T, cfg APIConfig) *fiber.App {
	t.Helper()
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	registerMemifyAPI(app, cfg)
	return app
}

func memifyPost(t *testing.T, app *fiber.App, body any) (int, map[string]any) {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	r := httptest.NewRequest("POST", "/memify", bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(r, -1)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer resp.Body.Close()
	buf, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(buf, &out)
	return resp.StatusCode, out
}

func TestMemify_RejectsWithoutGraphStore(t *testing.T) {
	app := newMemifyApp(t, APIConfig{})
	status, body := memifyPost(t, app, memifyRequest{Dataset: "main"})
	if status != 400 {
		t.Errorf("status = %d, want 400; body=%v", status, body)
	}
	if detail, _ := body["detail"].(string); detail == "" {
		t.Errorf("body.detail empty, want explanation")
	}
}

func TestMemify_SQLGraphReadCompletesWithoutNeo4j(t *testing.T) {
	db := newMemifySQLiteDB(t)
	app := newMemifyApp(t, APIConfig{DB: db})

	status, body := memifyPost(t, app, memifyRequest{
		Dataset:         "main",
		EnrichmentTasks: []string{"entity_consolidation", "rule_associations", "summary_generation"},
	})
	if status != 200 {
		t.Fatalf("status=%d body=%v", status, body)
	}
	if body["status"] != "COMPLETED" {
		t.Fatalf("body=%v, want completed SQL-only memify run", body)
	}
}

func TestMemify_SQLSummaryGenerationWritesGraph(t *testing.T) {
	db := newMemifySQLiteDB(t)
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Services share authentication and database responsibilities."}}]}`))
	}))
	defer llm.Close()
	t.Setenv("LLM_ENDPOINT", llm.URL)
	t.Setenv("LLM_MODEL", "test-model")

	app := newMemifyApp(t, APIConfig{DB: db})
	status, body := memifyPost(t, app, memifyRequest{
		Dataset:         "main",
		EnrichmentTasks: []string{"summary_generation"},
	})
	if status != 200 || body["status"] != "COMPLETED" {
		t.Fatalf("status=%d body=%v", status, body)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM graph_nodes WHERE type = 'TextSummary' AND dataset_id = 'main'`).Scan(&count); err != nil {
		t.Fatalf("count summaries: %v", err)
	}
	if count != 1 {
		t.Fatalf("summary count=%d, want 1", count)
	}
}

func TestMemifyStatus_UnknownRunReturns404(t *testing.T) {
	app := newMemifyApp(t, APIConfig{})
	resp, err := app.Test(httptest.NewRequest("GET", "/memify/no-such-run/status", nil), -1)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestMemifyStatus_KnownRunReturnsJSON(t *testing.T) {
	app := newMemifyApp(t, APIConfig{})
	runID := "test-run-known-123"
	want := &memifyRunStatus{
		RunID:     runID,
		Status:    "DONE",
		Stage:     "finished",
		Message:   "ok",
		Enriched:  7,
		ElapsedMs: 1234,
		StartedAt: time.Unix(1700000000, 0).UTC(),
	}
	memifyRuns.Store(runID, want)
	defer memifyRuns.Delete(runID)

	resp, err := app.Test(httptest.NewRequest("GET", "/memify/"+runID+"/status", nil), -1)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got memifyRunStatus
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.RunID != want.RunID || got.Status != want.Status || got.Stage != want.Stage || got.Enriched != want.Enriched {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestMemifyStream_UnknownRunReturns404(t *testing.T) {
	app := newMemifyApp(t, APIConfig{})
	resp, err := app.Test(httptest.NewRequest("GET", "/memify/no-such-run/stream", nil), -1)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestExtractJSON(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain object", `{"a":1}`, `{"a":1}`},
		{"plain array", `[1,2,3]`, `[1,2,3]`},
		{"object with prose prefix", "Sure, here you go: {\"k\":\"v\"} thanks!", `{"k":"v"}`},
		{"array with code fence", "```json\n[\"x\",\"y\"]\n```", `["x","y"]`},
		{"nested object", `prefix {"a":{"b":2},"c":[1,2]} suffix`, `{"a":{"b":2},"c":[1,2]}`},
		{"no JSON at all", "no brackets here", "no brackets here"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := extractJSON(c.in); got != c.want {
				t.Errorf("extractJSON(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func newMemifySQLiteDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+t.TempDir()+"/memify.db")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
		CREATE TABLE graph_nodes (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL DEFAULT '',
			type TEXT NOT NULL DEFAULT '',
			description TEXT NOT NULL DEFAULT '',
			properties TEXT NOT NULL DEFAULT '{}',
			dataset_id TEXT NOT NULL DEFAULT '',
			created_at TEXT,
			updated_at TEXT
		);
		CREATE TABLE graph_edges (
			id TEXT PRIMARY KEY,
			source_id TEXT NOT NULL,
			target_id TEXT NOT NULL,
			relationship_name TEXT NOT NULL DEFAULT '',
			properties TEXT NOT NULL DEFAULT '{}',
			valid_from TEXT,
			valid_until TEXT,
			superseded_by TEXT NOT NULL DEFAULT '',
			confidence REAL NOT NULL DEFAULT 1.0,
			dataset_id TEXT NOT NULL DEFAULT '',
			created_at TEXT,
			updated_at TEXT
		);
		INSERT INTO graph_nodes(id,name,type,description,properties,dataset_id) VALUES
			('n1','auth','service','auth service','{"name":"auth","description":"auth service"}','main'),
			('n2','db','service','database service','{"name":"db","description":"database service"}','main'),
			('n3','api','service','api service','{"name":"api","description":"api service"}','main');
		INSERT INTO graph_edges(id,source_id,target_id,relationship_name,properties,dataset_id) VALUES
			('e1','n1','n2','calls','{}','main');
	`); err != nil {
		t.Fatalf("seed sqlite: %v", err)
	}
	return db
}

func TestMemifyPreservesTemporalEpisodes(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.exec(`INSERT INTO graph_nodes(id,name,type,properties,dataset_id) VALUES
		 ('memify-source','Source','Entity','{"document_id":"source-proof"}','alpha'),
		 ('memify-a','A','Entity','{}','alpha'),('memify-b','B','Entity','{}','alpha')`)
		f.exec(`INSERT INTO graph_edges(id,source_id,target_id,relationship_name,properties,dataset_id,valid_from,valid_until,superseded_by) VALUES
		 ('memify-source_owns_memify-a','memify-source','memify-a','owns','{"evidence":"first"}','alpha','2026-01-01T00:00:00Z','2026-02-01T00:00:00Z','memify-source_owns_memify-b'),
		 ('memify-source_owns_memify-b','memify-source','memify-b','owns','{"evidence":"second"}','alpha','2026-02-01T00:00:00Z','2026-03-01T00:00:00Z','memify-return-a'),
		 ('memify-return-a','memify-source','memify-a','owns','{"evidence":"return"}','alpha','2026-03-01T00:00:00Z',NULL,'')`)
		snapshot := func() string {
			t.Helper()
			var records [][]string
			for _, query := range []string{
				`SELECT id,name,type,COALESCE(description,''),CAST(properties AS TEXT),dataset_id FROM graph_nodes ORDER BY id`,
				`SELECT id,source_id,target_id,relationship_name,CAST(properties AS TEXT),dataset_id,COALESCE(CAST(valid_from AS TEXT),''),COALESCE(CAST(valid_until AS TEXT),''),COALESCE(superseded_by,'') FROM graph_edges ORDER BY id`,
			} {
				rows, err := f.db.Query(query)
				if err != nil {
					t.Fatal(err)
				}
				columns, err := rows.Columns()
				if err != nil {
					rows.Close()
					t.Fatal(err)
				}
				for rows.Next() {
					values := make([]string, len(columns))
					dest := make([]any, len(columns))
					for i := range values {
						dest[i] = &values[i]
					}
					if err := rows.Scan(dest...); err != nil {
						rows.Close()
						t.Fatal(err)
					}
					records = append(records, values)
				}
				err = rows.Err()
				rows.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			data, err := json.Marshal(records)
			if err != nil {
				t.Fatal(err)
			}
			return string(data)
		}
		before := snapshot()
		status, body := memifyPost(t, newMemifyApp(t, APIConfig{DB: f.db}), memifyRequest{
			Dataset: "beta", EnrichmentTasks: []string{"triplet_embeddings"},
		})
		if status != 200 || body["status"] != "COMPLETED" {
			t.Fatalf("memify status=%d body=%v", status, body)
		}
		if after := snapshot(); after != before {
			t.Fatalf("memify changed temporal episodes or source metadata: before=%s after=%s", before, after)
		}
	})
}

func TestMemifyAuthenticatedRoutesHaveNoEffects(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		const runID = "memify-private-run"
		memifyRuns.Store(runID, &memifyRunStatus{RunID: runID, Status: "COMPLETED", Message: "private graph details"})
		defer memifyRuns.Delete(runID)
		for _, cfg := range []APIConfig{{DB: f.db, RequireAuth: true}, {DB: f.db}} {
			app := fiber.New(fiber.Config{DisableStartupMessage: true})
			app.Use(func(c *fiber.Ctx) error { c.Locals("user_id", "alice"); return c.Next() })
			registerMemifyAPI(app, cfg)
			for _, route := range []struct{ method, path string }{
				{"POST", "/memify"}, {"GET", "/memify/" + runID + "/status"}, {"GET", "/memify/" + runID + "/stream"},
			} {
				req := httptest.NewRequest(route.method, route.path, bytes.NewBufferString(`{"dataset":"alpha","enrichment_tasks":["summary_generation"]}`))
				req.Header.Set("Content-Type", "application/json")
				resp, err := app.Test(req, -1)
				if err != nil {
					t.Fatal(err)
				}
				data, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.StatusCode != 503 || bytes.Contains(data, []byte("private graph details")) {
					t.Fatalf("%s %s status=%d body=%s", route.method, route.path, resp.StatusCode, data)
				}
			}
		}
		var count int
		if err := f.db.QueryRow("SELECT COUNT(*) FROM graph_nodes").Scan(&count); err != nil || count != 0 {
			t.Fatalf("rejected memify wrote nodes: count=%d err=%v", count, err)
		}
	})
}

func TestMemifyConsolidationPreservesSourceMetadata(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.exec(`INSERT INTO graph_nodes(id,name,type,description,properties,dataset_id) VALUES
		 ('memify-primary','Same','Entity','First description','{"document_id":"original","content_revision":3,"generation":7,"collection":"Chunks"}','alpha'),
		 ('memify-secondary','Same','Entity','Second description','{"document_id":"other"}','alpha')`)
		var calls atomic.Int32
		llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Merged description"}}]}`))
		}))
		defer llm.Close()
		t.Setenv("LLM_ENDPOINT", llm.URL)
		t.Setenv("LLM_MODEL", "test-model")
		status, body := memifyPost(t, newMemifyApp(t, APIConfig{DB: f.db}), memifyRequest{Dataset: "beta", EnrichmentTasks: []string{"entity_consolidation"}})
		if status != 200 || body["status"] != "COMPLETED" || calls.Load() != 1 {
			t.Fatalf("local consolidation status=%d body=%v calls=%d", status, body, calls.Load())
		}
		var dataset, description, encoded string
		if err := f.db.QueryRow("SELECT dataset_id,description,CAST(properties AS TEXT) FROM graph_nodes WHERE id='memify-primary'").Scan(&dataset, &description, &encoded); err != nil {
			t.Fatal(err)
		}
		var props map[string]any
		if err := json.Unmarshal([]byte(encoded), &props); err != nil {
			t.Fatal(err)
		}
		if dataset != "alpha" || description != "Merged description" || props["document_id"] != "original" || props["content_revision"] != float64(3) || props["generation"] != float64(7) || props["collection"] != "Chunks" {
			t.Fatalf("consolidation lost source metadata: dataset=%s description=%s props=%v", dataset, description, props)
		}
	})
}
