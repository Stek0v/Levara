package access

import (
	"context"
	"fmt"
)

// Actor is a transport-independent principal. Adapters in internal/http and
// pkg/mcp construct it from Fiber locals and MCP session context respectively,
// so policy code never reads transport-specific state. The fields mirror the
// authn data the identity split (Phase 2B) needs: user identity, API-key
// permissions, the active tenant, the auth method, and a superuser flag.
type Actor struct {
	UserID            string
	APIKeyPermissions string
	TenantID          string
	AuthMethod        string
	Superuser         bool
}

// ResourceKind enumerates the protected resource families a policy understands.
type ResourceKind string

const (
	// ResourceWorkspace is a verifiable-workspace project (project_id).
	ResourceWorkspace ResourceKind = "workspace"
	// ResourceDataset is a dataset/collection (dataset_id).
	ResourceDataset ResourceKind = "dataset"
)

// Resource identifies the object an action targets.
type Resource struct {
	Kind ResourceKind
	ID   string
}

// Authorize is the single policy facade shared by every transport. It routes to
// the resource-specific decision while preserving the existing per-resource
// semantics, so REST, MCP, and gRPC callers get identical access behavior for
// the same Actor/Resource/Action.
func (p SQLPolicy) Authorize(ctx context.Context, actor Actor, res Resource, action string) (Decision, error) {
	switch res.Kind {
	case ResourceWorkspace:
		return p.AuthorizeWorkspace(ctx, WorkspaceRequest{
			UserID:            actor.UserID,
			ProjectID:         res.ID,
			Action:            action,
			APIKeyPermissions: actor.APIKeyPermissions,
		})
	case ResourceDataset:
		return p.AuthorizeDataset(ctx, actor, res.ID, action)
	default:
		return Decision{}, fmt.Errorf("access: unknown resource kind %q", res.Kind)
	}
}

// IsTenantMember reports whether userID belongs to tenantID. Empty inputs or a
// nil DB yield (false, nil), preserving the dev-mode / single-user contract
// where tenant isolation is not enforced.
func (p SQLPolicy) IsTenantMember(ctx context.Context, userID, tenantID string) (bool, error) {
	if p.DB == nil || userID == "" || tenantID == "" {
		return false, nil
	}
	var count int
	err := p.DB.QueryRowContext(ctx,
		p.rewrite("SELECT COUNT(*) FROM user_tenant WHERE user_id = $1 AND tenant_id = $2"),
		userID, tenantID,
	).Scan(&count)
	return count > 0, err
}
