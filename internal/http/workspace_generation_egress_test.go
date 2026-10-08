package http

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/mcp"
	"github.com/valyala/fasthttp"
)

// Transfer-boundary oracle: credential facts model verified middleware, while
// project admission, zero-hit workspace build, and completed-body sender are real.
func TestWorkspaceGenerationEmptySearchEgress(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		cfg, closeCfg := newWorkspaceTestConfig(t)
		defer closeCfg()
		cfg.DB, cfg.RequireAuth = f.db, true
		const collection = "empty_workspace"
		if _, err := cfg.Collections.GetOrCreateWithDim(collection, 2, "test-embed"); err != nil {
			t.Fatal(err)
		}
		owner := accesspkg.MetadataActor{Actor: f.owner, Credential: accesspkg.MetadataCredential{Kind: "jwt", ExpiresAt: time.Now().Add(time.Hour).Unix()}}
		req := workspaceReconcileRequest{workspaceReindexRequest: workspaceReindexRequest{ProjectID: "alpha", Branch: "main", Generation: "empty_generation", Collection: collection, ActivateGeneration: true}, DeleteMissing: true}
		if _, err := reconcileWorkspaceMarkdownAuthorized(context.Background(), cfg, req, owner); err != nil {
			t.Fatal(err)
		}
		for _, mode := range []string{"live200", "live400", "revoked"} {
			t.Run(mode, func(t *testing.T) {
				parent, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				actor := accesspkg.Actor{UserID: "viewer", TenantID: "a"}
				ctx := context.WithValue(parent, mcp.UserIDKey, actor.UserID)
				ctx = context.WithValue(ctx, mcp.TenantIDKey, actor.TenantID)
				ctx = context.WithValue(ctx, searchActorKey{}, actor)
				ctx = context.WithValue(ctx, searchEgressKey{}, searchEgress{cfg: cfg, actor: actor, kind: "jwt", expiresAt: time.Now().Add(time.Hour).Unix()})
				evidence := &searchEvidence{sources: make(map[searchDocumentSource]struct{})}
				ctx = context.WithValue(ctx, searchEvidenceKey{}, evidence)
				result := (&mcpHandler{cfg: cfg}).toolWorkspaceSearch(ctx, map[string]any{"project_id": "alpha", "branch": "main", "search_query": "unmatched query", "search_type": "BM25", "mode": "full", "collection": collection})
				if result.IsError || len(result.Content) != 1 {
					t.Fatalf("workspace admission/build failed: %+v", result)
				}
				var payload map[string]any
				if err := json.Unmarshal([]byte(result.Content[0].Text), &payload); err != nil {
					t.Fatal(err)
				}
				hits, ok := payload["results"].([]any)
				if !ok || len(hits) != 0 || payload["project_id"] != "alpha" || payload["generic_search_status"] != "ok" {
					t.Fatalf("zero-hit control failed: %v", payload)
				}
				if len(searchSources(ctx)) != 0 {
					t.Fatal("fixture must have no document evidence")
				}
				if f.db.Stats().InUse != 0 {
					t.Fatal("build retained a SQL connection")
				}
				if mode == "revoked" {
					f.exec("DELETE FROM dataset_shares WHERE id='viewer-share'")
				}
				app := fiber.New()
				c := app.AcquireCtx(&fasthttp.RequestCtx{})
				defer app.ReleaseCtx(c)
				status := 200
				if mode == "live400" {
					status = 400
				}
				c.Status(status)
				if err := c.JSON(payload); err != nil {
					t.Fatal(err)
				}
				err := sendProtectedResponseWithFence(c, ctx)
				if mode == "revoked" {
					var denied *fiber.Error
					if !errors.As(err, &denied) || denied.Code != 403 {
						t.Fatalf("revoked zero-hit transfer error=%v", err)
					}
					if c.Response().IsBodyStream() {
						t.Fatal("revoked project installed a metadata stream")
					}
					// Fiber's ordinary error handling replaces the staged unsent body.
					if err := app.Config().ErrorHandler(c, err); err != nil {
						t.Fatal(err)
					}
					body := string(c.Response().Body())
					if c.Response().StatusCode() != 403 || strings.Contains(body, "alpha") || strings.Contains(body, "empty_generation") || strings.Contains(body, "manifest_path") || strings.Contains(body, "freshness") {
						t.Fatalf("denied transfer emitted project metadata: %s", body)
					}
					if f.db.Stats().InUse != 0 {
						t.Fatal("denied transfer leaked SQL fence")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if c.Response().StatusCode() != status {
					t.Fatalf("sender changed response status: %d", c.Response().StatusCode())
				}
				stream, ok := c.Response().BodyStream().(*fencedResponse)
				if !ok {
					t.Fatal("live empty response lacks retained body fence")
				}
				defer stream.Close()
				if f.db.Stats().InUse != 1 {
					t.Fatal("live empty response did not retain SQL")
				}
				body, err := io.ReadAll(stream)
				if err != nil || !strings.Contains(string(body), "empty_generation") {
					t.Fatalf("live body=%q error=%v", body, err)
				}
				if f.db.Stats().InUse != 1 {
					t.Fatal("body EOF released authority before actual Close")
				}
				if err := stream.Close(); err != nil {
					t.Fatal(err)
				}
				if err := stream.Close(); err != nil {
					t.Fatal(err)
				}
				if f.db.Stats().InUse != 0 {
					t.Fatal("actual Close leaked retained SQL")
				}
			})
		}
	})
}
