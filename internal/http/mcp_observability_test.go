package http

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/stek0v/levara/pkg/runreg"

	_ "github.com/ncruces/go-sqlite3/driver"
)

// observabilityTestHandler builds a minimal *mcpHandler backed by an
// in-memory SQLite database and a fresh runreg.Registry. The DB has just
// the heartbeats table — toolRecentErrors / toolSyncStatus only ever
// touch that table.
func observabilityTestHandler(t *testing.T) (*mcpHandler, *sql.DB) {
	t.Helper()
	prev := GetDBProvider()
	SetDBProvider(DBSQLite)
	t.Cleanup(func() { SetDBProvider(prev) })

	dir := t.TempDir()
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(dir, "obs.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if _, err := db.Exec(`CREATE TABLE heartbeats (
		id TEXT PRIMARY KEY,
		event_type TEXT NOT NULL,
		payload TEXT NOT NULL DEFAULT '{}',
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		t.Fatalf("schema: %v", err)
	}

	h := &mcpHandler{cfg: APIConfig{
		DB:            db,
		Runs:          runreg.New(),
		EmbedEndpoint: "http://embed.test:9001",
		EmbedModel:    "test-embed",
	}}
	return h, db
}

func decodeText(t *testing.T, r mcpToolResult) map[string]any {
	t.Helper()
	if len(r.Content) == 0 {
		t.Fatalf("empty content; isError=%v", r.IsError)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(r.Content[0].Text), &m); err != nil {
		t.Fatalf("decode payload: %v\n%s", err, r.Content[0].Text)
	}
	return m
}

func TestToolRuntimeStats_BasicShape(t *testing.T) {
	h, _ := observabilityTestHandler(t)
	res := h.toolRuntimeStats(context.Background(), nil)
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content[0].Text)
	}
	out := decodeText(t, res)

	for _, k := range []string{
		"collections", "collection_count", "total_records",
		"embed_endpoint", "embed_model", "llm_provider", "llm_model",
		"rerank_enabled", "neo4j_enabled", "goroutines",
		"heap_alloc_bytes", "heap_sys_bytes", "num_gc", "snapshot_taken_at",
	} {
		if _, ok := out[k]; !ok {
			t.Errorf("missing key %q in runtime_stats output", k)
		}
	}
	if got := out["embed_endpoint"]; got != "http://embed.test:9001" {
		t.Errorf("embed_endpoint = %v, want test value", got)
	}
	if got, _ := out["rerank_enabled"].(bool); got {
		t.Errorf("rerank_enabled = true, want false (no endpoint configured)")
	}
	if g, _ := out["goroutines"].(float64); g <= 0 {
		t.Errorf("goroutines = %v, want > 0", g)
	}
}

func TestToolIngestionStatus_StateTransitionsAndSummary(t *testing.T) {
	h, _ := observabilityTestHandler(t)
	now := time.Now()
	h.cfg.Runs.Store("r-running", &runreg.Status{RunID: "r-running", Status: "RUNNING", Stage: "embed", StartedAt: now})
	h.cfg.Runs.Store("r-done", &runreg.Status{RunID: "r-done", Status: "COMPLETED", Stage: "write", StartedAt: now.Add(-time.Minute)})
	h.cfg.Runs.Store("r-fail", &runreg.Status{RunID: "r-fail", Status: "FAILED", Stage: "extract", Message: "boom", StartedAt: now.Add(-2 * time.Minute)})

	out := decodeText(t, h.toolIngestionStatus(context.Background(), nil))
	summary, ok := out["summary"].(map[string]any)
	if !ok {
		t.Fatalf("summary missing/wrong type: %T", out["summary"])
	}
	if summary["total"].(float64) != 3 {
		t.Errorf("total = %v, want 3", summary["total"])
	}
	if summary["running"].(float64) != 1 || summary["completed"].(float64) != 1 || summary["failed"].(float64) != 1 {
		t.Errorf("counts = %+v, want 1/1/1", summary)
	}
	runs := out["runs"].([]any)
	if len(runs) != 3 {
		t.Errorf("runs len = %d, want 3", len(runs))
	}

	// Filter by status
	filtered := decodeText(t, h.toolIngestionStatus(context.Background(), map[string]any{"status": "FAILED"}))
	got := filtered["runs"].([]any)
	if len(got) != 1 {
		t.Fatalf("filtered runs = %d, want 1", len(got))
	}
	if got[0].(map[string]any)["pipeline_run_id"] != "r-fail" {
		t.Errorf("filtered run = %v, want r-fail", got[0])
	}
}

func TestToolRecentErrors_AggregatesAndCaps(t *testing.T) {
	h, db := observabilityTestHandler(t)
	now := time.Now()

	// Two failed runs in the registry.
	h.cfg.Runs.Store("f1", &runreg.Status{RunID: "f1", Status: "FAILED", Stage: "embed", Message: "embed timeout", StartedAt: now.Add(-time.Minute)})
	h.cfg.Runs.Store("f2", &runreg.Status{RunID: "f2", Status: "FAILED", Stage: "write", Message: "wal full", StartedAt: now.Add(-2 * time.Minute)})
	h.cfg.Runs.Store("ok1", &runreg.Status{RunID: "ok1", Status: "COMPLETED", StartedAt: now}) // must not appear

	// One doctor heartbeat with two failing checks.
	payload := `{"status":"degraded","checks":[
		{"name":"neo4j","status":"fail","message":"connection refused"},
		{"name":"embed","status":"ok","message":"healthy"},
		{"name":"llm","status":"fail","message":"401"}
	]}`
	if _, err := db.Exec(`INSERT INTO heartbeats(id,event_type,payload,created_at) VALUES(?,?,?,?)`,
		"hb1", "doctor", payload, now.Format(time.RFC3339)); err != nil {
		t.Fatalf("seed heartbeat: %v", err)
	}

	out := decodeText(t, h.toolRecentErrors(context.Background(), nil))
	count := int(out["count"].(float64))
	if count != 4 {
		t.Errorf("count = %d, want 4 (2 failed runs + 2 failing checks)", count)
	}
	errs := out["errors"].([]any)
	sources := map[string]int{}
	for _, e := range errs {
		sources[e.(map[string]any)["source"].(string)]++
	}
	if sources["pipeline_run"] != 2 || sources["doctor"] != 2 {
		t.Errorf("sources = %+v, want 2 pipeline_run + 2 doctor", sources)
	}

	// Limit cap.
	capped := decodeText(t, h.toolRecentErrors(context.Background(), map[string]any{"limit": float64(2)}))
	if int(capped["count"].(float64)) != 2 {
		t.Errorf("capped count = %v, want 2", capped["count"])
	}
}

func TestToolSyncStatus_NoDB(t *testing.T) {
	h := &mcpHandler{cfg: APIConfig{DB: nil}}
	res := h.toolSyncStatus(context.Background(), nil)
	if !res.IsError {
		t.Fatalf("expected IsError when DB is nil")
	}
}

func TestToolSyncStatus_NoEventsReturnsEmptyArray(t *testing.T) {
	h, _ := observabilityTestHandler(t)
	out := decodeText(t, h.toolSyncStatus(context.Background(), nil))
	events, ok := out["events"].([]any)
	if !ok {
		t.Fatalf("events should be []any, got %T", out["events"])
	}
	if len(events) != 0 {
		t.Fatalf("events len = %d, want 0", len(events))
	}
}

func TestToolSyncStatus_GroupsByDirection(t *testing.T) {
	h, db := observabilityTestHandler(t)
	now := time.Now()
	rows := []struct {
		id, payload, at string
	}{
		{"s1", `{"direction":"push","remote":"http://a","types":["mem"]}`, now.Format(time.RFC3339)},
		{"s2", `{"direction":"pull","remote":"http://b","types":["mem"]}`, now.Add(-time.Minute).Format(time.RFC3339)},
		{"s3", `{"direction":"push","remote":"http://a","types":["mem","feedback"]}`, now.Add(-2 * time.Minute).Format(time.RFC3339)},
	}
	for _, r := range rows {
		if _, err := db.Exec(`INSERT INTO heartbeats(id,event_type,payload,created_at) VALUES(?,?,?,?)`,
			r.id, "sync", r.payload, r.at); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	out := decodeText(t, h.toolSyncStatus(context.Background(), nil))
	byDir, ok := out["by_direction"].(map[string]any)
	if !ok {
		t.Fatalf("by_direction missing: %T", out["by_direction"])
	}
	push, _ := byDir["push"].(map[string]any)
	pull, _ := byDir["pull"].(map[string]any)
	if push == nil || pull == nil {
		t.Fatalf("missing direction buckets: %+v", byDir)
	}
	if push["count"].(float64) != 2 {
		t.Errorf("push count = %v, want 2", push["count"])
	}
	if pull["count"].(float64) != 1 {
		t.Errorf("pull count = %v, want 1", pull["count"])
	}
	if push["last_remote"] != "http://a" {
		t.Errorf("push last_remote = %v, want http://a", push["last_remote"])
	}
	events := out["events"].([]any)
	if len(events) != 3 {
		t.Errorf("events len = %d, want 3", len(events))
	}
}
