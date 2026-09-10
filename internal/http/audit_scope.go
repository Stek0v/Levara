package http

import (
	"context"
	"strings"

	"github.com/stek0v/levara/pkg/audit"
)

// Audit attribution uses verified credentials, never a session/client label.
func verifiedAuditScope(ctx context.Context) audit.VerifiedScope {
	e, ok := ctx.Value(searchEgressKey{}).(searchEgress)
	if !ok || e.actor.UserID == "" {
		return audit.VerifiedScope{}
	}
	switch e.kind {
	case "jwt", "external", "api_key":
		return audit.VerifiedScope{ActorID: strings.Clone(e.actor.UserID), TenantID: strings.Clone(e.actor.TenantID), Verified: true}
	default:
		return audit.VerifiedScope{}
	}
}
