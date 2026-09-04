// Package auth — OIDC/OAuth2 bearer token verification against a JWKS.
//
// A1 (backlog): enterprise deployments can configure Levara to accept
// tokens issued by an external OIDC provider. Unlike the OIDCAdapter seam
// (pkg/access), which only translates already-verified claims, this file
// performs the verification itself: JWKS fetch + cache, RS256/ES256
// signature validation, iss/aud allowlists, exp/nbf with clock skew, and
// alg pinning (alg=none and cross-alg confusion are rejected).
package auth

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// OIDCVerifierConfig configures raw OIDC bearer verification.
type OIDCVerifierConfig struct {
	// JWKSURL is the provider's JSON Web Key Set URL (e.g.
	// https://idp.example.com/.well-known/jwks.json). Required.
	JWKSURL string
	// Issuers is the allowlist of accepted `iss` claim values. Required,
	// must not contain wildcards.
	Issuers []string
	// Audiences is the allowlist of accepted `aud` values. Required.
	Audiences []string
	// ClockSkew tolerates clock drift for exp/nbf/iat. Defaults to 5m.
	ClockSkew time.Duration
	// HTTPClient for JWKS fetches; nil → http.DefaultClient with 10s timeout.
	HTTPClient *http.Client
}

// OIDCVerifier verifies bearer tokens against a remote JWKS. Safe for
// concurrent use.
type OIDCVerifier struct {
	cfg     OIDCVerifierConfig
	client  *http.Client
	skew    time.Duration
	mu      sync.RWMutex
	keys    map[string]jsonWebKey
	fetched time.Time
	refetch chan struct{}
	maxKeys int
}

type jsonWebKey struct {
	Kid string
	Kty string
	Alg string
	Use string
	N   *big.Int
	E   int
	Crv elliptic.Curve
	X   *big.Int
	Y   *big.Int
}

type idTokenClaims struct {
	Issuer   string   `json:"iss"`
	Subject  string   `json:"sub"`
	Audience audience `json:"aud"`
	Expiry   int64    `json:"exp"`
	NotBef   int64    `json:"nbf"`
	IssuedAt int64    `json:"iat"`
	Email    string   `json:"email"`
	Name     string   `json:"name"`
	Groups   []string `json:"groups"`
	// PreferredUsername is the OIDC standard claim; used as fallback email hint.
	PreferredUsername string `json:"preferred_username"`
}

type audience []string

func (a *audience) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*a = []string{s}
		return nil
	}
	return json.Unmarshal(b, (*[]string)(a))
}

// NewOIDCVerifier validates config and eagerly fetches the JWKS. A JWKS
// fetch failure at construction is an error: verification is fail-closed,
// so starting a server whose identity provider is unreachable is a config
// mistake, not a runtime condition.
func NewOIDCVerifier(cfg OIDCVerifierConfig) (*OIDCVerifier, error) {
	if strings.TrimSpace(cfg.JWKSURL) == "" {
		return nil, errors.New("oidc: JWKSURL is required")
	}
	if !strings.HasPrefix(cfg.JWKSURL, "https://") {
		// Plain-http JWKS would let an attacker serve their own keys;
		// only allow for explicit localhost testing.
		if !strings.HasPrefix(cfg.JWKSURL, "http://localhost") && !strings.HasPrefix(cfg.JWKSURL, "http://127.0.0.1") {
			return nil, errors.New("oidc: JWKSURL must use https")
		}
	}
	if len(cfg.Issuers) == 0 || len(cfg.Audiences) == 0 {
		return nil, errors.New("oidc: at least one issuer and audience are required")
	}
	for _, is := range cfg.Issuers {
		if strings.ContainsAny(is, "*?[]") {
			return nil, fmt.Errorf("oidc: issuer %q must be an exact value", is)
		}
	}
	skew := cfg.ClockSkew
	if skew <= 0 {
		skew = 5 * time.Minute
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	v := &OIDCVerifier{
		cfg:     cfg,
		client:  client,
		skew:    skew,
		refetch: make(chan struct{}, 1),
		maxKeys: 100,
	}
	if err := v.refreshKeys(); err != nil {
		return nil, fmt.Errorf("oidc: initial JWKS fetch: %w", err)
	}
	return v, nil
}

// Verify validates the raw bearer token and returns the verified claims.
// The returned claims are guaranteed to have passed signature, iss, aud,
// and time checks.
func (v *OIDCVerifier) Verify(token string) (*OIDCTokenClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("oidc: token is not a JWS compact serialisation")
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
		Typ string `json:"typ"`
	}
	hb, err := decodeSegment(parts[0])
	if err != nil {
		return nil, fmt.Errorf("oidc: header: %w", err)
	}
	if err := json.Unmarshal(hb, &header); err != nil {
		return nil, fmt.Errorf("oidc: header: %w", err)
	}
	// Alg pinning: reject none and anything we cannot verify.
	if header.Alg != "RS256" && header.Alg != "ES256" {
		return nil, fmt.Errorf("oidc: algorithm %q not allowed", header.Alg)
	}
	if header.Kid == "" {
		return nil, errors.New("oidc: token header missing kid")
	}

	key, ok := v.keyByID(header.Kid)
	if !ok {
		// Unknown kid: refresh once (key rotation), then retry.
		if err := v.refreshKeys(); err != nil {
			return nil, fmt.Errorf("oidc: jwks refresh: %w", err)
		}
		key, ok = v.keyByID(header.Kid)
		if !ok {
			return nil, fmt.Errorf("oidc: unknown key id %q", header.Kid)
		}
	}
	if key.Alg != "" && key.Alg != header.Alg {
		return nil, fmt.Errorf("oidc: key %q is pinned to alg %q, token says %q", key.Kid, key.Alg, header.Alg)
	}

	payload, err := decodeSegment(parts[1])
	if err != nil {
		return nil, fmt.Errorf("oidc: payload: %w", err)
	}
	sig, err := decodeSegment(parts[2])
	if err != nil {
		return nil, fmt.Errorf("oidc: signature: %w", err)
	}
	signingInput := token[:len(parts[0])+1+len(parts[1])]
	if err := verifySignature(header.Alg, key, []byte(signingInput), sig); err != nil {
		return nil, fmt.Errorf("oidc: signature: %w", err)
	}

	var claims idTokenClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("oidc: claims: %w", err)
	}
	now := time.Now()
	if err := claims.validateTimes(now, v.skew); err != nil {
		return nil, err
	}
	if !containsExact(v.cfg.Issuers, claims.Issuer) {
		return nil, fmt.Errorf("oidc: issuer %q not allowed", claims.Issuer)
	}
	if len(claims.Audience) == 0 || !containsExact(v.cfg.Audiences, stringOf(claims.Audience)) {
		return nil, fmt.Errorf("oidc: audience %v not allowed", claims.Audience)
	}
	if claims.Subject == "" {
		return nil, errors.New("oidc: empty subject")
	}

	email := claims.Email
	if email == "" {
		email = claims.PreferredUsername
	}
	return &OIDCTokenClaims{
		Issuer:      claims.Issuer,
		Subject:     claims.Subject,
		Email:       email,
		DisplayName: claims.Name,
		Groups:      append([]string(nil), claims.Groups...),
	}, nil
}

// OIDCTokenClaims is the verified identity payload handed to OIDCAdapter
// (pkg/access) for bridge resolution.
type OIDCTokenClaims struct {
	Issuer      string
	Subject     string
	Email       string
	DisplayName string
	Groups      []string
}

func (c idTokenClaims) validateTimes(now time.Time, skew time.Duration) error {
	if c.Expiry == 0 {
		return errors.New("oidc: missing exp")
	}
	if now.After(time.Unix(c.Expiry, 0).Add(skew)) {
		return errors.New("oidc: token expired")
	}
	if c.NotBef != 0 && now.Before(time.Unix(c.NotBef, 0).Add(-skew)) {
		return errors.New("oidc: token not yet valid (nbf)")
	}
	if c.IssuedAt != 0 && c.IssuedAt > now.Add(skew).Unix() {
		return errors.New("oidc: token issued in the future (iat)")
	}
	return nil
}

func verifySignature(alg string, key jsonWebKey, signingInput, sig []byte) error {
	digest := sha256.Sum256(signingInput)
	switch alg {
	case "RS256":
		if key.Kty != "RSA" || key.N == nil || key.E == 0 {
			return errors.New("oidc: JWKS key is not RSA")
		}
		pub := &rsa.PublicKey{N: key.N, E: key.E}
		return rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig)
	case "ES256":
		if key.Kty != "EC" || key.Crv != elliptic.P256() {
			return errors.New("oidc: JWKS key is not ES256-capable EC P-256")
		}
		if len(sig) != 64 {
			return errors.New("oidc: ES256 signature must be 64-byte raw r||s")
		}
		pub := &ecdsa.PublicKey{Curve: key.Crv, X: key.X, Y: key.Y}
		r := new(big.Int).SetBytes(sig[:32])
		s := new(big.Int).SetBytes(sig[32:])
		if !ecdsa.Verify(pub, digest[:], r, s) {
			return errors.New("oidc: ECDSA verification failed")
		}
		return nil
	default:
		return fmt.Errorf("oidc: algorithm %q not allowed", alg)
	}
}

func (v *OIDCVerifier) keyByID(kid string) (jsonWebKey, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	k, ok := v.keys[kid]
	return k, ok
}

// refreshKeys re-fetches the JWKS. At most one in-flight refresh; concurrent
// callers share the result via mutex on write.
func (v *OIDCVerifier) refreshKeys() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	// Another goroutine refreshed very recently — skip (thundering herd on
	// unknown kid during rotation).
	if !v.fetched.IsZero() && time.Since(v.fetched) < time.Second {
		return nil
	}
	req, err := http.NewRequest(http.MethodGet, v.cfg.JWKSURL, nil)
	if err != nil {
		return err
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("jwks endpoint returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MiB cap
	if err != nil {
		return err
	}
	var doc struct {
		Keys []struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
			Alg string `json:"alg"`
			Use string `json:"use"`
			N   string `json:"n"`
			E   string `json:"e"`
			Crv string `json:"crv"`
			X   string `json:"x"`
			Y   string `json:"y"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return fmt.Errorf("jwks parse: %w", err)
	}
	keys := make(map[string]jsonWebKey, len(doc.Keys))
	for _, k := range doc.Keys {
		if k.Kid == "" {
			continue
		}
		jwk := jsonWebKey{Kid: k.Kid, Kty: k.Kty, Alg: k.Alg, Use: k.Use}
		switch k.Kty {
		case "RSA":
			if jwk.N, err = decodeBigInt(k.N); err != nil {
				continue
			}
			if jwk.E, err = decodeExponent(k.E); err != nil {
				continue
			}
		case "EC":
			if k.Crv != "P-256" {
				continue
			}
			jwk.Crv = elliptic.P256()
			if jwk.X, err = decodeBigInt(k.X); err != nil {
				continue
			}
			if jwk.Y, err = decodeBigInt(k.Y); err != nil {
				continue
			}
		default:
			continue
		}
		keys[jwk.Kid] = jwk
		if len(keys) >= v.maxKeys {
			break
		}
	}
	if len(keys) == 0 {
		return errors.New("jwks contained no usable keys")
	}
	v.keys = keys
	v.fetched = time.Now()
	return nil
}

// ── helpers ──

func decodeSegment(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(s)
}

func decodeBigInt(s string) (*big.Int, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	return new(big.Int).SetBytes(b), nil
}

func decodeExponent(s string) (int, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return 0, err
	}
	return int(new(big.Int).SetBytes(b).Int64()), nil
}

func containsExact(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// stringOf collapses an audience list to a comparable value: single-element
// lists compare by their element, multi-element lists never match an
// exact-single-string allowlist entry (caller must include the full azp
// handling explicitly if needed).
func stringOf(a audience) string {
	if len(a) == 1 {
		return a[0]
	}
	return strings.Join(a, "\x00")
}
