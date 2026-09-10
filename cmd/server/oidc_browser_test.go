package main

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	vectorHttp "github.com/stek0v/levara/internal/http"
	accesspkg "github.com/stek0v/levara/pkg/access"
	vectorAuth "github.com/stek0v/levara/pkg/auth"
)

func newBrowserAuthStore(t *testing.T, dialect string) *accesspkg.SCIMStore {
	t.Helper()
	_, store := scimTestAppDialect(t, dialect)
	old := vectorHttp.GetDBProvider()
	if dialect == "sqlite" {
		vectorHttp.SetDBProvider(vectorHttp.DBSQLite)
		store.DB.SetMaxOpenConns(1)
	} else {
		vectorHttp.SetDBProvider(vectorHttp.DBPostgres)
	}
	t.Cleanup(func() { vectorHttp.SetDBProvider(old) })
	if err := accesspkg.EnsureBrowserSessionSchema(context.Background(), store.DB, store.Q); err != nil {
		t.Fatal(err)
	}
	return store
}

type browserCode struct {
	challenge string
	claims    map[string]any
}
type oidcBrowserFixture struct {
	app           *fiber.App
	browser       *oidcBrowser
	store         *accesspkg.SCIMStore
	idp           *httptest.Server
	key           *rsa.PrivateKey
	mu            sync.Mutex
	codes         map[string]browserCode
	hits          int
	tokenStatus   int
	tokenRedirect string
}

func newOIDCBrowserFixture(t *testing.T, dialect string) *oidcBrowserFixture {
	t.Helper()
	t.Setenv("LEVARA_AUTH_PUBLIC_ORIGIN", "")
	f := &oidcBrowserFixture{store: newBrowserAuthStore(t, dialect), codes: make(map[string]browserCode)}
	var err error
	f.key, err = rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f.idp = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/jwks" {
			json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": "test", "alg": "RS256", "use": "sig", "n": base64.RawURLEncoding.EncodeToString(f.key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(f.key.E)).Bytes())}}})
			return
		}
		if r.URL.Path != "/token" {
			http.NotFound(w, r)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.hits++
		if f.tokenRedirect != "" {
			http.Redirect(w, r, f.tokenRedirect, 302)
			return
		}
		if f.tokenStatus != 0 {
			w.WriteHeader(f.tokenStatus)
			return
		}
		if r.Method != "POST" || r.ParseForm() != nil {
			http.Error(w, "bad request", 400)
			return
		}
		code, ok := f.codes[r.Form.Get("code")]
		delete(f.codes, r.Form.Get("code"))
		challenge := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if !ok || base64.RawURLEncoding.EncodeToString(challenge[:]) != code.challenge || r.Form.Get("redirect_uri") != "https://app.example.test/api/v1/auth/oidc/callback" || r.Form.Get("client_id") != "browser-client" || r.Form.Get("grant_type") != "authorization_code" {
			http.Error(w, "invalid exchange", 400)
			return
		}
		client, secret, ok := r.BasicAuth()
		if !ok || client != "browser-client" || secret != "client-secret" {
			http.Error(w, "invalid client", 401)
			return
		}
		header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "test"})
		payload, _ := json.Marshal(code.claims)
		input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
		digest := sha256.Sum256([]byte(input))
		signature, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, digest[:])
		if err != nil {
			http.Error(w, "sign", 500)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"id_token": input + "." + base64.RawURLEncoding.EncodeToString(signature), "access_token": "provider-secret-must-not-leak"})
	}))
	t.Cleanup(f.idp.Close)
	for key, value := range map[string]string{"LEVARA_SAML_ENABLED": "", "LEVARA_OIDC_CLIENT_ID": "browser-client", "LEVARA_OIDC_CLIENT_SECRET": "client-secret", "LEVARA_OIDC_ISSUERS": f.idp.URL + "/issuer", "LEVARA_OIDC_AUDIENCES": "api-audience", "LEVARA_OIDC_JWKS_URL": f.idp.URL + "/jwks", "LEVARA_OIDC_AUTHORIZATION_URL": f.idp.URL + "/authorize", "LEVARA_OIDC_TOKEN_URL": f.idp.URL + "/token", "LEVARA_OIDC_REDIRECT_URL": "https://app.example.test/api/v1/auth/oidc/callback", "LEVARA_SCIM_ISSUER": "directory", "LEVARA_AUTH_BROWSER_RETURN_PATH": "/chat"} {
		t.Setenv(key, value)
	}
	if _, _, err := f.store.ProvisionCreate(context.Background(), accesspkg.SCIMUser{Issuer: "directory", ExternalID: "subject", Email: "stored@example.test", Active: true}); err != nil {
		t.Fatal(err)
	}
	auth, _, _, browser, err := prepareIdentityAuth(context.Background(), f.store.DB, "session-secret", true)
	if err != nil {
		t.Fatal(err)
	}
	f.browser = browser
	f.app = fiber.New()
	api := f.app.Group("/api/v1")
	vectorHttp.RegisterAuthAPI(api, auth)
	browser.routes(api)
	return f
}

func TestOIDCBearerPreservesVerifiedTimes(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			f := newOIDCBrowserFixture(t, dialect)
			issued, expires := time.Now().Add(-time.Minute).Unix(), time.Now().Add(42*time.Second).Unix()
			header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "test"})
			body, _ := json.Marshal(map[string]any{"iss": f.idp.URL + "/issuer", "sub": "subject", "aud": "browser-client", "iat": issued, "exp": expires})
			input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(body)
			digest := sha256.Sum256([]byte(input))
			signature, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, digest[:])
			if err != nil {
				t.Fatal(err)
			}
			adapter := oidcBearerAuth{verifier: f.browser.verifier, adapter: accesspkg.OIDCAdapter{Bridge: accesspkg.SQLIdentityBridge{DB: f.store.DB, Q: f.store.Q, TrustedIssuers: map[string]string{f.idp.URL + "/issuer": "directory"}}}}
			p, err := adapter.Authenticate(context.Background(), input+"."+base64.RawURLEncoding.EncodeToString(signature))
			if err != nil || p.IssuedAt != issued || p.ExpiresAt != expires || p.UserID == "" {
				t.Fatalf("verified times lost: %+v err=%v", p, err)
			}
		})
	}
}

func TestOIDCBrowserConfiguration(t *testing.T) {
	for _, key := range []string{"LEVARA_SAML_ENABLED", "LEVARA_OIDC_CLIENT_ID", "LEVARA_OIDC_CLIENT_SECRET", "LEVARA_OIDC_AUTHORIZATION_URL", "LEVARA_OIDC_TOKEN_URL", "LEVARA_OIDC_REDIRECT_URL", "LEVARA_OIDC_ISSUERS", "LEVARA_OIDC_JWKS_URL", "LEVARA_AUTH_PUBLIC_ORIGIN", "LEVARA_AUTH_BROWSER_RETURN_PATH", "ENV"} {
		t.Setenv(key, "")
	}
	for _, raw := range []string{"https://app.test/path", "https://app.test/", "https://app.test?", "https://app.test#", "https://app.test?x=1", "https://u:p@app.test", "http://app.test", "https://app.test:0", "https://app.test:65536", "https://app.test:"} {
		t.Run(raw, func(t *testing.T) {
			t.Setenv("LEVARA_AUTH_PUBLIC_ORIGIN", raw)
			if _, _, _, _, err := prepareIdentityAuth(context.Background(), nil, "secret", false); err == nil {
				t.Fatalf("invalid origin accepted: %q", raw)
			}
		})
	}
	for _, raw := range []string{"https://app.test", "https://app.test:8443", "http://localhost:8080", "http://[::1]:8080"} {
		t.Run(raw, func(t *testing.T) {
			t.Setenv("LEVARA_AUTH_PUBLIC_ORIGIN", raw)
			auth, _, _, _, err := prepareIdentityAuth(context.Background(), nil, "secret", false)
			if err != nil || len(auth.CookieOrigins) != 1 || auth.CookieOrigins[0] != raw || auth.CookieSecure != strings.HasPrefix(raw, "https:") {
				t.Fatalf("origin config=%+v err=%v", auth, err)
			}
		})
	}
	for _, enabled := range []string{"LEVARA_SAML_ENABLED", "LEVARA_OIDC_JWKS_URL"} {
		t.Run(enabled, func(t *testing.T) {
			t.Setenv(enabled, "true")
			if _, _, _, _, err := prepareIdentityAuth(context.Background(), nil, "secret", true); err == nil || !strings.Contains(err.Error(), "SQL") {
				t.Fatalf("SSO without SQL err=%v", err)
			}
		})
	}
	for key, value := range map[string]string{"LEVARA_OIDC_CLIENT_ID": "client", "LEVARA_OIDC_ISSUERS": "https://idp.test", "LEVARA_OIDC_JWKS_URL": "https://idp.test/jwks", "LEVARA_OIDC_AUTHORIZATION_URL": "https://idp.test/authorize", "LEVARA_OIDC_TOKEN_URL": "https://idp.test/token", "LEVARA_OIDC_REDIRECT_URL": "https://app.test/api/v1/auth/oidc/callback"} {
		t.Setenv(key, value)
	}
	if cfg, err := oidcBrowserConfigFromEnv(); err != nil || cfg.returnPath != "/" || cfg.clientSecret != "" {
		t.Fatalf("public PKCE client config=%+v err=%v", cfg, err)
	}
	for _, tc := range []struct{ key, value string }{
		{"LEVARA_OIDC_ISSUERS", "https://idp.test,https://other.test"},
		{"LEVARA_OIDC_TOKEN_URL", "https://idp.test/token?"},
		{"LEVARA_OIDC_REDIRECT_URL", "https://app.test/callback?x=1"},
		{"LEVARA_OIDC_AUTHORIZATION_URL", "https://idp.test/authorize#"},
		{"LEVARA_OIDC_TOKEN_URL", "http://remote.test/token"},
		{"LEVARA_AUTH_BROWSER_RETURN_PATH", "/%25252fattacker.test"},
	} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			t.Setenv(tc.key, tc.value)
			if _, err := oidcBrowserConfigFromEnv(); err == nil {
				t.Fatalf("invalid config accepted: %s=%s", tc.key, tc.value)
			}
		})
	}
}

func TestOIDCBrowserConcurrentCallbackAndDeactivation(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			f := newOIDCBrowserFixture(t, dialect)
			flow := f.start(t, nil)
			statuses := make(chan int, 8)
			var wg sync.WaitGroup
			for range 8 {
				wg.Add(1)
				go func() { defer wg.Done(); statuses <- f.callback(t, flow).StatusCode }()
			}
			wg.Wait()
			close(statuses)
			success := 0
			for code := range statuses {
				if code == 303 {
					success++
				} else if code != 401 {
					t.Errorf("callback=%d", code)
				}
			}
			f.mu.Lock()
			hits := f.hits
			f.mu.Unlock()
			if success != 1 || hits != 1 {
				t.Fatalf("success=%d exchanges=%d", success, hits)
			}
			var rows int
			if err := f.store.DB.QueryRow("SELECT COUNT(*) FROM auth_sessions").Scan(&rows); err != nil || rows != 1 {
				t.Fatalf("sessions=%d err=%v", rows, err)
			}
			old := f.start(t, func(c map[string]any) { c["iat"] = time.Now().Add(-time.Minute).Unix() })
			if err := f.store.ProvisionDeactivate(context.Background(), "directory", "subject"); err != nil {
				t.Fatal(err)
			}
			if resp := f.callback(t, f.start(t, nil)); resp.StatusCode != 401 {
				t.Fatalf("inactive callback=%d", resp.StatusCode)
			}
			if _, _, err := f.store.ProvisionCreate(context.Background(), accesspkg.SCIMUser{Issuer: "directory", ExternalID: "subject", Email: "stored@example.test", Active: true}); err != nil {
				t.Fatal(err)
			}
			if resp := f.callback(t, old); resp.StatusCode != 401 {
				t.Fatalf("revoked external token revived=%d", resp.StatusCode)
			}
		})
	}
}

type oidcTestFlow struct {
	state, code string
	cookies     []*http.Cookie
}

func (f *oidcBrowserFixture) start(t *testing.T, mutate func(map[string]any)) oidcTestFlow {
	t.Helper()
	resp, err := f.app.Test(httptest.NewRequest("GET", "https://app.example.test/api/v1/auth/oidc/login?next=//evil.test", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 302 {
		t.Fatalf("login=%d", resp.StatusCode)
	}
	u, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if u.Scheme+"://"+u.Host != f.idp.URL || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || len(q.Get("code_challenge")) != 43 || len(q.Get("nonce")) != 43 || q.Get("client_id") != "browser-client" {
		t.Fatalf("invalid authorization request: %v", q)
	}
	cookies := resp.Cookies()
	if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode || cookies[0].Path != "/api/v1/auth/oidc/callback" {
		t.Fatalf("unsafe state cookie: %v", cookies)
	}
	claims := map[string]any{"iss": f.idp.URL + "/issuer", "sub": "subject", "aud": "browser-client", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(), "nonce": q.Get("nonce"), "email": "conflicting-claim@example.test"}
	if mutate != nil {
		mutate(claims)
	}
	code := "code-" + q.Get("state")
	f.mu.Lock()
	f.codes[code] = browserCode{challenge: q.Get("code_challenge"), claims: claims}
	f.mu.Unlock()
	return oidcTestFlow{state: q.Get("state"), code: code, cookies: cookies}
}
func (f *oidcBrowserFixture) callback(t *testing.T, flow oidcTestFlow) *http.Response {
	t.Helper()
	req := httptest.NewRequest("GET", "https://app.example.test/api/v1/auth/oidc/callback?"+url.Values{"state": {flow.state}, "code": {flow.code}}.Encode(), nil)
	for _, cookie := range flow.cookies {
		req.AddCookie(cookie)
	}
	resp, err := f.app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}
func sessionCookie(t *testing.T, resp *http.Response) *http.Cookie {
	t.Helper()
	var session *http.Cookie
	cleared := false
	for _, c := range resp.Cookies() {
		if c.Name == "auth_token" {
			session = c
		} else if strings.HasPrefix(c.Name, "levara_oidc_") && c.MaxAge < 0 {
			cleared = true
		}
	}
	if resp.StatusCode != 303 || resp.Header.Get("Location") != "/chat" || session == nil || !cleared {
		t.Fatalf("callback=%d location=%q cookies=%v", resp.StatusCode, resp.Header.Get("Location"), resp.Cookies())
	}
	if !session.HttpOnly || !session.Secure || session.SameSite != http.SameSiteLaxMode || session.Path != "/" {
		t.Fatalf("unsafe session: %v", session)
	}
	return session
}
func (f *oidcBrowserFixture) sessionRequest(t *testing.T, method, path, origin string, cookie *http.Cookie) int {
	t.Helper()
	req := httptest.NewRequest(method, "https://app.example.test/api/v1"+path, nil)
	req.AddCookie(cookie)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := f.app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func TestOIDCBrowserPKCESessionLogout(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			f := newOIDCBrowserFixture(t, dialect)
			flow := f.start(t, nil)
			response := f.callback(t, flow)
			session := sessionCookie(t, response)
			body, _ := io.ReadAll(response.Body)
			if strings.Contains(string(body), "provider-secret") || strings.Contains(response.Header.Get("Location"), "token") {
				t.Fatal("provider credentials leaked")
			}
			claims, valid := vectorAuth.VerifyJWT(session.Value, "session-secret")
			if !valid || claims.SessionID == "" || claims.Sub != accesspkg.SCIMUserID("directory", "subject") || claims.Email != "stored@example.test" {
				t.Fatalf("wrong mapped session: %+v", claims)
			}
			other := sessionCookie(t, f.callback(t, f.start(t, nil)))
			if code := f.sessionRequest(t, "GET", "/auth/me", "", session); code != 200 {
				t.Fatalf("me=%d", code)
			}
			for _, origin := range []string{"", "https://attacker.test"} {
				if code := f.sessionRequest(t, "POST", "/auth/logout", origin, session); code != 403 {
					t.Fatalf("logout origin=%q status=%d", origin, code)
				}
			}
			if code := f.sessionRequest(t, "POST", "/auth/logout", "https://app.example.test", session); code != 204 {
				t.Fatalf("logout=%d", code)
			}
			if code := f.sessionRequest(t, "GET", "/auth/me", "", session); code != 401 {
				t.Fatalf("logged-out session revived=%d", code)
			}
			if code := f.sessionRequest(t, "GET", "/auth/me", "", other); code != 200 {
				t.Fatalf("other session revoked=%d", code)
			}
			if _, err := f.store.DB.Exec(f.store.Q(`UPDATE auth_sessions SET expires_at=$1`), time.Now().Add(-time.Minute).Unix()); err != nil {
				t.Fatal(err)
			}
			if code := f.sessionRequest(t, "GET", "/auth/me", "", other); code != 401 {
				t.Fatalf("expired session accepted=%d", code)
			}
			if response := f.callback(t, flow); response.StatusCode != 401 {
				t.Fatalf("callback replay=%d", response.StatusCode)
			}
		})
	}
}

func TestOIDCBrowserRejectsUnboundOrInvalidTokens(t *testing.T) {
	f := newOIDCBrowserFixture(t, "sqlite")
	for name, mutate := range map[string]func(map[string]any){
		"wrong nonce": func(c map[string]any) { c["nonce"] = "other" }, "missing nonce": func(c map[string]any) { delete(c, "nonce") },
		"missing issued at": func(c map[string]any) { delete(c, "iat") },
		"issuer":            func(c map[string]any) { c["iss"] = "https://other.test" }, "audience": func(c map[string]any) { c["aud"] = "api-audience" },
		"expired":                  func(c map[string]any) { c["exp"] = time.Now().Add(-time.Hour).Unix() },
		"unprovisioned same email": func(c map[string]any) { c["sub"] = "unknown"; c["email"] = "stored@example.test" },
		"wrong authorized party":   func(c map[string]any) { c["aud"] = []string{"browser-client", "other"}; c["azp"] = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			flow := f.start(t, mutate)
			if response := f.callback(t, flow); response.StatusCode != 401 {
				t.Fatalf("accepted invalid %s: %d", name, response.StatusCode)
			}
		})
	}
	flow := f.start(t, nil)
	wrong := flow
	wrong.cookies = nil
	if resp := f.callback(t, wrong); resp.StatusCode != 401 {
		t.Fatalf("unbound callback=%d", resp.StatusCode)
	}
	sessionCookie(t, f.callback(t, flow)) // a foreign browser cannot burn this state
	expired := f.start(t, nil)
	f.browser.mu.Lock()
	p := f.browser.pending[expired.state]
	p.expires = time.Now().Add(-time.Minute)
	f.browser.pending[expired.state] = p
	f.browser.mu.Unlock()
	if resp := f.callback(t, expired); resp.StatusCode != 401 {
		t.Fatalf("expired state=%d", resp.StatusCode)
	}
	flow = f.start(t, nil)
	f.mu.Lock()
	f.tokenRedirect = f.idp.URL + "/unexpected"
	f.mu.Unlock()
	if resp := f.callback(t, flow); resp.StatusCode != 401 {
		t.Fatalf("token redirect accepted=%d", resp.StatusCode)
	}
}
