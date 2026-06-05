package access

import (
	"context"
	"errors"
	"testing"
)

func TestLocalIdentityBridgeRejectsExternal(t *testing.T) {
	var bridge IdentityBridge = LocalIdentityBridge{}
	_, err := bridge.ResolveExternal(context.Background(), ExternalIdentity{
		Issuer:  "https://idp.example",
		Subject: "subject-1",
	})
	if !errors.Is(err, ErrExternalIdentityUnsupported) {
		t.Fatalf("LocalIdentityBridge err=%v, want ErrExternalIdentityUnsupported", err)
	}
	if got := bridge.Method(); got != "local" {
		t.Fatalf("LocalIdentityBridge.Method()=%q, want local", got)
	}
}

func TestMappedIdentityBridgeResolvesSubject(t *testing.T) {
	// The directory links external (issuer, subject) → a Levara principal.
	bridge := MappedIdentityBridge{
		AuthMethod: "oidc",
		Resolver: func(ext ExternalIdentity) (Principal, bool) {
			if ext.Issuer == "https://idp.example" && ext.Subject == "ext-42" {
				return Principal{
					UserID:    "user-a",
					Email:     "a@example.com",
					TenantIDs: []string{"tenant-1", "tenant-2"},
					Superuser: true,
				}, true
			}
			return Principal{}, false
		},
	}

	p, err := bridge.ResolveExternal(context.Background(), ExternalIdentity{
		Issuer:  "https://idp.example",
		Subject: "ext-42",
		Email:   "a@example.com",
		Groups:  []string{"eng"},
	})
	if err != nil {
		t.Fatalf("ResolveExternal err=%v, want nil", err)
	}
	if p.UserID != "user-a" {
		t.Fatalf("principal UserID=%q, want user-a", p.UserID)
	}
	if p.AuthMethod != "oidc" {
		t.Fatalf("principal AuthMethod=%q, want oidc (stamped by bridge)", p.AuthMethod)
	}
	if len(p.TenantIDs) != 2 || !p.Superuser {
		t.Fatalf("principal tenants/superuser not carried through: %+v", p)
	}

	// Actor() projects identity for policy code but never pins an active tenant
	// (that is resolved per request), and carries the auth method + superuser.
	actor := p.Actor()
	if actor.UserID != "user-a" || actor.AuthMethod != "oidc" || !actor.Superuser {
		t.Fatalf("Actor()=%+v, want user-a/oidc/superuser", actor)
	}
	if actor.TenantID != "" {
		t.Fatalf("Actor().TenantID=%q, want empty (active tenant is per-request)", actor.TenantID)
	}
}

func TestMappedIdentityBridgeUnmappedSubject(t *testing.T) {
	bridge := MappedIdentityBridge{
		AuthMethod: "saml",
		Resolver:   func(ExternalIdentity) (Principal, bool) { return Principal{}, false },
	}
	_, err := bridge.ResolveExternal(context.Background(), ExternalIdentity{
		Issuer:  "https://idp.example",
		Subject: "unknown",
	})
	if !errors.Is(err, ErrSubjectNotMapped) {
		t.Fatalf("unmapped subject err=%v, want ErrSubjectNotMapped", err)
	}
}

func TestMappedIdentityBridgeRejectsEmptySubject(t *testing.T) {
	// A resolver that would happily map anything must never be consulted when the
	// external identity is missing its issuer/subject lookup key.
	bridge := MappedIdentityBridge{
		Resolver: func(ExternalIdentity) (Principal, bool) {
			t.Fatal("resolver must not run for an invalid external identity")
			return Principal{}, true
		},
	}
	for _, ext := range []ExternalIdentity{
		{Issuer: "https://idp.example"}, // no subject
		{Subject: "ext-1"},              // no issuer
		{},                              // neither
	} {
		if _, err := bridge.ResolveExternal(context.Background(), ext); !errors.Is(err, ErrInvalidExternalIdentity) {
			t.Fatalf("ResolveExternal(%+v) err=%v, want ErrInvalidExternalIdentity", ext, err)
		}
	}
}

func TestMappedIdentityBridgeNilResolver(t *testing.T) {
	bridge := MappedIdentityBridge{AuthMethod: "oidc"}
	_, err := bridge.ResolveExternal(context.Background(), ExternalIdentity{
		Issuer:  "https://idp.example",
		Subject: "ext-1",
	})
	if !errors.Is(err, ErrSubjectNotMapped) {
		t.Fatalf("nil resolver err=%v, want ErrSubjectNotMapped", err)
	}
}

func TestMappedIdentityBridgeResolverEmptyUserID(t *testing.T) {
	// A resolver that reports ok=true but yields no UserID is treated as unmapped:
	// an empty principal must never reach policy code.
	bridge := MappedIdentityBridge{
		Resolver: func(ExternalIdentity) (Principal, bool) { return Principal{Email: "a@x"}, true },
	}
	_, err := bridge.ResolveExternal(context.Background(), ExternalIdentity{
		Issuer:  "https://idp.example",
		Subject: "ext-1",
	})
	if !errors.Is(err, ErrSubjectNotMapped) {
		t.Fatalf("empty-UserID principal err=%v, want ErrSubjectNotMapped", err)
	}
}

func TestMappedIdentityBridgeMethodDefault(t *testing.T) {
	// An unset AuthMethod defaults to "sso", and that default is stamped onto a
	// resolved principal that did not carry its own method.
	bridge := MappedIdentityBridge{
		Resolver: func(ExternalIdentity) (Principal, bool) { return Principal{UserID: "user-a"}, true },
	}
	if got := bridge.Method(); got != "sso" {
		t.Fatalf("Method()=%q, want sso default", got)
	}
	p, err := bridge.ResolveExternal(context.Background(), ExternalIdentity{
		Issuer:  "https://idp.example",
		Subject: "ext-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.AuthMethod != "sso" {
		t.Fatalf("resolved principal AuthMethod=%q, want sso default", p.AuthMethod)
	}
}
