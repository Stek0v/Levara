package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

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
	ByEmail(ctx context.Context, issuer, email string) (users []scimUserRecord, total int, err error)
	List(ctx context.Context, issuer string, start, count int) (users []scimUserRecord, total int, err error)
	ByID(ctx context.Context, issuer, id string) (email string, active bool, externalID string, err error)
}

type scimUserRecord struct {
	ID, Email, ExternalID string
	Active                bool
}

type scimService struct {
	store   scimStore
	query   scimQuerier
	issuer  string
	token   string
	audit   func(action, externalID, userID string)
	managed *accesspkg.SCIMStore
}

// scimError renders RFC 7644 §3.12 error shapes.
type scimError struct {
	Schemas  []string `json:"schemas"`
	Status   string   `json:"status"`
	ScimType string   `json:"scimType,omitempty"`
	Detail   string   `json:"detail"`
}

func scimErr(c *fiber.Ctx, status int, scimType, detail string) error {
	return c.Status(status).JSON(scimError{
		Schemas: []string{"urn:ietf:params:scim:api:messages:2.0:Error"}, Status: fmt.Sprint(status), ScimType: scimType, Detail: detail,
	}, "application/scim+json")
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
	configured, managed, err := configureSCIMStore(context.Background(), store, issuer)
	if err != nil {
		return fmt.Errorf("scim: directory binding: %w", err)
	}
	svc.store, svc.managed = configured, managed
	svc.audit = func(action, externalID, userID string) {
		if audit != nil {
			audit(scimAuditLine(action, svc.issuer, externalID, userID))
		}
	}

	guard := func(c *fiber.Ctx) error {
		c.Set("Cache-Control", "no-store")
		auth := c.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(auth, prefix) ||
			!accesspkg.TokenCheck(strings.TrimPrefix(auth, prefix), svc.token) {
			return scimErr(c, 401, "", "invalid scim token")
		}
		ctx, cancel := context.WithTimeout(c.UserContext(), 5*time.Second)
		defer cancel()
		c.SetUserContext(ctx)
		err := c.Next()
		c.Set("Content-Type", "application/scim+json")
		return err
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

	svc.discoveryRoutes(g)
	if svc.managed != nil {
		svc.groupRoutes(g)
	}

	g.Post("/Users", func(c *fiber.Ctx) error {
		var req struct {
			UserName   string          `json:"userName"`
			ExternalID string          `json:"externalId"`
			Active     *bool           `json:"active"`
			Schemas    []string        `json:"schemas"`
			Enterprise json.RawMessage `json:"urn:ietf:params:scim:schemas:extension:enterprise:2.0:User"`
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
		var enterprise *accesspkg.SCIMEnterpriseUser
		if len(req.Enterprise) > 0 {
			if svc.managed == nil {
				return scimErr(c, 400, "invalidValue", "enterprise extension requires configured tenant")
			}
			fields, err := enterpriseFields(req.Enterprise)
			if err != nil {
				return scimStoreError(c, err)
			}
			enterprise = enterpriseProfile(fields)
		}
		external := req.ExternalID
		if external == "" && svc.managed != nil {
			return scimErr(c, 400, "invalidValue", "externalId is required for managed identities")
		}
		if external == "" {
			external = req.UserName // some IdP flows omit externalId on create
		}
		uid, created, err := svc.store.ProvisionCreate(c.UserContext(), accesspkg.SCIMUser{
			Issuer: svc.issuer, ExternalID: external, Email: req.UserName, Active: active, Enterprise: enterprise,
		})
		if errors.Is(err, accesspkg.ErrSCIMEmailConflict) {
			return scimErr(c, 409, "uniqueness", "userName already belongs to another identity")
		}
		if err != nil {
			return scimStoreError(c, err)
		}
		svc.audit("create", external, uid)
		status := 201
		email := req.UserName
		if !created {
			status = 200
			email, active, external, err = svc.query.ByID(c.UserContext(), svc.issuer, uid)
			if err != nil {
				return scimErr(c, 500, "", "lookup failed")
			}
		}
		resource, err := svc.userResource(c.UserContext(), uid, email, external, active)
		if err != nil {
			return scimStoreError(c, err)
		}
		return c.Status(status).JSON(resource)
	})

	g.Get("/Users", func(c *fiber.Ctx) error {
		if filter := c.Query("filter"); filter != "" {
			if val, ok := scimEqFilter(filter, "userName"); ok {
				records, total, err := svc.query.ByEmail(c.UserContext(), svc.issuer, val)
				if err != nil {
					return scimErr(c, 500, "", "lookup failed")
				}
				users := make([]fiber.Map, 0, len(records))
				for _, u := range records {
					resource, err := svc.userResource(c.UserContext(), u.ID, u.Email, u.ExternalID, u.Active)
					if err != nil {
						return scimStoreError(c, err)
					}
					users = append(users, resource)
				}
				return c.JSON(scimListResponse(users, 1, total))
			}
			if val, ok := scimEqFilter(filter, "externalId"); ok {
				uid, err := svc.store.Lookup(c.UserContext(), svc.issuer, val)
				if errors.Is(err, accesspkg.ErrUserNotFound) {
					return c.JSON(scimListResponse(nil, 1, 0))
				}
				if err != nil {
					return scimErr(c, 500, "", "lookup failed")
				}
				email, active, external, err := svc.query.ByID(c.UserContext(), svc.issuer, uid)
				if err != nil {
					return scimErr(c, 500, "", "lookup failed")
				}
				resource, err := svc.userResource(c.UserContext(), uid, email, external, active)
				if err != nil {
					return scimStoreError(c, err)
				}
				return c.JSON(scimListResponse([]fiber.Map{resource}, 1, 1))
			}
			return scimErr(c, 400, "invalidFilter", "only userName eq / externalId eq are supported")
		}
		start, count := scimPagination(c)
		records, total, err := svc.query.List(c.UserContext(), svc.issuer, start, count)
		if err != nil {
			return scimErr(c, 500, "", "list failed")
		}
		users := make([]fiber.Map, 0, len(records))
		for _, u := range records {
			resource, err := svc.userResource(c.UserContext(), u.ID, u.Email, u.ExternalID, u.Active)
			if err != nil {
				return scimStoreError(c, err)
			}
			users = append(users, resource)
		}
		return c.JSON(scimListResponse(users, start, total))
	})

	g.Get("/Users/:id", func(c *fiber.Ctx) error {
		uid := c.Params("id")
		email, active, external, err := svc.query.ByID(c.UserContext(), svc.issuer, uid)
		if errors.Is(err, accesspkg.ErrUserNotFound) {
			return scimErr(c, 404, "", "user not found")
		}
		if err != nil {
			return scimErr(c, 500, "", "lookup failed")
		}
		resource, err := svc.userResource(c.UserContext(), uid, email, external, active)
		if err != nil {
			return scimStoreError(c, err)
		}
		return c.JSON(resource)
	})

	g.Patch("/Users/:id", svc.patchUser)

	g.Delete("/Users/:id", func(c *fiber.Ctx) error {
		uid := c.Params("id")
		_, _, external, err := svc.query.ByID(c.UserContext(), svc.issuer, uid)
		if errors.Is(err, accesspkg.ErrUserNotFound) {
			return scimErr(c, 404, "", "user not found")
		}
		if err != nil {
			return scimErr(c, 500, "", "lookup failed")
		}
		if err := svc.store.ProvisionDeactivate(c.UserContext(), svc.issuer, external); err != nil {
			return scimErr(c, 500, "", "deactivate failed")
		}
		svc.audit("deactivate", external, uid)
		return c.SendStatus(204)
	})

	return nil
}

// ── resource shaping ──

func scimUserResource(id, userName, externalID string, active bool) fiber.Map {
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
	f := strings.TrimSpace(filter)
	parts := strings.SplitN(f, " ", 3)
	if len(parts) != 3 || !strings.EqualFold(parts[0], attr) || !strings.EqualFold(parts[1], "eq") {
		return "", false
	}
	var value string
	if json.Unmarshal([]byte(strings.TrimSpace(parts[2])), &value) != nil || value == "" {
		return "", false
	}
	return value, true
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

func scimAuditLine(action, issuer, _ string, userID string) string {
	// Mirror only stable IDs. JSON escaping prevents log-line injection and
	// externalId may be a legacy email, so it is not copied to the log sink.
	line, _ := json.Marshal(map[string]string{"actor": "scim", "action": action, "issuer": issuer, "user_id": userID})
	return string(line)
}
