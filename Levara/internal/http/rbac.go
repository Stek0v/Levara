// rbac.go — Role-based access control and dataset sharing.
// Roles: admin, editor, viewer. Sharing: dataset-level grants.
package http

import (
	"context"
	"database/sql"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	accesspkg "github.com/stek0v/levara/pkg/access"
)

// Role aliases keep legacy internal/http tests and DTO construction readable
// while the canonical role vocabulary lives in pkg/access.
const (
	RoleAdmin  = accesspkg.RoleAdmin
	RoleEditor = accesspkg.RoleEditor
	RoleViewer = accesspkg.RoleViewer
)

type ShareDTO struct {
	ID        string `json:"id"`
	DatasetID string `json:"dataset_id"`
	UserID    string `json:"user_id"`
	UserEmail string `json:"user_email,omitempty"`
	Role      string `json:"role"`
	GrantedBy string `json:"granted_by"`
	CreatedAt string `json:"created_at"`
}

// RegisterRBACAPI registers permission and sharing endpoints.
// Called from RegisterAPI (protected routes).
func RegisterRBACAPI(app fiber.Router, cfg APIConfig) {
	app.Get("/datasets/:id/shares", datasetSharesListHandler(cfg))
	app.Post("/datasets/:id/shares", datasetShareCreateHandler(cfg))
	app.Delete("/datasets/:id/shares/:shareId", datasetShareDeleteHandler(cfg))
	app.Get("/permissions/me", permissionsMeHandler(cfg))
}

func datasetSharesListHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := apiRequestContext(c)
		defer cancel()

		dsID := c.Params("id")
		if cfg.DB == nil {
			return c.JSON([]ShareDTO{})
		}

		rows, err := cfg.DB.QueryContext(ctx,
			Q(`SELECT s.id, s.dataset_id, s.user_id, COALESCE(u.email,''), s.role, s.granted_by, s.created_at
			 FROM dataset_shares s LEFT JOIN users u ON s.user_id = u.id
			 WHERE s.dataset_id = $1 ORDER BY s.created_at`), dsID)
		if err != nil {
			return c.JSON([]ShareDTO{})
		}
		defer rows.Close()

		var shares []ShareDTO
		for rows.Next() {
			var s ShareDTO
			var ca time.Time
			rows.Scan(&s.ID, &s.DatasetID, &s.UserID, &s.UserEmail, &s.Role, &s.GrantedBy, &ca)
			s.CreatedAt = ca.Format(time.RFC3339)
			shares = append(shares, s)
		}
		if shares == nil {
			shares = []ShareDTO{}
		}
		return c.JSON(shares)
	}
}

func datasetShareCreateHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := apiRequestContext(c)
		defer cancel()

		dsID := c.Params("id")
		granterID, _ := c.Locals("user_id").(string)

		var req struct {
			UserID string `json:"user_id"`
			Email  string `json:"email"` // alternative: look up by email
			Role   string `json:"role"`
		}
		if err := c.BodyParser(&req); err != nil {
			return c.Status(400).JSON(fiber.Map{"detail": "invalid request"})
		}

		if req.Role == "" {
			req.Role = accesspkg.RoleViewer
		}
		if !accesspkg.ValidRole(req.Role) {
			return c.Status(400).JSON(fiber.Map{"detail": "role must be admin, editor, or viewer"})
		}

		if cfg.DB == nil {
			return c.Status(503).JSON(fiber.Map{"detail": "database required for sharing"})
		}

		// Only the dataset owner or an admin-share holder can grant shares.
		if !(accesspkg.SQLPolicy{DB: cfg.DB, Q: Q}).CanGrantDatasetShare(ctx, dsID, granterID) {
			return c.Status(403).JSON(fiber.Map{"detail": "only owner or admin can share"})
		}

		// Resolve user by email if needed
		targetUserID := req.UserID
		if targetUserID == "" && req.Email != "" {
			cfg.DB.QueryRowContext(ctx,
				Q("SELECT id FROM users WHERE email = $1"), req.Email).Scan(&targetUserID)
			if targetUserID == "" {
				return c.Status(404).JSON(fiber.Map{"detail": "user not found"})
			}
		}
		if targetUserID == "" {
			return c.Status(400).JSON(fiber.Map{"detail": "user_id or email required"})
		}

		shareID := uuid.New().String()
		upsertSQL, upsertArgs := QArgs(`INSERT INTO dataset_shares (id, dataset_id, user_id, role, granted_by, created_at)
			 VALUES ($1, $2, $3, $4, $5, NOW())
			 ON CONFLICT (dataset_id, user_id) DO UPDATE SET role = $4`,
			shareID, dsID, targetUserID, req.Role, granterID)
		_, err := cfg.DB.ExecContext(ctx, upsertSQL, upsertArgs...)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"detail": "share failed: " + err.Error()})
		}

		return c.Status(201).JSON(ShareDTO{
			ID: shareID, DatasetID: dsID, UserID: targetUserID, Role: req.Role, GrantedBy: granterID,
		})
	}
}

func datasetShareDeleteHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := apiRequestContext(c)
		defer cancel()

		shareID := c.Params("shareId")
		dsID := c.Params("id")
		userID, _ := c.Locals("user_id").(string)

		if cfg.DB == nil {
			return c.JSON(fiber.Map{"deleted": true})
		}

		// Only the dataset owner or an admin-share holder can revoke shares.
		if !(accesspkg.SQLPolicy{DB: cfg.DB, Q: Q}).CanRevokeDatasetShare(ctx, dsID, userID) {
			return c.Status(403).JSON(fiber.Map{"detail": "only owner or admin can revoke shares"})
		}

		cfg.DB.ExecContext(ctx, Q("DELETE FROM dataset_shares WHERE id = $1"), shareID)
		return c.JSON(fiber.Map{"deleted": true})
	}
}

func permissionsMeHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := apiRequestContext(c)
		defer cancel()

		userID, _ := c.Locals("user_id").(string)
		if userID == "" {
			return c.Status(401).JSON(fiber.Map{"detail": "not authenticated"})
		}

		if cfg.DB == nil {
			return c.JSON(fiber.Map{
				"user_id": userID,
				"role":    "admin",
				"shares":  []ShareDTO{},
			})
		}

		// Check if superuser (shared access-policy lookup)
		isSuperuser, _ := accesspkg.SQLPolicy{DB: cfg.DB, Q: Q}.IsSuperuser(ctx, userID)

		globalRole := accesspkg.RoleEditor
		if isSuperuser {
			globalRole = accesspkg.RoleAdmin
		}

		// Get all dataset shares
		rows, err := cfg.DB.QueryContext(ctx,
			Q(`SELECT s.id, s.dataset_id, s.user_id, s.role, s.granted_by, s.created_at
			 FROM dataset_shares s WHERE s.user_id = $1`), userID)
		var shares []ShareDTO
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var s ShareDTO
				var ca time.Time
				rows.Scan(&s.ID, &s.DatasetID, &s.UserID, &s.Role, &s.GrantedBy, &ca)
				s.CreatedAt = ca.Format(time.RFC3339)
				shares = append(shares, s)
			}
		}
		if shares == nil {
			shares = []ShareDTO{}
		}

		return c.JSON(fiber.Map{
			"user_id":      userID,
			"role":         globalRole,
			"is_superuser": isSuperuser,
			"shares":       shares,
		})
	}
}

// GetAllowedDatasetIDs returns all dataset IDs that the user owns or has been shared.
// Returns nil if db is nil or userID is empty (dev mode = no filtering).
// Superusers (is_superuser=true) get nil (= see everything).
func GetAllowedDatasetIDs(db *sql.DB, ctx context.Context, userID string) []string {
	return accesspkg.SQLPolicy{DB: db, Q: Q, QA: QArgs}.AllowedDatasetIDs(ctx, userID)
}

// CheckDatasetAccess verifies the user can access a dataset (owner, shared, or no-auth mode).
func CheckDatasetAccess(db *sql.DB, c *fiber.Ctx, datasetID, userID string) bool {
	ctx, cancel := requestContextWithTimeout(c, timeoutFromEnvMs("HTTP_REQUEST_TIMEOUT_MS", defaultAPIRequestTimeout))
	defer cancel()
	return accesspkg.SQLPolicy{DB: db, Q: Q, QA: QArgs}.CanAccessDataset(ctx, datasetID, userID)
}
