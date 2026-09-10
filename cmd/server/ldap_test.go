package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	vectorHttp "github.com/stek0v/levara/internal/http"
	accesspkg "github.com/stek0v/levara/pkg/access"
	vectorAuth "github.com/stek0v/levara/pkg/auth"
)

func TestDirectoryAdapterUsesExactProvisionedIdentity(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			store := newBrowserAuthStore(t, dialect)
			ctx := context.Background()
			uid, _, err := store.ProvisionCreate(ctx, accesspkg.SCIMUser{Issuer: "directory", ExternalID: "00112233-4455-6677-8899-aabbccddeeff", Email: "stored@example.test", Active: true})
			if err != nil {
				t.Fatal(err)
			}
			claim := vectorAuth.LDAPIdentity{Issuer: "exact-provider", Subject: "00112233-4455-6677-8899-aabbccddeeff", Email: "changed-directory-email@example.test", IssuedAt: time.Now().Unix()}
			adapter := directoryPasswordAuth{verify: func(context.Context, string, string) (vectorAuth.LDAPIdentity, error) { return claim, nil }, bridge: accesspkg.SQLIdentityBridge{DB: store.DB, Q: store.Q, TrustedIssuers: map[string]string{"exact-provider": "directory", "other-provider": "other-directory"}}}
			for _, username := range []string{"before-rename", "after-rename"} {
				p, err := adapter.AuthenticateCredentials(ctx, username, "password")
				if err != nil || p.UserID != uid || p.Email != "stored@example.test" || p.IssuedAt != claim.IssuedAt {
					t.Fatalf("mapping=%+v err=%v", p, err)
				}
			}
			for _, bad := range []vectorAuth.LDAPIdentity{{Issuer: "other-provider", Subject: claim.Subject, Email: "stored@example.test"}, {Issuer: "exact-provider", Subject: "unknown", Email: "stored@example.test"}, {Issuer: "EXACT-provider", Subject: claim.Subject, Email: "stored@example.test"}} {
				old := claim
				claim = bad
				if p, err := adapter.AuthenticateCredentials(ctx, "same-email", "password"); err == nil || p.UserID != "" {
					t.Fatalf("unmapped identity accepted: %+v", p)
				}
				claim = old
			}
			if err := store.ProvisionDeactivate(ctx, "directory", claim.Subject); err != nil {
				t.Fatal(err)
			}
			if _, err := adapter.AuthenticateCredentials(ctx, "user", "password"); err == nil {
				t.Fatal("inactive user accepted")
			}
			adapter.verify = func(context.Context, string, string) (vectorAuth.LDAPIdentity, error) {
				return vectorAuth.LDAPIdentity{}, errors.New("bind failed")
			}
			if _, err := adapter.AuthenticateCredentials(ctx, "user", "password"); err == nil {
				t.Fatal("bind failure accepted")
			}
		})
	}
}

func TestDirectoryConfigurationRequiresCompleteSecureSetup(t *testing.T) {
	for _, key := range []string{"LEVARA_LDAP_URL", "LEVARA_LDAP_ISSUER", "LEVARA_LDAP_DIRECTORY_KIND", "LEVARA_LDAP_BASE_DN", "LEVARA_LDAP_BIND_DN", "LEVARA_LDAP_BIND_PASSWORD_FILE", "LEVARA_LDAP_CA_FILE", "LEVARA_LDAP_TIMEOUT", "LEVARA_LDAP_USERNAME_ATTRIBUTE", "LEVARA_LDAP_SUBJECT_ATTRIBUTE", "LEVARA_LDAP_REQUIRED_GROUP_DN", "LEVARA_LDAP_GROUP_BASE_DN", "LEVARA_LDAP_GROUP_MEMBER_ATTRIBUTE"} {
		t.Setenv(key, "")
	}
	if auth, err := newDirectoryAuthFromEnv(vectorHttp.AuthConfig{}); err != nil || auth != nil {
		t.Fatalf("disabled auth=%v err=%v", auth, err)
	}
	t.Setenv("LEVARA_LDAP_ISSUER", "directory-provider")
	if _, err := newDirectoryAuthFromEnv(vectorHttp.AuthConfig{}); err == nil {
		t.Fatal("partial config accepted")
	}
	t.Setenv("LEVARA_LDAP_URL", "ldaps://127.0.0.1:1")
	if _, err := newDirectoryAuthFromEnv(vectorHttp.AuthConfig{}); err == nil {
		t.Fatal("missing SQL accepted")
	}
	store := newBrowserAuthStore(t, "sqlite")
	cfg := vectorHttp.AuthConfig{DB: store.DB}
	secretFile := filepath.Join(t.TempDir(), "bind-password")
	if err := os.WriteFile(secretFile, []byte("secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{"LEVARA_LDAP_DIRECTORY_KIND": "ldap", "LEVARA_LDAP_BASE_DN": "dc=test", "LEVARA_LDAP_BIND_DN": "cn=reader,dc=test", "LEVARA_LDAP_BIND_PASSWORD_FILE": secretFile} {
		t.Setenv(key, value)
	}
	if auth, err := newDirectoryAuthFromEnv(cfg); err != nil || auth == nil {
		t.Fatalf("valid config auth=%v err=%v", auth, err)
	} // constructor does not contact directory
	for _, tc := range []struct{ key, value string }{{"LEVARA_LDAP_URL", "http://127.0.0.1"}, {"LEVARA_LDAP_URL", "ldaps://user:pass@directory.test"}, {"LEVARA_LDAP_URL", "ldap://directory.test/dc=test"}, {"LEVARA_LDAP_TIMEOUT", "1h"}, {"LEVARA_LDAP_USERNAME_ATTRIBUTE", "uid)(objectClass=*"}, {"LEVARA_LDAP_SUBJECT_ATTRIBUTE", "mail"}, {"LEVARA_LDAP_CA_FILE", secretFile}, {"LEVARA_LDAP_BIND_PASSWORD_FILE", secretFile + "-missing"}, {"LEVARA_LDAP_REQUIRED_GROUP_DN", "cn=group,dc=foreign"}} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			t.Setenv(tc.key, tc.value)
			if _, err := newDirectoryAuthFromEnv(cfg); err == nil {
				t.Fatal("unsafe directory configuration accepted")
			}
		})
	}
}
