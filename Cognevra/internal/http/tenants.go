// tenants.go — Multi-tenant management and ACL endpoints.
package http

import (
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

func RegisterTenantAPI(app fiber.Router, cfg APIConfig) {
	app.Post("/tenants", tenantCreateHandler(cfg))
	app.Get("/tenants", tenantListHandler(cfg))
	app.Post("/tenants/:id/users", tenantAddUserHandler(cfg))
	app.Delete("/tenants/:id/users/:uid", tenantRemoveUserHandler(cfg))
	app.Post("/acl", aclGrantHandler(cfg))
	app.Get("/acl/check", aclCheckHandler(cfg))
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
			cfg.DB.ExecContext(c.Context(),
				"INSERT INTO tenants (id, name, owner_id, created_at) VALUES ($1, $2, $3, $4) ON CONFLICT (name) DO NOTHING",
				id, req.Name, ownerID, time.Now().UTC())
		}
		return c.Status(201).JSON(fiber.Map{"id": id, "name": req.Name, "owner_id": ownerID})
	}
}

func tenantListHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if cfg.DB == nil {
			return c.JSON([]any{})
		}
		rows, err := cfg.DB.QueryContext(c.Context(), "SELECT id, name, owner_id, created_at FROM tenants ORDER BY created_at")
		if err != nil {
			return c.JSON([]any{})
		}
		defer rows.Close()
		var tenants []fiber.Map
		for rows.Next() {
			var id, name, ownerID string
			var ca time.Time
			rows.Scan(&id, &name, &ownerID, &ca)
			tenants = append(tenants, fiber.Map{"id": id, "name": name, "owner_id": ownerID, "created_at": ca.Format(time.RFC3339)})
		}
		if tenants == nil {
			tenants = []fiber.Map{}
		}
		return c.JSON(tenants)
	}
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
		if cfg.DB != nil {
			cfg.DB.ExecContext(c.Context(),
				"INSERT INTO user_tenant (user_id, tenant_id) VALUES ($1, $2) ON CONFLICT DO NOTHING",
				req.UserID, tenantID)
		}
		return c.Status(201).JSON(fiber.Map{"user_id": req.UserID, "tenant_id": tenantID, "added": true})
	}
}

func tenantRemoveUserHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		tenantID := c.Params("id")
		userID := c.Params("uid")
		if cfg.DB != nil {
			cfg.DB.ExecContext(c.Context(), "DELETE FROM user_tenant WHERE user_id = $1 AND tenant_id = $2", userID, tenantID)
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
		valid := map[string]bool{"read": true, "write": true, "delete": true, "share": true}
		if !valid[req.PermissionType] {
			return c.Status(400).JSON(fiber.Map{"detail": "permission_type must be read, write, delete, or share"})
		}
		id := uuid.New().String()
		if cfg.DB != nil {
			cfg.DB.ExecContext(c.Context(),
				`INSERT INTO acl (id, principal_id, dataset_id, permission_type) VALUES ($1, $2, $3, $4)
				 ON CONFLICT (principal_id, dataset_id, permission_type) DO NOTHING`,
				id, req.PrincipalID, req.DatasetID, req.PermissionType)
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
		perms := map[string]bool{"read": false, "write": false, "delete": false, "share": false}
		if cfg.DB != nil {
			rows, err := cfg.DB.QueryContext(c.Context(),
				"SELECT permission_type FROM acl WHERE principal_id = $1 AND dataset_id = $2", userID, datasetID)
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
