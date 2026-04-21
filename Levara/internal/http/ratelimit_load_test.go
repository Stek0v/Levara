package http

import (
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
)

// TestAuthRateLimiter_HighConcurrency fires many requests in parallel and
// verifies that the limiter ALWAYS lets exactly Max through within the
// window. The unit tests in ratelimit_test.go cover the count-and-reject
// logic sequentially; this test catches the easy-to-miss race where
// concurrent goroutines could double-count and let too many through (or
// trip on a non-thread-safe storage backend).
//
// 200 goroutines × 1 request each, Max=10 — strict equality on the
// allowed count is the security property we lean on for credential
// stuffing protection (T2 / D10).
func TestAuthRateLimiter_HighConcurrency(t *testing.T) {
	const (
		maxAllowed = 10
		callers    = 200
	)

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Post("/auth/login", AuthRateLimiter(RateLimitConfig{
		AuthMax:    maxAllowed,
		AuthWindow: 5 * time.Second,
	}), func(c *fiber.Ctx) error {
		return c.SendString("ok")
	})

	var allowed, rejected atomic.Int64
	var wg sync.WaitGroup

	start := make(chan struct{})
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			req := httptest.NewRequest("POST", "/auth/login", nil)
			resp, err := app.Test(req, 2000)
			if err != nil {
				t.Errorf("Test: %v", err)
				return
			}
			switch resp.StatusCode {
			case 200:
				allowed.Add(1)
			case fiber.StatusTooManyRequests:
				rejected.Add(1)
			default:
				t.Errorf("unexpected status %d", resp.StatusCode)
			}
		}()
	}
	close(start)
	wg.Wait()

	if got := allowed.Load(); got != int64(maxAllowed) {
		t.Errorf("allowed = %d, want exactly %d (limiter let too many through under concurrency)", got, maxAllowed)
	}
	if got := rejected.Load(); got != int64(callers-maxAllowed) {
		t.Errorf("rejected = %d, want %d (limiter rejected wrong count)", got, callers-maxAllowed)
	}
}

// Same property for the per-user limiter — different X-Test-User values
// must each get their own bucket, so the total allowed across N users is
// N * Max, not just Max.
func TestUserRateLimiter_HighConcurrency_PerUser(t *testing.T) {
	const (
		maxPerUser = 5
		users      = 4
		hitsEach   = 50
	)

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(func(c *fiber.Ctx) error {
		if uid := c.Get("X-Test-User"); uid != "" {
			c.Locals("user_id", uid)
		}
		return c.Next()
	})
	app.Use(UserRateLimiter(RateLimitConfig{
		UserMax:    maxPerUser,
		UserWindow: 5 * time.Second,
	}))
	app.Get("/x", func(c *fiber.Ctx) error { return c.SendString("ok") })

	type userCounts struct {
		allowed, rejected atomic.Int64
	}
	counts := make([]*userCounts, users)
	for i := range counts {
		counts[i] = &userCounts{}
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	for u := 0; u < users; u++ {
		for k := 0; k < hitsEach; k++ {
			wg.Add(1)
			go func(userIdx int) {
				defer wg.Done()
				<-start
				req := httptest.NewRequest("GET", "/x", nil)
				req.Header.Set("X-Test-User", string(rune('a'+userIdx)))
				resp, err := app.Test(req, 2000)
				if err != nil {
					t.Errorf("Test: %v", err)
					return
				}
				if resp.StatusCode == 200 {
					counts[userIdx].allowed.Add(1)
				} else {
					counts[userIdx].rejected.Add(1)
				}
			}(u)
		}
	}
	close(start)
	wg.Wait()

	for u, c := range counts {
		if got := c.allowed.Load(); got != int64(maxPerUser) {
			t.Errorf("user %d: allowed = %d, want %d (per-user bucket leaked between users?)", u, got, maxPerUser)
		}
	}
}
