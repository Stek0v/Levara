package http

import (
	"context"
	"database/sql"
	"encoding/json"
	_ "github.com/ncruces/go-sqlite3/driver"
	"path/filepath"
	"testing"
	"time"
)

func TestConsolidateAsyncReturnsImmediatelyAndCompletes(t *testing.T) {
	prev := GetDBProvider()
	SetDBProvider(DBSQLite)
	defer SetDBProvider(prev)
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE memories(id TEXT PRIMARY KEY,key TEXT,value TEXT,room TEXT,hall TEXT,collection_name TEXT,owner_id TEXT DEFAULT '',is_pinned INTEGER DEFAULT 0,tier TEXT DEFAULT 'raw',superseded_by TEXT DEFAULT '',created_at TEXT,updated_at TEXT)`)
	if err != nil {
		t.Fatal(err)
	}
	h := &mcpHandler{cfg: APIConfig{DB: db}}
	started := time.Now()
	res := h.toolConsolidateAsync(context.Background(), map[string]any{"collection": "levara", "dry_run": true})
	if res.IsError {
		t.Fatalf("enqueue: %+v", res)
	}
	if time.Since(started) > 100*time.Millisecond {
		t.Fatal("async enqueue blocked")
	}
	var body map[string]any
	if err = json.Unmarshal([]byte(res.Content[0].Text), &body); err != nil {
		t.Fatal(err)
	}
	id, _ := body["job_id"].(string)
	if id == "" {
		t.Fatal("missing job_id")
	}
	deadline := time.Now().Add(time.Second)
	for {
		status := h.toolConsolidationStatus(context.Background(), map[string]any{"job_id": id})
		var got map[string]any
		_ = json.Unmarshal([]byte(status.Content[0].Text), &got)
		if got["status"] == "completed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job did not complete: %v", got)
		}
		time.Sleep(time.Millisecond)
	}
}
