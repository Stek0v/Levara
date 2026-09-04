package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/gofiber/fiber/v2"
	accesspkg "github.com/stek0v/levara/pkg/access"
)

// SCIM HTTP surface (backlog A3, ADR-003).
//
// Scope: the RFC 7644 subset Entra ID/Okta actually drive — Users CRUD with
// PATCH active-flips and email renames, ServiceProviderConfig, Schemas,
// pagination (startIndex/count ≤ 200) and the eq-filters IdPs send.
// Auth is a dedicated static bearer token (LEVARA_SCIM_TOKEN) that grants
// nothing outside /scim/v2; the surface does not exist unless the token is
// configured. Identity matching follows ADR-003: externalId primary, email
// collisions → 409 uniqueness (no silent merges), delete = soft deactivate.

type scimStore interface {
	EnsureSchema(ctx context.Context) error
	ProvisionCreate(ctx context.Context, u accesspkg.SCIMUser) (string, bool, error)
	ProvisionUpdate(ctx context.Context, u accesspkg.SCIMUser, newEmail string) error
	ProvisionDeactivate(ctx context.Context, issuer, externalID string) error
	Lookup(ctx context.Context, issuer, externalID string) (string, error)
}

// scimQuerier is the read side the HTTP layer needs over the users table.
type scimQuerier interface {
	ByEmail(ctx context.Context, issuer, email string) (uids []string, externalIDs []string, total int, err error)
	List(ctx context.Context, issuer string, start, count int) (uids []string, externalIDs []string, total int, err error)
	ByID(ctx context.Context, id string) (email string, active bool, externalID string, err error)
}

type scimService struct {
	store  scimStore
	query  scimQuerier
	issuer string
	token  string
	mu     sync.Mutex
	audit  func(action, externalID, userID string)
}

// scimError renders RFC 7644 §3.12 error shapes.
type scimError struct {
	Status   string `json:"status"`
	ScimType string `json:"scimType,omitempty"`
	Detail   string `json:"detail"`
}

func scimErr(c *fiber.Ctx, status int, scimType, detail string) error {
	return c.Status(status).JSON(scimError{
		Status: fmt.Sprint(status), ScimType: scimType, Detail: detail,
	})
}

// SCIMRoutes registers /scim/v2/* on the public router. Returns silently
// when LEVARA_SCIM_TOKEN is unset — the surface does not exist unconfigured.
func SCIMRoutes(public fiber.Router, store scimStore, query scimQuerier, audit func(line string)) error {
	token := strings.TrimSpace(os.Getenv("LEVARA_SCIM_TOKEN"))
	if token == "" || store == nil || query == nil {
		return nil
	}
	issuer := strings.TrimSpace(os.Getenv("LEVARA_SCIM_ISSUER"))
	if issuer == "" {
		issuer = "scim-directory"
	}
	svc := &scimService{store: store, query: query, issuer: issuer, token: token}
	if err := store.EnsureSchema(context.Background()); err != nil {
		return fmt.Errorf("scim: schema: %w", err)
	}
	svc.audit = func(action, externalID, userID string) {
		if audit != nil {
			audit(scimAuditLine(action, svc.issuer, externalID, userID))
		}
	}

	guard := func(c *fiber.Ctx) error {
		auth := c.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(auth, prefix) ||
			!accesspkg.TokenCheck(strings.TrimPrefix(auth, prefix), svc.token) {
			return c.Status(401).JSON(scimError{Status: "401", Detail: "invalid scim token"})
		}
		return c.Next()
	}

	g := public.Group("/scim/v2", guard)

	g.Get("/ServiceProviderConfig", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{
			"schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:ServiceProviderConfig"},
			"patch":   fiber.Map{"supported": true},
			"filter":  fiber.Map{"supported": true, "maxResults": 200},
			"bulk":    fiber.Map{"supported": false},
			"sort":    fiber.Map{"supported": false},
		})
	})

	g.Get("/Schemas", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"schemas": []string{
			"urn:ietf:params:scim:schemas:core:2.0:User",
		}, "id": "urn:ietf:params:scim:schemas:core:2.0:User"})
	})

	g.Post("/Users", func(c *fiber.Ctx) error {
		var req struct {
			UserName   string   `json:"userName"`
			ExternalID string   `json:"externalId"`
			Active     *bool    `json:"active"`
			Schemas    []string `json:"schemas"`
		}
		if err := json.Unmarshal(c.Body(), &req); err != nil {
			return scimErr(c, 400, "invalidValue", "malformed JSON body")
		}
		if strings.TrimSpace(req.UserName) == "" {
			return scimErr(c, 400, "invalidValue", "userName is required")
		}
		active := true
		if req.Active != nil {
			active = *req.Active
		}
		external := req.ExternalID
		if external == "" {
			external = req.UserName // some IdP flows omit externalId on create
		}
		uid, created, err := svc.store.ProvisionCreate(c.Context(), accesspkg.SCIMUser{
			Issuer: svc.issuer, ExternalID: external, Email: req.UserName, Active: active,
		})
		if errors.Is(err, accesspkg.ErrSCIMEmailConflict) {
			return scimErr(c, 409, "uniqueness", "userName already belongs to another identity")
		}
		if err != nil {
			return scimErr(c, 500, "", "provisioning failed")
		}
		svc.audit("create", external, uid)
		status := 201
		if !created {
			status = 200
		}
		return c.Status(status).JSON(scimUserResource(uid, req.UserName, external, active))
	})

	g.Get("/Users", func(c *fiber.Ctx) error {
		if filter := c.Query("filter"); filter != "" {
			if val, ok := scimEqFilter(filter, "userName"); ok {
				uids, exts, total, err := svc.query.ByEmail(c.Context(), svc.issuer, val)
				if err != nil {
					return scimErr(c, 500, "", "lookup failed")
				}
				users := make([]fiber.Map, 0, len(uids))
				for i, uid := range uids {
					users = append(users, scimUserResource(uid, val, exts[i], true))
				}
				return c.JSON(scimListResponse(users, 1, total))
			}
			if val, ok := scimEqFilter(filter, "externalId"); ok {
				uid, err := svc.store.Lookup(c.Context(), svc.issuer, val)
				if errors.Is(err, accesspkg.ErrUserNotFound) {
					return c.JSON(scimListResponse(nil, 1, 0))
				}
				if err != nil {
					return scimErr(c, 500, "", "lookup failed")
				}
				return c.JSON(scimListResponse([]fiber.Map{scimUserResource(uid, "", val, true)}, 1, 1))
			}
			return scimErr(c, 400, "invalidFilter", "only userName eq / externalId eq are supported")
		}
		start, count := scimPagination(c)
		uids, exts, total, err := svc.query.List(c.Context(), svc.issuer, start, count)
		if err != nil {
			return scimErr(c, 500, "", "list failed")
		}
		users := make([]fiber.Map, 0, len(uids))
		for i, uid := range uids {
			users = append(users, scimUserResource(uid, "", exts[i], true))
		}
		return c.JSON(scimListResponse(users, start, total))
	})

	g.Get("/Users/:id", func(c *fiber.Ctx) error {
		uid := c.Params("id")
		email, active, external, err := svc.query.ByID(c.Context(), uid)
		if errors.Is(err, accesspkg.ErrUserNotFound) {
			return scimErr(c, 404, "", "user not found")
		}
		if err != nil {
			return scimErr(c, 500, "", "lookup failed")
		}
		return c.JSON(scimUserResource(uid, email, external, active))
	})

	g.Patch("/Users/:id", func(c *fiber.Ctx) error {
		uid := c.Params("id")
		var req struct {
			Operations []struct {
				Op    string          `json:"op"`
				Path  string          `json:"path"`
				Value json.RawMessage `json:"value"`
			} `json:"Operations"`
		}
		if err := json.Unmarshal(c.Body(), &req); err != nil {
			return scimErr(c, 400, "invalidValue", "malformed PATCH body")
		}
		email, active, external, err := svc.query.ByID(c.Context(), uid)
		if errors.Is(err, accesspkg.ErrUserNotFound) {
			return scimErr(c, 404, "", "user not found")
		}
		if err != nil {
			return scimErr(c, 500, "", "lookup failed")
		}
		desired := accesspkg.SCIMUser{Issuer: svc.issuer, ExternalID: external, Email: email, Active: active}
		var newEmail string
		for _, op := range req.Operations {
			switch strings.ToLower(strings.TrimSpace(op.Path)) {
			case "active":
				var v bool
				if err := json.Unmarshal(op.Value, &v); err != nil {
					return scimErr(c, 400, "invalidValue", "active must be boolean")
				}
				desired.Active = v
			case "username", "emails", "name":
				var v string
				if err := json.Unmarshal(op.Value, &v); err == nil && v != "" {
					newEmail = strings.TrimSpace(v)
				}
			case "":
				// Entra sends {"active": true} without a path on some flows.
				var m map[string]interface{}
				if err := json.Unmarshal(op.Value, &m); err == nil {
					if b, ok := m["active"].(bool); ok {
						desired.Active = b
					}
					if s, ok := m["userName"].(string); ok && s != "" {
						newEmail = strings.TrimSpace(s)
					}
				}
			default:
				return scimErr(c, 400, "invalidPath", "unsupported PATCH path: "+op.Path)
			}
		}
		if err := svc.store.ProvisionUpdate(c.Context(), desired, newEmail); err != nil {
			if errors.Is(err, accesspkg.ErrSCIMEmailConflict) {
				return scimErr(c, 409, "uniqueness", "userName already belongs to another identity")
			}
			return scimErr(c, 500, "", "update failed")
		}
		svc.audit("update", external, uid)
		return c.JSON(scimUserResource(uid, newEmailOr(newEmail, email), external, desired.Active))
	})

	g.Delete("/Users/:id", func(c *fiber.Ctx) error {
		uid := c.Params("id")
		_, _, external, err := svc.query.ByID(c.Context(), uid)
		if errors.Is(err, accesspkg.ErrUserNotFound) {
			return scimErr(c, 404, "", "user not found")
		}
		if err != nil {
			return scimErr(c, 500, "", "lookup failed")
		}
		if err := svc.store.ProvisionDeactivate(c.Context(), svc.issuer, external); err != nil {
			return scimErr(c, 500, "", "deactivate failed")
		}
		svc.audit("deactivate", external, uid)
		return c.SendStatus(204)
	})

	return nil
}

// ── resource shaping ──

func scimUserResource(id, userName, externalID string, active bool) fiber.Map {
	if userName == "" {
		userName = externalID
	}
	return fiber.Map{
		"schemas":    []string{"urn:ietf:params:scim:schemas:core:2.0:User"},
		"id":         id,
		"userName":   userName,
		"externalId": externalID,
		"active":     active,
		"meta":       fiber.Map{"resourceType": "User"},
	}
}

func scimListResponse(resources []fiber.Map, startIndex, total int) fiber.Map {
	if resources == nil {
		resources = []fiber.Map{}
	}
	return fiber.Map{
		"schemas":      []string{"urn:ietf:params:scim:api:messages:2.0:ListResponse"},
		"totalResults": total,
		"startIndex":   startIndex,
		"itemsPerPage": len(resources),
		"Resources":    resources,
	}
}

// scimEqFilter parses `attr eq "value"` (case-insensitive attr, quoted value).
func scimEqFilter(filter, attr string) (string, bool) {
	f := strings.ToLower(strings.TrimSpace(filter))
	needle := strings.ToLower(attr) + " eq "
	if !strings.HasPrefix(f, needle) {
		return "", false
	}
	v := strings.TrimSpace(filter[len(needle):])
	return strings.Trim(v, `"`), v != ""
}

func scimPagination(c *fiber.Ctx) (start, count int) {
	start = c.QueryInt("startIndex", 1)
	if start < 1 {
		start = 1
	}
	count = c.QueryInt("count", 100)
	if count <= 0 {
		count = 100
	}
	if count > 200 {
		count = 200
	}
	return start, count
}

func newEmailOr(new, old string) string {
	if new != "" {
		return new
	}
	return old
}

func scimAuditLine(action, issuer, externalID, userID string) string {
	return fmt.Sprintf("actor=scim action=%s issuer=%s external_id=%s user=%s",
		action, issuer, externalID, userID)
}
