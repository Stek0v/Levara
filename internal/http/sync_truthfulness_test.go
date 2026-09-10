package http

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/adaptor"
)

func syncTruthDB(t *testing.T) *sql.DB {
	t.Helper()
	SetDBProvider(DBSQLite)
	db := newSyncIntegDB(t, "truth")
	t.Cleanup(func() { db.Close(); SetDBProvider(DBPostgres) })
	return db
}

func TestSyncTruthPullTransportFailures(t *testing.T) {
	for _, kind := range []string{"memories", "interactions", "graph"} {
		for _, failure := range []string{"http", "json", "truncated", "offline"} {
			t.Run(kind+"/"+failure, func(t *testing.T) {
				db := syncTruthDB(t)
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch failure {
					case "http":
						w.WriteHeader(503)
						fmt.Fprint(w, `[]`)
					case "json":
						fmt.Fprint(w, `not json`)
					case "truncated":
						w.Header().Set("Content-Length", "100")
						fmt.Fprint(w, `[]`)
					}
				}))
				t.Cleanup(srv.Close)
				if failure == "offline" {
					srv.Close()
				}
				result := SyncPull(APIConfig{DB: db}, srv.URL, []string{kind}, "")
				if result[kind+"_error"] == nil {
					t.Errorf("%s failure became success: %#v", failure, result)
				}
			})
		}
	}
}

func TestSyncTruthManifestRejectsHTTPAndTrailingJSON(t *testing.T) {
	for _, payload := range []string{`{"detail":"unavailable"}`, `{"version":"v1"} {}`} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(payload, "detail") {
				w.WriteHeader(503)
			}
			fmt.Fprint(w, payload)
		}))
		_, err := SyncManifestFromRemote(srv.URL, "")
		srv.Close()
		if err == nil {
			t.Errorf("invalid manifest accepted: %s", payload)
		}
	}
}

func syncTruthApp(t *testing.T, db *sql.DB) *fiber.App {
	t.Helper()
	app := fiber.New()
	RegisterSyncAPI(app, APIConfig{DB: db, Version: "test"})
	return app
}

func TestSyncTruthPartialImportRetainsSuccess(t *testing.T) {
	db := syncTruthDB(t)
	// A primary-key collision is an actual row-level SQL error; the following
	// independent row must still import under the existing partial contract.
	seedMemory(t, db, "old", "unchanged")
	data := []syncMemory{{ID: "id-old", Key: "collision", Value: "bad", CreatedAt: "2026-09-05T00:00:00Z", UpdatedAt: "2026-09-05T00:00:00Z"}, {ID: "new", Key: "new", Value: "good", CreatedAt: "2026-09-05T00:00:00Z", UpdatedAt: "2026-09-05T00:00:00Z"}}
	payload, _ := json.Marshal(data)
	app := syncTruthApp(t, db)
	req := httptest.NewRequest("POST", "/sync/import/memories", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result["imported"] != float64(1) || result["failed"] != float64(1) || result["error"] == nil {
		t.Errorf("partial SQL error hidden: status=%d result=%#v", resp.StatusCode, result)
	}
	if !memoryExists(t, db, "new") {
		t.Error("valid independent row was lost")
	}
}

func TestSyncTruthDoSyncPartialAndRecovery(t *testing.T) {
	db := syncTruthDB(t)
	var fail atomic.Bool
	fail.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sync/manifest":
			fmt.Fprint(w, `{"version":"test"}`)
		case "/sync/export/memories":
			if fail.Load() {
				w.WriteHeader(503)
				fmt.Fprint(w, `[]`)
			} else {
				fmt.Fprint(w, `[]`)
			}
		case "/sync/export/interactions":
			fmt.Fprint(w, `[]`)
		}
	}))
	defer srv.Close()
	h := &mcpHandler{cfg: APIConfig{DB: db}}
	result, _, err := h.DoSync(context.Background(), srv.URL, "pull", []string{"memories", "interactions"}, "", nil)
	if err != nil || result["status"] != "partial" || result["memories_error"] == nil || result["interactions"] == nil {
		t.Errorf("partial result lost: %#v err=%v", result, err)
	}
	fail.Store(false)
	result, _, err = h.DoSync(context.Background(), srv.URL, "pull", []string{"memories", "interactions"}, "", nil)
	if err != nil || result["status"] != "ok" {
		t.Errorf("recovery state=%#v err=%v", result, err)
	}
}

func TestSyncTruthPushErrorsAndInteractions(t *testing.T) {
	for _, kind := range []string{"memories", "interactions", "graph"} {
		t.Run(kind, func(t *testing.T) {
			db := syncTruthDB(t)
			seedMemory(t, db, "push", "test")
			if _, err := db.Exec(`INSERT INTO interactions(id,created_at)VALUES('i','2026-09-05');INSERT INTO graph_nodes(id,name,type)VALUES('n','N','entity')`); err != nil {
				t.Fatal(err)
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503); fmt.Fprint(w, `{"imported":0}`) }))
			defer srv.Close()
			result := syncPush(context.Background(), APIConfig{DB: db}, srv.URL, []string{kind}, "")
			if result[kind+"_error"] == nil {
				t.Errorf("push failed silently: %#v", result)
			}
			db.Close()
			result = syncPush(context.Background(), APIConfig{DB: db}, srv.URL, []string{kind}, "")
			if result[kind+"_error"] == nil {
				t.Errorf("closed DB became empty success: %#v", result)
			}
		})
	}
}

func TestSyncTruthIndependentNodes(t *testing.T) {
	local := syncTruthDB(t)
	remote := syncTruthDB(t)
	seedMemory(t, remote, "remote-only", "from second database")
	if memoryExists(t, local, "remote-only") {
		t.Fatal("nodes share state before transport")
	}
	srv := httptest.NewServer(adaptor.FiberApp(syncTruthApp(t, remote)))
	defer srv.Close()
	h := &mcpHandler{cfg: APIConfig{DB: local}}
	result, _, err := h.DoSync(context.Background(), srv.URL, "pull", []string{"memories"}, "", nil)
	if err != nil || result["status"] != "ok" || !memoryExists(t, local, "remote-only") {
		t.Fatalf("independent pull: %#v err=%v", result, err)
	}
	if _, err := local.Exec(`INSERT INTO interactions(id,query,response,created_at)VALUES('i','hello','world','2026-09-05')`); err != nil {
		t.Fatal(err)
	}
	result, _, err = h.DoSync(context.Background(), srv.URL, "push", []string{"interactions"}, "", nil)
	var count int
	if e := remote.QueryRow(`SELECT COUNT(*) FROM interactions WHERE id='i'`).Scan(&count); e != nil {
		t.Fatal(e)
	}
	if err != nil || result["status"] != "ok" || count != 1 {
		t.Errorf("independent interactions push: %#v count=%d err=%v", result, count, err)
	}
}

func syncTruthPostgresDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("LEVARA_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("LEVARA_TEST_POSTGRES_DSN unset")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin := stdlib.OpenDB(*config)
	name := fmt.Sprintf("sync_truth_%d", time.Now().UnixNano())
	if _, err = admin.Exec("CREATE DATABASE " + name); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	config.Database = name
	db := stdlib.OpenDB(*config)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() {
		db.Close()
		if _, err := admin.Exec("DROP DATABASE " + name); err != nil {
			t.Error(err)
		}
		admin.Close()
		SetDBProvider(DBPostgres)
	})
	SetDBProvider(DBPostgres)
	if err := MigrateSchema(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestSyncTruthPostgresIndependentNodesAndPartialSQL(t *testing.T) {
	local := syncTruthPostgresDB(t)
	remote := syncTruthPostgresDB(t)
	// Two CREATE DATABASE calls above, not two HTTP processes sharing tables.
	if _, err := remote.Exec(`INSERT INTO memories(id,key,value)VALUES('remote','remote-only','remote value');INSERT INTO graph_nodes(id,name)VALUES('node','Node');INSERT INTO graph_edges(id,source_id,target_id,relationship_name,valid_from)VALUES('edge','node','node','knows','2026-09-05T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := exportSyncGraph(context.Background(), APIConfig{DB: remote}); err != nil {
		t.Errorf("direct PG graph export: %v", err)
	}
	if _, err := importSyncGraph(context.Background(), APIConfig{DB: local}, syncGraph{Edges: []syncGraphEdge{{ID: "direct", SourceID: "a", TargetID: "b", Properties: "{}", ValidFrom: "2026-09-05T00:00:00Z"}}}); err != nil {
		t.Errorf("direct PG graph import: %v", err)
	}

	var count int
	if err := local.QueryRow(`SELECT COUNT(*) FROM memories WHERE id='remote'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("nodes share initial data: %d %v", count, err)
	}
	srv := httptest.NewServer(adaptor.FiberApp(syncTruthApp(t, remote)))
	defer srv.Close()
	h := &mcpHandler{cfg: APIConfig{DB: local}}
	result, _, err := h.DoSync(context.Background(), srv.URL, "pull", []string{"memories", "graph"}, "", nil)
	if err != nil || result["status"] != "ok" {
		t.Errorf("PG pull: %#v err=%v", result, err)
	}
	if err := local.QueryRow(`SELECT COUNT(*) FROM graph_edges WHERE id='edge'`).Scan(&count); err != nil || count != 1 {
		t.Errorf("PG graph absent: %d %v", count, err)
	}
	if _, err := local.Exec(`INSERT INTO interactions(id,query)VALUES('local-interaction','hello')`); err != nil {
		t.Fatal(err)
	}
	result, _, err = h.DoSync(context.Background(), srv.URL, "push", []string{"interactions"}, "", nil)
	if err != nil || result["status"] != "ok" {
		t.Errorf("PG push: %#v err=%v", result, err)
	}
	if err := remote.QueryRow(`SELECT COUNT(*) FROM interactions WHERE id='local-interaction'`).Scan(&count); err != nil || count != 1 {
		t.Errorf("PG interaction absent: %d %v", count, err)
	}
	records := []syncMemory{{ID: "remote", Key: "collision", Value: "collision", CreatedAt: "2026-09-05T00:00:00Z", UpdatedAt: "2026-09-05T00:00:00Z"}, {ID: "new", Key: "new", Value: "accepted", CreatedAt: "2026-09-05T00:00:00Z", UpdatedAt: "2026-09-05T00:00:00Z"}}
	counts, _, importErr := importSyncMemories(context.Background(), APIConfig{DB: local}, records)
	if importErr == nil || counts["failed"] != 1 || counts["imported"] != 1 {
		t.Errorf("PG partial import hidden: %v err=%v", counts, importErr)
	}
}

func TestSyncTruthRESTHeartbeatRecordsFailure(t *testing.T) {
	db := syncTruthDB(t)
	if _, err := db.Exec(`CREATE TABLE heartbeats(id TEXT PRIMARY KEY,event_type TEXT,payload TEXT,created_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sync/manifest" {
			fmt.Fprint(w, `{"version":"test"}`)
			return
		}
		w.WriteHeader(503)
		fmt.Fprint(w, `[]`)
	}))
	defer remote.Close()
	app := syncTruthApp(t, db)
	payload, _ := json.Marshal(map[string]any{"remote_url": remote.URL, "types": []string{"memories"}})
	req := httptest.NewRequest("POST", "/sync/run", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	response, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	var recorded string
	if err := db.QueryRow(`SELECT payload FROM heartbeats WHERE event_type='sync'`).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(recorded), &event); err != nil {
		t.Fatal(err)
	}
	if event["status"] != "error" {
		t.Errorf("failed export heartbeat=%v", event)
	}
}

func TestSyncTruthSQLFailuresNeverExportAsEmpty(t *testing.T) {
	db := syncTruthDB(t)
	app := syncTruthApp(t, db)
	db.Close()
	for _, path := range []string{"manifest", "export/memories", "export/interactions", "export/graph"} {
		response, err := app.Test(httptest.NewRequest("GET", "/sync/"+path, nil))
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode < 500 {
			t.Errorf("%s closed DB returned %d", path, response.StatusCode)
		}
	}
}

func TestSyncTruthRequestCancellationAndSinceEscaping(t *testing.T) {
	db := syncTruthDB(t)
	since := "2026-09-05T12:00:00+03:00&surprise=yes"
	received := make(chan string, 1)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- r.URL.Query().Get("since")
		if r.URL.Query().Get("surprise") != "" {
			t.Error("since injected a query parameter")
		}
		<-r.Context().Done()
	}))
	defer remote.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	result := syncPullContext(ctx, APIConfig{DB: db}, remote.URL, []string{"memories"}, since)
	if result["memories_error"] == nil {
		t.Errorf("cancelled request became success: %v", result)
	}
	if got := <-received; got != since {
		t.Errorf("since=%q want %q", got, since)
	}
}

func TestSyncTruthCollectionsRejectHTTPJSONAndUnavailableImporter(t *testing.T) {
	for _, mode := range []string{"http", "json", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "http" {
					w.WriteHeader(503)
				}
				if mode == "json" {
					fmt.Fprint(w, `invalid`)
					return
				}
				fmt.Fprint(w, `{"collection":"docs","records":[{"id":"doc","text":"hello"}]}`)
			}))
			defer srv.Close()
			result := syncPullCollections(APIConfig{}, srv.URL, []string{"docs"})
			status := syncResultStatus(map[string]any{"collections_sync": result})
			if status != "error" {
				t.Errorf("collection %s hidden: %s %v", mode, status, result)
			}
		})
	}
}

func TestSyncTruthAsyncCollectionFailureIsNotCompleted(t *testing.T) {
	cfg, cleanup := newWorkspaceTestConfig(t)
	defer cleanup()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			fmt.Fprint(w, `{"data":[{"embedding":[1,0,0],"index":0}]}`)
			return
		}
		w.WriteHeader(503)
		fmt.Fprint(w, `{"error":"test embedding outage"}`)
	}))
	defer srv.Close()
	cfg.EmbedEndpoint = srv.URL
	result, code := startSyncCollectionImport(cfg, syncCollectionExport{Collection: "async-failure", Records: []syncCollectionRecord{{ID: "r1", Text: "hello", Metadata: json.RawMessage(`{}`)}}})
	if code != 200 || syncResultStatus(map[string]any{"collections_sync": map[string]any{"async-failure": result}}) != "running" {
		t.Fatalf("start result=%v code=%d", result, code)
	}
	runID := result["run_id"].(string)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status, _ := syncImportRuns.Load(runID)
		if status.Status != "RUNNING" {
			if status.Status != "FAILED" || status.Failed != 1 || status.Processed != 0 {
				t.Errorf("failed async batch reported: %+v", status)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("async failure not recorded")
}

func TestSyncTruthPushRejectsIncompleteAcknowledgement(t *testing.T) {
	for _, ack := range []string{`{"imported":0,"skipped":0,"total":1}`, `{"imported":"one","total":1}`, `{"imported":0,"skipped":0,"total":0}`} {
		db := syncTruthDB(t)
		seedMemory(t, db, "ack", "value")
		remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, ack) }))
		result := syncPush(context.Background(), APIConfig{DB: db}, remote.URL, []string{"memories"}, "")
		remote.Close()
		if result["memories_error"] == nil {
			t.Errorf("incomplete acknowledgement accepted: %s result=%v", ack, result)
		}
	}
}

func TestSyncTruthCollectionAcknowledgementShape(t *testing.T) {
	cfg, cleanup := newWorkspaceTestConfig(t)
	defer cleanup()
	if err := cfg.Collections.Create("ack-docs"); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Collections.Insert("ack-docs", "doc", []float32{1, 0}, json.RawMessage(`{"text":"hello"}`)); err != nil {
		t.Fatal(err)
	}
	for _, ack := range []string{
		`{"status":123}`, `{"status":"unknown"}`, `{"status":"ERROR"}`, `{"status":"Started"}`,
		`{"status":"started"}`, `{"status":"started","run_id":" ","records":1}`,
		`{"status":"started","run_id":123,"records":1}`, `{"status":"started","run_id":"run-1"}`,
		`{"status":"started","run_id":"run-1","records":"1"}`, `{"status":"started","run_id":"run-1","records":0}`,
		`{"status":"started","run_id":"run-1","records":2}`, `{"status":"started","run_id":"run-1","records":1.5}`,
		`{"status":"empty"}`, `{"status":"RUNNING","run_id":"run-1","records":1}`, `{"status":"COMPLETED"}`,
	} {
		t.Run(ack, func(t *testing.T) {
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, ack) }))
			defer remote.Close()
			result := syncPushCollections(context.Background(), cfg, remote.URL, []string{"ack-docs"})
			if status := syncResultStatus(map[string]any{"collections_sync": result}); status != "error" {
				t.Errorf("malformed acknowledgement became %s: %s result=%v", status, ack, result)
			}
		})
	}
	for _, value := range []any{map[string]any{"status": 123}, map[string]any{"status": "unknown"}, map[string]any{"status": "ERROR"}, map[string]any{"status": "started"}} {
		accepted, failed, running := syncTypeOutcome(value)
		if accepted || !failed || running {
			t.Errorf("malformed outcome accepted: %v -> %v,%v,%v", value, accepted, failed, running)
		}
	}
}

func TestSyncTruthRESTSQLDeadline(t *testing.T) {
	t.Setenv("SYNC_REQUEST_TIMEOUT_MS", "40")
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var db *sql.DB
			if dialect == "postgres" {
				db = syncTruthPostgresDB(t)
			} else {
				db = syncTruthDB(t)
			}
			db.SetMaxOpenConns(1)
			// Occupy the only connection until after the request's configured SQL
			// deadline. This does not rely on driver-specific lock timeout behavior.
			conn, err := db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/sync/manifest" {
					fmt.Fprint(w, `{"version":"test"}`)
					return
				}
				fmt.Fprint(w, `[{"id":"late","key":"late","value":"must not import after deadline","created_at":"2026-09-05T00:00:00Z","updated_at":"2026-09-05T00:00:00Z"}]`)
			}))
			defer remote.Close()
			app := syncTruthApp(t, db)
			body, _ := json.Marshal(map[string]any{"remote_url": remote.URL, "types": []string{"memories"}})
			req := httptest.NewRequest("POST", "/sync/run", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			// Even heartbeat persistence must return while SQL is still occupied.
			response, err := app.Test(req, 500)
			conn.Close()
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			var result map[string]any
			if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM memories WHERE id='late'`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 || result["status"] != "error" || result["memories_error"] == nil {
				t.Errorf("REST SQL exceeded configured deadline: rows=%d result=%v", count, result)
			}
		})
	}
}

func TestSyncTruthCollectionAcknowledgementValid(t *testing.T) {
	cfg, cleanup := newWorkspaceTestConfig(t)
	defer cleanup()
	for _, coll := range []string{"full-ack", "empty-ack"} {
		if err := cfg.Collections.Create(coll); err != nil {
			t.Fatal(err)
		}
	}
	if err := cfg.Collections.Insert("full-ack", "doc", []float32{1, 0}, json.RawMessage(`{"text":"hello"}`)); err != nil {
		t.Fatal(err)
	}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input syncCollectionExport
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if len(input.Records) == 0 {
			fmt.Fprint(w, `{"status":"empty"}`)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"status": "started", "run_id": "job-1", "records": len(input.Records), "collection": input.Collection})
	}))
	defer remote.Close()
	for _, tc := range []struct{ collection, status string }{{"full-ack", "running"}, {"empty-ack", "ok"}} {
		results := syncPushCollections(context.Background(), cfg, remote.URL, []string{tc.collection})
		if got := syncResultStatus(map[string]any{"collections_sync": results}); got != tc.status {
			t.Errorf("valid %s acknowledgement -> %s: %v", tc.collection, got, results)
		}
	}
}

func TestSyncTruthRESTManifestDeadline(t *testing.T) {
	t.Setenv("SYNC_REQUEST_TIMEOUT_MS", "40")
	db := syncTruthDB(t)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(200 * time.Millisecond):
			fmt.Fprint(w, `{"version":"test"}`)
		case <-r.Context().Done():
		}
	}))
	defer remote.Close()
	body, _ := json.Marshal(map[string]any{"remote_url": remote.URL, "types": []string{"memories"}})
	req := httptest.NewRequest("POST", "/sync/run", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	response, err := syncTruthApp(t, db).Test(req, 2000)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 400 {
		t.Errorf("manifest ignored configured REST deadline: status=%d", response.StatusCode)
	}
}
