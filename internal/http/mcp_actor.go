package http

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/gofiber/fiber/v2"
	accesspkg "github.com/stek0v/levara/pkg/access"
)

type verifiedMCPAuthorization struct {
	UserID      string
	TenantID    string
	Superuser   bool
}

type verifiedMCPSuperuserKey struct{}

func verifiedMCPAuthorizationFor(c *fiber.Ctx, userID string) (verifiedMCPAuthorization, bool) {
	verified, ok := c.Locals("verified_mcp_authorization").(verifiedMCPAuthorization)
	return verified, ok && userID != "" && verified.UserID == userID
}

func verifiedMCPDefaultTenantFor(c *fiber.Ctx, userID string) (string, bool) {
	verified, ok := verifiedMCPAuthorizationFor(c, userID)
	return verified.TenantID, ok
}

func verifiedMCPSuperuser(ctx context.Context, userID string) (bool, bool) {
	verified, ok := ctx.Value(verifiedMCPSuperuserKey{}).(verifiedMCPAuthorization)
	return verified.Superuser, ok && userID != "" && verified.UserID == userID
}

func verifiedMCPActor(c *fiber.Ctx, userID string) bool {
	if userID == "" {
		return false
	}
	if identity, ok := c.Locals("verified_api_key").(accesspkg.APIKeyIdentity); ok && identity.UserID == userID {
		return true
	}
	if payload, ok := c.Locals("verified_jwt").(jwtPayload); ok && payload.Sub == userID {
		return true
	}
	principal, ok := c.Locals("verified_external").(ExternalPrincipal)
	return ok && principal.UserID == userID
}

func (h *mcpHandler) resolveMCPActorTenant(c *fiber.Ctx, actor accesspkg.Actor) (accesspkg.Actor, error) {
	tenant := strings.Clone(c.Get("X-Tenant-Id"))
	if actor.UserID == "" {
		if tenant != "" {
			return actor, fmt.Errorf("tenant requires authentication")
		}
		return actor, nil
	}
	if h.cfg.DB == nil {
		if !h.cfg.RequireAuth && tenant == "" {
			return actor, nil
		}
		return actor, fmt.Errorf("database required for actor authorization")
	}
	policy := accesspkg.SQLPolicy{DB: h.cfg.DB, Q: Q}
	if !verifiedMCPActor(c, actor.UserID) {
		active, err := policy.IsActive(c.UserContext(), actor.UserID)
		if err != nil || !active {
			return actor, fmt.Errorf("inactive or unknown actor")
		}
	}
	var err error
	if tenant != "" {
		member, err := policy.IsTenantMember(c.UserContext(), actor.UserID, tenant)
		if err != nil || !member {
			return actor, fmt.Errorf("tenant access denied")
		}
	} else if verifiedTenant, ok := verifiedMCPDefaultTenantFor(c, actor.UserID); ok {
		tenant = verifiedTenant
	} else {
		tenant, err = tenantDefaultForUser(c.UserContext(), h.cfg.DB, actor.UserID)
		if err != nil {
			return actor, fmt.Errorf("tenant resolution failed")
		}
	}
	if tenant == "" && (os.Getenv("LEVARA_TENANT_ENFORCED") == "1" || strings.EqualFold(os.Getenv("LEVARA_TENANT_ENFORCED"), "true")) {
		return actor, fmt.Errorf("tenant membership required")
	}
	actor.TenantID = tenant
	return actor, nil
}
