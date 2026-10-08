// rbac.go — Role-based access control and dataset sharing.
// Roles: admin, editor, viewer. Sharing: dataset-level grants.
package http

import (
	"context"
	"database/sql"
	"errors"
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
	RegisterTaxonomyAPI(app, cfg)
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
		actor := uploadMetadataActor(c, cfg, ctx)
		if (cfg.RequireAuth && actor.UserID == "") || (actor.Credential.Kind == "" && (actor.UserID != "" || actor.TenantID != "")) {
			return c.Status(403).JSON(fiber.Map{"detail": "read access to dataset required"})
		}
		if cfg.DB == nil {
			return c.JSON([]ShareDTO{})
		}
		ctx, boundedCancel, err := workspaceRequestContext(ctx, actor, timeoutFromEnvMs("HTTP_REQUEST_TIMEOUT_MS", defaultAPIRequestTimeout))
		if err != nil {
			var authority *fiber.Error
			if errors.As(err, &authority) && authority.Code == fiber.StatusForbidden {
				return c.Status(403).JSON(fiber.Map{"detail": "read access to dataset required"})
			}
			return c.Status(503).JSON(fiber.Map{"detail": "share listing unavailable"})
		}
		defer boundedCancel()
		tx, policy, release, err := (accesspkg.SQLPolicy{DB: cfg.DB, Q: Q, QA: QArgs}).BeginTransferFenceTx(ctx, GetDBProvider() == DBSQLite)
		if err != nil {
			return c.Status(503).JSON(fiber.Map{"detail": "share listing unavailable"})
		}
		handedOff := false
		defer func() {
			if !handedOff {
				release()
			}
		}()
		if err := recheckDatasetShareActor(ctx, policy, actor); err != nil {
			return c.Status(403).JSON(fiber.Map{"detail": "read access to dataset required"})
		}
		if actor.TenantID != "" {
			filter, args := accesspkg.TenantOwnerFilterSQL(actor.TenantID, 2, false)
			query, args := QArgs("SELECT COUNT(*) FROM datasets WHERE id = $1"+filter, append([]any{dsID}, args...)...)
			var count int
			if err := tx.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
				return c.Status(503).JSON(fiber.Map{"detail": "share listing unavailable"})
			}
			if count == 0 {
				return c.Status(403).JSON(fiber.Map{"detail": "read access to dataset required"})
			}
		}
		decision, err := policy.AuthorizeDataset(ctx, actor.Actor, dsID, accesspkg.ActionRead)
		if err != nil {
			return c.Status(503).JSON(fiber.Map{"detail": "share listing unavailable"})
		}
		if !decision.Allowed {
			return c.Status(403).JSON(fiber.Map{"detail": "read access to dataset required"})
		}
		query, args := QArgs(`SELECT s.id, s.dataset_id, s.user_id, COALESCE(u.email,''), s.role, s.granted_by, s.created_at
			 FROM dataset_shares s LEFT JOIN users u ON s.user_id = u.id
			 WHERE s.dataset_id = $1 ORDER BY s.created_at`, dsID)
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return c.Status(503).JSON(fiber.Map{"detail": "share listing unavailable"})
		}
		defer rows.Close()
		var shares []ShareDTO
		for rows.Next() {
			var s ShareDTO
			if err := rows.Scan(&s.ID, &s.DatasetID, &s.UserID, &s.UserEmail, &s.Role, &s.GrantedBy, &s.CreatedAt); err != nil {
				return c.Status(503).JSON(fiber.Map{"detail": "share listing unavailable"})
			}
			shares = append(shares, s)
		}
		if err := rows.Err(); err != nil {
			return c.Status(503).JSON(fiber.Map{"detail": "share listing unavailable"})
		}
		if err := rows.Close(); err != nil {
			return c.Status(503).JSON(fiber.Map{"detail": "share listing unavailable"})
		}
		if shares == nil {
			shares = []ShareDTO{}
		}
		if err := c.JSON(shares); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return c.Status(503).JSON(fiber.Map{"detail": "share listing unavailable"})
		}
		deadline, _ := ctx.Deadline()
		streamCtx, streamCancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
		handedOff = true
		return sendFencedResponse(c, streamCtx, func() { release(); streamCancel() })
	}
}

func datasetShareCreateHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := apiRequestContext(c)
		defer cancel()

		dsID := c.Params("id")
		actor := uploadMetadataActor(c, cfg, ctx)
		granterID := actor.UserID

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

		// Fence credential, tenant membership, and project authority with the write.
		tx, policy, err := beginDatasetShareWrite(ctx, c, cfg)
		if err != nil {
			return datasetShareWriteError(c, err)
		}
		defer tx.Rollback()
		if !policy.CanGrantDatasetShare(ctx, dsID, granterID) {
			return c.Status(403).JSON(fiber.Map{"detail": "only owner or admin can share"})
		}

		targetUserID, err := policy.ResolveUserID(ctx, req.UserID, req.Email)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"detail": "user lookup failed: " + err.Error()})
		}
		if targetUserID == "" {
			if req.Email != "" {
				return c.Status(404).JSON(fiber.Map{"detail": "user not found"})
			}
			return c.Status(400).JSON(fiber.Map{"detail": "user_id or email required"})
		}

		shareID := uuid.New().String()
		upsertSQL, upsertArgs := QArgs(`INSERT INTO dataset_shares (id, dataset_id, user_id, role, granted_by, created_at)
			 VALUES ($1, $2, $3, $4, $5, NOW())
			 ON CONFLICT (dataset_id, user_id) DO UPDATE SET role = EXCLUDED.role, granted_by = EXCLUDED.granted_by
 RETURNING id`,
			shareID, dsID, targetUserID, req.Role, granterID)
		err = tx.QueryRowContext(ctx, upsertSQL, upsertArgs...).Scan(&shareID)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"detail": "share failed: " + err.Error()})
		}

		if err := recheckDatasetShareActor(ctx, policy, actor); err != nil {
			return datasetShareWriteError(c, err)
		}
		if err := tx.Commit(); err != nil {
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
		actor := uploadMetadataActor(c, cfg, ctx)
		userID := actor.UserID

		if cfg.DB == nil {
			return c.JSON(fiber.Map{"deleted": true})
		}

		// Use the same lock order as protected chat reads and publications.
		tx, policy, err := beginDatasetShareWrite(ctx, c, cfg)
		if err != nil {
			return datasetShareWriteError(c, err)
		}
		defer tx.Rollback()
		if !policy.CanRevokeDatasetShare(ctx, dsID, userID) {
			return c.Status(403).JSON(fiber.Map{"detail": "only owner or admin can revoke shares"})
		}

		if _, err := tx.ExecContext(ctx, Q("DELETE FROM dataset_shares WHERE id = $1 AND dataset_id = $2"), shareID, dsID); err != nil {
			return c.Status(500).JSON(fiber.Map{"detail": "share revoke failed: " + err.Error()})
		}
		if err := recheckDatasetShareActor(ctx, policy, actor); err != nil {
			return datasetShareWriteError(c, err)
		}
		if err := tx.Commit(); err != nil {
			return c.Status(500).JSON(fiber.Map{"detail": "share revoke failed: " + err.Error()})
		}
		return c.JSON(fiber.Map{"deleted": true})
	}
}

// Reuse the metadata fence so audience mutations cannot race protected transfers.
func beginDatasetShareWrite(ctx context.Context, c *fiber.Ctx, cfg APIConfig) (*sql.Tx, accesspkg.SQLPolicy, error) {
	actor := uploadMetadataActor(c, cfg, ctx)
	// Unverified locals cannot turn an anonymous compatibility request into a principal.
	if actor.Credential.Kind == "" && (actor.UserID != "" || actor.TenantID != "") {
		return nil, accesspkg.SQLPolicy{}, accesspkg.ErrRevokedCredential
	}
	return (accesspkg.SQLPolicy{DB: cfg.DB, Q: Q, QA: QArgs}).BeginMetadataWrite(ctx, actor, GetDBProvider() == DBSQLite)
}

// SQL fences stabilize revocations, but token expiry follows the wall clock.
func recheckDatasetShareActor(ctx context.Context, policy accesspkg.SQLPolicy, actor accesspkg.MetadataActor) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !actor.TrustedLocal {
		credential := actor.Credential
		if err := policy.RecheckCredential(ctx, actor.UserID, credential.Kind, credential.KeyID, actor.APIKeyPermissions, credential.SessionID, credential.Epoch, credential.IssuedAt, credential.ExpiresAt); err != nil {
			return err
		}
	}
	if actor.TenantID != "" {
		member, err := policy.IsTenantMember(ctx, actor.UserID, actor.TenantID)
		if err != nil {
			return err
		}
		if !member {
			return accesspkg.ErrDocumentForbidden
		}
	}
	return nil
}

func datasetShareWriteError(c *fiber.Ctx, err error) error {
	if errors.Is(err, accesspkg.ErrRevokedCredential) || errors.Is(err, accesspkg.ErrDocumentForbidden) {
		return c.Status(403).JSON(fiber.Map{"detail": "only owner or admin can manage shares"})
	}
	return c.Status(503).JSON(fiber.Map{"detail": "share authorization unavailable"})
}

func permissionsMeHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := apiRequestContext(c)
		defer cancel()
		actor := uploadMetadataActor(c, cfg, ctx)
		userID := actor.UserID
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
		if actor.Credential.Kind == "" {
			return c.Status(403).JSON(fiber.Map{"detail": "not authenticated"})
		}
		ctx, boundedCancel, err := workspaceRequestContext(ctx, actor, timeoutFromEnvMs("HTTP_REQUEST_TIMEOUT_MS", defaultAPIRequestTimeout))
		if err != nil {
			var authority *fiber.Error
			if errors.As(err, &authority) && authority.Code == fiber.StatusForbidden {
				return c.Status(403).JSON(fiber.Map{"detail": "not authenticated"})
			}
			return c.Status(503).JSON(fiber.Map{"detail": "permissions unavailable"})
		}
		defer boundedCancel()
		tx, policy, release, err := (accesspkg.SQLPolicy{DB: cfg.DB, Q: Q, QA: QArgs}).BeginTransferFenceTx(ctx, GetDBProvider() == DBSQLite)
		if err != nil {
			return c.Status(503).JSON(fiber.Map{"detail": "permissions unavailable"})
		}
		handedOff := false
		defer func() {
			if !handedOff {
				release()
			}
		}()
		if err := recheckDatasetShareActor(ctx, policy, actor); err != nil {
			return c.Status(403).JSON(fiber.Map{"detail": "not authenticated"})
		}
		isSuperuser, err := policy.IsSuperuser(ctx, userID)
		if err != nil {
			return c.Status(503).JSON(fiber.Map{"detail": "permissions unavailable"})
		}
		globalRole := accesspkg.RoleEditor
		if isSuperuser {
			globalRole = accesspkg.RoleAdmin
		}
		query, args := QArgs(`SELECT s.id, s.dataset_id, s.user_id, s.role, s.granted_by, s.created_at
			 FROM dataset_shares s WHERE s.user_id = $1`, userID)
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return c.Status(503).JSON(fiber.Map{"detail": "permissions unavailable"})
		}
		defer rows.Close()
		var shares []ShareDTO
		for rows.Next() {
			var s ShareDTO
			var rawCreatedAt any
			if err := rows.Scan(&s.ID, &s.DatasetID, &s.UserID, &s.Role, &s.GrantedBy, &rawCreatedAt); err != nil {
				return c.Status(503).JSON(fiber.Map{"detail": "permissions unavailable"})
			}
			stamp := timestampString(rawCreatedAt)
			createdAt, err := time.Parse(time.RFC3339Nano, stamp)
			if err != nil {
				createdAt, err = time.Parse("2006-01-02 15:04:05", stamp)
			}
			if err != nil {
				return c.Status(503).JSON(fiber.Map{"detail": "permissions unavailable"})
			}
			s.CreatedAt = createdAt.Format(time.RFC3339)
			shares = append(shares, s)
		}
		if err := rows.Err(); err != nil {
			return c.Status(503).JSON(fiber.Map{"detail": "permissions unavailable"})
		}
		if err := rows.Close(); err != nil {
			return c.Status(503).JSON(fiber.Map{"detail": "permissions unavailable"})
		}
		if shares == nil {
			shares = []ShareDTO{}
		}
		if err := c.JSON(fiber.Map{
			"user_id":      userID,
			"role":         globalRole,
			"is_superuser": isSuperuser,
			"shares":       shares,
		}); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return c.Status(503).JSON(fiber.Map{"detail": "permissions unavailable"})
		}
		deadline, _ := ctx.Deadline()
		streamCtx, streamCancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
		handedOff = true
		return sendFencedResponse(c, streamCtx, func() { release(); streamCancel() })
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

// datasetShareGrants lists share grants for a dataset (email, role,
// created_at string) — newest first, capped. Extracted here because the
// policy-boundary test forbids direct dataset_shares SQL outside this
// file; api_project_meta.go consumes it for the activity feed.
func datasetShareGrants(q func(string) string, db interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}, ctx context.Context, datasetID string) ([]shareGrant, error) {
	rows, err := db.QueryContext(ctx, q(`SELECT u.email, s.role, s.created_at
	   FROM dataset_shares s LEFT JOIN users u ON u.id = s.user_id
	   WHERE s.dataset_id = $1 ORDER BY s.created_at DESC LIMIT 50`), datasetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []shareGrant
	for rows.Next() {
		var g shareGrant
		if err := rows.Scan(&g.Email, &g.Role, &g.Created); err != nil {
			continue
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

type shareGrant struct {
	Email   string
	Role    string
	Created any
}
