package http

import (
	"fmt"
	"os"
	"strings"

	"github.com/gofiber/fiber/v2"
	accesspkg "github.com/stek0v/levara/pkg/access"
)

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
	active, err := policy.IsActive(c.UserContext(), actor.UserID)
	if err != nil || !active {
		return actor, fmt.Errorf("inactive or unknown actor")
	}
	if tenant != "" {
		member, err := policy.IsTenantMember(c.UserContext(), actor.UserID, tenant)
		if err != nil || !member {
			return actor, fmt.Errorf("tenant access denied")
		}
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
