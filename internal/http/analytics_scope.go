package http

import (
	"github.com/gofiber/fiber/v2"
	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/audit"
)

func scopedAuditFilter(c *fiber.Ctx, cfg APIConfig, f audit.EventFilter) (audit.EventFilter, error) {
	user, _ := c.Locals("user_id").(string)
	if user == "" {
		if cfg.RequireAuth {
			return f, fiber.NewError(401, "authentication required")
		}
		return f, nil
	}
	if cfg.DB == nil {
		return f, fiber.NewError(503, "database required for analytics authorization")
	}
	policy := accesspkg.SQLPolicy{DB: cfg.DB, Q: Q}
	active, err := policy.IsActive(c.UserContext(), user)
	if err != nil || !active {
		return f, fiber.NewError(403, "analytics access denied")
	}
	admin, err := policy.IsSuperuser(c.UserContext(), user)
	if err != nil {
		return f, fiber.NewError(403, "analytics access denied")
	}
	tenant := ResolveTenantID(c)
	if tenant != "" {
		member, err := policy.IsTenantMember(c.UserContext(), user, tenant)
		if err != nil || !member {
			return f, fiber.NewError(403, "tenant access denied")
		}
	}
	if !admin {
		f.AgentID = user
		f.RequireVerifiedScope = true
	}
	f.RestrictTenant = !admin || tenant != ""
	f.TenantID = tenant
	return f, nil
}
