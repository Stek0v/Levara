package http

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	accesspkg "github.com/stek0v/levara/pkg/access"
	vectorAuth "github.com/stek0v/levara/pkg/auth"
)

// ── shared helpers for OIDC middleware e2e ──

type mwEnv struct {
	tk *oidcTestKeys
	v  *vectorAuth.OIDCVerifier
}

func b64u(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func authedGet(path, bearer string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	return r
}

type oidcTestKeys struct {
	rsaKid string
	rsa    *rsa.PrivateKey
	server *httptest.Server
}

func newOIDCTestKeys(t *testing.T) *oidcTestKeys {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tk := &oidcTestKeys{rsaKid: "k1", rsa: key}
	mux := http.NewServeMux()
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{{
			"kty": "RSA", "kid": tk.rsaKid, "alg": "RS256", "use": "sig",
			"n": b64u(tk.rsa.N.Bytes()),
			"e": b64u(big.NewInt(int64(tk.rsa.E)).Bytes()),
		}}})
	})
	tk.server = httptest.NewServer(mux)
	t.Cleanup(tk.server.Close)
	return tk
}

func (tk *oidcTestKeys) sign(t *testing.T, claims map[string]any) string {
	t.Helper()
	header, _ := json.Marshal(map[string]any{"alg": "RS256", "kid": tk.rsaKid, "typ": "JWT"})
	payload, _ := json.Marshal(claims)
	si := b64u(header) + "." + b64u(payload)
	digest := sha256.Sum256([]byte(si))
	sig, err := rsa.SignPKCS1v15(rand.Reader, tk.rsa, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return si + "." + b64u(sig)
}

func newMW(t *testing.T, tk *oidcTestKeys, requireAuth bool) (*fiber.App, *vectorAuth.OIDCVerifier) {
	t.Helper()
	verifier, err := vectorAuth.NewOIDCVerifier(vectorAuth.OIDCVerifierConfig{
		JWKSURL:   tk.server.URL + "/jwks",
		Issuers:   []string{"https://idp.example.com"},
		Audiences: []string{"levara"},
	})
	if err != nil {
		t.Fatal(err)
	}
	oidcAuth := ExternalBearerAuth(&testExternalAuth{verifier: verifier})
	app := fiber.New()
	app.Use(JWTMiddlewareWithOIDC("test-secret-not-used-by-oidc", requireAuth, oidcAuth))
	app.Get("/whoami", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"user_id": c.Locals("user_id")})
	})
	return app, verifier
}

func oidcClaims(now time.Time) map[string]any {
	return map[string]any{
		"iss": "https://idp.example.com", "sub": "ext-user-1", "aud": "levara",
		"exp": now.Add(5 * time.Minute).Unix(), "iat": now.Unix(),
		"email": "alice@example.com", "groups": []string{"platform"},
	}
}

// ── e2e: external OIDC token flows through the middleware ──

func TestJWTMiddlewareWithOIDCValidToken(t *testing.T) {
	tk := newOIDCTestKeys(t)
	app, _ := newMW(t, tk, true)
	token := tk.sign(t, oidcClaims(time.Now()))
	resp, err := app.Test(authedGet("/whoami", token))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("valid OIDC token rejected: %d", resp.StatusCode)
	}
}

func TestJWTMiddlewareWithOIDCTamperedToken(t *testing.T) {
	tk := newOIDCTestKeys(t)
	app, _ := newMW(t, tk, true)
	token := tk.sign(t, oidcClaims(time.Now()))
	segments := strings.Split(token, ".")
	if len(segments) != 3 {
		t.Fatalf("expected 3 JWS segments, got %d", len(segments))
	}
	// Flip the first character of the payload segment: claims change, the
	// signature no longer matches, and the mutation is deterministic (the
	// previous scan-for-A approach could land on a signature char whose
	// base64 decode happened to still verify).
	payload := []byte(segments[1])
	if payload[0] == 'A' {
		payload[0] = 'B'
	} else {
		payload[0] = 'A'
	}
	segments[1] = string(payload)
	resp, err := app.Test(authedGet("/whoami", strings.Join(segments, ".")))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 {
		t.Fatalf("tampered OIDC token accepted: %d", resp.StatusCode)
	}
}

func TestJWTMiddlewareWithOIDCExpiredToken(t *testing.T) {
	tk := newOIDCTestKeys(t)
	app, _ := newMW(t, tk, true)
	claims := oidcClaims(time.Now())
	claims["exp"] = time.Now().Add(-time.Hour).Unix()
	resp, err := app.Test(authedGet("/whoami", tk.sign(t, claims)))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 {
		t.Fatalf("expired OIDC token accepted: %d", resp.StatusCode)
	}
}

func TestJWTMiddlewareWithOIDCWrongIssuer(t *testing.T) {
	tk := newOIDCTestKeys(t)
	app, _ := newMW(t, tk, true)
	claims := oidcClaims(time.Now())
	claims["iss"] = "https://evil.example.com"
	resp, err := app.Test(authedGet("/whoami", tk.sign(t, claims)))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 {
		t.Fatalf("wrong-issuer OIDC token accepted: %d", resp.StatusCode)
	}
}

// ── coexistence: Levara JWT and API keys keep working unchanged ──

func TestJWTMiddlewareWithOIDCLevaraJWTStillWorks(t *testing.T) {
	tk := newOIDCTestKeys(t)
	app, _ := newMW(t, tk, true)
	jwt := createJWT("local-user-1", "local@levara.dev", "test-secret-not-used-by-oidc")
	resp, err := app.Test(authedGet("/whoami", jwt))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("Levara JWT broken by OIDC path: %d", resp.StatusCode)
	}
}

func TestJWTMiddlewareWithOIDCNilEqualsBase(t *testing.T) {
	tk := newOIDCTestKeys(t)
	verifier, err := vectorAuth.NewOIDCVerifier(vectorAuth.OIDCVerifierConfig{
		JWKSURL: tk.server.URL + "/jwks", Issuers: []string{"i"}, Audiences: []string{"a"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = verifier
	app := fiber.New()
	app.Use(JWTMiddlewareWithOIDC("s", true, nil))
	app.Get("/x", func(c *fiber.Ctx) error { return c.SendStatus(200) })
	resp, err := app.Test(authedGet("/x", ""))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 {
		t.Fatalf("nil oidc must behave as base require-auth: %d", resp.StatusCode)
	}
}

func TestJWTMiddlewareWithOIDCSyntheticUserIDStable(t *testing.T) {
	a := accesspkg.SyntheticUserID("https://idp.example.com", "user-1")
	b := accesspkg.SyntheticUserID("https://idp.example.com", "user-1")
	c := accesspkg.SyntheticUserID("https://idp.example.com", "user-2")
	if a != b {
		t.Fatal("synthetic id not stable")
	}
	if a == c {
		t.Fatal("different subjects collide")
	}
	if !isExternalOIDCID(a) {
		t.Fatalf("unexpected id shape %q", a)
	}
}

func isExternalOIDCID(id string) bool {
	return len(id) == len("oidc-")+32 && id[:5] == "oidc-"
}

// testExternalAuth adapts the raw verifier to the ExternalBearerAuth seam in
// tests the same way cmd/server's composition root does in production.
type testExternalAuth struct {
	verifier *vectorAuth.OIDCVerifier
}

func (t *testExternalAuth) Authenticate(_ context.Context, token string) (ExternalPrincipal, error) {
	claims, err := t.verifier.Verify(token)
	if err != nil {
		return ExternalPrincipal{}, err
	}
	return ExternalPrincipal{
		UserID: accesspkg.SyntheticUserID(claims.Issuer, claims.Subject),
		Email:  claims.Email,
	}, nil
}
