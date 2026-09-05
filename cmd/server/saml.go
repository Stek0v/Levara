package main

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strings"

	"net/http"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/adaptor"
	vectorHttp "github.com/stek0v/levara/internal/http"
	accesspkg "github.com/stek0v/levara/pkg/access"
)

// samlConfig carries the env-derived SAML settings resolved at bootstrap.
// Values come from LEVARA_SAML_* variables; SAML is disabled unless
// LEVARA_SAML_ENABLED is truthy AND IdP metadata is configured.
type samlConfig struct {
	idpMetadataURL string
	idpMetadata    []byte
	entityID       string
	acsURL         string
	metadataURL    string
	keyPEM         string
	certPEM        string
}

func samlConfigFromEnv() (*samlConfig, error) {
	if !samlEnabled() {
		return nil, nil
	}
	cfg := &samlConfig{
		idpMetadataURL: strings.TrimSpace(os.Getenv("LEVARA_SAML_IDP_METADATA_URL")),
		entityID:       strings.TrimSpace(os.Getenv("LEVARA_SAML_ENTITY_ID")),
		acsURL:         strings.TrimSpace(os.Getenv("LEVARA_SAML_ACS_URL")),
		metadataURL:    strings.TrimSpace(os.Getenv("LEVARA_SAML_METADATA_URL")),
	}
	if p := strings.TrimSpace(os.Getenv("LEVARA_SAML_IDP_METADATA_FILE")); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("saml: idp metadata file: %w", err)
		}
		cfg.idpMetadata = b
	}
	if p := strings.TrimSpace(os.Getenv("LEVARA_SAML_KEY_FILE")); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("saml: key file: %w", err)
		}
		cfg.keyPEM = string(b)
	}
	if p := strings.TrimSpace(os.Getenv("LEVARA_SAML_CERT_FILE")); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("saml: cert file: %w", err)
		}
		cfg.certPEM = string(b)
	}
	return cfg, nil
}

func samlEnabled() bool {
	v := strings.TrimSpace(os.Getenv("LEVARA_SAML_ENABLED"))
	return v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes")
}

// newSAMLSPFromEnv builds the SP from env. (nil, nil) = disabled; any
// configured-but-broken value errors (fail-closed bootstrap).
func newSAMLSPFromEnv(ctx context.Context, bridge accesspkg.IdentityBridge) (*accesspkg.SAMLSP, error) {
	cfg, err := samlConfigFromEnv()
	if err != nil || cfg == nil {
		return nil, err
	}
	key, err := parseRSAPrivateKeyPEM(cfg.keyPEM)
	if err != nil {
		return nil, err
	}
	cert, err := parseCertificatePEM(cfg.certPEM)
	if err != nil {
		return nil, err
	}
	return accesspkg.NewSAMLSP(ctx, accesspkg.SAMLSPConfig{
		EntityID:       cfg.entityID,
		AcsURL:         cfg.acsURL,
		MetadataURL:    cfg.metadataURL,
		IDPMetadataURL: cfg.idpMetadataURL,
		IDPMetadataXML: cfg.idpMetadata,
		Key:            key,
		Certificate:    cert,
	}, bridge)
}

func parseRSAPrivateKeyPEM(pemStr string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("saml: key file has no PEM block")
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if rsaKey, ok := key.(*rsa.PrivateKey); ok {
			return rsaKey, nil
		}
		return nil, errors.New("saml: key is not RSA")
	}
	return x509.ParsePKCS1PrivateKey(block.Bytes)
}

func parseCertificatePEM(pemStr string) (*x509.Certificate, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("saml: cert file has no PEM block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("saml: parse certificate: %w", err)
	}
	return cert, nil
}

// samlRoutes registers the SAML endpoints on the public (pre-auth) router:
//
//	GET  /saml/login     — SP-initiated flow: redirect to the IdP
//	POST /saml/acs       — Assertion Consumer Service
//	GET  /saml/metadata  — SP metadata for IdP registration
//
// A successful ACS exchange issues a session JWT via the auth config so
// downstream middleware sees a normal authenticated principal.
func samlRoutes(public fiber.Router, sp *accesspkg.SAMLSP, jwtSecret string) {
	if sp == nil {
		return
	}
	public.Get("/saml/login", adaptor.HTTPHandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirect, err := sp.StartAuth(w, r)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(fiber.Map{"detail": "saml authentication failed"})
			return
		}
		http.Redirect(w, r, redirect, http.StatusFound)
	}))
	public.Post("/saml/acs", adaptor.HTTPHandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		principal, err := sp.ConsumeResponse(r.Context(), w, r)
		if err != nil {
			// Opaque rejection: never echo IdP response details to the client.
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(fiber.Map{"detail": "saml authentication failed"})
			return
		}
		token := vectorHttp.CreateSessionJWT(principal.UserID, principal.Email, jwtSecret)
		_ = json.NewEncoder(w).Encode(fiber.Map{"access_token": token, "token_type": "bearer"})
	}))
	public.Get("/saml/metadata", func(c *fiber.Ctx) error {
		c.Set("Content-Type", "application/samlmetadata+xml")
		return c.SendString(sp.Metadata())
	})
}
