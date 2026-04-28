// observe_middleware.go — Prometheus HTTP instrumentation (T17).
//
// Wraps a Fiber handler to bump levara_http_requests_total{operation,
// status, user_id} and levara_http_duration_seconds{operation, user_id}
// on every request. user_id labels go through metrics.UserBucket so
// cardinality stays bounded (top-50 active users + "other" + "anon" per
// 20.04-tasks.md D14).
//
// Place after JWTMiddleware so c.Locals("user_id") is populated for
// authenticated traffic; anonymous requests still produce useful series
// under user_id="anon".
package http

import (
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/stek0v/levara/internal/metrics"
)

// PromInstrumentationMiddleware returns a Fiber handler that records the
// request under the given logical operation label. Choose the label at
// registration time — one per endpoint group works well (e.g.
// "datasets", "cognify", "search") so dashboards aggregate naturally.
func PromInstrumentationMiddleware(operation string, bucket *metrics.UserBucket) fiber.Handler {
	return func(c *fiber.Ctx) error {
		start := time.Now()
		err := c.Next()

		uid, _ := c.Locals("user_id").(string)
		if bucket != nil {
			bucket.Observe(uid)
		}
		label := "anon"
		if bucket != nil {
			label = bucket.Label(uid)
		} else if uid != "" {
			label = uid
		}

		status := "success"
		code := c.Response().StatusCode()
		if err != nil || code >= 400 {
			status = "error"
			// Break out 4xx vs 5xx via a second label? One source of truth
			// is enough; the HTTP status class is observable via the body
			// or a separate dashboard query if someone wants it.
			_ = strconv.Itoa(code)
		}

		metrics.HTTPRequests.WithLabelValues(operation, status, label).Inc()
		metrics.HTTPDuration.WithLabelValues(operation, label).Observe(time.Since(start).Seconds())
		return err
	}
}
