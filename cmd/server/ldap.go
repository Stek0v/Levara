package main

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	vectorHttp "github.com/stek0v/levara/internal/http"
	accesspkg "github.com/stek0v/levara/pkg/access"
	vectorAuth "github.com/stek0v/levara/pkg/auth"
)

type directoryPasswordAuth struct {
	verify func(context.Context, string, string) (vectorAuth.LDAPIdentity, error)
	bridge accesspkg.IdentityBridge
}

func (d *directoryPasswordAuth) AuthenticateCredentials(ctx context.Context, username, password string) (vectorHttp.ExternalPrincipal, error) {
	identity, err := d.verify(ctx, username, password)
	if err != nil {
		return vectorHttp.ExternalPrincipal{}, err
	}
	principal, err := d.bridge.ResolveExternal(ctx, accesspkg.ExternalIdentity{Issuer: identity.Issuer, Subject: identity.Subject, Email: identity.Email})
	if err != nil {
		return vectorHttp.ExternalPrincipal{}, err
	}
	return vectorHttp.ExternalPrincipal{UserID: principal.UserID, Email: principal.Email, IssuedAt: identity.IssuedAt}, nil
}

func newDirectoryAuthFromEnv(auth vectorHttp.AuthConfig) (vectorHttp.ExternalPasswordAuth, error) {
	keys := []string{"LEVARA_LDAP_ISSUER", "LEVARA_LDAP_DIRECTORY_KIND", "LEVARA_LDAP_BASE_DN", "LEVARA_LDAP_BIND_DN", "LEVARA_LDAP_BIND_PASSWORD_FILE", "LEVARA_LDAP_CA_FILE", "LEVARA_LDAP_TIMEOUT", "LEVARA_LDAP_USERNAME_ATTRIBUTE", "LEVARA_LDAP_SUBJECT_ATTRIBUTE", "LEVARA_LDAP_REQUIRED_GROUP_DN", "LEVARA_LDAP_GROUP_BASE_DN", "LEVARA_LDAP_GROUP_MEMBER_ATTRIBUTE"}
	endpoint := os.Getenv("LEVARA_LDAP_URL")
	if endpoint == "" {
		for _, key := range keys {
			if os.Getenv(key) != "" {
				return nil, errors.New("directory configuration requires LEVARA_LDAP_URL")
			}
		}
		return nil, nil
	}
	if auth.DB == nil {
		return nil, errors.New("directory authentication requires SQL identity storage")
	}
	passwordFile := os.Getenv("LEVARA_LDAP_BIND_PASSWORD_FILE")
	if passwordFile == "" {
		return nil, errors.New("directory service account requires LEVARA_LDAP_BIND_PASSWORD_FILE")
	}
	password, err := os.ReadFile(passwordFile)
	if err != nil {
		return nil, fmt.Errorf("directory service password file: %w", err)
	}
	if len(password) > 64<<10 {
		return nil, errors.New("directory service password file exceeds 64KiB")
	}
	// Mounted secret files may have one final newline; preserve all other bytes.
	secret := strings.TrimSuffix(strings.TrimSuffix(string(password), "\n"), "\r")
	cfg := vectorAuth.LDAPConfig{URL: endpoint, Issuer: os.Getenv("LEVARA_LDAP_ISSUER"), Kind: os.Getenv("LEVARA_LDAP_DIRECTORY_KIND"), BaseDN: os.Getenv("LEVARA_LDAP_BASE_DN"), BindDN: os.Getenv("LEVARA_LDAP_BIND_DN"), BindPassword: secret, UsernameAttribute: os.Getenv("LEVARA_LDAP_USERNAME_ATTRIBUTE"), SubjectAttribute: os.Getenv("LEVARA_LDAP_SUBJECT_ATTRIBUTE"), RequiredGroupDN: os.Getenv("LEVARA_LDAP_REQUIRED_GROUP_DN"), GroupBaseDN: os.Getenv("LEVARA_LDAP_GROUP_BASE_DN"), GroupMemberAttribute: os.Getenv("LEVARA_LDAP_GROUP_MEMBER_ATTRIBUTE")}
	if raw := os.Getenv("LEVARA_LDAP_TIMEOUT"); raw != "" {
		cfg.Timeout, err = time.ParseDuration(raw)
		if err != nil {
			return nil, errors.New("invalid LEVARA_LDAP_TIMEOUT")
		}
	}
	if caFile := os.Getenv("LEVARA_LDAP_CA_FILE"); caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("directory CA file: %w", err)
		}
		cfg.RootCAs = x509.NewCertPool()
		if len(pem) > 1<<20 || !cfg.RootCAs.AppendCertsFromPEM(pem) {
			return nil, errors.New("directory CA file must contain trusted PEM certificates")
		}
	}
	verifier, err := vectorAuth.NewLDAPVerifier(cfg)
	if err != nil {
		return nil, err
	}
	if err := (accesspkg.SCIMStore{DB: auth.DB, Q: vectorHttp.SQLRewriter()}).EnsureSchema(context.Background()); err != nil {
		return nil, err
	}
	bridge := accesspkg.SQLIdentityBridge{DB: auth.DB, Q: vectorHttp.SQLRewriter(), TrustedIssuers: map[string]string{cfg.Issuer: identitySCIMIssuer()}}
	return &directoryPasswordAuth{verify: verifier.Authenticate, bridge: bridge}, nil
}
