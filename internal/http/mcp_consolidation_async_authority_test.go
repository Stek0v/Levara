package http

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/internal/store"
	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/embed"
	"github.com/stek0v/levara/pkg/mcp"
)

func asyncAuthorityHandler(f *documentHTTPFixture) *mcpHandler {
	cfg := f.cfg
	cfg.RequireAuth = true
	cfg.JWTSecret = "async-authority-test-secret"
	cfg.WorkspaceAuditSink = nil
	return &mcpHandler{cfg: cfg, sessions: mcp.NewSessionStore()}
}

func TestParseConsolidationProgressIncludesLLMCalls(t *testing.T) {
	a, b, c, calls := parseConsolidationProgress("candidates=7 clusters=3 actions=2 skipped=1 llm_calls=4")
	if a != 7 || b != 3 || c != 2 || calls != 4 {
		t.Fatalf("got %d,%d,%d,%d", a, b, c, calls)
	}
	a, b, c, calls = parseConsolidationProgress("candidates=7 clusters=3 actions=2")
	if a != 7 || b != 3 || c != 2 || calls != 0 {
		t.Fatalf("legacy got %d,%d,%d,%d", a, b, c, calls)
	}
	a, b, c, calls = parseConsolidationProgress("consolidate: provider failed llm_calls=4")
	if a != 0 || b != 0 || c != 0 || calls != 4 {
		t.Fatalf("error got %d,%d,%d,%d", a, b, c, calls)
	}
}

func asyncAuthorityApp(h *mcpHandler, ended chan context.Context) *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	wrap := func(handler fiber.Handler) fiber.Handler {
		return func(c *fiber.Ctx) error {
			err := handler(c)
			// Both real RPC handlers cancel their request context on return.
			if ended != nil {
				select {
				case ended <- c.UserContext():
				default:
				}
			}
			return err
		}
	}
	app.Post("/mcp", wrap(h.handleRPC))
	app.Post(latestMCPPath, wrap(h.handleLatestRPC))
	return app
}

func asyncAuthorityRPC(t *testing.T, app *fiber.App, h *mcpHandler, path, user, tool string, args map[string]any) mcp.ToolResult {
	t.Helper()
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, tool, encoded)
	tenant := "a"
	if user == "foreign" {
		tenant = "b"
	}
	headers := map[string]string{"Authorization": "Bearer " + createJWT(user, user+"@test.invalid", h.cfg.JWTSecret), "X-Tenant-Id": tenant}
	var envelope struct {
		Result mcp.ToolResult `json:"result"`
		Error  any            `json:"error"`
	}
	if path == latestMCPPath {
		body = fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":%q,"arguments":%s,%s}}`, tool, encoded, latestMCPMetaParams())
		for key, value := range latestMCPHeaders("tools/call") {
			headers[key] = value
		}
		headers["Mcp-Name"] = tool
		resp := latestMCPPost(t, app, body, headers)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("latest status=%d", resp.StatusCode)
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatal(err)
		}
	} else {
		headers["Mcp-Session-Id"] = h.createSession(user)
		status, raw := postRPC(t, app, body, headers)
		if status != http.StatusOK {
			t.Fatalf("legacy status=%d body=%s", status, raw)
		}
		if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
			t.Fatal(err)
		}
	}
	if envelope.Error != nil || len(envelope.Result.Content) == 0 {
		t.Fatalf("RPC response=%+v", envelope)
	}
	return envelope.Result
}

// The real embed client makes egress observable and keeps the job in flight
// until the submitting HTTP handler has returned and cancelled its context.
func asyncAuthorityBlockedEmbed(t *testing.T, h *mcpHandler) (<-chan []string, *atomic.Int32, func()) {
	t.Helper()
	inputs := make(chan []string, 32)
	calls := new(atomic.Int32)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		if r.URL.Path != "/v1/embeddings" || json.NewDecoder(r.Body).Decode(&req) != nil {
			http.Error(w, "invalid embedding request", http.StatusBadRequest)
			return
		}
		calls.Add(1)
		inputs <- req.Input
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"index":0,"embedding":[1,0]}]}`))
	}))
	cm, err := store.NewCollectionManager(2, t.TempDir())
	if err != nil {
		unblock()
		srv.Close()
		t.Fatal(err)
	}
	h.cfg.EmbedEndpoint = srv.URL + "/v1/embeddings"
	h.cfg.EmbedModel = "async-test"
	h.cfg.EmbedClient = embed.NewClient(h.cfg.EmbedEndpoint, "async-test", 1, 1)
	h.cfg.Collections = cm
	t.Cleanup(func() { unblock(); srv.Close(); _ = cm.Close() })
	return inputs, calls, unblock
}

func asyncAuthoritySeedMemories(f *documentHTTPFixture) {
	for _, row := range []struct{ id, owner, collection string }{
		{"mine", "peer", "main"}, {"foreign-memory", "foreign", "main"}, {"shared-memory", "", "main"}, {"other-collection", "peer", "other"},
	} {
		f.exec(`INSERT INTO memories(id,key,value,owner_id,collection_name,room,hall) VALUES($1,$2,$3,$4,$5,'memory','fact')`, row.id, row.id, "SECRET_"+row.id, row.owner, row.collection)
	}
}

type asyncAuthorityJob struct {
	owner, status, result, last   string
	candidates, clusters, actions int
}

func asyncAuthorityWaitJob(t *testing.T, f *documentHTTPFixture, id string) asyncAuthorityJob {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var job asyncAuthorityJob
	for {
		err := f.db.QueryRowContext(ctx, Q(`SELECT owner_id,status,result_text,last_error,candidates,clusters,actions FROM consolidation_jobs WHERE id=$1`), id).Scan(&job.owner, &job.status, &job.result, &job.last, &job.candidates, &job.clusters, &job.actions)
		if err != nil {
			t.Fatalf("job=%s query: %v", id, err)
		}
		if job.status == "completed" || job.status == "failed" {
			return job
		}
		select {
		case <-ctx.Done():
			t.Fatalf("job=%s did not terminate: %+v", id, job)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestMCPConsolidationAsyncAuthorityAfterCancellation(t *testing.T) {
	t.Setenv("LEVARA_MCP_TOOLSET", "full")
	t.Setenv("LEVARA_TENANT_ENFORCED", "1")
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		asyncAuthoritySeedMemories(f)
		for _, path := range []string{"/mcp", latestMCPPath} {
			t.Run(path, func(t *testing.T) {
				h := asyncAuthorityHandler(f)
				inputs, calls, unblock := asyncAuthorityBlockedEmbed(t, h)
				ended := make(chan context.Context, 1)
				app := asyncAuthorityApp(h, ended)
				got := asyncAuthorityRPC(t, app, h, path, "peer", "consolidate", map[string]any{"collection": "main", "wait": false, "dry_run": true, "max_duration_ms": float64(5000), "owner_id": "foreign", "actor_id": "foreign", "trusted_local": true})
				var submitted struct {
					JobID string `json:"job_id"`
				}
				if got.IsError || json.Unmarshal([]byte(got.Content[0].Text), &submitted) != nil || submitted.JobID == "" {
					t.Fatalf("enqueue=%+v", got)
				}
				requestCtx := <-ended
				if requestCtx.Err() != context.Canceled {
					t.Fatalf("HTTP request context was not cancelled: %v", requestCtx.Err())
				}
				select {
				case input := <-inputs:
					if len(input) != 1 || input[0] != "mine SECRET_mine" {
						t.Fatalf("foreign/shared input crossed verified namespace: %q", input)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("detached job did not reach embed provider after HTTP cancellation")
				}
				// Enqueue returned while the provider is still held: this proves
				// asynchronous execution without timing durable SQL publication.
				// The provider fence owns the pool's sole connection. Give this
				// read-only observer a second connection, then restore pool=1.
				f.db.SetMaxOpenConns(2)
				observerCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				var running string
				err := f.db.QueryRowContext(observerCtx, Q("SELECT status FROM consolidation_jobs WHERE id=$1"), submitted.JobID).Scan(&running)
				cancel()
				f.db.SetMaxOpenConns(1)
				if err != nil || running != "running" {
					t.Fatalf("enqueue did not return before downstream completed: status=%q error=%v", running, err)
				}
				unblock()
				job := asyncAuthorityWaitJob(t, f, submitted.JobID)
				if job.owner != "peer" || job.status != "completed" || job.candidates != 1 || job.clusters != 0 || job.actions != 0 || job.last != "" || calls.Load() != 1 {
					t.Fatalf("detached authority/result=%+v embed calls=%d", job, calls.Load())
				}
				for _, user := range []string{"peer", "foreign"} {
					status := asyncAuthorityRPC(t, app, h, path, user, "consolidation_status", map[string]any{"job_id": submitted.JobID, "owner_id": "peer", "actor_id": "peer"})
					if status.IsError != (user == "foreign") {
						t.Fatalf("status user=%s result=%+v", user, status)
					}
				}
			})
		}
	})
}

func TestConsolidationAsyncClaimOnce(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		asyncAuthoritySeedMemories(f)
		h := asyncAuthorityHandler(f)
		inputs, calls, unblock := asyncAuthorityBlockedEmbed(t, h)
		if err := ensureConsolidationJobs(context.Background(), f.db); err != nil {
			t.Fatal(err)
		}
		f.exec(`INSERT INTO consolidation_jobs(id,owner_id,status,args_json,created_at,updated_at) VALUES('claim','peer','pending','{}','old','old')`)
		// A direct runner has no HTTP transport boundary. Model only the
		// already-verified credential facts that the real HTTP test captures.
		ctx := context.WithValue(context.Background(), searchEgressKey{}, searchEgress{cfg: h.cfg, actor: accesspkg.Actor{UserID: "peer", TenantID: "a"}, kind: "jwt", expiresAt: time.Now().Add(time.Hour).Unix()})
		args := map[string]any{"collection": "main", "dry_run": true, "max_duration_ms": float64(5000)}
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); <-start; h.runConsolidationJob(ctx, "claim", args) }()
		}
		close(start)
		select {
		case <-inputs:
		case <-time.After(3 * time.Second):
			t.Fatal("winning runner never executed")
		}
		unblock()
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(6 * time.Second):
			t.Fatal("duplicate runners did not return")
		}
		job := asyncAuthorityWaitJob(t, f, "claim")
		h.runConsolidationJob(ctx, "claim", args)
		if job.status != "completed" || job.owner != "peer" || job.candidates != 1 || calls.Load() != 1 {
			t.Fatalf("duplicate claim executed: job=%+v embed calls=%d", job, calls.Load())
		}
	})
}

func TestConsolidationAsyncClaimTimeoutFinalized(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		h := asyncAuthorityHandler(f)
		if err := ensureConsolidationJobs(context.Background(), f.db); err != nil {
			t.Fatal(err)
		}
		for _, row := range []struct{ id, owner, status string }{
			{"pending", "peer", "pending"}, {"terminal", "peer", "completed"},
			{"claimed", "peer", "running"}, {"foreign-job", "foreign", "pending"},
		} {
			result, candidates := "kept", 77
			if row.id == "pending" {
				result, candidates = "", 0
			}
			f.exec(`INSERT INTO consolidation_jobs(id,owner_id,status,args_json,result_text,candidates,created_at,updated_at) VALUES($1,$2,$3,'{}',$4,$5,'old','old')`, row.id, row.owner, row.status, result, candidates)
		}
		conn, err := f.db.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		verified := context.WithValue(context.Background(), searchEgressKey{}, searchEgress{cfg: h.cfg, actor: accesspkg.Actor{UserID: "peer", TenantID: "a"}, kind: "jwt", expiresAt: time.Now().Add(time.Hour).Unix()})
		observer, cancel := context.WithTimeout(verified, 10*time.Millisecond)
		defer cancel()
		args := map[string]any{"collection": "main", "dry_run": true, "max_duration_ms": float64(5)}
		var wg sync.WaitGroup
		for _, id := range []string{"pending", "terminal", "claimed", "foreign-job"} {
			wg.Add(1)
			go func(id string) { defer wg.Done(); h.runConsolidationJob(observer, id, args) }(id)
		}
		// Expire the real observer before releasing the sole connection, even
		// if a runner is scheduled late. Finalization needs a detached bound.
		<-observer.Done()
		if observer.Err() != context.DeadlineExceeded {
			t.Fatalf("observer=%v", observer.Err())
		}
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("expired claim finalization did not return after releasing SQL connection")
		}
		for _, expected := range []struct{ id, owner, status string }{
			{"pending", "peer", "failed"}, {"terminal", "peer", "completed"},
			{"claimed", "peer", "running"}, {"foreign-job", "foreign", "pending"},
		} {
			var got asyncAuthorityJob
			var updated string
			if err := f.db.QueryRow(Q(`SELECT owner_id,status,result_text,last_error,candidates,clusters,actions,updated_at FROM consolidation_jobs WHERE id=$1`), expected.id).Scan(&got.owner, &got.status, &got.result, &got.last, &got.candidates, &got.clusters, &got.actions, &updated); err != nil {
				t.Fatal(err)
			}
			if got.owner != expected.owner || got.status != expected.status {
				t.Fatalf("job=%s failed claim left/overwrote status: %+v", expected.id, got)
			}
			if expected.id == "pending" {
				if !strings.Contains(got.last, "execution did not start") || got.result != "" || got.candidates != 0 || got.clusters != 0 || got.actions != 0 {
					t.Fatalf("expired claim has no honest diagnostic: %+v", got)
				}
			} else if got.result != "kept" || got.last != "" || got.candidates != 77 || updated != "old" {
				t.Fatalf("job=%s control state changed: %+v updated=%q", expected.id, got, updated)
			}
		}
	})
}

func TestMCPConsolidationAsyncDurationValidation(t *testing.T) {
	t.Setenv("LEVARA_MCP_TOOLSET", "full")
	t.Setenv("LEVARA_TENANT_ENFORCED", "1")
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		h := asyncAuthorityHandler(f)
		app := asyncAuthorityApp(h, nil)
		if err := ensureConsolidationJobs(context.Background(), f.db); err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			name  string
			value any
		}{
			{"null", nil}, {"string", "500"}, {"boolean", true}, {"array", []any{1}}, {"object", map[string]any{}},
			{"fraction", 1.5}, {"negative", float64(-1)}, {"zero", float64(0)}, {"above-five-minutes", float64(300001)}, {"duration-overflow", float64(9223372036854775808)}, {"huge", 1e100},
		} {
			t.Run(tc.name, func(t *testing.T) {
				for _, path := range []string{"/mcp", latestMCPPath} {
					got := asyncAuthorityRPC(t, app, h, path, "peer", "consolidate", map[string]any{"collection": "main", "wait": false, "max_duration_ms": tc.value})
					if !got.IsError || !strings.Contains(got.Content[0].Text, "max_duration_ms must be an integer from 1 to 300000") {
						t.Fatalf("path=%s duration=%v accepted: %+v", path, tc.value, got)
					}
				}
			})
		}
		var count int
		if err := f.db.QueryRow(`SELECT COUNT(*) FROM consolidation_jobs`).Scan(&count); err != nil || count != 0 {
			t.Fatalf("invalid durations enqueued jobs: count=%d err=%v", count, err)
		}
	})
}

func TestConsolidationJobDurationBounds(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]any
		want time.Duration
	}{
		{"omitted", nil, 5 * time.Minute}, {"minimum", map[string]any{"max_duration_ms": float64(1)}, time.Millisecond}, {"maximum", map[string]any{"max_duration_ms": float64(300000)}, 5 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := consolidationJobDuration(tc.args)
			if err != nil || got != tc.want {
				t.Fatalf("duration=%v err=%v want=%v", got, err, tc.want)
			}
		})
	}
	for _, value := range []any{math.NaN(), math.Inf(1), math.Inf(-1), math.MaxFloat64, int(1)} {
		if got, err := consolidationJobDuration(map[string]any{"max_duration_ms": value}); err == nil || got != 0 {
			t.Fatalf("non-JSON/overflow duration=%v accepted: duration=%v err=%v", value, got, err)
		}
	}
}

func TestConsolidationAsyncLegacySchemaChecked(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		f.exec(`DROP TABLE IF EXISTS consolidation_jobs`)
		f.exec(`CREATE TABLE consolidation_jobs(id TEXT PRIMARY KEY,status TEXT NOT NULL,args_json TEXT NOT NULL,result_text TEXT NOT NULL DEFAULT '',last_error TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL,updated_at TEXT NOT NULL)`)
		f.exec(`INSERT INTO consolidation_jobs(id,status,args_json,result_text,created_at,updated_at) VALUES('legacy','completed','{}','kept','old','old')`)
		for i := 0; i < 2; i++ {
			if err := ensureConsolidationJobs(context.Background(), f.db); err != nil {
				t.Fatalf("initialization %d: %v", i, err)
			}
		}
		var owner, status, result string
		var candidates, clusters, actions, llmCalls int
		if err := f.db.QueryRow(`SELECT owner_id,status,result_text,candidates,clusters,actions,llm_calls FROM consolidation_jobs WHERE id='legacy'`).Scan(&owner, &status, &result, &candidates, &clusters, &actions, &llmCalls); err != nil {
			t.Fatal(err)
		}
		if owner != "" || status != "completed" || result != "kept" || candidates != 0 || clusters != 0 || actions != 0 || llmCalls != 0 {
			t.Fatalf("legacy row not preserved/defaulted: owner=%q status=%q result=%q progress=%d/%d/%d/%d", owner, status, result, candidates, clusters, actions, llmCalls)
		}
		// An incompatible legacy relation cannot silently suppress ALTER errors.
		f.exec(`DROP TABLE consolidation_jobs`)
		f.exec(`CREATE VIEW consolidation_jobs AS SELECT 'legacy' AS id,'completed' AS status,'{}' AS args_json`)
		err := ensureConsolidationJobs(context.Background(), f.db)
		if err == nil || (!strings.Contains(strings.ToLower(err.Error()), "view") && !strings.Contains(err.Error(), "SQLSTATE 42809")) {
			t.Fatalf("incompatible legacy relation did not report ALTER failure: %v", err)
		}
	})
}

func TestConsolidationAsyncRecoveryAuthority(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		asyncAuthoritySeedMemories(f)
		if err := ensureConsolidationJobs(context.Background(), f.db); err != nil {
			t.Fatal(err)
		}
		for _, authenticated := range []bool{false, true} {
			t.Run(fmt.Sprintf("require-auth=%t", authenticated), func(t *testing.T) {
				prefix := fmt.Sprintf("auth-%t-", authenticated)
				jobs := []struct {
					name, owner, status, raw string
					complete                 bool
				}{
					{"shared-pending", "", "pending", `{"collection":"main","shared":true,"dry_run":true}`, !authenticated},
					{"shared-running", "", "running", `{"collection":"main","shared":true,"dry_run":true}`, false},
					{"private-pending", "peer", "pending", `{"collection":"main","dry_run":true,"owner_id":"peer","trusted_local":true}`, false},
					{"private-running", "peer", "running", `{"collection":"main","dry_run":true}`, false},
					{"invalid-json", "", "pending", `{`, false},
					{"invalid-duration", "", "pending", `{"collection":"main","max_duration_ms":300001}`, false},
				}
				for _, job := range jobs {
					f.exec(`INSERT INTO consolidation_jobs(id,owner_id,status,args_json,created_at,updated_at) VALUES($1,$2,$3,$4,'old','old')`, prefix+job.name, job.owner, job.status, job.raw)
				}
				f.exec(`INSERT INTO consolidation_jobs(id,owner_id,status,args_json,result_text,candidates,created_at,updated_at) VALUES($1,'peer','completed','{}','kept',77,'old','old')`, prefix+"terminal")
				cfg := f.cfg
				cfg.RequireAuth = authenticated
				done := make(chan struct{})
				go func() { StartConsolidationRecovery(cfg); close(done) }()
				select {
				case <-done:
				case <-time.After(time.Second):
					f.db.SetMaxOpenConns(2)
					t.Fatal("recovery did not return with a one-connection pool")
				}
				for _, expected := range jobs {
					got := asyncAuthorityWaitJob(t, f, prefix+expected.name)
					if expected.complete {
						if got.status != "completed" || got.owner != "" || got.candidates != 1 || got.last != "" {
							t.Fatalf("trusted-local pending shared recovery=%+v", got)
						}
					} else if got.status != "failed" || got.candidates != 0 || got.result != "" || !strings.Contains(got.last, "unknown outcome") || !strings.Contains(got.last, "resubmit") {
						t.Fatalf("job=%s recovery invented authority/outcome: %+v", expected.name, got)
					}
				}
				terminal := asyncAuthorityWaitJob(t, f, prefix+"terminal")
				if terminal.status != "completed" || terminal.result != "kept" || terminal.candidates != 77 {
					t.Fatalf("terminal job changed: %+v", terminal)
				}
			})
		}
	})
}
