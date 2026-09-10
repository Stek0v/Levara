package http

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"
)

func mcpRequestContext(c *fiber.Ctx, req jsonRPCRequest) (context.Context, context.CancelFunc) {
	var params struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(req.Params, &params)
	if req.Method == "tools/call" && (params.Name == "sync" || params.Name == "sync_status") {
		return syncRequestContext(c)
	}
	return apiRequestContext(c)
}

const (
	// Default upper bound for one search request end-to-end (vector/graph/LLM).
	defaultSearchRequestTimeout = 60 * time.Second
	// Default upper bound for regular HTTP handlers.
	defaultAPIRequestTimeout = 60 * time.Second
	// Sync endpoints may process larger payloads, so keep a longer default.
	defaultSyncRequestTimeout = 5 * time.Minute
	// Detached goroutines should still be bounded to avoid hanging forever.
	defaultBackgroundTaskTimeout = 30 * time.Minute
)

// searchRequestContext returns a request-scoped context with a bounded deadline.
//
// Timeout can be tuned via SEARCH_REQUEST_TIMEOUT_MS.
func searchRequestContext(c *fiber.Ctx) (context.Context, context.CancelFunc) {
	timeout := timeoutFromEnvMs("SEARCH_REQUEST_TIMEOUT_MS", defaultSearchRequestTimeout)
	ctx, cancel := requestContextWithTimeout(c, timeout)
	if c != nil {
		ctx = context.WithValue(ctx, searchActorKey{}, workspaceActorFromFiber(c))
		if _, ok := ctx.Value(searchEvidenceKey{}).(*searchEvidence); !ok {
			ctx = context.WithValue(ctx, searchEvidenceKey{}, &searchEvidence{sources: make(map[searchDocumentSource]struct{})})
		}
		c.SetUserContext(ctx)
	}
	return ctx, cancel
}

// apiRequestContext returns a request-scoped context for regular API handlers.
//
// Timeout can be tuned via HTTP_REQUEST_TIMEOUT_MS.
func apiRequestContext(c *fiber.Ctx) (context.Context, context.CancelFunc) {
	timeout := timeoutFromEnvMs("HTTP_REQUEST_TIMEOUT_MS", defaultAPIRequestTimeout)
	return requestContextWithTimeout(c, timeout)
}

// syncRequestContext returns a request-scoped context for sync handlers.
//
// Timeout can be tuned via SYNC_REQUEST_TIMEOUT_MS.
func syncRequestContext(c *fiber.Ctx) (context.Context, context.CancelFunc) {
	timeout := timeoutFromEnvMs("SYNC_REQUEST_TIMEOUT_MS", defaultSyncRequestTimeout)
	return requestContextWithTimeout(c, timeout)
}

// backgroundTaskContext returns a detached timeout-bounded context for
// asynchronous goroutines that must outlive the incoming HTTP request.
//
// Timeout can be tuned via BACKGROUND_TASK_TIMEOUT_MS.
func backgroundTaskContext() (context.Context, context.CancelFunc) {
	timeout := timeoutFromEnvMs("BACKGROUND_TASK_TIMEOUT_MS", defaultBackgroundTaskTimeout)
	return context.WithTimeout(context.Background(), timeout)
}

func requestContextWithTimeout(c *fiber.Ctx, timeout time.Duration) (context.Context, context.CancelFunc) {
	var base context.Context
	if c != nil {
		base = c.UserContext()
	}
	if base == nil {
		base = context.Background()
	}

	// Respect an existing tighter deadline when upstream middleware already set one.
	if dl, ok := base.Deadline(); ok {
		remaining := time.Until(dl)
		if remaining <= 0 {
			return context.WithCancel(base)
		}
		if remaining < timeout {
			timeout = remaining
		}
	}
	return context.WithTimeout(base, timeout)
}

func timeoutFromEnvMs(key string, fallback time.Duration) time.Duration {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	ms, err := strconv.Atoi(raw)
	if err != nil || ms <= 0 {
		return fallback
	}
	return time.Duration(ms) * time.Millisecond
}
