package http

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"

	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/mcp"
)

// Phase-1 B3 invariant (MODERNIZATION_BRIEF.md §5): both transports must
// carry the authenticated caller's API-key permissions into the
// transport-independent Actor. The policy facade enforces "key narrows,
// never widens"; these tests pin the adapter side — the permissions string
// must reach the Actor verbatim on REST (Fiber locals) and MCP (call
// context). gRPC has no API-key authentication path (verified 2026-10-16:
// zero APIKey references under internal/grpc), so the invariant is vacuous
// there until a key path is introduced through access.Actor.

func TestWorkspaceActorFromFiberCarriesAPIKeyPermissions(t *testing.T) {
	app := fiber.New()
	var got accesspkg.Actor
	app.Get("/actor", func(c *fiber.Ctx) error {
		c.Locals("user_id", "user-1")
		c.Locals("api_key_permissions", "read write")
		c.Locals("tenant_id", "tenant-1")
		got = workspaceActorFromFiber(c)
		return c.SendStatus(fiber.StatusOK)
	})
	resp, err := app.Test(httptest.NewRequest(fiber.MethodGet, "/actor", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got.UserID != "user-1" || got.APIKeyPermissions != "read write" || got.TenantID != "tenant-1" {
		t.Fatalf("actor = %+v, want permissions carried verbatim", got)
	}
}

func TestWorkspaceActorFromFiberDefaultsWhenUnauthenticated(t *testing.T) {
	app := fiber.New()
	var got accesspkg.Actor
	app.Get("/actor", func(c *fiber.Ctx) error {
		got = workspaceActorFromFiber(c) // no locals set
		return c.SendStatus(fiber.StatusOK)
	})
	if _, err := app.Test(httptest.NewRequest(fiber.MethodGet, "/actor", nil)); err != nil {
		t.Fatal(err)
	}
	if got.UserID != "" || got.APIKeyPermissions != "" || got.TenantID != "" {
		t.Fatalf("actor = %+v, want zero Actor for unauthenticated request", got)
	}
}

func TestWorkspaceActorFromMCPCarriesAPIKeyPermissions(t *testing.T) {
	ctx := context.WithValue(context.Background(), mcpUserIDKey, "user-1")
	ctx = context.WithValue(ctx, mcpAPIKeyPermissionsKey, "admin")
	ctx = context.WithValue(ctx, mcp.TenantIDKey, "tenant-1")
	got := workspaceActorFromMCP(ctx)
	if got.UserID != "user-1" || got.APIKeyPermissions != "admin" || got.TenantID != "tenant-1" {
		t.Fatalf("actor = %+v, want permissions carried verbatim", got)
	}
}
