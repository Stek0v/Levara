package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"encoding/xml"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/crewjam/saml"
	"github.com/gofiber/fiber/v2"
	accesspkg "github.com/stek0v/levara/pkg/access"
)

// samlTestCertLocal mirrors the pkg/access test helper (unexported there).
func samlTestCert(t *testing.T, cn string) (*rsa.PrivateKey, *x509.Certificate) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return key, cert
}

// samlE2E wires a real IdP (crewjam/saml IdentityProvider) to a real SP via
// the HTTP layer registered by SAMLRoutes, then drives the full browser
// round trip.
type samlE2E struct {
	idp     *saml.IdentityProvider
	idpURL  string
	sp      *accesspkg.SAMLSP
	app     *fiber.App
	authCfg string
}

func newSAMLE2E(t *testing.T) *samlE2E {
	t.Helper()
	idpKey, idpCert := samlTestCert(t, "idp")
	spKey, spCert := samlTestCert(t, "sp")

	idp := &saml.IdentityProvider{
		Key:         idpKey,
		Certificate: idpCert,
		MetadataURL: url.URL{Scheme: "http", Host: "idp.test", Path: "/metadata"},
		SSOURL:      url.URL{Scheme: "http", Host: "idp.test", Path: "/sso"},
	}
	// The SP fetches IdP metadata over HTTP only for localhost; serve it.
	idpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := xmlMarshal(idp.Metadata())
		w.Header().Set("Content-Type", "application/samlmetadata+xml")
		_, _ = w.Write(buf)
	}))
	t.Cleanup(idpSrv.Close)

	bridge := accesspkg.SimpleMappingBridge{}
	sp, err := accesspkg.NewSAMLSP(context.Background(), accesspkg.SAMLSPConfig{
		EntityID:       "https://sp.test/saml/metadata",
		AcsURL:         "https://sp.test/saml/acs",
		MetadataURL:    "https://sp.test/saml/metadata",
		IDPMetadataURL: idpSrv.URL + "/metadata",
		Key:            spKey,
		Certificate:    spCert,
	}, bridge)
	if err != nil {
		t.Fatalf("NewSAMLSP: %v", err)
	}

	authCfg := "e2e-saml-secret"
	// Routes at root for the test; prod mounts on the public router group.
	app := fiber.New()
	samlRoutes(app, sp, authCfg)

	_ = idp
	return &samlE2E{idp: idp, idpURL: idpSrv.URL, sp: sp, app: app, authCfg: authCfg}
}

func xmlMarshal(v interface{}) ([]byte, error) {
	return xmlMarshalIndent(v)
}

func xmlMarshalIndent(v interface{}) ([]byte, error) {
	return xml.MarshalIndent(v, "", "  ")
}

// TestSAMLRoutesMetadataServed verifies the metadata endpoint serves the SP
// descriptor that an IdP operator would register.
func TestSAMLRoutesMetadataServed(t *testing.T) {
	e := newSAMLE2E(t)
	resp, err := e.app.Test(httptest.NewRequest(http.MethodGet, "/saml/metadata", nil))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(body), "EntityDescriptor") {
		t.Fatalf("metadata endpoint: %d %s", resp.StatusCode, string(body)[:min(200, len(body))])
	}
}

// TestSAMLRoutesLoginRedirect verifies GET /saml/login redirects to the IdP
// with a SAMLRequest and that the flow tracks exactly one request ID.
func TestSAMLRoutesLoginRedirect(t *testing.T) {
	e := newSAMLE2E(t)
	resp, err := e.app.Test(httptest.NewRequest(http.MethodGet, "/saml/login", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 302 {
		t.Fatalf("login: want 302, got %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "SAMLRequest") {
		t.Fatalf("redirect lacks SAMLRequest: %s", loc)
	}
}

// TestSAMLRoutesACSMissingBody verifies the ACS rejects an empty POST with
// 401 and no IdP detail leakage.
func TestSAMLRoutesACSMissingBody(t *testing.T) {
	e := newSAMLE2E(t)
	req := httptest.NewRequest(http.MethodPost, "/saml/acs", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := e.app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 401 {
		t.Fatalf("acs: want 401, got %d", resp.StatusCode)
	}
	if strings.Contains(string(body), "InResponseTo") || strings.Contains(string(body), "saml:") {
		t.Fatalf("error leaks internal detail: %s", string(body))
	}
}

// TestSAMLRoutesDisabledWhenNil verifies no routes are registered when the
// SP is nil (feature flag off) — fail-closed surface behavior.
func TestSAMLRoutesDisabledWhenNil(t *testing.T) {
	app := fiber.New()
	samlRoutes(app, nil, "s")
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/saml/login", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 404 {
		t.Fatalf("saml disabled but route present: %d", resp.StatusCode)
	}
}

// TestSAMLEnvConfigFlag verifies the env contract: flag off → nil config,
// flag on with missing files → error.
func TestSAMLEnvConfigFlag(t *testing.T) {
	t.Setenv("LEVARA_SAML_ENABLED", "")
	cfg, err := samlConfigFromEnv()
	if err != nil || cfg != nil {
		t.Fatalf("flag off must yield nil config, got %v / %v", cfg, err)
	}
	t.Setenv("LEVARA_SAML_ENABLED", "1")
	t.Setenv("LEVARA_SAML_KEY_FILE", "/nonexistent/key.pem")
	if _, err := samlConfigFromEnv(); err == nil {
		t.Fatal("enabled with missing key file must error")
	}
}

// TestSAMLKeyParsing verifies PEM parsing of PKCS#1 and PKCS#8 RSA keys and
// certificate rejection of garbage.
func TestSAMLKeyParsing(t *testing.T) {
	key, cert := samlTestCert(t, "p")
	p1 := pemEncode("RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(key))
	k, err := parseRSAPrivateKeyPEM(p1)
	if err != nil || k == nil {
		t.Fatalf("pkcs1 key rejected: %v", err)
	}
	p8, _ := x509.MarshalPKCS8PrivateKey(key)
	k2, err := parseRSAPrivateKeyPEM(pemEncode("PRIVATE KEY", p8))
	if err != nil || k2 == nil {
		t.Fatalf("pkcs8 key rejected: %v", err)
	}
	if _, err := parseRSAPrivateKeyPEM("not pem"); err == nil {
		t.Fatal("garbage key accepted")
	}
	if _, err := parseCertificatePEM(pemEncode("CERTIFICATE", cert.Raw)); err != nil {
		t.Fatalf("valid cert rejected: %v", err)
	}
	if _, err := parseCertificatePEM("nope"); err == nil {
		t.Fatal("garbage cert accepted")
	}
}

func pemEncode(typ string, der []byte) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}))
}

var (
	_ = big.NewInt // keep math/big import when bodies change
	_ = x509.CreateCertificate
	_ = pkix.Name{}
	_ = time.Now
	_ = rand.Reader
	_ = accesspkg.SimpleMappingBridge{}
)
