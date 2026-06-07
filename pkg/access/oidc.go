package access

import (
	"context"
)

// OIDCClaims is the verified input a deployment-specific OIDC verifier passes
// into Levara. Signature validation, nonce checks, expiry, and audience checks
// happen before this type is constructed; this adapter only translates verified
// identity facts into the policy-facing IdentityBridge seam.
type OIDCClaims struct {
	Issuer      string
	Subject     string
	Email       string
	DisplayName string
	Groups      []string
	Attributes  map[string]string
}

// OIDCAdapter is an optional in-tree protocol adapter for already-verified OIDC
// sessions/tokens. It stays above IdentityBridge: protocol code maps claims and
// group hints, while the bridge remains the only policy-facing identity seam.
//
// Personal, Solo Pro, and Team can disable OIDC entirely by leaving Bridge nil
// or wiring LocalIdentityBridge.
type OIDCAdapter struct {
	Bridge IdentityBridge
	// GroupTenantMap maps upstream group names to Levara tenant ids. It is a
	// deployment policy input, not an authorization decision; SQLPolicy still
	// enforces the active tenant membership per request.
	GroupTenantMap map[string]string
	// SuperuserGroups names upstream groups that should mark the resolved
	// principal as superuser. Use sparingly; SQLPolicy still performs final
	// authorization checks.
	SuperuserGroups map[string]bool
}

// ResolveVerified maps verified OIDC claims to a Levara Principal.
func (a OIDCAdapter) ResolveVerified(ctx context.Context, claims OIDCClaims) (Principal, error) {
	if a.Bridge == nil {
		return Principal{}, ErrExternalIdentityUnsupported
	}
	ext := claims.ExternalIdentity()
	if err := ext.Validate(); err != nil {
		return Principal{}, err
	}
	principal, err := a.Bridge.ResolveExternal(ctx, ext)
	if err != nil {
		return Principal{}, err
	}
	if principal.AuthMethod == "" {
		principal.AuthMethod = a.Method()
	}
	principal.TenantIDs = mergeTenantIDs(principal.TenantIDs, tenantIDsForGroups(claims.Groups, a.GroupTenantMap))
	if hasMappedGroup(claims.Groups, a.SuperuserGroups) {
		principal.Superuser = true
	}
	return principal, nil
}

// Method returns the auth-method label for OIDC actors.
func (OIDCAdapter) Method() string { return "oidc" }

// ExternalIdentity converts verified OIDC claims into the protocol-agnostic
// bridge input.
func (c OIDCClaims) ExternalIdentity() ExternalIdentity {
	return ExternalIdentity{
		Issuer:      c.Issuer,
		Subject:     c.Subject,
		Email:       c.Email,
		DisplayName: c.DisplayName,
		Groups:      append([]string(nil), c.Groups...),
		Attributes:  cloneStringMap(c.Attributes),
	}
}

func tenantIDsForGroups(groups []string, groupTenantMap map[string]string) []string {
	if len(groups) == 0 || len(groupTenantMap) == 0 {
		return nil
	}
	var tenantIDs []string
	seen := make(map[string]bool)
	for _, group := range groups {
		tenantID := groupTenantMap[group]
		if tenantID == "" || seen[tenantID] {
			continue
		}
		seen[tenantID] = true
		tenantIDs = append(tenantIDs, tenantID)
	}
	return tenantIDs
}

func mergeTenantIDs(existing, mapped []string) []string {
	if len(mapped) == 0 {
		return append([]string(nil), existing...)
	}
	merged := make([]string, 0, len(existing)+len(mapped))
	seen := make(map[string]bool, len(existing)+len(mapped))
	for _, tenantID := range existing {
		if tenantID == "" || seen[tenantID] {
			continue
		}
		seen[tenantID] = true
		merged = append(merged, tenantID)
	}
	for _, tenantID := range mapped {
		if tenantID == "" || seen[tenantID] {
			continue
		}
		seen[tenantID] = true
		merged = append(merged, tenantID)
	}
	return merged
}

func hasMappedGroup(groups []string, mapped map[string]bool) bool {
	if len(groups) == 0 || len(mapped) == 0 {
		return false
	}
	for _, group := range groups {
		if mapped[group] {
			return true
		}
	}
	return false
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
