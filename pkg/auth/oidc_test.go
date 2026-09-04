package auth

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ── test key material & JWKS server ──

type testKeys struct {
	rsaKid   string
	rsa      *rsa.PrivateKey
	ecKid    string
	ec       *ecdsa.PrivateKey
	server   *httptest.Server
	jwksReqs int
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func newTestKeys(t *testing.T) *testKeys {
	t.Helper()
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tk := &testKeys{rsaKid: "rsa-1", ecKid: "ec-1", rsa: rsaKey, ec: ecKey}
	mux := http.NewServeMux()
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		tk.jwksReqs++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]any{
				{
					"kty": "RSA", "kid": tk.rsaKid, "alg": "RS256", "use": "sig",
					"n": b64(tk.rsa.PublicKey.N.Bytes()),
					"e": b64(big.NewInt(int64(tk.rsa.PublicKey.E)).Bytes()),
				},
				{
					"kty": "EC", "kid": tk.ecKid, "alg": "ES256", "use": "sig", "crv": "P-256",
					"x": b64(tk.ec.PublicKey.X.Bytes()),
					"y": b64(tk.ec.PublicKey.Y.Bytes()),
				},
			},
		})
	})
	tk.server = httptest.NewServer(mux)
	t.Cleanup(tk.server.Close)
	return tk
}

func (tk *testKeys) signRS256(t *testing.T, kid string, header map[string]any, claims map[string]any) string {
	t.Helper()
	return tk.sign(t, "RS256", kid, header, claims, func(digest []byte) []byte {
		sig, err := rsa.SignPKCS1v15(rand.Reader, tk.rsa, crypto.SHA256, digest)
		if err != nil {
			t.Fatal(err)
		}
		return sig
	})
}

func (tk *testKeys) signES256(t *testing.T, kid string, header map[string]any, claims map[string]any) string {
	t.Helper()
	return tk.sign(t, "ES256", kid, header, claims, func(digest []byte) []byte {
		r, s, err := ecdsa.Sign(rand.Reader, tk.ec, digest)
		if err != nil {
			t.Fatal(err)
		}
		sig := make([]byte, 64)
		r.FillBytes(sig[:32])
		s.FillBytes(sig[32:])
		return sig
	})
}

func (tk *testKeys) sign(t *testing.T, alg, kid string, header map[string]any, claims map[string]any, signFn func([]byte) []byte) string {
	t.Helper()
	if header == nil {
		header = map[string]any{}
	}
	header["alg"] = alg
	if _, ok := header["kid"]; !ok {
		header["kid"] = kid
	}
	header["typ"] = "JWT"
	h, _ := json.Marshal(header)
	p, _ := json.Marshal(claims)
	signingInput := b64(h) + "." + b64(p)
	digest := sha256.Sum256([]byte(signingInput))
	return signingInput + "." + b64(signFn(digest[:]))
}

func baseClaims(now time.Time) map[string]any {
	return map[string]any{
		"iss": "https://idp.example.com",
		"sub": "user-123",
		"aud": "levara",
		"exp": now.Add(10 * time.Minute).Unix(),
		"iat": now.Unix(),
	}
}

func newVerifier(t *testing.T, tk *testKeys) *OIDCVerifier {
	t.Helper()
	v, err := NewOIDCVerifier(OIDCVerifierConfig{
		JWKSURL:   tk.server.URL + "/jwks",
		Issuers:   []string{"https://idp.example.com"},
		Audiences: []string{"levara"},
	})
	if err != nil {
		t.Fatalf("NewOIDCVerifier: %v", err)
	}
	return v
}

// ── happy paths ──

func TestOIDCVerifyRS256(t *testing.T) {
	tk := newTestKeys(t)
	v := newVerifier(t, tk)
	token := tk.signRS256(t, tk.rsaKid, nil, baseClaims(time.Now()))
	claims, err := v.Verify(token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.Subject != "user-123" || claims.Issuer != "https://idp.example.com" {
		t.Fatalf("unexpected claims: %+v", claims)
	}
}

func TestOIDCVerifyES256(t *testing.T) {
	tk := newTestKeys(t)
	v := newVerifier(t, tk)
	token := tk.signES256(t, tk.ecKid, nil, baseClaims(time.Now()))
	claims, err := v.Verify(token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.Subject != "user-123" {
		t.Fatalf("unexpected subject %q", claims.Subject)
	}
}

func TestOIDCAudienceAsString(t *testing.T) {
	tk := newTestKeys(t)
	v := newVerifier(t, tk)
	token := tk.signRS256(t, tk.rsaKid, nil, baseClaims(time.Now()))
	if _, err := v.Verify(token); err != nil {
		t.Fatalf("single-string aud: %v", err)
	}
}

// ── signature corner cases ──

func TestOIDCRejectsTamperedPayload(t *testing.T) {
	tk := newTestKeys(t)
	v := newVerifier(t, tk)
	token := tk.signRS256(t, tk.rsaKid, nil, baseClaims(time.Now()))
	parts := strings.Split(token, ".")
	// Replace payload with a different subject, keep signature.
	evil, _ := json.Marshal(map[string]any{"iss": "https://idp.example.com", "sub": "admin", "aud": "levara", "exp": time.Now().Add(time.Hour).Unix()})
	evilToken := parts[0] + "." + b64(evil) + "." + parts[2]
	if _, err := v.Verify(evilToken); err == nil {
		t.Fatal("tampered payload accepted")
	}
}

func TestOIDCRejectsAlgNone(t *testing.T) {
	tk := newTestKeys(t)
	v := newVerifier(t, tk)
	h := b64([]byte(`{"alg":"none","kid":"rsa-1","typ":"JWT"}`))
	p, _ := json.Marshal(baseClaims(time.Now()))
	token := h + "." + b64(p) + "."
	if _, err := v.Verify(token); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("alg none: want rejection, got %v", err)
	}
}

func TestOIDCRejectsAlgConfusionHS256WithRSAKeyMaterial(t *testing.T) {
	tk := newTestKeys(t)
	v := newVerifier(t, tk)
	// Attacker signs with HS256 using the public RSA key material — classic
	// confusion attack. Must be rejected on alg pinning alone.
	header := b64([]byte(`{"alg":"HS256","kid":"rsa-1","typ":"JWT"}`))
	p, _ := json.Marshal(baseClaims(time.Now()))
	mac := sha256.Sum256([]byte(header + "." + b64(p)))
	token := header + "." + b64(p) + "." + b64(mac[:])
	if _, err := v.Verify(token); err == nil {
		t.Fatal("HS256 confusion accepted")
	}
}

func TestOIDCRejectsWrongKeySignature(t *testing.T) {
	tk := newTestKeys(t)
	v := newVerifier(t, tk)
	// Token claims kid=rsa-1 but header says ec alg — key/alg pinning mismatch.
	token := tk.signES256(t, tk.rsaKid, nil, baseClaims(time.Now()))
	if _, err := v.Verify(token); err == nil || !strings.Contains(err.Error(), "pinned") {
		t.Fatalf("key/alg mismatch: want pinned error, got %v", err)
	}
}

func TestOIDCKeyRotationUnknownKidRefreshes(t *testing.T) {
	tk := newTestKeys(t)
	v := newVerifier(t, tk)
	// Sign with a kid that is not in the JWKS yet.
	token := tk.signRS256(t, "rsa-2", nil, baseClaims(time.Now()))
	if _, err := v.Verify(token); err == nil {
		t.Fatal("unknown kid accepted without rotation")
	}
	// Add the new key to the JWKS and retry after the 1s refresh throttle:
	// verifier must refresh and accept. (Sub-second rotation is out of scope
	// by design — the throttle prevents thundering-herd refreshes.)
	tk.rsaKid = "rsa-2"
	time.Sleep(1100 * time.Millisecond)
	token2 := tk.signRS256(t, "rsa-2", nil, baseClaims(time.Now()))
	if _, err := v.Verify(token2); err != nil {
		t.Fatalf("rotation refresh: %v", err)
	}
}

// ── time-based corner cases ──

func TestOIDCTimeBoundaries(t *testing.T) {
	tk := newTestKeys(t)
	v := newVerifier(t, tk)
	now := time.Now()

	cases := []struct {
		name    string
		exp     time.Time
		nbf     time.Time
		wantErr string
	}{
		{"expired beyond skew", now.Add(-6 * time.Minute), now.Add(-time.Hour), "expired"},
		{"expired within skew accepted", now.Add(-4 * time.Minute), now.Add(-time.Hour), ""},
		{"nbf beyond skew", now.Add(10 * time.Minute), now.Add(6 * time.Minute), "not yet valid"},
		{"nbf within skew accepted", now.Add(10 * time.Minute), now.Add(4 * time.Minute), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			claims := baseClaims(now)
			claims["exp"] = tc.exp.Unix()
			claims["nbf"] = tc.nbf.Unix()
			token := tk.signRS256(t, tk.rsaKid, nil, claims)
			_, err := v.Verify(token)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("want accept, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want %q error, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestOIDCRejectsMissingExp(t *testing.T) {
	tk := newTestKeys(t)
	v := newVerifier(t, tk)
	claims := baseClaims(time.Now())
	delete(claims, "exp")
	token := tk.signRS256(t, tk.rsaKid, nil, claims)
	if _, err := v.Verify(token); err == nil || !strings.Contains(err.Error(), "exp") {
		t.Fatalf("missing exp: got %v", err)
	}
}

// ── iss / aud allowlists ──

func TestOIDCRejectsWrongIssuer(t *testing.T) {
	tk := newTestKeys(t)
	v := newVerifier(t, tk)
	claims := baseClaims(time.Now())
	claims["iss"] = "https://evil.example.com"
	token := tk.signRS256(t, tk.rsaKid, nil, claims)
	if _, err := v.Verify(token); err == nil || !strings.Contains(err.Error(), "issuer") {
		t.Fatalf("wrong issuer: got %v", err)
	}
}

func TestOIDCRejectsWrongAudience(t *testing.T) {
	tk := newTestKeys(t)
	v := newVerifier(t, tk)
	claims := baseClaims(time.Now())
	claims["aud"] = "other-app"
	token := tk.signRS256(t, tk.rsaKid, nil, claims)
	if _, err := v.Verify(token); err == nil || !strings.Contains(err.Error(), "audience") {
		t.Fatalf("wrong audience: got %v", err)
	}
}

func TestOIDCMultiAudienceNeverMatchesSingleAllowlist(t *testing.T) {
	tk := newTestKeys(t)
	v := newVerifier(t, tk)
	// aud contains "levara" plus another client — with plain exact matching
	// this must NOT pass unless the deployment allowlists the joined form.
	claims := baseClaims(time.Now())
	claims["aud"] = []string{"levara", "partner-app"}
	token := tk.signRS256(t, tk.rsaKid, nil, claims)
	if _, err := v.Verify(token); err == nil {
		t.Fatal("multi-aud token accepted without explicit azp handling")
	}
}

// ── config / fail-closed ──

func TestOIDCConfigValidation(t *testing.T) {
	if _, err := NewOIDCVerifier(OIDCVerifierConfig{JWKSURL: ""}); err == nil {
		t.Fatal("empty JWKSURL accepted")
	}
	if _, err := NewOIDCVerifier(OIDCVerifierConfig{JWKSURL: "http://evil.example.com/jwks", Issuers: []string{"x"}, Audiences: []string{"y"}}); err == nil {
		t.Fatal("plain-http JWKS accepted")
	}
	if _, err := NewOIDCVerifier(OIDCVerifierConfig{JWKSURL: "https://x/jwks"}); err == nil {
		t.Fatal("missing issuers accepted")
	}
	if _, err := NewOIDCVerifier(OIDCVerifierConfig{JWKSURL: "https://x/jwks", Issuers: []string{"https://*.evil.com"}, Audiences: []string{"y"}}); err == nil {
		t.Fatal("wildcard issuer accepted")
	}
}

func TestOIDCFailClosedOnDeadJWKS(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	if _, err := NewOIDCVerifier(OIDCVerifierConfig{
		JWKSURL: srv.URL + "/jwks", Issuers: []string{"iss"}, Audiences: []string{"aud"},
	}); err == nil {
		t.Fatal("construction succeeded with dead JWKS endpoint")
	}
}

func TestOIDCJWKSWithNoUsableKeys(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{}})
	}))
	defer srv.Close()
	if _, err := NewOIDCVerifier(OIDCVerifierConfig{
		JWKSURL: srv.URL, Issuers: []string{"iss"}, Audiences: []string{"aud"},
	}); err == nil {
		t.Fatal("empty JWKS accepted")
	}
}

func TestOIDCJWKSKeyCountCap(t *testing.T) {
	// 150 keys in JWKS: verifier must cap at maxKeys and still verify.
	var keys []map[string]any
	for i := 0; i < 150; i++ {
		keys = append(keys, map[string]any{"kty": "EC", "kid": fmt.Sprintf("k%d", i), "crv": "P-256", "x": "AA", "y": "AA"})
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": keys})
	}))
	defer srv.Close()
	v, err := NewOIDCVerifier(OIDCVerifierConfig{JWKSURL: srv.URL, Issuers: []string{"i"}, Audiences: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	if len(v.keys) > 100 {
		t.Fatalf("key cap exceeded: %d", len(v.keys))
	}
}

func TestOIDCTokenGarbage(t *testing.T) {
	tk := newTestKeys(t)
	v := newVerifier(t, tk)
	for _, tok := range []string{"", "abc", "a.b", "a.b.c.d", "!!!.!!!.!!!"} {
		if _, err := v.Verify(tok); err == nil {
			t.Fatalf("garbage token %q accepted", tok)
		}
	}
}

func TestOIDCConcurrentVerifyWithRotation(t *testing.T) {
	tk := newTestKeys(t)
	v := newVerifier(t, tk)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			token := tk.signRS256(t, fmt.Sprintf("kid-%d", i%3), nil, baseClaims(time.Now()))
			_, _ = v.Verify(token) // may fail on unknown kid — must not race/panic
		}
	}()
	for i := 0; i < 50; i++ {
		token := tk.signRS256(t, tk.rsaKid, nil, baseClaims(time.Now()))
		if _, err := v.Verify(token); err != nil {
			t.Fatalf("concurrent verify: %v", err)
		}
	}
	<-done
}
