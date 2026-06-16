// ratelimit.go — HTTP rate limiting (T2).
//
// Two buckets, both in-memory (single-node; see D2 in 20.04-tasks.md):
//
//   - auth bucket: per-IP, 10 req/min, applied to /auth/login and /auth/register
//     (hot targets for credential-stuffing).
//   - user bucket: per-user_id (JWT-resolved) with per-IP fallback for anonymous
//     requests, 100 req/min, applied as a trailing middleware after JWTMiddleware.
//
// Rejections are logged in the Prometheus counter levara_rate_limit_rejected_total
// with channel="http" and bucket="auth"|"user".
package http

import (
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/limiter"

	"github.com/stek0v/levara/internal/metrics"
)

// RateLimitConfig holds tunable limits. Defaults match 20.04-tasks.md D10.
type RateLimitConfig struct {
	AuthMax        int           // req per AuthWindow, per IP (default 10)
	AuthWindow     time.Duration // default 1 min
	UserMax        int           // req per UserWindow, per user (default 100)
	UserWindow     time.Duration // default 1 min
}

func (c *RateLimitConfig) withDefaults() {
	if c.AuthMax <= 0 {
		c.AuthMax = 10
	}
	if c.AuthWindow <= 0 {
		c.AuthWindow = time.Minute
	}
	if c.UserMax <= 0 {
		c.UserMax = 100
	}
	if c.UserWindow <= 0 {
		c.UserWindow = time.Minute
	}
}

// AuthRateLimiter returns a middleware that caps unauthenticated hits on the
// /auth/login and /auth/register endpoints per source IP. Callers attach it
// to the specific auth routes, not to the whole group — register, login and
// refresh are the only paths worth capping at 10/min; /auth/me is per-user.
func AuthRateLimiter(cfg RateLimitConfig) fiber.Handler {
	cfg.withDefaults()
	max := cfg.AuthMax
	return limiter.New(limiter.Config{
		Max:        max,
		Expiration: cfg.AuthWindow,
		KeyGenerator: func(c *fiber.Ctx) string {
			return c.IP()
		},
		LimitReached: func(c *fiber.Ctx) error {
			metrics.RateLimitRejected.WithLabelValues("http", "auth").Inc()
			return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
				"error":       "rate limit exceeded",
				"retry_after": int(cfg.AuthWindow.Seconds()),
			})
		},
	})
}

// UserRateLimiter returns a middleware that caps requests per authenticated
// user. When user_id is not populated (public route or pre-JWT middleware),
// the bucket falls back to the source IP so anonymous traffic cannot be used
// to escape the cap.
//
// Place it AFTER JWTMiddleware in the middleware chain so c.Locals("user_id")
// is populated for authenticated requests.
func UserRateLimiter(cfg RateLimitConfig) fiber.Handler {
	cfg.withDefaults()
	return limiter.New(limiter.Config{
		Max:        cfg.UserMax,
		Expiration: cfg.UserWindow,
		KeyGenerator: func(c *fiber.Ctx) string {
			if uid, ok := c.Locals("user_id").(string); ok && uid != "" {
				return "user:" + uid
			}
			return "ip:" + c.IP()
		},
		LimitReached: func(c *fiber.Ctx) error {
			metrics.RateLimitRejected.WithLabelValues("http", "user").Inc()
			return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
				"error":       "rate limit exceeded",
				"retry_after": int(cfg.UserWindow.Seconds()),
			})
		},
	})
}
