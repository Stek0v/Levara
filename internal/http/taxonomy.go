package http

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/gofiber/fiber/v2"
	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/taxonomy"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

func RegisterTaxonomyAPI(app fiber.Router, cfg APIConfig) {
	app.Post("/datasets/:id/taxonomy/import", taxonomyImportHandler(cfg))
	app.Get("/datasets/:id/taxonomy", taxonomyListHandler(cfg))
	app.Delete("/datasets/:id/taxonomy", taxonomyRemoveHandler(cfg))
}
func taxonomyHTTPError(err error) error {
	switch {
	case errors.Is(err, taxonomy.ErrInvalid):
		return fiber.NewError(400, "invalid taxonomy request")
	case errors.Is(err, accesspkg.ErrRevokedCredential):
		return fiber.NewError(401, "verified authentication required")
	case errors.Is(err, accesspkg.ErrDocumentForbidden):
		return fiber.NewError(403, "taxonomy access denied")
	case errors.Is(err, taxonomy.ErrConflict):
		return fiber.NewError(409, "taxonomy conflict")
	default:
		return fiber.NewError(503, "taxonomy storage unavailable")
	}
}
func taxonomyDecode(c *fiber.Ctx, target any) error {
	if len(c.Body()) > 6*taxonomy.MaxSeedBytes+4096 || !utf8.Valid(c.Body()) {
		return taxonomy.ErrInvalid
	}
	decoder := json.NewDecoder(strings.NewReader(string(c.Body())))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return taxonomy.ErrInvalid
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return taxonomy.ErrInvalid
	}
	return nil
}
func taxonomyActor(c *fiber.Ctx, cfg APIConfig, ctx context.Context) (accesspkg.MetadataActor, error) {
	actor := uploadMetadataActor(c, cfg, ctx)
	if actor.TrustedLocal || actor.UserID == "" || actor.Credential.Kind == "" {
		return actor, accesspkg.ErrRevokedCredential
	}
	if actor.Credential.ExpiresAt > 0 && time.Now().Unix() >= actor.Credential.ExpiresAt {
		return actor, accesspkg.ErrRevokedCredential
	}
	if cfg.DB == nil {
		return actor, errors.New("taxonomy storage missing")
	}
	return actor, nil
}
func taxonomyRequestError(ctx context.Context, actor accesspkg.MetadataActor, err error) error {
	if errors.Is(err, context.DeadlineExceeded) && actor.Credential.ExpiresAt > 0 && time.Now().Unix() >= actor.Credential.ExpiresAt && !errors.Is(ctx.Err(), context.Canceled) {
		return accesspkg.ErrRevokedCredential
	}
	return err
}
func taxonomyCredentialContext(ctx context.Context, actor accesspkg.MetadataActor) (context.Context, context.CancelFunc) {
	deadline, _ := ctx.Deadline()
	if expiry := time.Unix(actor.Credential.ExpiresAt, 0); actor.Credential.ExpiresAt > 0 && expiry.Before(deadline) {
		deadline = expiry
	}
	return context.WithDeadline(ctx, deadline)
}
func taxonomyImportHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := apiRequestContext(c)
		defer cancel()
		actor, err := taxonomyActor(c, cfg, ctx)
		if err != nil {
			return taxonomyHTTPError(taxonomyRequestError(ctx, actor, err))
		}
		bounded, boundedCancel := taxonomyCredentialContext(ctx, actor)
		defer boundedCancel()
		ctx = bounded
		var req taxonomy.ImportRequest
		if err := taxonomyDecode(c, &req); err != nil {
			return taxonomyHTTPError(taxonomyRequestError(ctx, actor, err))
		}
		if _, err := taxonomy.Parse(req.Seed); err != nil {
			return taxonomyHTTPError(taxonomyRequestError(ctx, actor, err))
		}
		tx, locked, err := documentSQLPolicy(cfg).BeginMetadataWrite(ctx, actor, GetDBProvider() == DBSQLite)
		if err != nil {
			return taxonomyHTTPError(taxonomyRequestError(ctx, actor, err))
		}
		defer tx.Rollback()
		report, err := taxonomy.Import(ctx, tx, locked, actor.Actor, c.Params("id"), req)
		if err != nil {
			return taxonomyHTTPError(taxonomyRequestError(ctx, actor, err))
		}
		if err := recheckDatasetShareActor(ctx, locked, actor); err != nil {
			return taxonomyHTTPError(taxonomyRequestError(ctx, actor, err))
		}
		if err := tx.Commit(); err != nil {
			return taxonomyHTTPError(taxonomyRequestError(ctx, actor, err))
		}
		return c.JSON(report)
	}
}
func taxonomyRemoveHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := apiRequestContext(c)
		defer cancel()
		actor, err := taxonomyActor(c, cfg, ctx)
		if err != nil {
			return taxonomyHTTPError(taxonomyRequestError(ctx, actor, err))
		}
		bounded, boundedCancel := taxonomyCredentialContext(ctx, actor)
		defer boundedCancel()
		ctx = bounded
		var req taxonomy.RemoveRequest
		if err := taxonomyDecode(c, &req); err != nil {
			return taxonomyHTTPError(taxonomyRequestError(ctx, actor, err))
		}
		tx, locked, err := documentSQLPolicy(cfg).BeginMetadataWrite(ctx, actor, GetDBProvider() == DBSQLite)
		if err != nil {
			return taxonomyHTTPError(taxonomyRequestError(ctx, actor, err))
		}
		defer tx.Rollback()
		report, err := taxonomy.Remove(ctx, tx, locked, actor.Actor, c.Params("id"), req)
		if err != nil {
			return taxonomyHTTPError(taxonomyRequestError(ctx, actor, err))
		}
		if err := recheckDatasetShareActor(ctx, locked, actor); err != nil {
			return taxonomyHTTPError(taxonomyRequestError(ctx, actor, err))
		}
		if err := tx.Commit(); err != nil {
			return taxonomyHTTPError(taxonomyRequestError(ctx, actor, err))
		}
		return c.JSON(report)
	}
}
func taxonomyListHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := apiRequestContext(c)
		defer cancel()
		actor, err := taxonomyActor(c, cfg, ctx)
		if err != nil {
			return taxonomyHTTPError(err)
		}
		deadline, _ := ctx.Deadline()
		if expires := time.Unix(actor.Credential.ExpiresAt, 0); actor.Credential.ExpiresAt > 0 && expires.Before(deadline) {
			deadline = expires
		}
		capped, cappedCancel := context.WithDeadline(ctx, deadline)
		defer cappedCancel()
		ctx = capped
		tx, locked, release, err := documentSQLPolicy(cfg).BeginTransferFenceTx(ctx, GetDBProvider() == DBSQLite)
		if err != nil {
			return taxonomyHTTPError(taxonomyRequestError(ctx, actor, err))
		}
		fail := func(err error) error { release(); return taxonomyHTTPError(taxonomyRequestError(ctx, actor, err)) }
		if err := recheckDatasetShareActor(ctx, locked, actor); err != nil {
			return fail(err)
		}
		filter, extra := accesspkg.TenantOwnerFilterSQL(actor.TenantID, 2, false)
		var dataset string
		args := append([]any{c.Params("id")}, extra...)
		if err := tx.QueryRowContext(ctx, "SELECT id FROM datasets WHERE id=$1"+filter, args...).Scan(&dataset); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fail(accesspkg.ErrDocumentForbidden)
			}
			return fail(err)
		}
		decision, err := locked.AuthorizeDataset(ctx, actor.Actor, c.Params("id"), accesspkg.ActionRead)
		if err != nil {
			return fail(err)
		}
		if !decision.Allowed {
			return fail(accesspkg.ErrDocumentForbidden)
		}
		catalog, err := taxonomy.List(ctx, tx, actor.UserID, actor.TenantID, dataset)
		if err != nil {
			return fail(err)
		}
		if err := recheckDatasetShareActor(ctx, locked, actor); err != nil {
			return fail(err)
		}
		if err := c.JSON(catalog); err != nil {
			release()
			return err
		}
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		streamCtx, streamCancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
		return sendFencedResponse(c, streamCtx, func() { release(); streamCancel() })
	}
}
