package http

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
	_ "github.com/ncruces/go-sqlite3/driver"
)

func taskReadTestApp(t *testing.T) (*fiber.App, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite3", t.TempDir()+"/tasks.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS tasks (
			id TEXT PRIMARY KEY, idempotency_key TEXT NOT NULL, owner_id TEXT NOT NULL DEFAULT '',
			collection_name TEXT NOT NULL, room TEXT NOT NULL, objective TEXT NOT NULL,
			authority_json TEXT NOT NULL DEFAULT '{}', risk_level TEXT NOT NULL DEFAULT 'medium',
			status TEXT NOT NULL DEFAULT 'draft', version INTEGER NOT NULL DEFAULT 1,
			current_workspace_revision TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP, completed_at TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS task_criteria (
			id TEXT NOT NULL, task_id TEXT NOT NULL, description TEXT NOT NULL,
			required BOOLEAN NOT NULL DEFAULT TRUE, verification_json TEXT NOT NULL DEFAULT '{}',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP, PRIMARY KEY(task_id, id)
		)`,
		`CREATE TABLE IF NOT EXISTS task_steps (
			id TEXT NOT NULL, task_id TEXT NOT NULL, description TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending', required BOOLEAN NOT NULL DEFAULT TRUE,
			dependencies_json TEXT NOT NULL DEFAULT '[]', criterion_ids_json TEXT NOT NULL DEFAULT '[]',
			attempts INTEGER NOT NULL DEFAULT 0, position INTEGER NOT NULL DEFAULT 0,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP, PRIMARY KEY(task_id, id)
		)`,
		`CREATE TABLE IF NOT EXISTS task_leases (
			step_id TEXT NOT NULL, task_id TEXT NOT NULL, actor_id TEXT NOT NULL,
			expires_at TIMESTAMP NOT NULL, created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY(task_id, step_id)
		)`,
		`CREATE TABLE IF NOT EXISTS task_receipts (
			id TEXT PRIMARY KEY, task_id TEXT NOT NULL, idempotency_key TEXT NOT NULL,
			owner_id TEXT NOT NULL DEFAULT '', receipt_type TEXT NOT NULL, status TEXT NOT NULL,
			criterion_ids_json TEXT NOT NULL DEFAULT '[]', observation TEXT NOT NULL DEFAULT '',
			exit_code INTEGER, evidence_uri TEXT NOT NULL DEFAULT '',
			artifact_digest TEXT NOT NULL DEFAULT '', workspace_revision TEXT NOT NULL DEFAULT '',
			metadata_json TEXT NOT NULL DEFAULT '{}',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS task_checkpoints (
			id TEXT PRIMARY KEY, task_id TEXT NOT NULL, idempotency_key TEXT NOT NULL,
			step_id TEXT NOT NULL DEFAULT '', summary TEXT NOT NULL,
			verified_json TEXT NOT NULL DEFAULT '[]', failed_json TEXT NOT NULL DEFAULT '[]',
			next_action TEXT NOT NULL DEFAULT '', workspace_revision TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS task_blockers (
			id TEXT PRIMARY KEY, task_id TEXT NOT NULL, reason TEXT NOT NULL,
			required_decision TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'active',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP, resolved_at TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS task_events (
			id TEXT PRIMARY KEY, task_id TEXT NOT NULL, actor_id TEXT NOT NULL DEFAULT '',
			event_type TEXT NOT NULL, payload_json TEXT NOT NULL DEFAULT '{}',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	SetDBProvider(DBSQLite)
	t.Cleanup(func() { SetDBProvider(DBPostgres) })
	app := fiber.New()
	RegisterTaskReadAPI(app, APIConfig{DB: db})
	return app, db
}

func taskSeed(t *testing.T, db *sql.DB) string {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO tasks (id, idempotency_key, owner_id, collection_name, room, objective, status)
		VALUES ('t-1', 'ik-1', 'agent:reviewer', 'levara', 'memory', 'ship the thing', 'in_progress')`); err != nil {
		t.Fatal(err)
	}
	stmts := []string{
		`INSERT INTO task_steps (id, task_id, description, status, position) VALUES ('s-1','t-1','first','passed',0)`,
		`INSERT INTO task_steps (id, task_id, description, status, position) VALUES ('s-2','t-1','second','claimed',1)`,
		`INSERT INTO task_steps (id, task_id, description, status, position) VALUES ('s-3','t-1','third','pending',2)`,
		// Live lease on s-2, expired lease on s-1 (must not surface).
		`INSERT INTO task_leases (step_id, task_id, actor_id, expires_at) VALUES ('s-2','t-1','agent:worker', datetime('now','+10 minutes'))`,
		`INSERT INTO task_leases (step_id, task_id, actor_id, expires_at) VALUES ('s-1','t-1','agent:stale', datetime('now','-10 minutes'))`,
		`INSERT INTO task_receipts (id, task_id, idempotency_key, receipt_type, status, observation) VALUES ('r-1','t-1','rk-1','command','pass','tests green')`,
		`INSERT INTO task_checkpoints (id, task_id, idempotency_key, summary) VALUES ('c-1','t-1','ck-1','step one verified')`,
		`INSERT INTO task_blockers (id, task_id, reason, status) VALUES ('b-1','t-1','needs human decision','active')`,
		`INSERT INTO task_events (id, task_id, actor_id, event_type) VALUES ('e-1','t-1','agent:reviewer','task_opened')`,
	}
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return "t-1"
}

func taskGetJSON(t *testing.T, app *fiber.App, path string) (int, map[string]any) {
	t.Helper()
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, path, nil))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(body, &m)
	return resp.StatusCode, m
}

func TestTaskReadList(t *testing.T) {
	app, db := taskReadTestApp(t)
	taskSeed(t, db)
	code, m := taskGetJSON(t, app, "/tasks")
	if code != 200 {
		t.Fatalf("list: %d %v", code, m)
	}
	tasks := m["tasks"].([]any)
	if len(tasks) != 1 || m["count"].(float64) != 1 {
		t.Fatalf("list shape: %v", m)
	}
	first := tasks[0].(map[string]any)
	if first["id"] != "t-1" || first["status"] != "in_progress" {
		t.Fatalf("task fields: %v", first)
	}
	counts := first["step_counts"].(map[string]any)
	// sqlite (modernc/ncruces) may not support FILTER — if counts are zero the
	// aggregate path failed silently; assert what the DB actually returned.
	if counts["passed"].(float64) != 1 || counts["claimed"].(float64) != 1 || counts["pending"].(float64) != 1 {
		t.Fatalf("step counts wrong: %v", counts)
	}
	if first["blocker_count"].(float64) != 1 {
		t.Fatalf("blocker count wrong: %v", first)
	}
}

func TestTaskReadListFilters(t *testing.T) {
	app, db := taskReadTestApp(t)
	taskSeed(t, db)
	code, m := taskGetJSON(t, app, "/tasks?status=done")
	if code != 200 || m["count"].(float64) != 0 {
		t.Fatalf("status filter: %d %v", code, m)
	}
	code2, m2 := taskGetJSON(t, app, "/tasks?collection_name=levara")
	if code2 != 200 || m2["count"].(float64) != 1 {
		t.Fatalf("collection filter: %d %v", code2, m2)
	}
}

func TestTaskReadDetail(t *testing.T) {
	app, db := taskReadTestApp(t)
	id := taskSeed(t, db)
	code, m := taskGetJSON(t, app, "/tasks/"+id)
	if code != 200 {
		t.Fatalf("detail: %d %v", code, m)
	}
	steps := m["steps"].([]any)
	if len(steps) != 3 {
		t.Fatalf("steps: %v", m["steps"])
	}
	// s-2 carries the live lease.
	var leased map[string]any
	for _, raw := range steps {
		s := raw.(map[string]any)
		if s["id"] == "s-2" {
			leased = s
		}
		if s["id"] == "s-1" && s["leased_by"] != nil && s["leased_by"] != "" {
			t.Fatalf("expired lease surfaced: %v", s)
		}
	}
	if leased == nil || leased["leased_by"] != "agent:worker" {
		t.Fatalf("live lease missing: %v", leased)
	}
	if len(m["receipts"].([]any)) != 1 || len(m["checkpoints"].([]any)) != 1 || len(m["blockers"].([]any)) != 1 {
		t.Fatalf("detail collections: %v", m)
	}
	if len(m["recent_events"].([]any)) != 1 {
		t.Fatalf("events: %v", m["recent_events"])
	}
}

func TestTaskReadDetailNotFound(t *testing.T) {
	app, _ := taskReadTestApp(t)
	code, _ := taskGetJSON(t, app, "/tasks/ghost")
	if code != 404 {
		t.Fatalf("ghost: %d", code)
	}
}

func TestTaskReadDisabledWithoutDB(t *testing.T) {
	app := fiber.New()
	RegisterTaskReadAPI(app, APIConfig{DB: nil})
	code, _ := taskGetJSON(t, app, "/tasks")
	if code != 404 {
		t.Fatalf("routes registered without DB: %d", code)
	}
}
