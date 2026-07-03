package http

import (
	"strings"

	"github.com/stek0v/levara/pkg/audit"
)

func classifyOutcome(r mcpToolResult) audit.Outcome {
	if !r.IsError {
		return audit.OutcomeOK
	}
	if len(r.Content) == 0 {
		return audit.OutcomeServerError
	}
	low := strings.ToLower(r.Content[0].Text)
	switch {
	case strings.Contains(low, "timeout"), strings.Contains(low, "deadline exceeded"):
		return audit.OutcomeTimeout
	case strings.Contains(low, "unauthorized"), strings.Contains(low, "forbidden"), strings.Contains(low, "permission denied"):
		return audit.OutcomeUnauthorized
	case strings.Contains(low, "rate limit"), strings.Contains(low, "too many requests"):
		return audit.OutcomeRateLimited
	case strings.Contains(low, "invalid"), strings.Contains(low, "missing"), strings.Contains(low, "bad request"):
		return audit.OutcomeClientError
	default:
		return audit.OutcomeServerError
	}
}

func resultPayloadSize(r mcpToolResult) int {
	var n int
	for _, c := range r.Content {
		n += len(c.Text)
	}
	return n
}

func truncateAuditField(s string) string {
	const lim = 256
	if len(s) <= lim {
		return s
	}
	return s[:lim] + "…"
}
