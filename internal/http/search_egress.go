package http

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"

	"github.com/gofiber/fiber/v2"
	accesspkg "github.com/stek0v/levara/pkg/access"
)

type searchEgressKey struct{}
type searchEgress struct {
	cfg                        APIConfig
	actor                      accesspkg.Actor
	kind, keyID, sessionID     string
	epoch, issuedAt, expiresAt int64
	global                     bool
}

func documentReadContext(c *fiber.Ctx, cfg APIConfig, ctx context.Context, ref accesspkg.DocumentRef) (context.Context, error) {
	policy := accesspkg.SQLPolicy{DB: cfg.DB, Q: Q, QA: QArgs}
	r, err := policy.GetDocumentResource(ctx, ref)
	if err != nil && !errors.Is(err, accesspkg.ErrDocumentNotFound) {
		return ctx, documentHTTPError(err)
	}
	source := searchDocumentSource{DatasetID: ref.DatasetID, DocumentID: ref.DataID, ContentRevision: r.ContentRevision}
	source.SourceRevision, source.RawContentHash, err = policy.SourceVersion(ctx, ref)
	if err != nil && !errors.Is(err, accesspkg.ErrDocumentVersionConflict) {
		return ctx, documentHTTPError(err)
	}
	ctx = context.WithValue(ctx, searchActorKey{}, workspaceActorFromFiber(c))
	ctx = context.WithValue(ctx, searchEvidenceKey{}, &searchEvidence{sources: map[searchDocumentSource]struct{}{source: {}}})
	ctx = searchEgressContext(c, cfg, ctx)
	c.SetUserContext(ctx)
	return ctx, nil
}

type fencedResponse struct {
	reader  *bytes.Reader
	ctx     context.Context
	release func()
	once    sync.Once
}

func (r *fencedResponse) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
func (r *fencedResponse) Close() error { r.once.Do(r.release); return nil }

func sendProtectedResponseWithFence(c *fiber.Ctx, ctx context.Context) error {
	if c.Response().IsBodyStream() {
		return fiber.NewError(500, "unexpected protected response stream")
	}
	if err := ctx.Err(); err != nil {
		return fiber.NewError(504, "search deadline exceeded")
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return fiber.NewError(500, "search deadline missing")
	}
	// Fiber sends the body after the handler returns. Transfer ownership of
	// the bounded context and SQL fence to the response stream's Close.
	streamCtx, cancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
	streamCtx, release, err := beginSearchReadFence(streamCtx)
	if err != nil {
		cancel()
		return err
	}
	return sendFencedResponse(c, streamCtx, func() { release(); cancel() })
}

func sendFencedResponse(c *fiber.Ctx, ctx context.Context, release func()) error {
	if c.Response().IsBodyStream() {
		release()
		return fiber.NewError(500, "unexpected protected response stream")
	}
	if err := ctx.Err(); err != nil {
		release()
		return fiber.NewError(504, "search deadline exceeded")
	}
	body := append([]byte(nil), c.Response().Body()...)
	reader := &fencedResponse{reader: bytes.NewReader(body), ctx: ctx, release: release}
	if err := c.SendStream(io.ReadCloser(reader), len(body)); err != nil {
		_ = reader.Close()
		return err
	}
	return nil
}

// withProtectedPolicyResponse keeps one SQL snapshot from policy discovery
// through Fiber's asynchronous response drain.
func withProtectedPolicyResponse(c *fiber.Ctx, cfg APIConfig, ctx context.Context, build func(context.Context, accesspkg.SQLPolicy) error) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		return fiber.NewError(500, "request deadline missing")
	}
	streamCtx, cancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
	streamCtx = searchEgressContext(c, cfg, streamCtx)
	fenced, release, err := beginSearchReadFence(streamCtx)
	if err != nil {
		cancel()
		return err
	}
	locked, ok := fenced.Value(searchReadPolicyKey{}).(accesspkg.SQLPolicy)
	if !ok {
		locked = accesspkg.SQLPolicy{DB: cfg.DB, Q: Q, QA: QArgs}
	}
	closeFence := func() { release(); cancel() }
	if err := build(fenced, locked); err != nil {
		closeFence()
		return err
	}
	return sendFencedResponse(c, fenced, closeFence)
}

func searchEgressContext(c *fiber.Ctx, cfg APIConfig, ctx context.Context) context.Context {
	e := searchEgress{cfg: cfg, actor: workspaceActorFromFiber(c)}
	if key, ok := c.Locals("verified_api_key").(accesspkg.APIKeyIdentity); ok {
		e.kind, e.keyID = "api_key", key.KeyID
	} else if jwt, ok := c.Locals("verified_jwt").(jwtPayload); ok {
		e.kind, e.epoch, e.expiresAt, e.sessionID = "jwt", jwt.CredentialEpoch, jwt.Exp, jwt.SessionID
	} else if external, ok := c.Locals("verified_external").(ExternalPrincipal); ok {
		e.kind, e.issuedAt, e.expiresAt = "external", external.IssuedAt, external.ExpiresAt
	}
	return context.WithValue(ctx, searchEgressKey{}, e)
}

func beginSearchReadFence(ctx context.Context) (context.Context, func(), error) {
	e, present := ctx.Value(searchEgressKey{}).(searchEgress)
	if !present || (!e.cfg.RequireAuth && (e.actor.UserID == "" || e.cfg.DB == nil)) {
		return ctx, func() {}, nil
	}
	policy := accesspkg.SQLPolicy{DB: e.cfg.DB, Q: Q, QA: QArgs}
	locked, release, err := policy.BeginReadFence(ctx, GetDBProvider() == DBSQLite)
	if err != nil {
		return ctx, nil, fiber.NewError(503, "document transfer authorization unavailable")
	}
	ctx = context.WithValue(ctx, searchReadPolicyKey{}, locked)
	if e.kind != "" || e.cfg.RequireAuth {
		if err := locked.RecheckCredential(ctx, e.actor.UserID, e.kind, e.keyID, e.actor.APIKeyPermissions, e.sessionID, e.epoch, e.issuedAt, e.expiresAt); err != nil {
			release()
			return ctx, nil, fiber.NewError(401, "credential revoked")
		}
	}
	if e.global || searchEvidenceRequiresAdmin(ctx) {
		active, activeErr := locked.IsActive(ctx, e.actor.UserID)
		admin, adminErr := locked.IsSuperuser(ctx, e.actor.UserID)
		if activeErr != nil || adminErr != nil || !active || !admin || e.actor.TenantID != "" || !accesspkg.APIKeyAllows(e.actor.APIKeyPermissions, accesspkg.ActionRead) {
			release()
			return ctx, nil, fiber.NewError(403, "global search requires instance administrator")
		}
	}
	for _, source := range searchSources(ctx) {
		allowed, err := searchDocumentAllowed(ctx, e.cfg, e.actor, source)
		if err != nil {
			release()
			return ctx, nil, fiber.NewError(503, "document transfer authorization unavailable")
		}
		if !allowed {
			release()
			return ctx, nil, fiber.NewError(403, "document access revoked")
		}
	}
	return ctx, release, nil
}

func globalSearchStrategy(name string) bool {
	switch name {
	case "COMMUNITY_GLOBAL", "COMMUNITY_LOCAL", "TEMPORAL", "NATURAL_LANGUAGE", "CYPHER":
		return true
	default:
		return false
	}
}

func withSearchReadFence(ctx context.Context, transfer func(context.Context) error) error {
	ctx, release, err := beginSearchReadFence(ctx)
	if err != nil {
		return err
	}
	defer release()
	return transfer(ctx)
}
