package access

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// SimpleMappingBridge is a minimal production IdentityBridge for OIDC bearer
// verification (backlog A1): it deterministically maps a verified external
// identity onto a synthetic Levara user id derived from issuer+subject.
// Deployments that need explicit user provisioning / tenant binding should
// provide their own bridge implementation or wait for SCIM (A3).
//
// The mapping is intentionally injective and stable across restarts:
//
//	<sha256(issuer "\n" subject)>  →  "oidc-" + first 32 hex chars.
//
// There is no directory lookup: any verified token from a configured
// issuer/audience pair yields a principal. Tenant assignment therefore
// stays empty here and is the deployment's policy concern (GroupTenantMap
// on the adapter or future SCIM provisioning).
type SimpleMappingBridge struct {
	// SuperuserGroups names upstream groups that mark the principal
	// superuser. Defaults to empty (never superuser).
	SuperuserGroups map[string]bool
}

// ResolveExternal implements IdentityBridge.
func (b SimpleMappingBridge) ResolveExternal(_ context.Context, ext ExternalIdentity) (Principal, error) {
	if err := ext.Validate(); err != nil {
		return Principal{}, err
	}
	id := SyntheticUserID(ext.Issuer, ext.Subject)
	p := Principal{
		UserID:     id,
		Email:      ext.Email,
		AuthMethod: "oidc",
	}
	for _, g := range ext.Groups {
		if b.SuperuserGroups[g] {
			p.Superuser = true
			break
		}
	}
	return p, nil
}

// Method implements IdentityBridge.
func (b SimpleMappingBridge) Method() string { return "oidc" }

// SyntheticUserID derives a stable Levara user id for an external identity.
// Exported so tests and future provisioning code (SCIM) reuse the exact same
// derivation instead of inventing a second one.
func SyntheticUserID(issuer, subject string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(issuer)) + "\n" + strings.TrimSpace(subject)))
	return fmt.Sprintf("oidc-%s", hex.EncodeToString(sum[:])[:32])
}
