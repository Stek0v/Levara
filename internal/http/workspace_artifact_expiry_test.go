package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/valyala/fasthttp"
)

func TestWorkspaceArtifactExpiryBoundary(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		cfg, closeCfg := newWorkspaceTestConfig(t)
		defer closeCfg()
		cfg.DB, cfg.RequireAuth = f.db, true
		f.db.SetMaxOpenConns(1)
		file, _, err := workspaceFilePath(cfg, "alpha", "main", "note.md")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(strings.Repeat("Copper seventeen. Zinc twenty-three. Total forty units. ", 20)), 0600); err != nil {
			t.Fatal(err)
		}
		registry := workspaceContextArtifactRegistry{Version: workspaceArtifactRegistryVersion, Artifacts: []workspaceContextArtifactRequest{{ProjectID: "alpha", Path: "note.md"}}}
		p := workspaceContextArtifactRegistryPath(cfg)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(registry)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, body, 0600); err != nil {
			t.Fatal(err)
		}
		t.Run("rest_preserves_forbidden_status", func(t *testing.T) {
			raw := &fasthttp.RequestCtx{}
			raw.Request.Header.SetMethod("POST")
			raw.Request.Header.SetContentType("application/json")
			raw.Request.SetBodyString(`{"project_id":"alpha","generation":"expired-admission"}`)
			c := f.app.AcquireCtx(raw)
			defer f.app.ReleaseCtx(c)
			c.Locals("user_id", "owner")
			c.Locals("tenant_id", "a")
			c.Locals("verified_jwt", jwtPayload{Sub: "owner", Exp: time.Now().Add(-time.Second).Unix()})
			err := workspaceReindexArtifactsHandler(cfg)(c)
			code := c.Response().StatusCode()
			var authority *fiber.Error
			if errors.As(err, &authority) {
				code = authority.Code
			}
			if code != fiber.StatusForbidden {
				t.Fatalf("final authority status=%d error=%v", code, err)
			}
		})
		t.Run("expires_during_embedding", func(t *testing.T) {
			expires := time.Now().Unix() + 2
			endpoint, err := url.Parse(cfg.EmbedEndpoint)
			if err != nil {
				t.Fatal(err)
			}
			proxy := httputil.NewSingleHostReverseProxy(endpoint)
			entered := make(chan struct{}, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				entered <- struct{}{}
				time.Sleep(time.Until(time.Unix(expires, 0)) + 100*time.Millisecond)
				proxy.ServeHTTP(w, r)
			}))
			defer server.Close()
			requestCfg := cfg
			requestCfg.EmbedEndpoint = server.URL
			actor := accesspkg.MetadataActor{Actor: f.owner, Credential: accesspkg.MetadataCredential{Kind: "jwt", ExpiresAt: expires}}
			request := workspaceReindexArtifactsRequest{workspaceReindexRequest: workspaceReindexRequest{ProjectID: "alpha", Generation: "expired-provider", Collection: "expired_artifacts", ActivateGeneration: true}}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := reindexWorkspaceContextArtifactsAuthorized(ctx, requestCfg, request, actor); err == nil {
				t.Error("expired provider result published")
			}
			select {
			case <-entered:
			default:
				t.Fatal("provider fixture was never reached")
			}
			manifest, _, err := loadWorkspaceManifest(cfg, "alpha", "main")
			if err != nil {
				t.Fatal(err)
			}
			if manifest.ActiveGeneration != "" || len(manifest.Chunks) != 0 || f.db.Stats().InUse != 0 {
				t.Fatal("expiry published active chunks or leaked SQL")
			}
			collection, err := cfg.Collections.Get("expired_artifacts")
			if err == nil && collection.Count() != 0 {
				t.Fatal("expired vectors remain in native collection")
			}
		})
	})
}
