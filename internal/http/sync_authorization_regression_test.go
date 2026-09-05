package http

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/pkg/mcp"
)

func TestSyncRejectsOrdinaryAuthenticatedUser(t *testing.T) {
	db := newMCPMemoryBehaviorDB(t)
	for _, q := range []string{
		`INSERT INTO principals(id) VALUES('ordinary')`,
		`INSERT INTO users(id,email,hashed_password) VALUES('ordinary','ordinary@example.test','unused')`,
		`INSERT INTO memories(id,key,value,owner_id,collection_name) VALUES('private','private','victim secret','victim','private')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	app := fiber.New()
	api := app.Group("/api/v1", JWTMiddleware(syncTestSecret, true), APIKeyPermissionMiddleware())
	RegisterSyncAPI(api, APIConfig{DB: db, RequireAuth: true})
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/sync/export/memories", ""},
		{"POST", "/sync/import/memories", `[{"id":"private","key":"private","value":"poisoned","owner_id":"victim","collection_name":"private","created_at":"2026-09-05T00:00:00Z","updated_at":"2099-01-01T00:00:00Z"}]`},
		{"GET", "/sync/export/interactions", ""},
		{"POST", "/sync/import/interactions", `[]`},
		{"GET", "/sync/export/graph", ""},
		{"POST", "/sync/import/graph", `{"nodes":[],"edges":[]}`},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "/api/v1"+tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer "+createJWT("ordinary", "ordinary@example.test", syncTestSecret))
			req.Header.Set("Content-Type", "application/json")
			resp, err := app.Test(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("status=%d, want 403 before sync access", resp.StatusCode)
			}
		})
	}
	var value string
	if err := db.QueryRow(`SELECT value FROM memories WHERE id='private'`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "victim secret" {
		t.Errorf("foreign memory changed to %q", value)
	}
}

func TestSyncDoesNotSendServerTokenToUntrustedDestination(t *testing.T) {
	var calls atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(w, `{"version":"test"}`)
	}))
	defer remote.Close()
	ctx := context.WithValue(context.Background(), mcp.UserIDKey, "ordinary")
	h := &mcpHandler{cfg: APIConfig{RequireAuth: true, SyncToken: "server-secret-test-only"}}
	_, _, err := h.DoSync(ctx, remote.URL, "pull", []string{"none"}, "", nil)
	if err == nil || calls.Load() != 0 {
		t.Fatalf("err=%v outbound_requests=%d; untrusted sync must fail before sending credentials", err, calls.Load())
	}
}

func TestSyncDoesNotFollowCredentialRedirect(t *testing.T) {
	var leaked atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked.Store(r.Header.Get("Authorization") != "")
		io.WriteString(w, `{}`)
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer origin.Close()
	resp, _ := syncAuthGet(&http.Client{}, origin.URL, "server-secret-test-only")
	if resp != nil {
		resp.Body.Close()
	}
	if leaked.Load() {
		t.Fatal("sync followed redirect and disclosed bearer token to another origin")
	}
}

func TestSyncTrustedDestinationAndRevocation(t *testing.T) {
	db := newMCPMemoryBehaviorDB(t)
	for _, query := range []string{`INSERT INTO principals(id) VALUES('admin')`, `INSERT INTO users(id,email,hashed_password,is_superuser,is_active) VALUES('admin','admin@example.test','unused',TRUE,TRUE)`} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	var calls atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer isolated-token" {
			t.Error("missing configured credential")
		}
		io.WriteString(w, `{"version":"test"}`)
	}))
	defer remote.Close()
	ctx := context.WithValue(context.Background(), mcp.UserIDKey, "admin")
	cfg := APIConfig{DB: db, RequireAuth: true, SyncToken: "isolated-token", SyncRemoteURL: remote.URL + "/api/v1"}
	for _, candidate := range []string{remote.URL + "/other", "http://other.invalid/api/v1", "ftp://other.invalid/api/v1", remote.URL + "/api/v1?query=1", "http://user:pass@localhost/api/v1"} {
		if _, _, err := (&mcpHandler{cfg: cfg}).DoSync(ctx, candidate, "pull", []string{"none"}, "", nil); err == nil {
			t.Errorf("accepted untrusted %q", candidate)
		}
	}
	withoutTarget := cfg
	withoutTarget.SyncRemoteURL = ""
	if _, _, err := (&mcpHandler{cfg: withoutTarget}).DoSync(ctx, cfg.SyncRemoteURL, "pull", []string{"none"}, "", nil); err == nil {
		t.Error("missing trusted destination accepted")
	}
	readCtx := context.WithValue(ctx, mcpAPIKeyPermissionsKey, "read")
	if _, _, err := (&mcpHandler{cfg: cfg}).DoSync(readCtx, cfg.SyncRemoteURL, "pull", []string{"none"}, "", nil); err == nil {
		t.Error("read-only key accepted")
	}
	if calls.Load() != 0 {
		t.Fatal("denied operation contacted remote")
	}
	if _, _, err := (&mcpHandler{cfg: cfg}).DoSync(ctx, cfg.SyncRemoteURL, "pull", []string{"none"}, "", nil); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("authorized control did not contact configured remote")
	}
	if _, err := db.Exec(`UPDATE users SET is_active=FALSE WHERE id='admin'`); err != nil {
		t.Fatal(err)
	}
	if err := authorizeSync(ctx, cfg); err == nil {
		t.Error("inactive admin accepted")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := authorizeSync(ctx, cfg); err == nil {
		t.Error("database error failed open")
	}
}

type syncAuditTransport func(*http.Request) (*http.Response, error)

func (f syncAuditTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSyncCollectionPullImportsLocallyWithoutForwardingCredential(t *testing.T) {
	cfg, cleanup := newWorkspaceTestConfig(t)
	defer cleanup()
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer isolated-token" {
			t.Error("missing remote credential")
		}
		io.WriteString(w, `{"collection":"local-import","records":[{"id":"record","text":"verified local import","metadata":{}}]}`)
	}))
	defer remote.Close()
	cfg.SyncToken = "isolated-token"
	transport := http.DefaultTransport
	defer func() { http.DefaultTransport = transport }()
	var unexpected atomic.Int32
	http.DefaultTransport = syncAuditTransport(func(r *http.Request) (*http.Response, error) {
		if !strings.HasPrefix(r.URL.String(), remote.URL+"/") && !strings.HasPrefix(r.URL.String(), cfg.EmbedEndpoint+"/") {
			unexpected.Add(1)
			return nil, fmt.Errorf("unexpected outbound destination")
		}
		if strings.HasPrefix(r.URL.String(), cfg.EmbedEndpoint+"/") && r.Header.Get("Authorization") != "" {
			t.Error("credential sent to embed endpoint")
		}
		return transport.RoundTrip(r)
	})
	result := syncPullCollections(cfg, remote.URL, []string{"local-import"})
	payload, _ := json.Marshal(result["local-import"])
	var started struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(payload, &started); err != nil || started.RunID == "" {
		t.Fatalf("import not started: %s", payload)
	}
	deadline := time.After(5 * time.Second)
	for {
		status, ok := syncImportRuns.Load(started.RunID)
		if ok && status.Status != "RUNNING" {
			if status.Status != "COMPLETED" || status.Processed != 1 {
				t.Fatalf("import=%+v", status)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatal("import did not finish")
		case <-time.After(time.Millisecond):
		}
	}
	if unexpected.Load() != 0 {
		t.Fatal("collection pull contacted an unconfigured destination")
	}
	if !cfg.Collections.HasRecord("local-import", "record") {
		t.Fatal("local vector absent")
	}
}

func TestSyncStatusHiddenThroughHeartbeat(t *testing.T) {
	db := newMCPMemoryBehaviorDB(t)
	if _, err := db.Exec(`INSERT INTO heartbeats(id,event_type,payload,created_at) VALUES('sync-secret','sync','{"remote":"private-host"}','2026-09-05'),('public','test','{}','2026-09-05')`); err != nil {
		t.Fatal(err)
	}
	h := &mcpHandler{cfg: APIConfig{DB: db, RequireAuth: true}}
	ctx := context.WithValue(context.Background(), mcp.UserIDKey, "ordinary")
	if !h.toolSyncStatus(ctx, nil).IsError || !h.toolHeartbeat(ctx, map[string]any{"event_type": "sync"}).IsError {
		t.Fatal("ordinary caller read sync status")
	}
	result := h.toolHeartbeat(ctx, nil)
	if result.IsError || strings.Contains(result.Content[0].Text, "private-host") {
		t.Fatalf("heartbeat leaked sync or failed: %+v", result)
	}
}
