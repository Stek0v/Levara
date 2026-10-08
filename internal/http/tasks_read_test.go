package http

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	_ "github.com/ncruces/go-sqlite3/driver"
	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/mcp"
	"github.com/valyala/fasthttp"
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
			request_digest TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS task_checkpoints (
			id TEXT PRIMARY KEY, task_id TEXT NOT NULL, idempotency_key TEXT NOT NULL,
			step_id TEXT NOT NULL DEFAULT '', summary TEXT NOT NULL,
			verified_json TEXT NOT NULL DEFAULT '[]', failed_json TEXT NOT NULL DEFAULT '[]',
			next_action TEXT NOT NULL DEFAULT '', workspace_revision TEXT NOT NULL DEFAULT '',
			request_digest TEXT NOT NULL DEFAULT '',
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
	defer resp.Body.Close()
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

func TestTaskReadListNativeStatuses(t *testing.T) {
	t.Setenv("LEVARA_LONG_HORIZON_RUNTIME", "1")
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := executorTestDB(t, dialect)
			cfg := APIConfig{DB: db}
			deps := NewMCPDeps(cfg)
			ctx := context.WithValue(context.Background(), mcp.UserIDKey, "executor-owner")
			opened := executorPayload(t, mcp.ToolTaskOpen(ctx, deps, map[string]any{
				"collection": "tests", "room": "task-runtime", "objective": "native status observation",
				"idempotency_key": "native-read-status", "risk_level": "low",
				"definition_of_done": []any{map[string]any{"criterion_id": "observed", "description": "native lease visible"}},
			}))
			id := opened["task_id"].(string)
			planned := executorPayload(t, mcp.ToolTaskPlan(ctx, deps, map[string]any{
				"task_id": id, "base_version": opened["version"],
				"steps": []any{map[string]any{"step_id": "native", "description": "native active", "criterion_ids": []any{"observed"}}},
			}))
			claimed := executorPayload(t, mcp.ToolTaskStep(ctx, deps, map[string]any{
				"task_id": id, "base_version": planned["version"], "step_id": "native",
				"actor_id": "native-browser", "action": "claim", "lease_seconds": 300,
			}))
			q, args := QArgs("INSERT INTO task_steps (id, task_id, description, status, position) VALUES ('legacy',$1,'legacy claimed','claimed',1)", id)
			if _, err := db.Exec(q, args...); err != nil {
				t.Fatal(err)
			}
			app := fiber.New()
			RegisterTaskReadAPI(app, cfg)
			code, m := taskGetJSON(t, app, "/tasks?status=running")
			if code != 200 || m["count"].(float64) != 1 {
				t.Fatalf("native running filter: %d %v", code, m)
			}
			first := m["tasks"].([]any)[0].(map[string]any)
			if first["status"] != "running" || first["step_counts"].(map[string]any)["claimed"].(float64) != 2 {
				t.Fatalf("native active and legacy claimed must both count: %v", first)
			}
			if first["completed_at"] != nil {
				t.Fatalf("unfinished task has completion timestamp: %v", first)
			}
			for _, field := range []string{"created_at", "updated_at"} {
				if _, err := time.Parse(time.RFC3339Nano, first[field].(string)); err != nil {
					t.Fatalf("%s is not a public RFC3339 timestamp: %v", field, first[field])
				}
			}
			expired := time.Now().UTC().Add(-time.Minute)
			q, args = QArgs("INSERT INTO task_leases (step_id, task_id, actor_id, expires_at) VALUES ('legacy',$1,'expired-native',$2)", id, expired.Format(time.RFC3339Nano))
			if _, err := db.Exec(q, args...); err != nil {
				t.Fatal(err)
			}
			code, detail := taskGetJSON(t, app, "/tasks/"+id)
			if code != 200 {
				t.Fatalf("native detail: %d %v", code, detail)
			}
			steps := detail["steps"].([]any)
			if len(steps) != 2 || detail["step_counts"].(map[string]any)["claimed"].(float64) != 2 || len(detail["criteria"].([]any)) != 1 {
				t.Fatalf("native detail silently lost steps or criteria: %v", detail)
			}
			var native map[string]any
			for _, raw := range steps {
				step := raw.(map[string]any)
				if step["id"] == "native" {
					native = step
				}
				if step["id"] == "legacy" && (step["leased_by"] != nil || step["lease_expires_at"] != nil) {
					t.Fatalf("expired native TEXT lease visible: %v", step)
				}
			}
			if native == nil || native["status"] != "active" || native["leased_by"] != "native-browser" {
				t.Fatalf("native active lease missing: %v", native)
			}
			leaseExpiry, err := time.Parse(time.RFC3339Nano, native["lease_expires_at"].(string))
			if err != nil || !leaseExpiry.After(time.Now()) {
				t.Fatalf("native lease expiry: %v %v", native, err)
			}
			executorPayload(t, mcp.ToolTaskStep(ctx, deps, map[string]any{
				"task_id": id, "base_version": claimed["version"], "step_id": "native",
				"actor_id": "native-browser", "action": "pass",
			}))
			code, detail = taskGetJSON(t, app, "/tasks/"+id)
			if code != 200 || detail["step_counts"].(map[string]any)["passed"].(float64) != 1 || detail["step_counts"].(map[string]any)["claimed"].(float64) != 1 {
				t.Fatalf("native passed step observation: %d %v", code, detail)
			}
			for _, raw := range detail["steps"].([]any) {
				step := raw.(map[string]any)
				if step["id"] == "native" && (step["leased_by"] != nil || step["lease_expires_at"] != nil) {
					t.Fatalf("passed step retained lease: %v", step)
				}
			}
			completed := time.Now().UTC().Truncate(time.Microsecond)
			q, args = QArgs("UPDATE tasks SET completed_at=$1 WHERE id=$2", completed.Format(time.RFC3339Nano), id)
			if _, err := db.Exec(q, args...); err != nil {
				t.Fatal(err)
			}
			code, detail = taskGetJSON(t, app, "/tasks/"+id)
			if code != 200 {
				t.Fatalf("native completed timestamp: %d %v", code, detail)
			}
			completedAt, err := time.Parse(time.RFC3339Nano, detail["completed_at"].(string))
			if err != nil || !completedAt.Equal(completed) {
				t.Fatalf("completed timestamp lost precision: %v %v", detail["completed_at"], err)
			}
			code, _ = taskGetJSON(t, app, "/tasks/missing-native")
			if code != http.StatusNotFound {
				t.Fatalf("missing native task: %d", code)
			}
			if _, err := db.Exec("ALTER TABLE task_leases RENAME COLUMN expires_at TO unavailable_expiry"); err != nil {
				t.Fatal(err)
			}
			code, detail = taskGetJSON(t, app, "/tasks/"+id)
			if code != http.StatusInternalServerError || detail["error"] == nil {
				t.Fatalf("native lease query failure must not drop steps: %d %v", code, detail)
			}

		})
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
	if m["step_counts"].(map[string]any)["claimed"].(float64) != 1 || m["blocker_count"].(float64) != 1 {
		t.Fatalf("detail counters missing: %v", m)
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

func TestTaskReadDetailQueryErrorsAreNotMissingOrEmpty(t *testing.T) {
	for _, table := range []string{"tasks", "task_criteria", "task_steps", "task_leases", "task_receipts", "task_checkpoints", "task_blockers", "task_events"} {
		t.Run(table, func(t *testing.T) {
			app, db := taskReadTestApp(t)
			id := taskSeed(t, db)
			if _, err := db.Exec("DROP TABLE " + table); err != nil {
				t.Fatal(err)
			}
			code, body := taskGetJSON(t, app, "/tasks/"+id)
			if code != http.StatusInternalServerError || body["error"] == nil {
				t.Fatalf("broken %s must fail visibly, not 404 or partial success: %d %v", table, code, body)
			}
		})
	}
}

func TestTaskReadTimePreservesNativePrecisionAndRejectsMalformed(t *testing.T) {
	instant := time.Date(2026, 10, 7, 12, 13, 14, 123456789, time.UTC)
	for _, value := range []any{instant, instant.Format(time.RFC3339Nano), []byte(instant.Format(time.RFC3339Nano)), "2026-10-07 12:13:14.123456789"} {
		parsed, err := taskReadTime(value, false)
		if err != nil || !parsed.Equal(instant) {
			t.Fatalf("timestamp %v lost precision: %v %v", value, parsed, err)
		}
	}
	if parsed, err := taskReadTime(nil, true); err != nil || parsed != nil {
		t.Fatalf("optional NULL: %v %v", parsed, err)
	}
	for _, value := range []any{nil, "", "invalid", 42} {
		if _, err := taskReadTime(value, false); err == nil {
			t.Fatalf("malformed required timestamp accepted: %v", value)
		}
	}
}

func TestTaskReadMalformedTimestampsFailVisibly(t *testing.T) {
	for _, query := range []string{
		"UPDATE tasks SET created_at='invalid'",
		"UPDATE tasks SET completed_at='invalid'",
		"UPDATE task_leases SET expires_at='invalid' WHERE step_id='s-2'",
	} {
		t.Run(query, func(t *testing.T) {
			app, db := taskReadTestApp(t)
			id := taskSeed(t, db)
			if _, err := db.Exec(query); err != nil {
				t.Fatal(err)
			}
			code, body := taskGetJSON(t, app, "/tasks/"+id)
			if code != http.StatusInternalServerError || body["error"] == nil {
				t.Fatalf("malformed timestamp accepted: %d %v", code, body)
			}
		})
	}
}

func TestTaskReadVerifiedOwnerAndReadKey(t *testing.T) {
	t.Setenv("LEVARA_LONG_HORIZON_RUNTIME", "1")
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := executorTestDB(t, dialect)
			taskReadAuthSchema(t, db)
			if _, err := db.Exec("INSERT INTO principals(id,type) VALUES ('task-peer','user')"); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("INSERT INTO users(id,email,hashed_password,is_active) VALUES ('task-peer','task-peer@test.invalid','unused',true)"); err != nil {
				t.Fatal(err)
			}
			deps := NewMCPDeps(APIConfig{DB: db})
			ids := map[string]string{}
			for _, owner := range []string{"executor-owner", "task-peer", ""} {
				ctx := context.WithValue(context.Background(), mcp.UserIDKey, owner)
				opened := executorPayload(t, mcp.ToolTaskOpen(ctx, deps, map[string]any{
					"collection": "shared-task-collection", "room": "task-runtime", "objective": "owned by " + owner,
					"idempotency_key": "read-owner-" + owner, "risk_level": "low",
					"definition_of_done": []any{map[string]any{"criterion_id": "read", "description": "owner observation"}},
				}))
				ids[owner] = opened["task_id"].(string)
			}
			const secret = "task-read-test-secret"
			tokens := map[string]string{}
			for _, owner := range []string{"executor-owner", "task-peer"} {
				token, err := IssueSessionJWT(context.Background(), db, owner, owner+"@test.invalid", secret)
				if err != nil {
					t.Fatal(err)
				}
				tokens[owner] = token
			}
			const readKey = "task-owner-read-key"
			q, args := QArgs("INSERT INTO api_keys (id,key_hash,user_id,name,permissions,created_at) VALUES ($1,$2,$3,$4,$5,$6)",
				"task-read-key", apikeyHash(readKey), "executor-owner", "task reader", "read", time.Now().UTC().Format(time.RFC3339Nano))
			if _, err := db.Exec(q, args...); err != nil {
				t.Fatal(err)
			}
			app := fiber.New()
			app.Use(func(c *fiber.Ctx) error {
				c.Locals("auth_db", &DBRef{DB: db})
				return c.Next()
			})
			app.Use(JWTMiddleware(secret, true), APIKeyPermissionMiddleware())
			RegisterTaskReadAPI(app, APIConfig{DB: db, RequireAuth: true})
			get := func(path, bearer, key string) (int, map[string]any) {
				t.Helper()
				req := httptest.NewRequest(http.MethodGet, path, nil)
				if bearer != "" {
					req.Header.Set("Authorization", "Bearer "+bearer)
				}
				if key != "" {
					req.Header.Set("X-API-Key", key)
				}
				resp, err := app.Test(req, -1)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				body, err := io.ReadAll(resp.Body)
				if err != nil {
					t.Fatal(err)
				}
				var out map[string]any
				if err := json.Unmarshal(body, &out); err != nil {
					t.Fatalf("response %d: %s", resp.StatusCode, body)
				}
				return resp.StatusCode, out
			}
			for _, owner := range []string{"executor-owner", "task-peer"} {
				code, body := get("/tasks?collection_name=shared-task-collection", tokens[owner], "")
				if code != http.StatusOK || body["count"].(float64) != 2 {
					t.Fatalf("verified owner list %s: %d %v", owner, code, body)
				}
				for _, raw := range body["tasks"].([]any) {
					row := raw.(map[string]any)
					if row["owner_id"] != owner && row["owner_id"] != "" {
						t.Fatalf("foreign task listed for %s: %v", owner, row)
					}
				}
				for _, visible := range []string{owner, ""} {
					if code, body := get("/tasks/"+ids[visible], tokens[owner], ""); code != http.StatusOK || body["id"] != ids[visible] {
						t.Fatalf("own/shared task detail: %d %v", code, body)
					}
				}
				other := "task-peer"
				if owner == other {
					other = "executor-owner"
				}
				if code, body := get("/tasks/"+ids[other], tokens[owner], ""); code != http.StatusNotFound || body["steps"] != nil || body["criteria"] != nil {
					t.Fatalf("foreign task detail exposed: %d %v", code, body)
				}
			}
			if code, body := get("/tasks", "", readKey); code != http.StatusOK || body["count"].(float64) != 2 {
				t.Fatalf("read key own/shared task list: %d %v", code, body)
			}
			if code, body := get("/tasks/"+ids["task-peer"], "", readKey); code != http.StatusNotFound || body["steps"] != nil {
				t.Fatalf("read key foreign detail: %d %v", code, body)
			}
			if _, err := db.Exec("UPDATE api_keys SET revoked=true WHERE id='task-read-key'"); err != nil {
				t.Fatal(err)
			}
			if code, _ := get("/tasks", "", readKey); code != http.StatusUnauthorized {
				t.Fatalf("revoked read key status: %d", code)
			}
			if code, _ := get("/tasks", "", ""); code != http.StatusUnauthorized {
				t.Fatalf("missing authenticated identity: %d", code)
			}
		})
	}
}

func taskReadAuthSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	if err := accesspkg.EnsureIdentitySchema(context.Background(), db, Q); err != nil {
		t.Fatal(err)
	}
	if err := accesspkg.EnsureBrowserSessionSchema(context.Background(), db, Q); err != nil {
		t.Fatal(err)
	}
}

func TestTaskReadCredentialExpiryRetainsFenceUntilClose(t *testing.T) {
	t.Setenv("HTTP_REQUEST_TIMEOUT_MS", "5000")
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := executorTestDB(t, dialect)
			taskReadAuthSchema(t, db)
			db.SetMaxOpenConns(1)
			const secret = "task-read-expiry-secret"
			app := fiber.New()
			app.Use(func(c *fiber.Ctx) error {
				c.Locals("auth_db", &DBRef{DB: db})
				return c.Next()
			})
			app.Use(JWTMiddleware(secret, true), APIKeyPermissionMiddleware())
			RegisterTaskReadAPI(app, APIConfig{DB: db, RequireAuth: true})
			expiresAt := time.Now().Unix() + 2
			token := signSessionPayload(jwtPayload{
				Sub: "executor-owner", Email: "executor-owner@test.invalid",
				Iat: time.Now().Unix(), Exp: expiresAt,
			}, secret)
			raw := &fasthttp.RequestCtx{}
			raw.Request.Header.SetMethod(http.MethodGet)
			raw.Request.SetRequestURI("/tasks")
			raw.Request.Header.Set("Authorization", "Bearer "+token)
			app.Handler()(raw)
			stream, ok := raw.Response.BodyStream().(*fencedResponse)
			if !ok || raw.Response.StatusCode() != http.StatusOK {
				t.Fatalf("Task response lacks protected stream: status=%d", raw.Response.StatusCode())
			}
			defer stream.Close()
			deadline, ok := stream.ctx.Deadline()
			if !ok || !deadline.Equal(time.Unix(expiresAt, 0)) {
				t.Fatalf("Task deadline exceeds credential expiry: %v", deadline)
			}
			if db.Stats().InUse != 1 {
				t.Fatal("Task response did not retain SQL fence")
			}
			if n, err := stream.Read(make([]byte, 8)); n != 8 || err != nil {
				t.Fatalf("partial Task drain: %d %v", n, err)
			}
			select {
			case <-stream.ctx.Done():
			case <-time.After(3 * time.Second):
				t.Fatal("Task response failed to expire")
			}
			if n, err := stream.Read(make([]byte, 8)); n != 0 || !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("expired Task response still readable: %d %v", n, err)
			}
			if db.Stats().InUse != 1 {
				t.Fatal("credential expiry released SQL before response close")
			}
			if err := stream.Close(); err != nil {
				t.Fatal(err)
			}
			if err := stream.Close(); err != nil {
				t.Fatal(err)
			}
			if db.Stats().InUse != 0 {
				t.Fatal("Task response close leaked SQL fence")
			}
		})
	}
}
