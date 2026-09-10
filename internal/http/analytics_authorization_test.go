package http

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/audit"
	"github.com/stek0v/levara/pkg/llm"
	"github.com/stek0v/levara/pkg/mcp"
)

func TestAnalyticsOwnerIsolation(t *testing.T) {
	db := newMCPMemoryBehaviorDB(t)
	for _, query := range []string{
		`INSERT INTO principals(id) VALUES ('alice'),('bob')`,
		`INSERT INTO users(id,email,hashed_password) VALUES ('alice','alice@test.local','unused'),('bob','bob@test.local','unused')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	rm, err := audit.NewReadModel(db, 16)
	if err != nil {
		t.Fatal(err)
	}
	defer rm.Close()
	for i, user := range []string{"alice", "bob", ""} {
		rm.Log(audit.Entry{ScopeVerified: true, RequestID: "event-" + user, TS: time.Now().Add(time.Duration(i) * time.Millisecond).UTC().Format(time.RFC3339Nano), AgentID: user, TraceID: "trace-" + user, Tool: "recall_memory", Outcome: audit.OutcomeOK, ResultCount: 1, Collection: "same-collection"})
	}
	// A pre-migration entry can have a known owner but an unknown tenant.
	rm.Log(audit.Entry{RequestID: "old-owner", TS: time.Now().UTC().Format(time.RFC3339Nano), AgentID: "alice", TraceID: "old-unknown-tenant", Tool: "recall_memory", Outcome: audit.OutcomeOK})
	waitAuditRows(t, db, 4)
	cfg := APIConfig{DB: db, RequireAuth: true, MCPAuditReadModel: rm}
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error { c.Locals("user_id", "alice"); return c.Next() })
	app.Get("/trajectories", agentTrajectoriesHandler(cfg))
	app.Get("/trajectories/:id", agentTrajectoryDetailHandler(cfg))
	app.Get("/events", mcpAnalyticsEventsHandler(cfg))
	app.Get("/summary", mcpAnalyticsHandler(cfg))
	app.Post("/review", memoryReviewRunHandler(cfg))
	app.Get("/reviews", memoryReviewListHandler(cfg))
	app.Get("/proposals", memoryScaffoldProposalListHandler(cfg))
	if err := ensureMemoryReviewSchema(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"current", "historical"} {
		run := memoryReviewRun{ID: id, Status: "completed", Scope: memoryReviewScope{AgentID: "alice"}, Findings: []memoryReviewFinding{{ID: id, Category: "blind_save", Summary: id}}}
		if err := insertMemoryReviewRun(context.Background(), db, run); err != nil {
			t.Fatal(err)
		}
		if _, err := createMemoryScaffoldProposalsFromReview(context.Background(), db, run); err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range []string{`UPDATE memory_review_runs SET scope_verified=0 WHERE id='historical'`, `UPDATE memory_scaffold_proposals SET scope_verified=0 WHERE source_run_id='historical'`} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"/reviews", "/proposals"} {
		resp, err := app.Test(httptest.NewRequest("GET", path, nil))
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 200 || strings.Contains(string(raw), "historical") || !strings.Contains(string(raw), "current") {
			t.Fatalf("unverified history %s: %d %s", path, resp.StatusCode, raw)
		}
	}
	for _, path := range []string{"/trajectories?collection=same-collection", "/events", "/summary", "/trajectories/trace:trace-bob", "/review"} {
		t.Run(path, func(t *testing.T) {
			method, body := "GET", ""
			if path == "/review" {
				method, body = "POST", `{"dry_run":true}`
			}
			req := httptest.NewRequest(method, path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			resp, err := app.Test(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			raw, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(path, "/trajectories/trace:") {
				if resp.StatusCode != 404 {
					t.Fatalf("foreign trajectory status=%d body=%s", resp.StatusCode, raw)
				}
				return
			}
			if resp.StatusCode != 200 {
				t.Fatalf("status=%d body=%s", resp.StatusCode, raw)
			}
			if strings.Contains(string(raw), "trace-bob") {
				t.Fatalf("foreign trajectory leaked: %s", raw)
			}
			var result map[string]any
			if err := json.Unmarshal(raw, &result); err != nil {
				t.Fatal(err)
			}
			switch {
			case strings.HasPrefix(path, "/trajectories"):
				if result["total"] != float64(1) {
					t.Fatalf("unscoped aggregate: %s", raw)
				}
			case path == "/events":
				if len(result["events"].([]any)) != 1 {
					t.Fatalf("unscoped events: %s", raw)
				}
			case path == "/summary":
				if result["summary"].(map[string]any)["total"] != float64(1) {
					t.Fatalf("unscoped summary: %s", raw)
				}
			case path == "/review":
				if result["trajectory_count"] != float64(1) {
					t.Fatalf("unscoped LLM review: %s", raw)
				}
			}
		})
	}
}

type analyticsRevokingProvider struct{ revoke func() error }

func (analyticsRevokingProvider) Name() string { return "revocation-test" }
func (p analyticsRevokingProvider) ChatCompletion(context.Context, llm.CompletionRequest) (*llm.CompletionResponse, error) {
	if err := p.revoke(); err != nil {
		return nil, err
	}
	return &llm.CompletionResponse{Content: `{"summary":"late-output","findings":[{"category":"blind_save","summary":"late-output"}]}`}, nil
}

func TestAnalyticsReviewRejectsLatePublication(t *testing.T) {
	t.Setenv("LLM_MODEL", "local-test")
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			_, db := documentACLHTTPFixture(t, dialect)
			rm, err := audit.NewReadModel(db, 16)
			if err != nil {
				t.Fatal(err)
			}
			defer rm.Close()
			cfg := APIConfig{DB: db, RequireAuth: true, MCPAuditReadModel: rm, LLMProvider: analyticsRevokingProvider{revoke: func() error { _, err := db.Exec(`UPDATE users SET is_active=false WHERE id='alice'`); return err }}}
			app := fiber.New()
			app.Use(func(c *fiber.Ctx) error { c.Locals("user_id", "alice"); return c.Next() })
			app.Post("/", memoryReviewRunHandler(cfg))
			req := httptest.NewRequest("POST", "/", strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			resp, err := app.Test(req)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != 403 || strings.Contains(string(raw), "late-output") {
				t.Fatalf("late disclosure status=%d body=%s", resp.StatusCode, raw)
			}
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM memory_review_findings`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("published %d findings after revoke", count)
			}
		})
	}
}

func TestAnalyticsMCPUsesVerifiedActorTenant(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			_, db := documentACLHTTPFixture(t, dialect)
			if err := accesspkg.EnsureIdentitySchema(context.Background(), db, Q); err != nil {
				t.Fatal(err)
			}
			for _, q := range []string{`INSERT INTO tenants(id,name,owner_id) VALUES ('t1','Tenant1','alice'),('t2','Tenant2','bob')`, `INSERT INTO user_tenant(user_id,tenant_id) VALUES ('alice','t1'),('bob','t2')`} {
				if _, err := db.Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			sink := &captureSink{}
			app, h := mcpAdoptApp(t, APIConfig{DB: db, RequireAuth: true, JWTSecret: "test-secret", MCPAudit: sink})
			headers := map[string]string{"Authorization": "Bearer " + createJWT("alice", "alice@example.test", "test-secret"), "X-Tenant-Id": "t1"}
			body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"levara_instructions","arguments":{"tenant_id":"t2","owner_id":"bob"}}}`
			status, raw := postRPC(t, app, body, headers)
			if status != 200 {
				t.Fatalf("status=%d %s", status, raw)
			}
			if len(sink.entries) != 1 || sink.entries[0].AgentID != "alice" || sink.entries[0].TenantID != "t1" {
				t.Fatalf("audit attribution=%+v", sink.entries)
			}
			headers["X-Tenant-Id"] = "t2"
			status, _ = postRPC(t, app, body, headers)
			if status != 404 || len(sink.entries) != 1 {
				t.Fatalf("foreign tenant reached tool: status=%d audit=%+v", status, sink.entries)
			}
			// A session owner must never recover a rejected request credential.
			sess := h.sessions.Create()
			sess.SetUserID("alice")
			headers["Mcp-Session-Id"] = sess.ID
			headers["X-Tenant-Id"] = "t1"
			headers["Authorization"] = "Bearer invalid"
			status, _ = postRPC(t, app, body, headers)
			if status != 404 || len(sink.entries) != 1 {
				t.Fatalf("session restored invalid identity: status=%d", status)
			}
			ctx := context.WithValue(context.Background(), mcp.UserIDKey, "alice")
			ctx = context.WithValue(ctx, mcp.TenantIDKey, "t1")
			ctx = context.WithValue(ctx, searchEgressKey{}, searchEgress{kind: "jwt", actor: accesspkg.Actor{UserID: "alice", TenantID: "t1"}})
			h.recordMCPAudit(ctx, &mcp.Session{UserID: "bob"}, "test", nil, mcpToolResult{}, 0)
			last := sink.entries[len(sink.entries)-1]
			if last.AgentID != "alice" || last.TenantID != "t1" {
				t.Fatalf("session overrode request: %+v", last)
			}
		})
	}
}

func TestAnalyticsTenantAndReviewIsolation(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			_, db := documentACLHTTPFixture(t, dialect)
			db.SetMaxOpenConns(1)
			for _, q := range []string{
				`INSERT INTO tenants(id,name,owner_id) VALUES ('t1','Tenant 1','alice'),('t2','Tenant 2','alice')`,
				`INSERT INTO user_tenant(user_id,tenant_id) VALUES ('alice','t1'),('alice','t2'),('bob','t1')`,
			} {
				if _, err := db.Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			rm, err := audit.NewReadModel(db, 16)
			if err != nil {
				t.Fatal(err)
			}
			defer rm.Close()
			if err := ensureMemoryReviewSchema(context.Background(), db); err != nil {
				t.Fatal(err)
			}
			for _, e := range []audit.Entry{
				{RequestID: "own", AgentID: "alice", TenantID: "t1"},
				{RequestID: "other-tenant", AgentID: "alice", TenantID: "t2"},
				{RequestID: "foreign", AgentID: "bob", TenantID: "t1"},
				{RequestID: "legacy", AgentID: "", TenantID: ""},
			} {
				e.ScopeVerified = true
				e.TS = time.Now().UTC().Format(time.RFC3339Nano)
				e.Tool = "recall_memory"
				e.TraceID = e.RequestID
				e.Outcome = audit.OutcomeOK
				e.ResultCount = 1
				rm.Log(e)
				if err := insertMemoryReviewRun(context.Background(), db, memoryReviewRun{ID: e.RequestID, Status: "completed", CreatedAt: e.TS, Summary: e.RequestID, Scope: memoryReviewScope{AgentID: e.AgentID, TenantID: e.TenantID, RestrictTenant: true}}); err != nil {
					t.Fatal(err)
				}
				proposals, err := createMemoryScaffoldProposalsFromReview(context.Background(), db, memoryReviewRun{ID: e.RequestID, Status: "completed", Scope: memoryReviewScope{AgentID: e.AgentID, TenantID: e.TenantID, RestrictTenant: true}, Findings: []memoryReviewFinding{{ID: e.RequestID, Category: "blind_save", Summary: e.RequestID}}})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(memoryReviewQuery(`UPDATE memory_scaffold_proposals SET id=? WHERE id=?`), e.RequestID, proposals[0].ID); err != nil {
					t.Fatal(err)
				}
			}
			waitAuditRows(t, db, 4)
			cfg := APIConfig{DB: db, RequireAuth: true, MCPAuditReadModel: rm}
			app := fiber.New()
			app.Use(func(c *fiber.Ctx) error { c.Locals("user_id", "alice"); c.Locals("tenant_id", "t1"); return c.Next() })
			app.Get("/events", mcpAnalyticsEventsHandler(cfg))
			app.Get("/behavior", memoryBehaviorHandler(cfg))
			app.Get("/reviews", memoryReviewListHandler(cfg))
			app.Get("/reviews/:id", memoryReviewDetailHandler(cfg))
			app.Get("/proposals", memoryScaffoldProposalListHandler(cfg))
			app.Get("/proposals/:id", memoryScaffoldProposalDetailHandler(cfg))
			for _, path := range []string{"/events", "/behavior", "/reviews", "/reviews/own", "/reviews/foreign", "/reviews/other-tenant", "/reviews/legacy", "/proposals", "/proposals/own", "/proposals/foreign", "/proposals/other-tenant", "/proposals/legacy"} {
				req := httptest.NewRequest("GET", path, nil)
				resp, err := app.Test(req)
				if err != nil {
					t.Fatal(err)
				}
				raw, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
				want := 200
				if (strings.HasPrefix(path, "/reviews/") || strings.HasPrefix(path, "/proposals/")) && !strings.HasSuffix(path, "/own") {
					want = 404
				}
				if resp.StatusCode != want {
					t.Fatalf("%s status=%d body=%s", path, resp.StatusCode, raw)
				}
				if want == 404 {
					continue
				}
				for _, marker := range []string{"other-tenant", "foreign", "legacy"} {
					if strings.Contains(string(raw), marker) {
						t.Fatalf("%s leaked %s: %s", path, marker, raw)
					}
				}
				var got map[string]any
				if err := json.Unmarshal(raw, &got); err != nil {
					t.Fatal(err)
				}
				switch path {
				case "/proposals":
					if len(got["proposals"].([]any)) != 1 {
						t.Fatalf("proposals=%s", raw)
					}
				case "/events":
					if len(got["events"].([]any)) != 1 {
						t.Fatalf("events=%s", raw)
					}
				case "/reviews":
					if len(got["runs"].([]any)) != 1 {
						t.Fatalf("reviews=%s", raw)
					}
				case "/behavior":
					if got["summary"].(map[string]any)["total_trajectories"] != float64(1) {
						t.Fatalf("behavior=%s", raw)
					}
				}
			}
			if _, err := db.Exec(`DELETE FROM user_tenant WHERE user_id='alice' AND tenant_id='t1'`); err != nil {
				t.Fatal(err)
			}
			resp, err := app.Test(httptest.NewRequest("GET", "/events", nil))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != 403 {
				t.Fatalf("revoked membership status=%d", resp.StatusCode)
			}
		})
	}
}
