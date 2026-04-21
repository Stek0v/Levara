package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// signTestJWT mirrors internal/http.createJWT so we can verify against a
// known-good token without importing the HTTP package (which would create
// a cycle). Test-only; production callers always go through the HTTP
// /auth/login endpoint.
func signTestJWT(t *testing.T, sub, email, secret string, ttl time.Duration) string {
	t.Helper()
	header := map[string]string{"alg": "HS256", "typ": "JWT"}
	payload := Payload{
		Sub:   sub,
		Email: email,
		Exp:   time.Now().Add(ttl).Unix(),
		Iat:   time.Now().Unix(),
	}
	hJSON, _ := json.Marshal(header)
	pJSON, _ := json.Marshal(payload)
	sigInput := base64.RawURLEncoding.EncodeToString(hJSON) + "." + base64.RawURLEncoding.EncodeToString(pJSON)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(sigInput))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return sigInput + "." + sig
}

func TestVerifyJWT_ValidToken(t *testing.T) {
	secret := "my-shared-secret"
	tok := signTestJWT(t, "alice", "alice@test.local", secret, time.Hour)

	p, ok := VerifyJWT(tok, secret)
	if !ok {
		t.Fatal("expected valid token")
	}
	if p.Sub != "alice" {
		t.Errorf("Sub = %q, want alice", p.Sub)
	}
	if p.Email != "alice@test.local" {
		t.Errorf("Email = %q, want alice@test.local", p.Email)
	}
}

func TestVerifyJWT_WrongSecret(t *testing.T) {
	tok := signTestJWT(t, "alice", "", "right-secret", time.Hour)
	if _, ok := VerifyJWT(tok, "wrong-secret"); ok {
		t.Fatal("expected wrong-secret token to be rejected")
	}
}

func TestVerifyJWT_Expired(t *testing.T) {
	// Negative TTL → already expired.
	tok := signTestJWT(t, "alice", "", "s3cret", -time.Hour)
	if _, ok := VerifyJWT(tok, "s3cret"); ok {
		t.Fatal("expected expired token to be rejected")
	}
}

func TestVerifyJWT_Malformed(t *testing.T) {
	cases := []struct {
		name  string
		token string
	}{
		{"empty", ""},
		{"single dot", "abc.def"},
		{"random bytes", "definitely-not-jwt-format"},
		{"binary garbage in payload", "abc.!!!.def"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := VerifyJWT(tc.token, "any"); ok {
				t.Errorf("expected %q to be rejected", tc.token)
			}
		})
	}
}

func TestVerifyJWT_TamperedPayload(t *testing.T) {
	// Sign a token, then swap the payload section (keeping the original
	// signature). VerifyJWT must reject — this is the core security
	// property of HMAC-signed tokens.
	tok := signTestJWT(t, "alice", "", "s3cret", time.Hour)
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("unexpected token shape: %s", tok)
	}
	tampered := signTestJWT(t, "mallory", "", "s3cret", time.Hour)
	tamperedParts := strings.Split(tampered, ".")
	// Stitch: alice's header + mallory's payload + alice's signature.
	frankenstein := parts[0] + "." + tamperedParts[1] + "." + parts[2]
	if _, ok := VerifyJWT(frankenstein, "s3cret"); ok {
		t.Fatal("expected payload-swap attack to be rejected")
	}
}
