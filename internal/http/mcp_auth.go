package http

import (
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

// authenticateMCPRequest verifies the caller's identity from API key or JWT.
func (h *mcpHandler) authenticateMCPRequest(c *fiber.Ctx) (accesspkg.Actor, error) {
	if apiKey := firstNonEmpty(c.Get("X-API-Key"), c.Get("X-Api-Key")); apiKey != "" {
		if h.cfg.DB == nil {
			return accesspkg.Actor{}, fmt.Errorf("database required for API key auth")
		}
		id := verifyAPIKey(h.cfg.DB, apiKey)
		if !id.Valid() {
			return accesspkg.Actor{}, fmt.Errorf("invalid API key")
		}
		return accesspkg.Actor{UserID: id.UserID, APIKeyPermissions: id.Permissions, AuthMethod: "api_key"}, nil
	}

	token := bearerToken(c.Get("Authorization"))
	if token == "" {
		token = c.Cookies("auth_token")
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
		return accesspkg.Actor{}, fmt.Errorf("invalid token")
	}
	return accesspkg.Actor{UserID: payload.Sub, AuthMethod: "jwt"}, nil
}
