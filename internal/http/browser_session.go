package http

import (
	"context"
	"database/sql"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	accesspkg "github.com/stek0v/levara/pkg/access"
)

// IssueBrowserSessionJWT adds a revocable session to the normal live epoch.
func IssueBrowserSessionJWT(ctx context.Context, db *sql.DB, userID, email, secret string) (string, error) {
	epoch, err := accesspkg.CurrentCredentialEpoch(ctx, db, Q, userID)
	if err != nil {
		return "", err
	}
	return issueBrowserSessionAtEpoch(ctx, db, userID, email, secret, epoch)
}

// IssueExternalBrowserSessionJWT binds session issuance to the credential epoch
// at which the verified external assertion passed its revocation check.
func IssueExternalBrowserSessionJWT(ctx context.Context, db *sql.DB, userID, email, secret string, issuedAt int64) (string, error) {
	epoch, err := accesspkg.ExternalCredentialEpoch(ctx, db, Q, userID, issuedAt)
	if err != nil {
		return "", err
	}
	return issueBrowserSessionAtEpoch(ctx, db, userID, email, secret, epoch)
}

func issueBrowserSessionAtEpoch(ctx context.Context, db *sql.DB, userID, email, secret string, epoch int64) (string, error) {
	if err := accesspkg.ValidateCredential(ctx, db, Q, userID, epoch); err != nil {
		return "", err
	}
	expires := time.Now().Add(24 * time.Hour).Unix()
	id, err := accesspkg.CreateBrowserSession(ctx, db, Q, userID, expires)
	if err != nil {
		return "", err
	}
	return signSessionPayload(jwtPayload{Sub: userID, Email: email, Exp: expires, Iat: time.Now().Unix(), CredentialEpoch: epoch, SessionID: id}, secret), nil
}

// BrowserSessionCookie is shared by local and federated login handlers.
// Production never sets a session cookie without Secure.
func BrowserSessionCookie(token string, secure bool) *http.Cookie {
	return &http.Cookie{Name: "auth_token", Value: token, Path: "/", MaxAge: 86400, Expires: time.Now().Add(24 * time.Hour), HttpOnly: true, Secure: secure || os.Getenv("ENV") == "production", SameSite: http.SameSiteLaxMode}
}

// CookieRequestAllowed only trusts configured public origins, or the actual
// direct request origin when no proxy/public origin was configured. Forwarded
// host/proto headers never expand this allowlist.
func CookieRequestAllowed(c *fiber.Ctx, requireOrigin bool, origins ...string) bool {
	origin := c.Get("Origin")
	if origin == "" {
		return !requireOrigin && c.Get("Sec-Fetch-Site") != "cross-site"
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.User != nil || strings.ContainsAny(origin, "?#") || parsed.Path != "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return false
	}
	if len(origins) == 0 {
		scheme := "http"
		if c.Context().IsTLS() {
			scheme = "https"
		}
		origins = []string{scheme + "://" + string(c.Request().Header.Host())}
	}
	for _, allowed := range origins {
		if origin == allowed {
			return true
		}
	}
	return false
}

func cookieMutationAllowed(c *fiber.Ctx, origins ...string) bool {
	switch c.Method() {
	case fiber.MethodGet, fiber.MethodHead, fiber.MethodOptions:
		return true
	}
	return CookieRequestAllowed(c, true, origins...)
}

func logoutHandler(cfg AuthConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := apiRequestContext(c)
		defer cancel()
		c.SetUserContext(ctx)
		c.Set("Cache-Control", "no-store")
		token := bearerToken(c.Get("Authorization"))
		if token == "" {
			if !CookieRequestAllowed(c, true, cfg.CookieOrigins...) {
				return c.SendStatus(fiber.StatusForbidden)
			}
			token = c.Cookies("auth_token")
		}
		payload, valid := verifyJWT(token, cfg.JWTSecret)
		if !valid || !validSession(ctx, cfg.DB, cfg.RequireAuth, payload) {
			return c.SendStatus(fiber.StatusUnauthorized)
		}
		if payload.SessionID != "" {
			if cfg.DB == nil {
				return c.SendStatus(fiber.StatusUnauthorized)
			}
			err := accesspkg.RevokeBrowserSession(ctx, cfg.DB, Q, payload.Sub, payload.SessionID)
			if err != nil {
				return c.SendStatus(fiber.StatusServiceUnavailable)
			}
		}
		cookie := BrowserSessionCookie("", cfg.CookieSecure || c.Context().IsTLS())
		cookie.MaxAge = -1
		cookie.Expires = time.Unix(1, 0)
		c.Response().Header.Add("Set-Cookie", cookie.String())
		return c.SendStatus(fiber.StatusNoContent)
	}
}

// BrowserReturnPath accepts only a local absolute path, never a network path,
// backslash, control character, fragment or a double-encoded redirect prefix.
func BrowserReturnPath(raw string) (string, error) {
	if raw == "" {
		return "/", nil
	}
	u, err := url.Parse(raw)
	if err != nil || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") || u.IsAbs() || u.Host != "" || strings.Contains(raw, "#") {
		return "", accesspkg.ErrInvalidExternalIdentity
	}
	decoded := raw
	for {
		if strings.ContainsAny(decoded, "\\\r\n\t") || strings.HasPrefix(decoded, "//") {
			return "", accesspkg.ErrInvalidExternalIdentity
		}
		for _, r := range decoded {
			if r < 32 || r == 127 {
				return "", accesspkg.ErrInvalidExternalIdentity
			}
		}
		next, err := url.PathUnescape(decoded)
		if err != nil {
			return "", err
		}
		if next == decoded {
			break
		}
		decoded = next
	}
	return raw, nil
}
