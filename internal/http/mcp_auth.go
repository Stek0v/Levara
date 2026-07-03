package http

import (
	"fmt"
	"strings"

	"github.com/gofiber/fiber/v2"
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
func (h *mcpHandler) authenticateMCPRequest(c *fiber.Ctx) (string, error) {
	if apiKey := firstNonEmpty(c.Get("X-API-Key"), c.Get("X-Api-Key")); apiKey != "" {
		if h.cfg.DB == nil {
			return "", fmt.Errorf("database required for API key auth")
		}
		id := verifyAPIKey(h.cfg.DB, apiKey)
		if !id.Valid() {
			return "", fmt.Errorf("invalid API key")
		}
		return id.UserID, nil
	}

	token := bearerToken(c.Get("Authorization"))
	if token == "" {
		token = c.Cookies("auth_token")
	}
	if token == "" {
		if h.cfg.RequireAuth {
			return "", fmt.Errorf("authorization required")
		}
		return "", nil
	}
	if h.cfg.JWTSecret == "" {
		return "", fmt.Errorf("JWT secret not configured")
	}
	payload, ok := verifyJWT(token, h.cfg.JWTSecret)
	if !ok {
		return "", fmt.Errorf("invalid token")
	}
	return payload.Sub, nil
}
