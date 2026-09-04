package access

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// ── test scaffolding ──

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

func mustURL(t *testing.T, s string) url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return *u
}

func testBridge() IdentityBridge { return SimpleMappingBridge{} }

// ── construction / config-validation corner cases ──

func TestSAMLSPConfigValidation(t *testing.T) {
	ctx := context.Background()
	key, cert := samlTestCert(t, "sp")
	if _, err := NewSAMLSP(ctx, SAMLSPConfig{}, testBridge()); err == nil {
		t.Fatal("empty config accepted")
	}
	if _, err := NewSAMLSP(ctx, SAMLSPConfig{EntityID: "e", AcsURL: "https://a"}, testBridge()); err == nil {
		t.Fatal("missing key accepted")
	}
	if _, err := NewSAMLSP(ctx, SAMLSPConfig{EntityID: "e", AcsURL: "https://a", Key: key, Certificate: cert}, nil); err == nil {
		t.Fatal("missing bridge accepted")
	}
	if _, err := NewSAMLSP(ctx, SAMLSPConfig{
		EntityID: "e", AcsURL: "https://a", Key: key, Certificate: cert,
		IDPMetadataURL: "https://idp/metadata", IDPMetadataXML: []byte("<x/>"),
	}, testBridge()); err == nil {
		t.Fatal("both metadata sources accepted")
	}
	if _, err := NewSAMLSP(ctx, SAMLSPConfig{
		EntityID: "e", AcsURL: "https://a", Key: key, Certificate: cert,
		IDPMetadataURL: "http://evil.example.com/metadata",
	}, testBridge()); err == nil {
		t.Fatal("plain-http metadata URL accepted")
	}
	if _, err := NewSAMLSP(ctx, SAMLSPConfig{
		EntityID: "e", AcsURL: "https://a", Key: key, Certificate: cert,
	}, testBridge()); err == nil {
		t.Fatal("missing IdP metadata accepted")
	}
}

func TestSAMLSPDeadIDPMetadataFailsClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	_, err := NewSAMLSP(context.Background(), SAMLSPConfig{
		EntityID:       "e",
		AcsURL:         "https://a",
		IDPMetadataURL: srv.URL + "/metadata",
	}, testBridge())
	if err == nil {
		t.Fatal("construction succeeded with dead IdP metadata endpoint")
	}
}

func TestSAMLSPIdPMetadataXMLFromLocalFile(t *testing.T) {
	// Metadata parsed from a trusted local source (no network fetch).
	meta := `<?xml version="1.0"?>
<EntityDescriptor xmlns="urn:oasis:names:tc:SAML:2.0:metadata" entityID="https://idp.example.com/metadata">
  <IDPSSODescriptor protocolSupportEnumeration="urn:oasis:names:tc:SAML:2.0:protocol">
    <KeyDescriptor use="signing">
      <KeyInfo xmlns="http://www.w3.org/2000/09/xmldsig#"><X509Data><X509Certificate></X509Certificate></X509Data></KeyInfo>
    </KeyDescriptor>
    <NameIDFormat>urn:oasis:names:tc:SAML:2.0:nameid-format:persistent</NameIDFormat>
    <SingleSignOnService Binding="urn:oasis:names:tc:SAML:2.0:bindings:HTTP-Redirect" Location="https://idp.example.com/sso"/>
  </IDPSSODescriptor>
</EntityDescriptor>`
	key, cert := samlTestCert(t, "sp")
	_, err := NewSAMLSP(context.Background(), SAMLSPConfig{
		EntityID:       "https://sp.example.com/metadata",
		AcsURL:         "https://sp.example.com/acs",
		IDPMetadataXML: []byte(meta),
		Key:            key,
		Certificate:    cert,
	}, testBridge())
	// Empty certificate content will fail signature verification later, but
	// construction itself must succeed (structure is valid).
	if err != nil {
		t.Fatalf("valid local metadata rejected: %v", err)
	}
}

// ── one-time-use store (replay protection core) ──

func TestSAMLIDStoreConsumeIsOneTime(t *testing.T) {
	s := newSAMLIDStore(time.Minute)
	s.track("req-1")
	if !s.consumeAll([]string{"req-1"}) {
		t.Fatal("first consume failed")
	}
	if s.consumeAll([]string{"req-1"}) {
		t.Fatal("second consume succeeded — replay possible")
	}
}

func TestSAMLIDStoreUnknownIDRejected(t *testing.T) {
	s := newSAMLIDStore(time.Minute)
	if s.consumeAll([]string{"never-tracked"}) {
		t.Fatal("unknown id consumed")
	}
}

func TestSAMLIDStoreTTLExpiry(t *testing.T) {
	s := newSAMLIDStore(10 * time.Millisecond)
	s.track("req-1")
	time.Sleep(30 * time.Millisecond)
	if s.consumeAll([]string{"req-1"}) {
		t.Fatal("expired id consumed")
	}
}

func TestSAMLIDStoreBounded(t *testing.T) {
	s := newSAMLIDStore(time.Minute)
	for i := 0; i < 250; i++ {
		s.track("req-" + itoa(i))
	}
	s.mu.Lock()
	n := len(s.ids)
	s.mu.Unlock()
	if n > 100 {
		t.Fatalf("store unbounded: %d", n)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// ── consume-path preconditions ──

func TestSAMLConsumeResponseRequiresFormField(t *testing.T) {
	key, cert := samlTestCert(t, "sp")
	sp, err := NewSAMLSP(context.Background(), SAMLSPConfig{
		EntityID:       "https://sp/metadata",
		AcsURL:         "https://sp/acs",
		IDPMetadataXML: minimalIDPMetadata(t),
		Key:            key,
		Certificate:    cert,
	}, testBridge())
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/acs", nil)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if _, err := sp.ConsumeResponse(context.Background(), r); err == nil || !strings.Contains(err.Error(), "missing SAMLResponse") {
		t.Fatalf("want missing-field error, got %v", err)
	}
	// Garbage base64 — the library wraps decode failures in an opaque
	// "Authentication failed" (deliberate: no oracle for malformed input),
	// so assert rejection rather than a specific message.
	r2 := httptest.NewRequest(http.MethodPost, "/acs", strings.NewReader("SAMLResponse=!!!not-base64!!!"))
	r2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if _, err := sp.ConsumeResponse(context.Background(), r2); err == nil {
		t.Fatal("garbage SAMLResponse accepted")
	}
}

// minimalIDPMetadata returns structurally valid IdP metadata with an empty
// certificate slot: enough for construction and request-side tests. Response
// signature validation requires a real IdP and is covered by the library's
// own extensive tests plus live-stand verification.
func minimalIDPMetadata(t *testing.T) []byte {
	t.Helper()
	return []byte(`<?xml version="1.0"?>
<EntityDescriptor xmlns="urn:oasis:names:tc:SAML:2.0:metadata" entityID="https://idp.example.com/metadata">
  <IDPSSODescriptor protocolSupportEnumeration="urn:oasis:names:tc:SAML:2.0:protocol">
    <KeyDescriptor use="signing">
      <KeyInfo xmlns="http://www.w3.org/2000/09/xmldsig#"><X509Data><X509Certificate></X509Certificate></X509Data></KeyInfo>
    </KeyDescriptor>
    <NameIDFormat>urn:oasis:names:tc:SAML:2.0:nameid-format:persistent</NameIDFormat>
    <SingleSignOnService Binding="urn:oasis:names:tc:SAML:2.0:bindings:HTTP-Redirect" Location="https://idp.example.com/sso"/>
  </IDPSSODescriptor>
</EntityDescriptor>`)
}

// ── SP metadata renders ──

func TestSAMLMetadataRenders(t *testing.T) {
	key, cert := samlTestCert(t, "sp")
	sp, err := NewSAMLSP(context.Background(), SAMLSPConfig{
		EntityID:       "https://sp.example.com/saml/metadata",
		AcsURL:         "https://sp.example.com/saml/acs",
		IDPMetadataXML: minimalIDPMetadata(t),
		Key:            key,
		Certificate:    cert,
	}, testBridge())
	if err != nil {
		t.Fatal(err)
	}
	md := sp.Metadata()
	if !strings.Contains(md, "EntityDescriptor") || !strings.Contains(md, "https://sp.example.com/saml/acs") {
		t.Fatalf("metadata missing expected content:\n%s", md)
	}
}
