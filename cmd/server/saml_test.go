package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/xml"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/beevik/etree"
	"github.com/crewjam/saml"
	"github.com/crewjam/saml/samlsp"
	"github.com/gofiber/fiber/v2"
	dsig "github.com/russellhaering/goxmldsig"
	vectorHttp "github.com/stek0v/levara/internal/http"
	accesspkg "github.com/stek0v/levara/pkg/access"
	vectorAuth "github.com/stek0v/levara/pkg/auth"
)

const samlTestACS = "https://sp.example.test/saml/acs"

type samlBrowserFlow struct {
	request *saml.IdpAuthnRequest
	cookies []*http.Cookie
	body    string
}

type samlBrowserFixture struct {
	app   *fiber.App
	store *accesspkg.SCIMStore
	idp   *saml.IdentityProvider
	spKey *rsa.PrivateKey
	meta  *saml.EntityDescriptor
}

func newSAMLBrowserFixture(t *testing.T) samlBrowserFixture {
	return newSAMLBrowserFixtureDialect(t, "sqlite")
}

func newSAMLBrowserFixtureDialect(t *testing.T, dialect string) samlBrowserFixture {
	t.Helper()
	certificate := func(name string) (*rsa.PrivateKey, *x509.Certificate) {
		t.Helper()
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name},
			NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature}
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
	idpKey, idpCert := certificate("idp")
	idp := &saml.IdentityProvider{Key: idpKey, Certificate: idpCert,
		MetadataURL: url.URL{Scheme: "https", Host: "idp.example.test", Path: "/metadata"},
		SSOURL:      url.URL{Scheme: "https", Host: "idp.example.test", Path: "/sso"}, SignatureMethod: dsig.RSASHA256SignatureMethod}
	idpXML, err := xml.Marshal(idp.Metadata())
	if err != nil {
		t.Fatal(err)
	}
	store := newBrowserAuthStore(t, dialect)
	for _, user := range []string{"alice", "bob"} {
		if _, _, err := store.ProvisionCreate(context.Background(), accesspkg.SCIMUser{Issuer: "directory", ExternalID: user, Email: user + "@corp.test", Active: true}); err != nil {
			t.Fatal(err)
		}
	}
	spKey, spCert := certificate("sp")
	sp, err := accesspkg.NewSAMLSP(context.Background(), accesspkg.SAMLSPConfig{
		EntityID: "https://sp.example.test/saml/metadata", AcsURL: samlTestACS,
		Key: spKey, Certificate: spCert, IDPMetadataXML: idpXML,
	}, accesspkg.SQLIdentityBridge{DB: store.DB, Q: store.Q, TrustedIssuers: map[string]string{idp.MetadataURL.String(): "directory"}})
	if err != nil {
		t.Fatal(err)
	}
	meta, err := samlsp.ParseMetadata([]byte(sp.Metadata()))
	if err != nil {
		t.Fatal(err)
	}
	// Exercise signed plaintext assertions and signed responses (encryption is
	// orthogonal to request binding and would hide tampering in this test).
	meta.SPSSODescriptors[0].KeyDescriptors = nil
	app := fiber.New()
	samlRoutes(app, sp, vectorHttp.AuthConfig{DB: store.DB, RequireAuth: true, JWTSecret: "local-test-session-secret", CookieSecure: true}, "/")
	return samlBrowserFixture{app: app, store: store, idp: idp, spKey: spKey, meta: meta}
}

func TestSAMLPreRevocationResponseCannotRecreateSession(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			f := newSAMLBrowserFixtureDialect(t, dialect)
			flow := f.start(t, "alice")
			flow.request.Assertion.IssueInstant = time.Now().Add(-time.Minute)
			flow.body = signedSAMLBody(t, flow.request)
			if err := f.store.ProvisionDeactivate(context.Background(), "directory", "alice"); err != nil {
				t.Fatal(err)
			}
			if _, _, err := f.store.ProvisionCreate(context.Background(), accesspkg.SCIMUser{Issuer: "directory", ExternalID: "alice", Email: "alice@corp.test", Active: true}); err != nil {
				t.Fatal(err)
			}
			f.consume(t, flow.body, flow.cookies, http.StatusUnauthorized)
			var sessions int
			if err := f.store.DB.QueryRow("SELECT COUNT(*) FROM auth_sessions").Scan(&sessions); err != nil || sessions != 0 {
				t.Fatalf("revoked assertion created sessions=%d err=%v", sessions, err)
			}
			// A newly issued assertion strictly after the revocation watermark
			// may create a session at the current epoch (within normal clock skew).
			fresh := f.start(t, "alice")
			fresh.request.Assertion.IssueInstant = time.Now().Add(time.Second)
			fresh.body = signedSAMLBody(t, fresh.request)
			resp := f.consume(t, fresh.body, fresh.cookies, http.StatusSeeOther)
			for _, cookie := range resp.Cookies() {
				if cookie.Name != "auth_token" {
					continue
				}
				claims, valid := vectorAuth.VerifyJWT(cookie.Value, "local-test-session-secret")
				if !valid || claims.CredentialEpoch != 1 || claims.SessionID == "" {
					t.Fatalf("new session claims=%+v valid=%v", claims, valid)
				}
				return
			}
			t.Fatal("fresh assertion did not create a session cookie")
		})
	}
}

func TestSAMLFutureIssueTimeRejected(t *testing.T) {
	f := newSAMLBrowserFixture(t)
	flow := f.start(t, "alice")
	flow.request.Assertion.IssueInstant = time.Now().Add(time.Hour)
	f.consume(t, signedSAMLBody(t, flow.request), flow.cookies, http.StatusUnauthorized)
}

func (f samlBrowserFixture) start(t *testing.T, user string) samlBrowserFlow {
	t.Helper()
	resp, err := f.app.Test(httptest.NewRequest("GET", "https://sp.example.test/saml/login?RelayState=untrusted-input", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 302 {
		t.Fatalf("login status=%d", resp.StatusCode)
	}
	req, err := saml.NewIdpAuthnRequest(f.idp, httptest.NewRequest("GET", resp.Header.Get("Location"), nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := xml.Unmarshal(req.RequestBuffer, &req.Request); err != nil {
		t.Fatal(err)
	}
	req.ServiceProviderMetadata = f.meta
	req.SPSSODescriptor = &f.meta.SPSSODescriptors[0]
	req.ACSEndpoint = &req.SPSSODescriptor.AssertionConsumerServices[0]
	if err := (saml.DefaultAssertionMaker{}).MakeAssertion(req, &saml.Session{NameID: user, UserEmail: user + "@corp.test", CreateTime: time.Now()}); err != nil {
		t.Fatal(err)
	}
	return samlBrowserFlow{request: req, cookies: resp.Cookies(), body: signedSAMLBody(t, req)}
}

func signedSAMLBody(t *testing.T, req *saml.IdpAuthnRequest) string {
	t.Helper()
	// Re-sign the current assertion after a test changes a claim; MakeResponse
	// otherwise reuses a previously signed, cached AssertionEl.
	req.AssertionEl = nil
	req.Assertion.Signature = nil
	if err := req.MakeResponse(); err != nil {
		t.Fatal(err)
	}
	doc := etree.NewDocument()
	doc.SetRoot(req.ResponseEl)
	xmlBody, err := doc.WriteToBytes()
	if err != nil {
		t.Fatal(err)
	}
	return url.Values{"SAMLResponse": {base64.StdEncoding.EncodeToString(xmlBody)}, "RelayState": {req.RelayState}}.Encode()
}

func (f samlBrowserFixture) consume(t *testing.T, body string, cookies []*http.Cookie, want int) *http.Response {
	t.Helper()
	req := httptest.NewRequest("POST", samlTestACS, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	resp, err := f.app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	if resp.StatusCode != want {
		t.Errorf("ACS status=%d want=%d", resp.StatusCode, want)
	}
	return resp
}

func TestSAMLConcurrentBrowserFlows(t *testing.T) {
	for _, order := range [][2]int{{0, 1}, {1, 0}} {
		t.Run(string(rune('0'+order[0])), func(t *testing.T) {
			f := newSAMLBrowserFixture(t)
			flows := []samlBrowserFlow{f.start(t, "alice"), f.start(t, "bob")}
			// Both correlation cookies can coexist in one browser (parallel tabs).
			browserCookies := append(append([]*http.Cookie{}, flows[0].cookies...), flows[1].cookies...)
			for _, i := range order {
				flow := flows[i]
				if flow.request.RelayState == "" || flow.request.RelayState == "untrusted-input" || len(flow.cookies) != 1 {
					t.Errorf("login did not generate browser-bound state: relay=%q cookies=%d", flow.request.RelayState, len(flow.cookies))
				}
				for _, cookie := range flow.cookies {
					if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteNoneMode || cookie.Path != "/saml/acs" || cookie.Domain != "" {
						t.Errorf("unsafe correlation cookie: %s", cookie)
					}
				}
				response := f.consume(t, flow.body, browserCookies, 303)
				if response.Header.Get("Location") != "/" {
					t.Errorf("untrusted redirect %q", response.Header.Get("Location"))
				}
				var session, cleared *http.Cookie
				for _, cookie := range response.Cookies() {
					if cookie.Name == "auth_token" {
						session = cookie
					} else if cookie.MaxAge < 0 {
						cleared = cookie
					}
				}
				if session == nil || !session.HttpOnly || !session.Secure || session.SameSite != http.SameSiteLaxMode || session.Path != "/" {
					t.Fatalf("unsafe session cookie: %v", session)
				}
				claims, valid := vectorAuth.VerifyJWT(session.Value, "local-test-session-secret")
				if !valid || claims.SessionID == "" || claims.Sub != accesspkg.SCIMUserID("directory", []string{"alice", "bob"}[i]) {
					t.Fatalf("wrong session principal: %+v", claims)
				}
				if cleared == nil || !cleared.Secure || !cleared.HttpOnly || cleared.Path != "/saml/acs" {
					t.Errorf("request cookie not cleared: %v", cleared)
				}

				f.consume(t, flow.body, flow.cookies, 401)
			}
		})
	}
}

func TestSAMLBrowserStateAndSignatureRejection(t *testing.T) {
	f := newSAMLBrowserFixture(t)
	first, second := f.start(t, "alice"), f.start(t, "bob")
	// The assertion from browser B must not log in a browser without B's cookie.
	f.consume(t, second.body, nil, 401)
	f.consume(t, second.body, first.cookies, 401)
	// Even valid browser state must name the exact request answered by the IdP.
	mismatched, _ := url.ParseQuery(second.body)
	mismatched.Set("RelayState", first.request.RelayState)
	f.consume(t, mismatched.Encode(), first.cookies, 401)
	// Failed XML signature verification must not burn a legitimate login.
	tampered, _ := url.ParseQuery(second.body)
	xmlBody, _ := base64.StdEncoding.DecodeString(tampered.Get("SAMLResponse"))
	tampered.Set("SAMLResponse", base64.StdEncoding.EncodeToString([]byte(strings.ReplaceAll(string(xmlBody), "bob", "mallory"))))
	f.consume(t, tampered.Encode(), second.cookies, 401)
	f.consume(t, second.body, second.cookies, 303)
	f.consume(t, first.body, first.cookies, 303)
}

func TestSAMLExpiredTamperedAndUnknownBrowserState(t *testing.T) {
	f := newSAMLBrowserFixture(t)
	acs, _ := url.Parse(samlTestACS)
	codec := samlsp.DefaultTrackedRequestCodec(samlsp.Options{URL: *acs, Key: f.spKey})
	for _, invalid := range []string{"expired", "tampered", "unknown"} {
		t.Run(invalid, func(t *testing.T) {
			flow := f.start(t, "alice")
			if len(flow.cookies) != 1 {
				t.Fatal("missing browser cookie")
			}
			cookie := *flow.cookies[0]
			body := flow.body
			signer := codec
			tracked := samlsp.TrackedRequest{Index: flow.request.RelayState, SAMLRequestID: flow.request.Request.ID}
			switch invalid {
			case "expired":
				signer.MaxAge = -time.Minute
			case "unknown":
				tracked.SAMLRequestID = "_unknown-request"
				flow.request.Request.ID = tracked.SAMLRequestID
				flow.request.Assertion.Subject.SubjectConfirmations[0].SubjectConfirmationData.InResponseTo = tracked.SAMLRequestID
				flow.request.Assertion.Signature = nil
				flow.request.AssertionEl = nil
				body = signedSAMLBody(t, flow.request)
			}
			var err error
			cookie.Value, err = signer.Encode(tracked)
			if err != nil {
				t.Fatal(err)
			}
			if invalid == "tampered" {
				parts := strings.Split(cookie.Value, ".")
				parts[1] = base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"forged"}`))
				cookie.Value = strings.Join(parts, ".")
			}
			f.consume(t, body, []*http.Cookie{&cookie}, 401)
			f.consume(t, flow.body, flow.cookies, 303)
		})
	}
}

func TestSAMLConcurrentReplayAcceptedOnce(t *testing.T) {
	f := newSAMLBrowserFixture(t)
	flow := f.start(t, "alice")
	statuses := make(chan int, 8)
	var wg sync.WaitGroup
	for i := 0; i < cap(statuses); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("POST", samlTestACS, strings.NewReader(flow.body))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			for _, cookie := range flow.cookies {
				req.AddCookie(cookie)
			}
			resp, err := f.app.Test(req)
			if err != nil {
				t.Error(err)
				return
			}
			_ = resp.Body.Close()
			statuses <- resp.StatusCode
		}()
	}
	wg.Wait()
	close(statuses)
	accepted, rejected := 0, 0
	for status := range statuses {
		switch status {
		case 303:
			accepted++
		case 401:
			rejected++
		default:
			t.Errorf("unexpected ACS status: %d", status)
		}
	}
	if accepted != 1 || rejected != 7 {
		t.Fatalf("concurrent replay: accepted=%d rejected=%d", accepted, rejected)
	}
}
