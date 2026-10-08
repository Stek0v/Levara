package http

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/gofiber/fiber/v2"
	accesspkg "github.com/stek0v/levara/pkg/access"
)

// bearerToken extracts the Bearer token from an Authorization header.
func bearerToken(header string) string {
	if header == "" {
		return ""
	}
	token := strings.TrimPrefix(header, "Bearer ")
	if token == "null" || token == "undefined" {
		return ""
	}
	return token
}

// firstNonEmpty returns the first non-empty string from the variadic list.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func mcpAuthFailureStatus(err error, denied int) int {
	if errors.Is(err, errIdentityUnavailable) {
		return fiber.StatusServiceUnavailable
	}
	return denied
}

func validMCPSession(c *fiber.Ctx, db *sql.DB, requireAuth bool, payload *jwtPayload) (verifiedMCPAuthorization, error) {
	if payload == nil || payload.Sub == "" {
		return verifiedMCPAuthorization{}, accesspkg.ErrInactiveIdentity
	}
	if db == nil {
		if requireAuth {
			return verifiedMCPAuthorization{}, accesspkg.ErrProvisioningNoDB
		}
		return verifiedMCPAuthorization{UserID: payload.Sub}, nil
	}
	tenant, superuser, err := accesspkg.ValidateSessionCredentialAuthorization(c.UserContext(), db, Q, payload.Sub, payload.CredentialEpoch, payload.SessionID)
	return verifiedMCPAuthorization{UserID: payload.Sub, TenantID: tenant, Superuser: superuser}, err
}

// authenticateMCPRequest verifies the caller's identity from API key or JWT.
func (h *mcpHandler) authenticateMCPRequest(c *fiber.Ctx) (accesspkg.Actor, error) {
	if apiKey := firstNonEmpty(c.Get("X-API-Key"), c.Get("X-Api-Key")); apiKey != "" {
		if h.cfg.DB == nil {
			return accesspkg.Actor{}, fmt.Errorf("%w: database required for API key auth", errIdentityUnavailable)
		}
		id, err := verifyAPIKey(c.UserContext(), h.cfg.DB, apiKey)
		if err != nil {
			return accesspkg.Actor{}, fmt.Errorf("%w: %v", errIdentityUnavailable, err)
		}
		if !id.Valid() {
			return accesspkg.Actor{}, fmt.Errorf("invalid API key")
		}
		c.Locals("verified_api_key", id)
		return accesspkg.Actor{UserID: id.UserID, APIKeyPermissions: id.Permissions, AuthMethod: "api_key"}, nil
	}

	token := bearerToken(c.Get("Authorization"))
	if token == "" {
		token = c.Cookies("auth_token")
		if token != "" && !cookieMutationAllowed(c, h.cfg.AuthCookieOrigins...) {
			return accesspkg.Actor{}, fmt.Errorf("cookie origin denied")
		}
	}
	if token == "" {
		if h.cfg.RequireAuth {
			return accesspkg.Actor{}, fmt.Errorf("authorization required")
		}
		return accesspkg.Actor{}, nil
	}
	if h.cfg.JWTSecret == "" {
		return accesspkg.Actor{}, fmt.Errorf("JWT secret not configured")
	}
	payload, ok := verifyJWT(token, h.cfg.JWTSecret)
	if !ok {
		// Not a Levara JWT: fall back to the external OIDC provider (A1),
		// mirroring JWTMiddlewareWithOIDC. Fail-closed when both reject.
		if h.cfg.OIDCBearer != nil {
			if principal, err := h.cfg.OIDCBearer.Authenticate(c.UserContext(), token); err == nil {
				if err := validateExternalUser(c.UserContext(), h.cfg.DB, principal); err == nil {
					c.Locals("verified_external", principal)
					return accesspkg.Actor{UserID: principal.UserID, AuthMethod: "oidc"}, nil
				} else if identityFailureStatus(err) == fiber.StatusServiceUnavailable {
					return accesspkg.Actor{}, fmt.Errorf("%w: %v", errIdentityUnavailable, err)
				}
			}
		}
		return accesspkg.Actor{}, fmt.Errorf("invalid token")
	}
	authorization, err := validMCPSession(c, h.cfg.DB, h.cfg.RequireAuth, payload)
	if err != nil {
		if identityFailureStatus(err) == fiber.StatusServiceUnavailable {
			return accesspkg.Actor{}, fmt.Errorf("%w: %v", errIdentityUnavailable, err)
		}
		return accesspkg.Actor{}, err
	}
	c.Locals("verified_jwt", *payload)
	c.Locals("verified_mcp_authorization", authorization)
	return accesspkg.Actor{UserID: payload.Sub, AuthMethod: "jwt"}, nil
}
