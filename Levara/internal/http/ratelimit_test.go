package http

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
)

// AuthRateLimiter: 11th request from the same IP over 2 req/s window returns 429.
func TestAuthRateLimiter_RejectsOver11th(t *testing.T) {
	app := fiber.New()
	app.Post("/auth/login", AuthRateLimiter(RateLimitConfig{
		AuthMax:    2,
		AuthWindow: 5 * time.Second,
	}), func(c *fiber.Ctx) error {
		return c.SendString("ok")
	})

	// 2 allowed
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("POST", "/auth/login", nil)
		req.RemoteAddr = "10.0.0.1:1234"
		resp, err := app.Test(req, 1000)
		if err != nil {
			t.Fatalf("Test: %v", err)
		}
		if resp.StatusCode != 200 {
			t.Fatalf("request %d expected 200, got %d", i+1, resp.StatusCode)
		}
	}

	// 3rd must be 429
	req := httptest.NewRequest("POST", "/auth/login", nil)
	req.RemoteAddr = "10.0.0.1:1234"
	resp, err := app.Test(req, 1000)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if resp.StatusCode != fiber.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", resp.StatusCode)
	}
}

// UserRateLimiter: key falls through to user_id when present; IP when not.
// Same user from two IPs shares the bucket; different users do not.
func TestUserRateLimiter_PerUserBucket(t *testing.T) {
	app := fiber.New()
	// Middleware that sets user_id from header so test can switch users.
	app.Use(func(c *fiber.Ctx) error {
		if uid := c.Get("X-Test-User"); uid != "" {
			c.Locals("user_id", uid)
		}
		return c.Next()
	})
	app.Use(UserRateLimiter(RateLimitConfig{
		UserMax:    2,
		UserWindow: 5 * time.Second,
	}))
	app.Get("/x", func(c *fiber.Ctx) error { return c.SendString("ok") })

	hit := func(userID, ip string) int {
		req := httptest.NewRequest("GET", "/x", nil)
		req.RemoteAddr = ip + ":1000"
		if userID != "" {
			req.Header.Set("X-Test-User", userID)
		}
		resp, err := app.Test(req, 1000)
		if err != nil {
			t.Fatalf("Test: %v", err)
		}
		return resp.StatusCode
	}

	// user=alice from two IPs: still one shared bucket
	if code := hit("alice", "10.0.0.1"); code != 200 {
		t.Fatalf("alice#1: expected 200, got %d", code)
	}
	if code := hit("alice", "10.0.0.2"); code != 200 {
		t.Fatalf("alice#2: expected 200, got %d", code)
	}
	if code := hit("alice", "10.0.0.3"); code != fiber.StatusTooManyRequests {
		t.Fatalf("alice#3: expected 429, got %d", code)
	}

	// user=bob independently: fresh bucket
	if code := hit("bob", "10.0.0.1"); code != 200 {
		t.Fatalf("bob#1: expected 200, got %d", code)
	}
}

// UserRateLimiter falls back to IP when user_id isn't set — anon requests
// from the same connection share a single bucket. The security property we
// care about is "unauthenticated callers cannot escape the cap by omitting
// the JWT"; the per-IP partitioning itself relies on Fiber's c.IP() which is
// covered by fiber's own tests.
func TestUserRateLimiter_AnonSharesBucket(t *testing.T) {
	app := fiber.New()
	app.Use(UserRateLimiter(RateLimitConfig{
		UserMax:    2,
		UserWindow: 5 * time.Second,
	}))
	app.Get("/x", func(c *fiber.Ctx) error { return c.SendString("ok") })

	hit := func() int {
		req := httptest.NewRequest("GET", "/x", nil)
		resp, err := app.Test(req, 1000)
		if err != nil {
			t.Fatalf("Test: %v", err)
		}
		return resp.StatusCode
	}

	if code := hit(); code != 200 {
		t.Fatalf("anon#1 expected 200, got %d", code)
	}
	if code := hit(); code != 200 {
		t.Fatalf("anon#2 expected 200, got %d", code)
	}
	if code := hit(); code != fiber.StatusTooManyRequests {
		t.Fatalf("anon#3 expected 429, got %d", code)
	}
}
