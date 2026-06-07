package access

import (
	"context"
	"errors"
)

// Phase 4B: enterprise identity adapter boundary.
//
// An IdentityBridge maps an externally-authenticated subject (OIDC sub, SAML
// NameID, …) onto a Levara Principal. The bridge owns only the mapping; it does
// NOT verify the upstream assertion (that is the protocol adapter's job) and it
// does NOT decide access — activation, ownership, and roles stay in SQLPolicy.
// This keeps enterprise SSO addable as an adapter without touching core search,
// memory, workspace, or MCP code: a new bridge is constructed and consulted at
// the auth seam, the rest of the system keeps seeing a plain Actor.
//
// The default deployment wires LocalIdentityBridge, which performs no external
// mapping at all — JWT and API keys remain the only credentials for personal,
// solo_pro, and team profiles.

var (
	// ErrExternalIdentityUnsupported is returned by bridges that do not accept
	// external identities (the local/default deployment). Callers treat it as
	// "no SSO configured" and fall back to JWT/API-key auth.
	ErrExternalIdentityUnsupported = errors.New("access: external identity not supported by this bridge")
	// ErrSubjectNotMapped is returned when a bridge recognises the request shape
	// but no local principal is linked to the external subject (and
	// auto-provisioning is not configured).
	ErrSubjectNotMapped = errors.New("access: external subject is not mapped to a Levara principal")
	// ErrInvalidExternalIdentity is returned when the external identity is
	// missing the issuer or subject needed to resolve it.
	ErrInvalidExternalIdentity = errors.New("access: external identity must carry an issuer and subject")
)

// ExternalIdentity is the normalised claim set a protocol adapter (OIDC/SAML)
// presents after it has verified the upstream assertion. It is transport- and
// protocol-agnostic so the bridge interface is identical for OIDC and SAML.
type ExternalIdentity struct {
	// Issuer is the upstream identity provider (OIDC iss / SAML EntityID).
	Issuer string
	// Subject is the stable external subject (OIDC sub / SAML NameID). Together
	// with Issuer it is the lookup key for the local principal.
	Subject string
	// Email and DisplayName are convenience claims; mapping must not rely on
	// Email for identity because it can be reassigned upstream.
	Email       string
	DisplayName string
	// Groups are the upstream group/role claims an adapter may translate into
	// tenant memberships or the superuser flag.
	Groups []string
	// Attributes carries any remaining provider-specific claims verbatim.
	Attributes map[string]string
}

// Validate reports whether the external identity carries the issuer+subject a
// bridge needs to resolve it.
func (e ExternalIdentity) Validate() error {
	if e.Issuer == "" || e.Subject == "" {
		return ErrInvalidExternalIdentity
	}
	return nil
}

// Principal is the resolved Levara identity a bridge maps an ExternalIdentity
// onto. UserID is the canonical Levara user id policy code consumes. TenantIDs
// lists every tenant the principal belongs to (the *active* tenant for a request
// is still selected per-request by the tenant middleware, not by the bridge),
// so Actor() deliberately does not pick one.
type Principal struct {
	UserID     string
	Email      string
	TenantIDs  []string
	Superuser  bool
	AuthMethod string // "oidc" | "saml" — stamped onto Actor.AuthMethod
}

// Actor projects the resolved principal into the shared Actor shape so policy
// code never has to know the request originated from an SSO bridge. TenantID is
// left empty on purpose: the active tenant is resolved per request, and a
// principal may belong to several tenants (Principal.TenantIDs).
func (p Principal) Actor() Actor {
	return Actor{
		UserID:     p.UserID,
		AuthMethod: p.AuthMethod,
		Superuser:  p.Superuser,
	}
}

// IdentityBridge maps a verified external identity to a Levara principal.
type IdentityBridge interface {
	// ResolveExternal maps a verified external identity to a Levara principal.
	// It returns ErrExternalIdentityUnsupported when the bridge does not accept
	// external identities, ErrSubjectNotMapped when no local principal is linked,
	// and ErrInvalidExternalIdentity when the identity lacks an issuer/subject.
	ResolveExternal(ctx context.Context, ext ExternalIdentity) (Principal, error)
	// Method is the auth-method label the bridge stamps onto resolved actors
	// ("oidc", "saml", or "local" for the default bridge).
	Method() string
}

// LocalIdentityBridge is the default bridge: it accepts no external identities,
// so JWT and API keys remain the only credentials. Wiring it is equivalent to
// "enterprise SSO not configured" and keeps the local-auth contract for
// personal, solo_pro, and team profiles.
type LocalIdentityBridge struct{}

// ResolveExternal always reports that external identities are unsupported.
func (LocalIdentityBridge) ResolveExternal(context.Context, ExternalIdentity) (Principal, error) {
	return Principal{}, ErrExternalIdentityUnsupported
}

// Method returns "local".
func (LocalIdentityBridge) Method() string { return "local" }

// SubjectResolver maps a verified external identity to a Levara principal. An
// enterprise deployment supplies its own resolver (backed by a directory, a
// link table, or an attribute mapping); the resolver is the only thing an SSO
// integration has to write — the rest of the bridge is generic.
type SubjectResolver func(ext ExternalIdentity) (Principal, bool)

// MappedIdentityBridge is the reusable reference bridge: it validates the
// external identity, delegates the issuer+subject→principal mapping to an
// injected SubjectResolver, and stamps Method onto the resolved principal. It
// holds no storage of its own, so it composes with any backing store an
// enterprise chooses without changing the bridge contract.
type MappedIdentityBridge struct {
	// AuthMethod is the label stamped onto resolved principals (e.g. "oidc").
	AuthMethod string
	// Resolver performs the external-subject → principal lookup.
	Resolver SubjectResolver
}

// ResolveExternal validates the identity, runs the resolver, and stamps the
// bridge's auth method onto the resolved principal.
func (b MappedIdentityBridge) ResolveExternal(_ context.Context, ext ExternalIdentity) (Principal, error) {
	if err := ext.Validate(); err != nil {
		return Principal{}, err
	}
	if b.Resolver == nil {
		return Principal{}, ErrSubjectNotMapped
	}
	p, ok := b.Resolver(ext)
	if !ok || p.UserID == "" {
		return Principal{}, ErrSubjectNotMapped
	}
	if p.AuthMethod == "" {
		p.AuthMethod = b.Method()
	}
	return p, nil
}

// Method returns the configured auth method, defaulting to "sso" when unset.
func (b MappedIdentityBridge) Method() string {
	if b.AuthMethod == "" {
		return "sso"
	}
	return b.AuthMethod
}

// Compile-time guarantees that the default and reference bridges satisfy the
// interface, so an enterprise build can swap in its own without surprises.
var (
	_ IdentityBridge = LocalIdentityBridge{}
	_ IdentityBridge = MappedIdentityBridge{}
)
