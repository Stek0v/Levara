package http

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/audit"
)

// RegisterDocumentPolicyAPI is mounted by RegisterAPI behind the server's
// authentication and tenant middleware.
func RegisterDocumentPolicyAPI(app fiber.Router, cfg APIConfig) {
	app.Get("/datasets/:id/data/:dataId", documentGetHandler(cfg))
	app.Post("/datasets/:id/data/:dataId/policy", documentRegisterHandler(cfg))
	app.Get("/datasets/:id/data/:dataId/policy", documentPolicyGetHandler(cfg))
	app.Patch("/datasets/:id/data/:dataId/policy", documentPolicyUpdateHandler(cfg))
	app.Post("/datasets/:id/data/:dataId/grants", documentGrantHandler(cfg, false))
	app.Delete("/datasets/:id/data/:dataId/grants/:kind/:principalId", documentGrantHandler(cfg, true))
	app.Post("/document-groups", documentGroupCreateHandler(cfg))
	app.Get("/document-groups/:groupId", documentGroupGetHandler(cfg))
	app.Put("/document-groups/:groupId/members", documentGroupMembersHandler(cfg))
}

func documentMutationAuditOutcome(c *fiber.Ctx, cfg APIConfig, eventType, subject, outcome string, metadata map[string]any) {
	if cfg.WorkspaceAuditSink == nil {
		return
	}
	ctx := searchEgressContext(c, cfg, c.UserContext())
	scope := verifiedAuditScope(ctx)
	cfg.WorkspaceAuditSink.LogEvent(audit.Event{
		VerifiedScope: scope,
		TS:            time.Now().UTC().Format(time.RFC3339Nano),
		Source:        "document.rest",
		Type:          eventType,
		Subject:       subject,
		ActorID:       scope.ActorID,
		Outcome:       outcome,
		Metadata:      metadata,
	})
}

func documentMutationAudit(c *fiber.Ctx, cfg APIConfig, eventType, subject string, metadata map[string]any) {
	documentMutationAuditOutcome(c, cfg, eventType, subject, "success", metadata)
}

func documentMutationAuditError(c *fiber.Ctx, cfg APIConfig, eventType, subject string, metadata map[string]any, err error) {
	outcome := "failure"
	if errors.Is(err, accesspkg.ErrRevokedCredential) || errors.Is(err, accesspkg.ErrDocumentForbidden) || errors.Is(err, accesspkg.ErrGroupForbidden) {
		outcome = "denied"
	}
	documentMutationAuditOutcome(c, cfg, eventType, subject, outcome, metadata)
}

func documentAuditSubject(ref accesspkg.DocumentRef) string {
	return ref.DatasetID + "/" + ref.DataID
}

func documentSQLPolicy(cfg APIConfig) accesspkg.SQLPolicy {
	return accesspkg.SQLPolicy{DB: cfg.DB, Q: Q, QA: QArgs}
}

func withDocumentMutation(c *fiber.Ctx, cfg APIConfig, mutate func(context.Context, accesspkg.SQLPolicy, accesspkg.Actor) error) error {
	ctx, cancel := apiRequestContext(c)
	defer cancel()
	metadataActor := uploadMetadataActor(c, cfg, ctx)
	return documentSQLPolicy(cfg).WithMetadataWrite(ctx, metadataActor, GetDBProvider() == DBSQLite, func(locked accesspkg.SQLPolicy) error {
		return mutate(ctx, locked, metadataActor.Actor)
	})
}

func documentRefFromFiber(c *fiber.Ctx) accesspkg.DocumentRef {
	return accesspkg.DocumentRef{DatasetID: c.Params("id"), DataID: c.Params("dataId")}
}
func documentHTTPError(err error) error {
	switch {
	case errors.Is(err, accesspkg.ErrRevokedCredential):
		return fiber.NewError(401, "credential revoked")
	case errors.Is(err, accesspkg.ErrDocumentNotFound), errors.Is(err, accesspkg.ErrGroupNotFound):
		return fiber.NewError(404, "not found")
	case errors.Is(err, accesspkg.ErrDocumentForbidden), errors.Is(err, accesspkg.ErrGroupForbidden):
		return fiber.NewError(403, "document access denied")
	case errors.Is(err, accesspkg.ErrDocumentInvalid), errors.Is(err, accesspkg.ErrGroupInvalid):
		return fiber.NewError(400, "invalid document policy request")
	case errors.Is(err, accesspkg.ErrDocumentVersionConflict), errors.Is(err, accesspkg.ErrGroupVersionConflict):
		return fiber.NewError(409, "policy version conflict")
	case errors.Is(err, accesspkg.ErrDocumentSharedMetadata):
		return fiber.NewError(409, "document name is shared by multiple datasets")
	default:
		return fiber.NewError(500, "document policy operation failed")
	}
}

func authorizeDocumentFiber(c *fiber.Ctx, cfg APIConfig, ref accesspkg.DocumentRef, action string) error {
	d, err := documentSQLPolicy(cfg).AuthorizeDocument(c.UserContext(), workspaceActorFromFiber(c), ref, action)
	if err != nil {
		return documentHTTPError(err)
	}
	if !d.Allowed {
		return documentHTTPError(accesspkg.ErrDocumentForbidden)
	}
	return nil
}

func documentRegistration(c *fiber.Ctx, cfg APIConfig, ref accesspkg.DocumentRef) (*accesspkg.DocumentResource, error) {
	r, err := documentSQLPolicy(cfg).GetDocumentResource(c.UserContext(), ref)
	if errors.Is(err, accesspkg.ErrDocumentNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, documentHTTPError(err)
	}
	return &r, nil
}

func documentETag(r accesspkg.DocumentResource) string {
	return fmt.Sprintf("\"%d:%d\"", r.ACLRevision, r.ContentRevision)
}
func documentExpectedVersion(c *fiber.Ctx) (int64, int64, error) {
	value := c.Get("If-Match")
	if value == "" {
		return 0, 0, fiber.NewError(428, "If-Match document revision required")
	}
	if len(value) < 5 || value[0] != '"' || value[len(value)-1] != '"' {
		return 0, 0, fiber.NewError(400, "invalid document If-Match")
	}
	parts := strings.Split(value[1:len(value)-1], ":")
	if len(parts) != 2 {
		return 0, 0, fiber.NewError(400, "invalid document If-Match")
	}
	a, errA := strconv.ParseInt(parts[0], 10, 64)
	b, errB := strconv.ParseInt(parts[1], 10, 64)
	if errA != nil || errB != nil || a <= 0 || b <= 0 {
		return 0, 0, fiber.NewError(400, "invalid document If-Match")
	}
	return a, b, nil
}

func documentRawProxy(c *fiber.Ctx, ref accesspkg.DocumentRef) string {
	return fmt.Sprintf("%s/api/v1/datasets/%s/data/%s/raw", c.BaseURL(), url.PathEscape(ref.DatasetID), url.PathEscape(ref.DataID))
}

func documentResourceJSON(r accesspkg.DocumentResource) fiber.Map {
	return fiber.Map{"dataset_id": r.DatasetID, "data_id": r.DataID, "tenant_id": r.TenantID, "mode": r.Mode, "acl_revision": r.ACLRevision, "content_revision": r.ContentRevision, "tombstoned": r.Tombstoned, "hold": r.Hold}
}
func sendDocumentResource(c *fiber.Ctx, r accesspkg.DocumentResource) error {
	c.Set("ETag", documentETag(r))
	c.Set("Cache-Control", "private, no-store")
	return c.JSON(documentResourceJSON(r))
}

func documentRegisterHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if cfg.DB == nil {
			return fiber.NewError(503, "database required")
		}
		var req struct {
			TenantID string `json:"tenant_id"`
			Mode     string `json:"mode"`
		}
		if err := c.BodyParser(&req); err != nil {
			return fiber.NewError(400, "invalid document registration")
		}
		ref := documentRefFromFiber(c)
		var r accesspkg.DocumentResource
		err := withDocumentMutation(c, cfg, func(ctx context.Context, p accesspkg.SQLPolicy, actor accesspkg.Actor) error {
			var err error
			r, err = p.RegisterDocument(ctx, actor, ref, req.TenantID, req.Mode)
			return err
		})
		if err != nil {
			documentMutationAuditError(c, cfg, "register", documentAuditSubject(ref), map[string]any{"mode": req.Mode}, err)
			return documentHTTPError(err)
		}
		documentMutationAudit(c, cfg, "register", documentAuditSubject(ref), map[string]any{"mode": r.Mode, "acl_revision": r.ACLRevision})
		return sendDocumentResource(c, r)
	}
}

func documentPolicyGetHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		r, grants, err := documentSQLPolicy(cfg).ReadDocumentPolicy(c.UserContext(), workspaceActorFromFiber(c), documentRefFromFiber(c))
		if err != nil {
			return documentHTTPError(err)
		}
		out := documentResourceJSON(r)
		out["grants"] = grants
		c.Set("ETag", documentETag(r))
		c.Set("Cache-Control", "private, no-store")
		return c.JSON(out)
	}
}

func documentPolicyUpdateHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req struct {
			ACLRevision int64   `json:"acl_revision"`
			Mode        *string `json:"mode"`
			Hold        *bool   `json:"hold"`
		}
		if err := c.BodyParser(&req); err != nil || (req.Mode == nil) == (req.Hold == nil) {
			return fiber.NewError(400, "exactly one of mode or hold is required")
		}
		var r accesspkg.DocumentResource
		eventType := "set_hold"
		if req.Mode != nil {
			eventType = "set_mode"
		}
		ref := documentRefFromFiber(c)
		err := withDocumentMutation(c, cfg, func(ctx context.Context, p accesspkg.SQLPolicy, actor accesspkg.Actor) error {
			var err error
			if req.Mode != nil {
				r, err = p.SetDocumentMode(ctx, actor, ref, req.ACLRevision, *req.Mode)
			} else {
				r, err = p.SetDocumentHold(ctx, actor, ref, req.ACLRevision, *req.Hold)
			}
			return err
		})
		if err != nil {
			documentMutationAuditError(c, cfg, eventType, documentAuditSubject(ref), nil, err)
			return documentHTTPError(err)
		}
		documentMutationAudit(c, cfg, eventType, documentAuditSubject(ref), map[string]any{"acl_revision": r.ACLRevision})
		return sendDocumentResource(c, r)
	}
}

func documentGrantHandler(cfg APIConfig, revoke bool) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req struct {
			ACLRevision   int64  `json:"acl_revision"`
			PrincipalKind string `json:"principal_kind"`
			PrincipalID   string `json:"principal_id"`
			Role          string `json:"role"`
		}
		if err := c.BodyParser(&req); err != nil {
			return fiber.NewError(400, "invalid grant request")
		}
		var r accesspkg.DocumentResource
		ref := documentRefFromFiber(c)
		err := withDocumentMutation(c, cfg, func(ctx context.Context, p accesspkg.SQLPolicy, actor accesspkg.Actor) error {
			var err error
			if revoke {
				r, err = p.RevokeDocument(ctx, actor, ref, req.ACLRevision, accesspkg.DocumentPrincipal{Kind: c.Params("kind"), ID: c.Params("principalId")})
			} else {
				r, err = p.GrantDocument(ctx, actor, ref, req.ACLRevision, accesspkg.DocumentPrincipal{Kind: req.PrincipalKind, ID: req.PrincipalID}, req.Role)
			}
			return err
		})
		if err != nil {
			kind, principalID, eventType := req.PrincipalKind, req.PrincipalID, "grant"
			if revoke {
				kind, principalID, eventType = c.Params("kind"), c.Params("principalId"), "revoke"
			}
			documentMutationAuditError(c, cfg, eventType, documentAuditSubject(ref), map[string]any{"principal_kind": kind, "principal_id": principalID}, err)
			return documentHTTPError(err)
		}
		kind, principalID, eventType := req.PrincipalKind, req.PrincipalID, "grant"
		metadata := map[string]any{"acl_revision": r.ACLRevision, "role": req.Role}
		if revoke {
			kind, principalID, eventType = c.Params("kind"), c.Params("principalId"), "revoke"
			delete(metadata, "role")
		}
		metadata["principal_kind"], metadata["principal_id"] = kind, principalID
		documentMutationAudit(c, cfg, eventType, documentAuditSubject(ref), metadata)
		return sendDocumentResource(c, r)
	}
}

func documentGroupJSON(g accesspkg.AccessGroup) fiber.Map {
	return fiber.Map{"id": g.ID, "tenant_id": g.TenantID, "name": g.Name, "revision": g.Revision, "members": g.Members}
}
func documentGroupCreateHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req struct {
			TenantID string `json:"tenant_id"`
			Name     string `json:"name"`
		}
		if err := c.BodyParser(&req); err != nil {
			return fiber.NewError(400, "invalid group request")
		}
		var g accesspkg.AccessGroup
		err := withDocumentMutation(c, cfg, func(ctx context.Context, p accesspkg.SQLPolicy, actor accesspkg.Actor) error {
			var err error
			g, err = p.CreateGroup(ctx, actor, req.TenantID, req.Name)
			return err
		})
		if err != nil {
			documentMutationAuditError(c, cfg, "group_create", "group", map[string]any{"tenant_id": req.TenantID}, err)
			return documentHTTPError(err)
		}
		documentMutationAudit(c, cfg, "group_create", "group/"+g.ID, map[string]any{"tenant_id": g.TenantID, "revision": g.Revision})
		return c.JSON(documentGroupJSON(g))
	}
}
func documentGroupGetHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		g, err := documentSQLPolicy(cfg).GetGroup(c.UserContext(), workspaceActorFromFiber(c), c.Params("groupId"))
		if err != nil {
			return documentHTTPError(err)
		}
		return c.JSON(documentGroupJSON(g))
	}
}
func documentGroupMembersHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req struct {
			Revision int64     `json:"revision"`
			Members  *[]string `json:"members"`
		}
		if err := c.BodyParser(&req); err != nil || req.Members == nil {
			return fiber.NewError(400, "members array required")
		}
		var g accesspkg.AccessGroup
		err := withDocumentMutation(c, cfg, func(ctx context.Context, p accesspkg.SQLPolicy, actor accesspkg.Actor) error {
			var err error
			g, err = p.ReplaceGroupMembers(ctx, actor, c.Params("groupId"), req.Revision, *req.Members)
			return err
		})
		if err != nil {
			documentMutationAuditError(c, cfg, "group_members_replace", "group/"+c.Params("groupId"), map[string]any{"member_count": len(*req.Members)}, err)
			return documentHTTPError(err)
		}
		documentMutationAudit(c, cfg, "group_members_replace", "group/"+g.ID, map[string]any{"revision": g.Revision, "member_count": len(g.Members)})
		return c.JSON(documentGroupJSON(g))
	}
}

func documentGetHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if cfg.DB == nil {
			return fiber.NewError(404, "not found")
		}
		ref := documentRefFromFiber(c)
		if err := authorizeDocumentFiber(c, cfg, ref, accesspkg.ActionRead); err != nil {
			return err
		}
		ctx, cancel := apiRequestContext(c)
		defer cancel()
		ctx, accessErr := documentReadContext(c, cfg, ctx, ref)
		if accessErr != nil {
			return accessErr
		}
		var d DataDTO
		err := cfg.DB.QueryRowContext(c.UserContext(), Q(`SELECT d.id,d.name,d.extension,d.mime_type,COALESCE(d.data_size,0),COALESCE(d.pipeline_status,'{}'),COALESCE(d.tags,'[]'),d.source_revision,LOWER(d.raw_content_hash),d.created_at FROM data d JOIN dataset_data dd ON dd.data_id=d.id WHERE dd.dataset_id=$1 AND d.id=$2`), ref.DatasetID, ref.DataID).Scan(&d.ID, &d.Name, &d.Extension, &d.MimeType, &d.DataSize, &d.PipelineStatus, &d.Tags, &d.SourceRevision, &d.RawContentHash, &d.CreatedAt)
		if errors.Is(err, sql.ErrNoRows) {
			return fiber.NewError(404, "not found")
		}
		if err != nil {
			return documentHTTPError(err)
		}
		d.PipelineStatus, err = pipelineStatusForDocument(ctx, cfg.DB, ref.DatasetID, ref.DataID, d.PipelineStatus)
		if err != nil {
			return documentHTTPError(err)
		}
		r, err := documentRegistration(c, cfg, ref)
		if err != nil {
			return err
		}
		if r != nil {
			c.Set("ETag", documentETag(*r))
		}
		d.RawDataLocation = documentRawProxy(c, ref)
		if err := authorizeDocumentFiber(c, cfg, ref, accesspkg.ActionRead); err != nil {
			return err
		}
		c.Set("Cache-Control", "private, no-store")
		if err := c.JSON(d); err != nil {
			return err
		}
		return sendProtectedResponseWithFence(c, ctx)
	}
}

// The SQL cursor must close before calling the policy: SQLite deployments may
// have one connection. Counts derive solely from currently readable documents.
func documentDatasetStats(ctx context.Context, c *fiber.Ctx, cfg APIConfig, datasetID string) (int, int64, error) {
	rows, err := cfg.DB.QueryContext(ctx, Q("SELECT d.id,COALESCE(d.data_size,0) FROM data d JOIN dataset_data dd ON dd.data_id=d.id WHERE dd.dataset_id=$1"), datasetID)
	if err != nil {
		return 0, 0, err
	}
	type item struct {
		id   string
		size int64
	}
	items := []item{}
	for rows.Next() {
		var i item
		if err := rows.Scan(&i.id, &i.size); err != nil {
			rows.Close()
			return 0, 0, err
		}
		items = append(items, i)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, 0, err
	}
	count := 0
	var size int64
	for _, i := range items {
		d, err := documentSQLPolicy(cfg).AuthorizeDocument(ctx, workspaceActorFromFiber(c), accesspkg.DocumentRef{DatasetID: datasetID, DataID: i.id}, accesspkg.ActionRead)
		if err != nil {
			return 0, 0, err
		}
		if d.Allowed {
			count++
			size += i.size
		}
	}
	return count, size, nil
}
