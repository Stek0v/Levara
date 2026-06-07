package access

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestOIDCAdapterResolveVerifiedMapsClaimsAndGroups(t *testing.T) {
	var seen ExternalIdentity
	adapter := OIDCAdapter{
		Bridge: MappedIdentityBridge{
			AuthMethod: "oidc",
			Resolver: func(ext ExternalIdentity) (Principal, bool) {
				seen = ext
				if ext.Issuer == "https://idp.example" && ext.Subject == "sub-42" {
					return Principal{
						UserID:    "user-a",
						Email:     ext.Email,
						TenantIDs: []string{"tenant-existing"},
					}, true
				}
				return Principal{}, false
			},
		},
		GroupTenantMap: map[string]string{
			"eng":      "tenant-eng",
			"platform": "tenant-existing",
		},
		SuperuserGroups: map[string]bool{
			"admins": true,
		},
	}

	principal, err := adapter.ResolveVerified(context.Background(), OIDCClaims{
		Issuer:      "https://idp.example",
		Subject:     "sub-42",
		Email:       "a@example.com",
		DisplayName: "Alice",
		Groups:      []string{"eng", "platform", "admins"},
		Attributes:  map[string]string{"acr": "mfa"},
	})
	if err != nil {
		t.Fatalf("ResolveVerified: %v", err)
	}
	if seen.Issuer != "https://idp.example" ||
		seen.Subject != "sub-42" ||
		seen.Email != "a@example.com" ||
		seen.DisplayName != "Alice" ||
		!reflect.DeepEqual(seen.Groups, []string{"eng", "platform", "admins"}) ||
		seen.Attributes["acr"] != "mfa" {
		t.Fatalf("bridge saw unexpected external identity: %+v", seen)
	}
	if principal.UserID != "user-a" || principal.AuthMethod != "oidc" {
		t.Fatalf("principal identity = %+v, want user-a/oidc", principal)
	}
	if !principal.Superuser {
		t.Fatalf("principal should be superuser from admins group: %+v", principal)
	}
	wantTenants := []string{"tenant-existing", "tenant-eng"}
	if !reflect.DeepEqual(principal.TenantIDs, wantTenants) {
		t.Fatalf("principal tenants = %v, want %v", principal.TenantIDs, wantTenants)
	}
}

func TestOIDCAdapterDisabledForLocalProfiles(t *testing.T) {
	for name, adapter := range map[string]OIDCAdapter{
		"nil bridge":   {},
		"local bridge": {Bridge: LocalIdentityBridge{}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := adapter.ResolveVerified(context.Background(), OIDCClaims{
				Issuer:  "https://idp.example",
				Subject: "sub-1",
			})
			if !errors.Is(err, ErrExternalIdentityUnsupported) {
				t.Fatalf("ResolveVerified err = %v, want ErrExternalIdentityUnsupported", err)
			}
		})
	}
}

func TestOIDCAdapterRejectsUnusableClaimsBeforeMapping(t *testing.T) {
	adapter := OIDCAdapter{
		Bridge: MappedIdentityBridge{
			Resolver: func(ExternalIdentity) (Principal, bool) {
				t.Fatal("resolver must not run for invalid claims")
				return Principal{}, true
			},
		},
	}
	for _, claims := range []OIDCClaims{
		{Issuer: "https://idp.example"},
		{Subject: "sub-1"},
		{},
	} {
		if _, err := adapter.ResolveVerified(context.Background(), claims); !errors.Is(err, ErrInvalidExternalIdentity) {
			t.Fatalf("ResolveVerified(%+v) err = %v, want ErrInvalidExternalIdentity", claims, err)
		}
	}
}

func TestOIDCClaimsExternalIdentityIsDefensiveCopy(t *testing.T) {
	claims := OIDCClaims{
		Issuer:     "https://idp.example",
		Subject:    "sub-1",
		Groups:     []string{"eng"},
		Attributes: map[string]string{"acr": "mfa"},
	}
	ext := claims.ExternalIdentity()
	claims.Groups[0] = "changed"
	claims.Attributes["acr"] = "changed"

	if ext.Groups[0] != "eng" || ext.Attributes["acr"] != "mfa" {
		t.Fatalf("ExternalIdentity should not alias mutable claims: %+v", ext)
	}
}

func TestProtocolAdaptersStayOutOfCoreAndWorkspaceHandlers(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	paths := []string{
		filepath.Join(repoRoot, "pkg", "bm25"),
		filepath.Join(repoRoot, "pkg", "vectorstore"),
		filepath.Join(repoRoot, "pkg", "graphstore"),
		filepath.Join(repoRoot, "pkg", "orchestrator"),
		filepath.Join(repoRoot, "internal", "http"),
	}
	for _, root := range paths {
		root := root
		t.Run(filepath.ToSlash(root), func(t *testing.T) {
			err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() || !strings.HasSuffix(path, ".go") {
					return nil
				}
				base := filepath.Base(path)
				if strings.Contains(base, "test") {
					return nil
				}
				body, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				text := string(body)
				if strings.Contains(text, "OIDCAdapter") ||
					strings.Contains(text, "OIDCClaims") ||
					strings.Contains(text, "SAML") ||
					strings.Contains(text, "SCIM") {
					t.Fatalf("%s contains protocol adapter code; keep OIDC/SAML/SCIM out of core and workspace handlers", path)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
