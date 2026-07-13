// tenants.go — Multi-tenant management, ACL endpoints, and tenant isolation middleware.
package http

import (
	"context"
	"database/sql"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	accesspkg "github.com/stek0v/levara/pkg/access"
)

func RegisterTenantAPI(app fiber.Router, cfg APIConfig) {
	app.Post("/tenants", tenantCreateHandler(cfg))
	app.Get("/tenants", tenantListHandler(cfg))
	app.Get("/tenants/mine", tenantMyTenantsHandler(cfg))
	app.Post("/tenants/select", tenantSelectHandler(cfg))
	app.Post("/tenants/:id/users", tenantAddUserHandler(cfg))
	app.Delete("/tenants/:id/users/:uid", tenantRemoveUserHandler(cfg))
	app.Post("/acl", aclGrantHandler(cfg))
	app.Get("/acl/check", aclCheckHandler(cfg))
}

// TenantMiddleware resolves the active tenant for the current user.
// Priority: verified X-Tenant-Id header > user's single tenant > empty (no isolation).
// Sets c.Locals("tenant_id") for downstream handlers.
func TenantMiddleware(accessCfg AccessConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		db := accessCfg.DB
		// 1. Explicit header
		tenantID := c.Get("X-Tenant-Id")
		if tenantID != "" {
			if db != nil {
				userID, _ := c.Locals("user_id").(string)
				if userID == "" {
					return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"detail": "tenant membership required"})
				}
				member, err := tenantUserIsMember(c.UserContext(), db, userID, tenantID)
				if err != nil {
					return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"detail": "tenant membership check failed"})
				}
				if !member {
					return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"detail": "not a member of this tenant"})
				}
			}
			c.Locals("tenant_id", tenantID)
			return c.Next()
		}

		// 2. Resolve from user's tenant memberships (policy decides; a query
		// failure leaves the request in the no-isolation path, as before).
		userID, _ := c.Locals("user_id").(string)
		if userID != "" && db != nil {
			if tid, _ := tenantDefaultForUser(c.UserContext(), db, userID); tid != "" {
				c.Locals("tenant_id", tid)
			}
		}

		// 3. No tenant = no isolation (dev mode / single-user)
		return c.Next()
	}
}

// ResolveTenantID returns the active tenant_id from context, or "".
func ResolveTenantID(c *fiber.Ctx) string {
	tid, _ := c.Locals("tenant_id").(string)
	return tid
}

// TenantFilter returns SQL WHERE clause for tenant isolation.
// If tenant_id is set: filters by owner's tenant membership.
// If empty: no filtering (dev mode / single-user).
func TenantFilter(tenantID string) string {
	clause, _ := TenantFilterSQL(tenantID, 1)
	return clause
}

// TenantFilterSQL returns a tenant-isolation SQL clause plus bind args. The
// clause shape and placeholder dialect are decided by the shared policy in
// pkg/access; only the sqlite-vs-positional choice is a transport detail.
func TenantFilterSQL(tenantID string, startIdx int) (string, []any) {
	return accesspkg.TenantOwnerFilterSQL(tenantID, startIdx, GetDBProvider() == DBSQLite)
}

// tenantUserIsMember delegates the membership decision to the shared
// transport-independent policy in pkg/access so REST handlers do not embed
// authorization SQL of their own.
func tenantUserIsMember(ctx context.Context, db *sql.DB, userID, tenantID string) (bool, error) {
	return accesspkg.SQLPolicy{DB: db, Q: Q}.IsTenantMember(ctx, userID, tenantID)
}

// tenantDefaultForUser delegates the auto-select decision (which tenant to
// activate when no X-Tenant-Id header is present) to the shared policy, keeping
// the middleware free of tenant-resolution SQL.
func tenantDefaultForUser(ctx context.Context, db *sql.DB, userID string) (string, error) {
	return accesspkg.SQLPolicy{DB: db, Q: Q}.DefaultTenantForUser(ctx, userID)
}

// CollectionPrefix returns the tenant-scoped collection name.
// If tenant_id is set: "{tenant_id}/{collection}".
// If empty: collection as-is (backward compatible).
func CollectionPrefix(tenantID, collection string) string {
	if tenantID == "" {
		return collection
	}
	return tenantID + "/" + collection
}

// tenantSelectHandler lets user pick their active tenant (for multi-tenant users).
func tenantSelectHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req struct {
			TenantID string `json:"tenant_id"`
		}
		c.BodyParser(&req)
		if req.TenantID == "" {
			return c.Status(400).JSON(fiber.Map{"detail": "tenant_id required"})
		}
		// Verify user belongs to this tenant
		userID, _ := c.Locals("user_id").(string)
		if cfg.DB != nil && userID != "" {
			member, err := tenantUserIsMember(c.UserContext(), cfg.DB, userID, req.TenantID)
			if err != nil || !member {
				return c.Status(403).JSON(fiber.Map{"detail": "not a member of this tenant"})
			}
		}
		return c.JSON(fiber.Map{"tenant_id": req.TenantID, "selected": true})
	}
}

// tenantMyTenantsHandler lists tenants the current user belongs to.
func tenantMyTenantsHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		userID, _ := c.Locals("user_id").(string)
		if cfg.DB == nil || userID == "" {
			return c.JSON([]any{})
		}
		rows, err := cfg.DB.QueryContext(context.Background(),
			Q(`SELECT t.id, t.name, t.owner_id FROM tenants t
			   JOIN user_tenant ut ON ut.tenant_id = t.id
			   WHERE ut.user_id = $1`), userID)
		if err != nil {
			return c.JSON([]any{})
		}
		defer rows.Close()
		var tenants []fiber.Map
		for rows.Next() {
			var id, name, ownerID string
			rows.Scan(&id, &name, &ownerID)
			tenants = append(tenants, fiber.Map{"id": id, "name": name, "owner_id": ownerID})
		}
		if tenants == nil {
			tenants = []fiber.Map{}
		}
		return c.JSON(tenants)
	}
}

func tenantCreateHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req struct {
			Name string `json:"name"`
		}
		if err := c.BodyParser(&req); err != nil || req.Name == "" {
			return c.Status(400).JSON(fiber.Map{"detail": "name required"})
		}
		id := uuid.New().String()
		ownerID, _ := c.Locals("user_id").(string)
		if cfg.DB != nil {
			if ownerID == "" {
				return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"detail": "authorization required"})
			}
			tx, err := cfg.DB.BeginTx(c.UserContext(), nil)
			if err != nil {
				return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"detail": "tenant creation failed"})
			}
			defer tx.Rollback()
			result, err := tx.ExecContext(c.UserContext(),
				Q("INSERT INTO tenants (id, name, owner_id, created_at) VALUES ($1, $2, $3, $4) ON CONFLICT (name) DO NOTHING"),
				id, req.Name, ownerID, time.Now().UTC())
			if err != nil {
				return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"detail": "tenant creation failed"})
			}
			if inserted, err := result.RowsAffected(); err == nil && inserted == 0 {
				return c.Status(fiber.StatusConflict).JSON(fiber.Map{"detail": "tenant name already exists"})
			}
			if _, err := tx.ExecContext(c.UserContext(),
				Q("INSERT INTO user_tenant (user_id, tenant_id) VALUES ($1, $2) ON CONFLICT DO NOTHING"),
				ownerID, id); err != nil {
				return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"detail": "tenant creation failed"})
			}
			if err := tx.Commit(); err != nil {
				return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"detail": "tenant creation failed"})
			}
		}
		return c.Status(201).JSON(fiber.Map{"id": id, "name": req.Name, "owner_id": ownerID})
	}
}

func tenantListHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if cfg.DB == nil {
			return c.JSON([]any{})
		}
		userID, _ := c.Locals("user_id").(string)
		showAll := userID == ""
		if !showAll {
			var err error
			showAll, err = (accesspkg.SQLPolicy{DB: cfg.DB, Q: Q}).IsSuperuser(c.UserContext(), userID)
			if err != nil {
				return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"detail": "tenant listing failed"})
			}
		}
		query := "SELECT id, name, owner_id, created_at FROM tenants ORDER BY created_at"
		args := []any(nil)
		if !showAll {
			query = `SELECT t.id, t.name, t.owner_id, t.created_at FROM tenants t
				JOIN user_tenant ut ON ut.tenant_id = t.id
				WHERE ut.user_id = $1 ORDER BY t.created_at`
			args = []any{userID}
		}
		rows, err := cfg.DB.QueryContext(c.UserContext(), Q(query), args...)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"detail": "tenant listing failed"})
		}
		defer rows.Close()
		var tenants []fiber.Map
		for rows.Next() {
			var id, name, ownerID, ca string
			if err := rows.Scan(&id, &name, &ownerID, &ca); err != nil {
				return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"detail": "tenant listing failed"})
			}
			tenants = append(tenants, fiber.Map{"id": id, "name": name, "owner_id": ownerID, "created_at": ca})
		}
		if tenants == nil {
			tenants = []fiber.Map{}
		}
		if err := rows.Err(); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"detail": "tenant listing failed"})
		}
		return c.JSON(tenants)
	}
}

func requireTenantManager(c *fiber.Ctx, cfg APIConfig, tenantID string) bool {
	if cfg.DB == nil {
		return true
	}
	userID, _ := c.Locals("user_id").(string)
	allowed, err := (accesspkg.SQLPolicy{DB: cfg.DB, Q: Q}).CanManageTenant(c.UserContext(), userID, tenantID)
	if err != nil {
		_ = c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"detail": "tenant authorization failed"})
		return false
	}
	if !allowed {
		_ = c.Status(fiber.StatusForbidden).JSON(fiber.Map{"detail": "tenant owner or superuser required"})
		return false
	}
	return true
}

func tenantAddUserHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		tenantID := c.Params("id")
		var req struct {
			UserID string `json:"user_id"`
		}
		c.BodyParser(&req)
		if req.UserID == "" {
			return c.Status(400).JSON(fiber.Map{"detail": "user_id required"})
		}
		if !requireTenantManager(c, cfg, tenantID) {
			return nil
		}
		if cfg.DB != nil {
			if _, err := cfg.DB.ExecContext(c.UserContext(),
				Q("INSERT INTO user_tenant (user_id, tenant_id) VALUES ($1, $2) ON CONFLICT DO NOTHING"),
				req.UserID, tenantID); err != nil {
				return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"detail": "tenant membership update failed"})
			}
		}
		return c.Status(201).JSON(fiber.Map{"user_id": req.UserID, "tenant_id": tenantID, "added": true})
	}
}

func tenantRemoveUserHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		tenantID := c.Params("id")
		userID := c.Params("uid")
		if !requireTenantManager(c, cfg, tenantID) {
			return nil
		}
		if cfg.DB != nil {
			if _, err := cfg.DB.ExecContext(c.UserContext(), Q("DELETE FROM user_tenant WHERE user_id = $1 AND tenant_id = $2"), userID, tenantID); err != nil {
				return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"detail": "tenant membership update failed"})
			}
		}
		return c.JSON(fiber.Map{"removed": true})
	}
}

func aclGrantHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req struct {
			PrincipalID    string `json:"principal_id"`
			DatasetID      string `json:"dataset_id"`
			PermissionType string `json:"permission_type"` // read, write, delete, share
		}
		c.BodyParser(&req)
		if req.PrincipalID == "" || req.DatasetID == "" || req.PermissionType == "" {
			return c.Status(400).JSON(fiber.Map{"detail": "principal_id, dataset_id, permission_type required"})
		}
		if !accesspkg.ValidPermissionType(req.PermissionType) {
			return c.Status(400).JSON(fiber.Map{"detail": "permission_type must be read, write, delete, or share"})
		}
		id := uuid.New().String()
		if cfg.DB != nil {
			actorID, _ := c.Locals("user_id").(string)
			policy := accesspkg.SQLPolicy{DB: cfg.DB, Q: Q}
			superuser, err := policy.IsSuperuser(c.UserContext(), actorID)
			if err != nil {
				return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"detail": "ACL authorization failed"})
			}
			if !superuser && !policy.CanGrantDatasetShare(c.UserContext(), req.DatasetID, actorID) {
				return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"detail": "dataset owner or admin required"})
			}
			if _, err := cfg.DB.ExecContext(c.UserContext(),
				Q(`INSERT INTO acl (id, principal_id, dataset_id, permission_type) VALUES ($1, $2, $3, $4)
				 ON CONFLICT (principal_id, dataset_id, permission_type) DO NOTHING`),
				id, req.PrincipalID, req.DatasetID, req.PermissionType); err != nil {
				return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"detail": "ACL update failed"})
			}
		}
		return c.Status(201).JSON(fiber.Map{"id": id, "granted": true})
	}
}

func aclCheckHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		userID := c.Query("user_id")
		datasetID := c.Query("dataset_id")
		if userID == "" || datasetID == "" {
			return c.Status(400).JSON(fiber.Map{"detail": "user_id and dataset_id query params required"})
		}
		perms := map[string]bool{}
		for _, pt := range accesspkg.PermissionTypes() {
			perms[pt] = false
		}
		if cfg.DB != nil {
			rows, err := cfg.DB.QueryContext(context.Background(),
				Q("SELECT permission_type FROM acl WHERE principal_id = $1 AND dataset_id = $2"), userID, datasetID)
			if err == nil {
				defer rows.Close()
				for rows.Next() {
					var pt string
					rows.Scan(&pt)
					perms[pt] = true
				}
			}
		}
		return c.JSON(fiber.Map{"user_id": userID, "dataset_id": datasetID, "permissions": perms})
	}
}
