package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	vectorHttp "github.com/stek0v/levara/internal/http"
	accesspkg "github.com/stek0v/levara/pkg/access"
	vectorAuth "github.com/stek0v/levara/pkg/auth"
)

type oidcBrowserConfig struct {
	clientID, clientSecret, issuer          string
	authorizationURL, tokenURL, redirectURL *url.URL
	returnPath                              string
}

func trustedProviderURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || strings.Contains(raw, "#") || u.Opaque != "" {
		return nil, errors.New("SSO URL must be absolute without userinfo or fragment")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return nil, errors.New("SSO URL port must be between 1 and 65535")
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return nil, errors.New("SSO URL has an empty port")
	}
	loopback := strings.EqualFold(u.Hostname(), "localhost") || net.ParseIP(u.Hostname()).IsLoopback()
	if u.Scheme != "https" && (u.Scheme != "http" || !loopback) {
		return nil, errors.New("SSO URL requires HTTPS except loopback")
	}
	return u, nil
}

func oidcBrowserConfigFromEnv() (*oidcBrowserConfig, error) {
	clientID := strings.TrimSpace(os.Getenv("LEVARA_OIDC_CLIENT_ID"))
	if clientID == "" {
		for _, key := range []string{"LEVARA_OIDC_CLIENT_SECRET", "LEVARA_OIDC_AUTHORIZATION_URL", "LEVARA_OIDC_TOKEN_URL", "LEVARA_OIDC_REDIRECT_URL"} {
			if os.Getenv(key) != "" {
				return nil, errors.New("browser OIDC requires LEVARA_OIDC_CLIENT_ID")
			}
		}
		return nil, nil
	}
	issuers := splitCSVEnv("LEVARA_OIDC_ISSUERS")
	if len(issuers) != 1 {
		return nil, errors.New("browser OIDC requires exactly one configured issuer")
	}
	if os.Getenv("LEVARA_OIDC_JWKS_URL") == "" {
		return nil, errors.New("browser OIDC requires a JWKS URL")
	}
	cfg := &oidcBrowserConfig{clientID: clientID, clientSecret: os.Getenv("LEVARA_OIDC_CLIENT_SECRET"), issuer: issuers[0]}
	var err error
	if cfg.authorizationURL, err = trustedProviderURL(os.Getenv("LEVARA_OIDC_AUTHORIZATION_URL")); err != nil {
		return nil, fmt.Errorf("authorization URL: %w", err)
	}
	if cfg.tokenURL, err = trustedProviderURL(os.Getenv("LEVARA_OIDC_TOKEN_URL")); err != nil {
		return nil, fmt.Errorf("token URL: %w", err)
	}
	if cfg.redirectURL, err = trustedProviderURL(os.Getenv("LEVARA_OIDC_REDIRECT_URL")); err != nil {
		return nil, fmt.Errorf("redirect URL: %w", err)
	}
	if cfg.tokenURL.RawQuery != "" || cfg.redirectURL.RawQuery != "" || cfg.tokenURL.ForceQuery || cfg.redirectURL.ForceQuery {
		return nil, errors.New("token and callback URLs must not have query parameters")
	}
	if cfg.returnPath, err = vectorHttp.BrowserReturnPath(os.Getenv("LEVARA_AUTH_BROWSER_RETURN_PATH")); err != nil {
		return nil, errors.New("browser return must be a local absolute path")
	}
	return cfg, nil
}

type oidcPending struct {
	verifier, nonce, binding string
	expires                  time.Time
}
type oidcBrowser struct {
	cfg      oidcBrowserConfig
	auth     vectorHttp.AuthConfig
	bridge   accesspkg.IdentityBridge
	verifier *vectorAuth.OIDCVerifier
	client   *http.Client
	mu       sync.Mutex
	pending  map[string]oidcPending
}

func newOIDCBrowser(cfg oidcBrowserConfig, auth vectorHttp.AuthConfig, bridge accesspkg.IdentityBridge) (*oidcBrowser, error) {
	if auth.DB == nil {
		return nil, errors.New("browser OIDC requires SQL identity storage")
	}
	verifier, err := vectorAuth.NewOIDCVerifier(vectorAuth.OIDCVerifierConfig{JWKSURL: os.Getenv("LEVARA_OIDC_JWKS_URL"), Issuers: []string{cfg.issuer}, Audiences: []string{cfg.clientID}, ClockSkew: 30 * time.Second})
	if err != nil {
		return nil, err
	}
	return &oidcBrowser{cfg: cfg, auth: auth, bridge: bridge, verifier: verifier, client: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, pending: make(map[string]oidcPending)}, nil
}

func randomBrowserSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (b *oidcBrowser) routes(public fiber.Router) {
	public.Get("/auth/oidc/login", b.login)
	public.Get("/auth/oidc/callback", b.callback)
}

func (b *oidcBrowser) cookie(state, binding string, clear bool) *http.Cookie {
	maxAge := 300
	expires := time.Now().Add(5 * time.Minute)
	if clear {
		maxAge = -1
		expires = time.Unix(1, 0)
	}
	return &http.Cookie{Name: "levara_oidc_" + state, Value: binding, Path: b.cfg.redirectURL.Path, HttpOnly: true, Secure: b.cfg.redirectURL.Scheme == "https" || os.Getenv("ENV") == "production", SameSite: http.SameSiteLaxMode, MaxAge: maxAge, Expires: expires}
}

func (b *oidcBrowser) login(c *fiber.Ctx) error {
	c.Set("Cache-Control", "no-store")
	c.Set("Referrer-Policy", "no-referrer")
	state, err := randomBrowserSecret()
	if err != nil {
		return c.SendStatus(500)
	}
	verifier, err := randomBrowserSecret()
	if err != nil {
		return c.SendStatus(500)
	}
	nonce, err := randomBrowserSecret()
	if err != nil {
		return c.SendStatus(500)
	}
	binding, err := randomBrowserSecret()
	if err != nil {
		return c.SendStatus(500)
	}
	b.mu.Lock()
	for key, p := range b.pending {
		if !time.Now().Before(p.expires) {
			delete(b.pending, key)
		}
	}
	if len(b.pending) >= 1024 {
		b.mu.Unlock()
		return c.SendStatus(503)
	}
	b.pending[state] = oidcPending{verifier: verifier, nonce: nonce, binding: binding, expires: time.Now().Add(5 * time.Minute)}
	b.mu.Unlock()
	c.Response().Header.Add("Set-Cookie", b.cookie(state, binding, false).String())
	challenge := sha256.Sum256([]byte(verifier))
	redirect := *b.cfg.authorizationURL
	query := redirect.Query()
	query.Set("response_type", "code")
	query.Set("client_id", b.cfg.clientID)
	query.Set("redirect_uri", b.cfg.redirectURL.String())
	query.Set("scope", "openid email profile")
	query.Set("state", state)
	query.Set("nonce", nonce)
	query.Set("code_challenge", base64.RawURLEncoding.EncodeToString(challenge[:]))
	query.Set("code_challenge_method", "S256")
	redirect.RawQuery = query.Encode()
	return c.Redirect(redirect.String(), http.StatusFound)
}

func (b *oidcBrowser) callback(c *fiber.Ctx) error {
	c.Set("Cache-Control", "no-store")
	c.Set("Referrer-Policy", "no-referrer")
	query, err := url.ParseQuery(string(c.Request().URI().QueryString()))
	if err != nil || len(query["state"]) != 1 {
		return c.SendStatus(401)
	}
	state := query.Get("state")
	if len(state) != 43 {
		return c.SendStatus(401)
	}
	b.mu.Lock()
	pending, ok := b.pending[state]
	bound := ok && subtle.ConstantTimeCompare([]byte(c.Cookies("levara_oidc_"+state)), []byte(pending.binding)) == 1
	if bound {
		delete(b.pending, state)
	}
	b.mu.Unlock()
	if !bound {
		return c.SendStatus(401)
	}
	c.Response().Header.Add("Set-Cookie", b.cookie(state, "", true).String())
	if !time.Now().Before(pending.expires) {
		return c.SendStatus(401)
	}
	if query.Get("error") != "" || len(query["code"]) != 1 || query.Get("code") == "" {
		return c.SendStatus(401)
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {query.Get("code")}, "client_id": {b.cfg.clientID}, "redirect_uri": {b.cfg.redirectURL.String()}, "code_verifier": {pending.verifier}}
	req, err := http.NewRequestWithContext(c.UserContext(), "POST", b.cfg.tokenURL.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return c.SendStatus(401)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if b.cfg.clientSecret != "" {
		req.SetBasicAuth(url.QueryEscape(b.cfg.clientID), url.QueryEscape(b.cfg.clientSecret))
	}
	response, err := b.client.Do(req)
	if err != nil {
		return c.SendStatus(401)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return c.SendStatus(401)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return c.SendStatus(401)
	}
	var token struct {
		IDToken string `json:"id_token"`
	}
	if json.Unmarshal(raw, &token) != nil || token.IDToken == "" {
		return c.SendStatus(401)
	}
	claims, err := b.verifier.Verify(token.IDToken)
	if err != nil || claims.IssuedAt <= 0 || subtle.ConstantTimeCompare([]byte(claims.Nonce), []byte(pending.nonce)) != 1 {
		return c.SendStatus(401)
	}
	if (len(claims.Audiences) > 1 && claims.AuthorizedParty != b.cfg.clientID) || (claims.AuthorizedParty != "" && claims.AuthorizedParty != b.cfg.clientID) {
		return c.SendStatus(401)
	}
	principal, err := b.bridge.ResolveExternal(c.UserContext(), accesspkg.ExternalIdentity{Issuer: claims.Issuer, Subject: claims.Subject, Email: claims.Email})
	if err != nil {
		return c.SendStatus(401)
	}
	session, err := vectorHttp.IssueExternalBrowserSessionJWT(c.UserContext(), b.auth.DB, principal.UserID, principal.Email, b.auth.JWTSecret, claims.IssuedAt)
	if err != nil {
		return c.SendStatus(401)
	}
	c.Response().Header.Add("Set-Cookie", vectorHttp.BrowserSessionCookie(session, b.auth.CookieSecure || b.cfg.redirectURL.Scheme == "https").String())
	return c.Redirect(b.cfg.returnPath, http.StatusSeeOther)
}

func identitySCIMIssuer() string {
	issuer := strings.TrimSpace(os.Getenv("LEVARA_SCIM_ISSUER"))
	if issuer == "" {
		issuer = "scim-directory"
	}
	return issuer
}

// prepareIdentityAuth validates and constructs the public auth surface before
// the protected middleware is registered. Enabled SSO always requires SQL.
func prepareIdentityAuth(ctx context.Context, db *sql.DB, jwtSecret string, requireAuth bool) (*vectorHttp.AuthConfig, *accesspkg.SAMLSP, vectorHttp.ExternalBearerAuth, *oidcBrowser, error) {
	fail := func(err error) (*vectorHttp.AuthConfig, *accesspkg.SAMLSP, vectorHttp.ExternalBearerAuth, *oidcBrowser, error) {
		return nil, nil, nil, nil, err
	}
	browserCfg, err := oidcBrowserConfigFromEnv()
	if err != nil {
		return fail(err)
	}
	if db == nil && (samlEnabled() || os.Getenv("LEVARA_OIDC_JWKS_URL") != "" || browserCfg != nil) {
		return fail(errors.New("SSO requires SQL identity storage"))
	}
	if db != nil {
		if err := accesspkg.EnsureIdentitySchema(ctx, db, vectorHttp.SQLRewriter()); err != nil {
			return fail(err)
		}
		if err := accesspkg.EnsureBrowserSessionSchema(ctx, db, vectorHttp.SQLRewriter()); err != nil {
			return fail(err)
		}
	}
	auth := &vectorHttp.AuthConfig{DB: db, JWTSecret: jwtSecret, RequireAuth: requireAuth, CookieSecure: os.Getenv("ENV") == "production"}
	if raw := os.Getenv("LEVARA_AUTH_PUBLIC_ORIGIN"); raw != "" {
		u, err := trustedProviderURL(raw)
		if err != nil {
			return fail(err)
		}
		if u.Path != "" || u.RawQuery != "" || u.ForceQuery {
			return fail(errors.New("auth public origin must not contain a path or query"))
		}
		auth.CookieOrigins = append(auth.CookieOrigins, u.Scheme+"://"+u.Host)
		auth.CookieSecure = auth.CookieSecure || u.Scheme == "https"
	}
	// Generate before constructing callbacks so all issuers share the same key.
	if auth.JWTSecret == "" {
		auth.JWTSecret, err = randomBrowserSecret()
		if err != nil {
			return fail(err)
		}
	}
	mapping := map[string]string{}
	for _, issuer := range splitCSVEnv("LEVARA_OIDC_ISSUERS") {
		mapping[issuer] = identitySCIMIssuer()
	}
	bridge := accesspkg.SQLIdentityBridge{DB: db, Q: vectorHttp.SQLRewriter(), TrustedIssuers: mapping}
	samlSP, err := newSAMLSPFromEnv(ctx, bridge)
	if err != nil {
		return fail(err)
	}
	if samlSP != nil {
		mapping[samlSP.IDPIssuer()] = identitySCIMIssuer()
		u, err := trustedProviderURL(os.Getenv("LEVARA_SAML_ACS_URL"))
		if err != nil {
			return fail(err)
		}
		auth.CookieOrigins = append(auth.CookieOrigins, u.Scheme+"://"+u.Host)
		auth.CookieSecure = auth.CookieSecure || u.Scheme == "https"
	}
	var external vectorHttp.ExternalBearerAuth
	if jwksURL := strings.TrimSpace(os.Getenv("LEVARA_OIDC_JWKS_URL")); jwksURL != "" {
		verifier, err := vectorAuth.NewOIDCVerifier(vectorAuth.OIDCVerifierConfig{JWKSURL: jwksURL, Issuers: splitCSVEnv("LEVARA_OIDC_ISSUERS"), Audiences: splitCSVEnv("LEVARA_OIDC_AUDIENCES")})
		if err != nil {
			return fail(err)
		}
		external = &oidcBearerAuth{verifier: verifier, adapter: accesspkg.OIDCAdapter{Bridge: bridge}}
	}
	var browser *oidcBrowser
	if browserCfg != nil {
		auth.CookieOrigins = append(auth.CookieOrigins, browserCfg.redirectURL.Scheme+"://"+browserCfg.redirectURL.Host)
		auth.CookieSecure = auth.CookieSecure || browserCfg.redirectURL.Scheme == "https"
		browser, err = newOIDCBrowser(*browserCfg, *auth, bridge)
		if err != nil {
			return fail(err)
		}
	}
	return auth, samlSP, external, browser, nil
}
