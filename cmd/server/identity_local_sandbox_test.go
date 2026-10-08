package main

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/beevik/etree"
	"github.com/crewjam/saml"
	"github.com/crewjam/saml/samlsp"
	"github.com/gofiber/fiber/v2"
	dsig "github.com/russellhaering/goxmldsig"
	httpapi "github.com/stek0v/levara/internal/http"
	accesspkg "github.com/stek0v/levara/pkg/access"
	vectorAuth "github.com/stek0v/levara/pkg/auth"
	"github.com/valyala/fasthttp"
)

type identitySandboxReply struct {
	status  int
	body    []byte
	header  http.Header
	cookies []*http.Cookie
}

func identitySandboxHTTP(client *http.Client, method, endpoint, body string, cookie *http.Cookie, headers map[string]string) (identitySandboxReply, error) {
	req, err := http.NewRequest(method, endpoint, strings.NewReader(body))
	if err != nil {
		return identitySandboxReply{}, err
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := client.Do(req)
	if err != nil {
		return identitySandboxReply{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return identitySandboxReply{status: resp.StatusCode, body: data, header: resp.Header.Clone(), cookies: resp.Cookies()}, err
}
func identitySandboxJSON(t *testing.T, response identitySandboxReply) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(response.body, &value); err != nil {
		t.Fatalf("JSON status=%d: %s: %v", response.status, response.body, err)
	}
	return value
}
func identitySandboxCookie(t *testing.T, response identitySandboxReply, userID, email string) *http.Cookie {
	t.Helper()
	if response.status != 303 {
		t.Fatalf("login status=%d body=%s", response.status, response.body)
	}
	for _, cookie := range response.cookies {
		if cookie.Name != "auth_token" {
			continue
		}
		claims, valid := vectorAuth.VerifyJWT(cookie.Value, "identity-local-session-secret")
		if !valid || claims.Sub != userID || claims.Email != email || claims.SessionID == "" || !cookie.Secure || !cookie.HttpOnly {
			t.Fatalf("wrong immutable session identity or cookie: %+v %+v", claims, cookie)
		}
		return cookie
	}
	t.Fatal("login did not issue session")
	return nil
}

// One shared native store and actual TLS listener exercise the existing routes.
// Replacing route/browser/SP objects below is deliberately not a binary restart.
func TestIdentityLocalSandboxLifecycle(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			ctx := context.Background()
			t.Setenv("LEVARA_SCIM_TOKEN", "")
			t.Setenv("LEVARA_SCIM_TENANT_ID", "")
			store := newBrowserAuthStore(t, dialect)
			if _, err := store.DB.Exec("DROP TABLE api_keys"); err != nil {
				t.Fatal(err)
			}
			if err := httpapi.MigrateSchema(store.DB); err != nil {
				t.Fatal(err)
			}
			if _, err := store.DB.Exec("INSERT INTO tenants(id,name) VALUES('identity-tenant','Identity Sandbox')"); err != nil {
				t.Fatal(err)
			}
			t.Setenv("LEVARA_SCIM_TOKEN", scimTestToken)
			t.Setenv("LEVARA_SCIM_ISSUER", "identity-directory")
			t.Setenv("LEVARA_SCIM_TENANT_ID", "identity-tenant")
			// Only this local TLS client trusts the disposable test certificate.
			spKey, spCert := samlTestCert(t, "identity-sandbox")
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			tlsListener := tls.NewListener(listener, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{Certificate: [][]byte{spCert.Raw}, PrivateKey: spKey}}})
			origin := "https://" + listener.Addr().String()
			transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true}} // disposable loopback fixture only
			client := &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			t.Cleanup(transport.CloseIdleConnections)
			var current atomic.Pointer[fiber.App]
			server := &fasthttp.Server{ReadTimeout: 15 * time.Second, IdleTimeout: 5 * time.Second, Handler: func(c *fasthttp.RequestCtx) {
				if app := current.Load(); app != nil {
					app.Handler()(c)
				} else {
					c.SetStatusCode(503)
				}
			}}
			serveDone := make(chan error, 1)
			go func() { serveDone <- server.Serve(tlsListener) }()
			t.Cleanup(func() {
				transport.CloseIdleConnections()
				shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer shutdownCancel()
				if err := server.ShutdownWithContext(shutdownCtx); err != nil {
					t.Errorf("TLS server shutdown: %v", err)
				}
				_ = tlsListener.Close()
				select {
				case err := <-serveDone:
					if err != nil {
						t.Errorf("TLS server Serve: %v", err)
					}
				case <-time.After(5 * time.Second):
					t.Error("TLS listener did not stop")
				}
			})

			oidcKey, err := rsa.GenerateKey(rand.Reader, 2048)
			if err != nil {
				t.Fatal(err)
			}
			idpKey, idpCert := samlTestCert(t, "identity-idp")
			idp := &saml.IdentityProvider{Key: idpKey, Certificate: idpCert, SignatureMethod: dsig.RSASHA256SignatureMethod}
			var provider *httptest.Server
			var providerMu sync.Mutex
			codes := map[string]browserCode{}
			var spMeta *saml.EntityDescriptor
			provider = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/jwks":
					_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": "sandbox", "alg": "RS256", "use": "sig", "n": base64.RawURLEncoding.EncodeToString(oidcKey.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(oidcKey.E)).Bytes())}}})
				case "/authorize":
					q := r.URL.Query()
					if q.Get("client_id") != "sandbox-client" || q.Get("redirect_uri") != origin+"/api/v1/auth/oidc/callback" || q.Get("code_challenge_method") != "S256" || q.Get("response_type") != "code" {
						http.Error(w, "invalid authorization", 400)
						return
					}
					code := "sandbox-" + q.Get("state")
					claims := map[string]any{"iss": provider.URL + "/issuer", "sub": q.Get("sandbox_subject"), "aud": "sandbox-client", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(), "nonce": q.Get("nonce"), "email": "different-oidc-claim@example.test"}
					providerMu.Lock()
					codes[code] = browserCode{challenge: q.Get("code_challenge"), claims: claims}
					providerMu.Unlock()
					http.Redirect(w, r, q.Get("redirect_uri")+"?"+url.Values{"state": {q.Get("state")}, "code": {code}}.Encode(), http.StatusFound)
				case "/token":
					if r.Method != "POST" || r.ParseForm() != nil {
						http.Error(w, "bad exchange", 400)
						return
					}
					providerMu.Lock()
					code, ok := codes[r.Form.Get("code")]
					delete(codes, r.Form.Get("code"))
					providerMu.Unlock()
					digest := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
					username, password, basic := r.BasicAuth()
					if !ok || !basic || username != "sandbox-client" || password != "sandbox-secret" || r.Form.Get("client_id") != "sandbox-client" || r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("redirect_uri") != origin+"/api/v1/auth/oidc/callback" || base64.RawURLEncoding.EncodeToString(digest[:]) != code.challenge {
						http.Error(w, "invalid exchange", 400)
						return
					}
					header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "sandbox"})
					payload, _ := json.Marshal(code.claims)
					input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
					hash := sha256.Sum256([]byte(input))
					signature, err := rsa.SignPKCS1v15(rand.Reader, oidcKey, crypto.SHA256, hash[:])
					if err != nil {
						http.Error(w, "sign failed", 500)
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]string{"id_token": input + "." + base64.RawURLEncoding.EncodeToString(signature)})
				case "/metadata":
					w.Header().Set("Content-Type", "application/samlmetadata+xml")
					_ = xml.NewEncoder(w).Encode(idp.Metadata())
				case "/sso":
					request, err := saml.NewIdpAuthnRequest(idp, r)
					if err != nil {
						http.Error(w, err.Error(), 400)
						return
					}
					if err := xml.Unmarshal(request.RequestBuffer, &request.Request); err != nil {
						http.Error(w, err.Error(), 400)
						return
					}
					providerMu.Lock()
					meta := spMeta
					providerMu.Unlock()
					if meta == nil {
						http.Error(w, "SP metadata absent", 500)
						return
					}
					request.ServiceProviderMetadata = meta
					request.SPSSODescriptor = &meta.SPSSODescriptors[0]
					request.ACSEndpoint = &request.SPSSODescriptor.AssertionConsumerServices[0]
					if err := (saml.DefaultAssertionMaker{}).MakeAssertion(request, &saml.Session{NameID: r.URL.Query().Get("sandbox_subject"), UserEmail: "different-saml-claim@example.test", CreateTime: time.Now()}); err != nil {
						http.Error(w, err.Error(), 500)
						return
					}
					// The test client submits these signed response form fields to the native ACS.
					w.Header().Set("Content-Type", "application/x-www-form-urlencoded")
					if err := request.MakeResponse(); err != nil {
						http.Error(w, err.Error(), 500)
						return
					}
					document := etree.NewDocument()
					document.SetRoot(request.ResponseEl)
					signed, err := document.WriteToBytes()
					if err != nil {
						http.Error(w, err.Error(), 500)
						return
					}
					_, _ = io.WriteString(w, url.Values{"SAMLResponse": {base64.StdEncoding.EncodeToString(signed)}, "RelayState": {request.RelayState}}.Encode())
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(provider.Close)
			metadataURL, _ := url.Parse(provider.URL + "/metadata")
			ssoURL, _ := url.Parse(provider.URL + "/sso")
			idp.MetadataURL = *metadataURL
			idp.SSOURL = *ssoURL
			for key, value := range map[string]string{"LEVARA_SAML_ENABLED": "", "LEVARA_OIDC_CLIENT_ID": "sandbox-client", "LEVARA_OIDC_CLIENT_SECRET": "sandbox-secret", "LEVARA_OIDC_ISSUERS": provider.URL + "/issuer", "LEVARA_OIDC_AUDIENCES": "sandbox-client", "LEVARA_OIDC_JWKS_URL": provider.URL + "/jwks", "LEVARA_OIDC_AUTHORIZATION_URL": provider.URL + "/authorize", "LEVARA_OIDC_TOKEN_URL": provider.URL + "/token", "LEVARA_OIDC_REDIRECT_URL": origin + "/api/v1/auth/oidc/callback", "LEVARA_AUTH_PUBLIC_ORIGIN": origin, "LEVARA_AUTH_BROWSER_RETURN_PATH": "/chat"} {
				t.Setenv(key, value)
			}
			storagePath := t.TempDir()
			rebuild := func() {
				auth, _, external, browser, err := prepareIdentityAuth(ctx, store.DB, "identity-local-session-secret", true)
				if err != nil {
					t.Fatal(err)
				}
				sp, err := accesspkg.NewSAMLSP(ctx, accesspkg.SAMLSPConfig{EntityID: origin + "/api/v1/saml/metadata", AcsURL: origin + "/api/v1/saml/acs", MetadataURL: origin + "/api/v1/saml/metadata", IDPMetadataURL: provider.URL + "/metadata", Key: spKey, Certificate: spCert}, accesspkg.SQLIdentityBridge{DB: store.DB, Q: store.Q, TrustedIssuers: map[string]string{provider.URL + "/metadata": "identity-directory"}})
				if err != nil {
					t.Fatal(err)
				}
				meta, err := samlsp.ParseMetadata([]byte(sp.Metadata()))
				if err != nil {
					t.Fatal(err)
				}
				meta.SPSSODescriptors[0].KeyDescriptors = nil // signed plaintext assertion; encryption is a separate contract
				providerMu.Lock()
				spMeta = meta
				providerMu.Unlock()
				app := fiber.New(fiber.Config{DisableStartupMessage: true})
				if err := SCIMRoutes(app, *store, pgSCIMQuery{DB: store.DB, Q: store.Q}, nil); err != nil {
					t.Fatal(err)
				}
				api := app.Group("/api/v1")
				httpapi.RegisterAuthAPI(api, auth)
				browser.routes(api)
				samlRoutes(api, sp, *auth, "/chat")
				authDB := &httpapi.DBRef{DB: store.DB}
				api.Use(func(c *fiber.Ctx) error { c.Locals("auth_db", authDB); return c.Next() })
				api.Use(httpapi.JWTMiddlewareWithOIDC(auth.JWTSecret, true, external, auth.CookieOrigins...))
				api.Use(httpapi.APIKeyPermissionMiddleware())
				api.Use(httpapi.TenantMiddleware(httpapi.AccessConfig{DB: store.DB}))
				httpapi.RegisterAPI(api, httpapi.APIConfig{DB: store.DB, RequireAuth: true, JWTSecret: auth.JWTSecret, AuthCookieOrigins: auth.CookieOrigins, StoragePath: storagePath, WorkspacePath: filepath.Join(storagePath, "workspace")})
				current.Store(app)
			}
			rebuild()
			wire := func(method, path, body string, cookie *http.Cookie, headers map[string]string, want int) identitySandboxReply {
				t.Helper()
				response, err := identitySandboxHTTP(client, method, origin+path, body, cookie, headers)
				if err != nil {
					t.Fatal(err)
				}
				if response.status != want {
					t.Fatalf("%s %s status=%d want=%d body=%s", method, path, response.status, want, response.body)
				}
				return response
			}
			scim := func(method, path, body string, want int) identitySandboxReply {
				return wire(method, path, body, nil, map[string]string{"Authorization": "Bearer " + scimTestToken, "Content-Type": "application/scim+json"}, want)
			}
			first := identitySandboxJSON(t, scim("POST", "/scim/v2/Users", `{"externalId":"owner-subject","userName":"owner@stored.test","active":true}`, 201))["id"].(string)
			second := identitySandboxJSON(t, scim("POST", "/scim/v2/Users", `{"externalId":"reader-subject","userName":"reader@stored.test","active":true}`, 201))["id"].(string)
			if first != accesspkg.SCIMUserID("identity-directory", "owner-subject") || second != accesspkg.SCIMUserID("identity-directory", "reader-subject") || first == second {
				t.Fatal("provisioning did not bind exact immutable identity")
			}
			// Provisioning establishes identity; tenant admission remains an explicit
			// administrator action, required before managed-group membership.
			for _, id := range []string{first, second} {
				if _, err := store.DB.Exec(store.Q("INSERT INTO user_tenant(user_id,tenant_id) VALUES($1,'identity-tenant')"), id); err != nil {
					t.Fatal(err)
				}
			}
			group := identitySandboxJSON(t, scim("POST", "/scim/v2/Groups", fmt.Sprintf(`{"externalId":"readers","displayName":"Readers","members":[{"value":%q}]}`, second), 201))["id"].(string)
			groupPath := "/scim/v2/Groups/" + group
			cookieHeaders := func(cookies []*http.Cookie) map[string]string {
				values := make([]string, 0, len(cookies))
				for _, c := range cookies {
					values = append(values, c.Name+"="+c.Value)
				}
				return map[string]string{"Cookie": strings.Join(values, "; ")}
			}
			oidcStart := func(subject string) (string, []*http.Cookie) {
				response := wire("GET", "/api/v1/auth/oidc/login", "", nil, nil, 302)
				authorization, err := url.Parse(response.header.Get("Location"))
				if err != nil {
					t.Fatal(err)
				}
				query := authorization.Query()
				query.Set("sandbox_subject", subject)
				authorization.RawQuery = query.Encode()
				authorized, err := identitySandboxHTTP(client, "GET", authorization.String(), "", nil, nil)
				if err != nil || authorized.status != 302 {
					t.Fatalf("OIDC provider authorization=%+v err=%v", authorized, err)
				}
				return strings.TrimPrefix(authorized.header.Get("Location"), origin), response.cookies
			}
			oidcCallback := func(path string, cookies []*http.Cookie, want int) identitySandboxReply {
				return wire("GET", path, "", nil, cookieHeaders(cookies), want)
			}
			oidcLogin := func(subject, id, email string) *http.Cookie {
				path, cookies := oidcStart(subject)
				return identitySandboxCookie(t, oidcCallback(path, cookies, 303), id, email)
			}
			samlStart := func(subject string) (string, []*http.Cookie) {
				response := wire("GET", "/api/v1/saml/login", "", nil, nil, 302)
				authorization, err := url.Parse(response.header.Get("Location"))
				if err != nil {
					t.Fatal(err)
				}
				query := authorization.Query()
				query.Set("sandbox_subject", subject)
				authorization.RawQuery = query.Encode()
				signed, err := identitySandboxHTTP(client, "GET", authorization.String(), "", nil, nil)
				if err != nil || signed.status != 200 {
					t.Fatalf("SAML provider response=%+v err=%v", signed, err)
				}
				return string(signed.body), response.cookies
			}
			samlCallback := func(body string, cookies []*http.Cookie, want int) identitySandboxReply {
				headers := cookieHeaders(cookies)
				headers["Content-Type"] = "application/x-www-form-urlencoded"
				return wire("POST", "/api/v1/saml/acs", body, nil, headers, want)
			}
			samlLogin := func(subject, id, email string) *http.Cookie {
				body, cookies := samlStart(subject)
				return identitySandboxCookie(t, samlCallback(body, cookies, 303), id, email)
			}
			owner := oidcLogin("owner-subject", first, "owner@stored.test")
			reader := oidcLogin("reader-subject", second, "reader@stored.test")
			samlReader := samlLogin("reader-subject", second, "reader@stored.test")
			wire("GET", "/api/v1/auth/me", "", reader, nil, 200)
			wire("GET", "/api/v1/auth/me", "", samlReader, nil, 200)
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := store.DB.Exec(store.Q(query), args...); err != nil {
					t.Fatal(err)
				}
			}
			exec("UPDATE tenants SET owner_id=$1 WHERE id='identity-tenant'", first)
			exec("INSERT INTO datasets(id,name,owner_id) VALUES('identity-docs','Identity Documents',$1)", first)
			raw := []byte("protected identity sandbox document")
			rawPath := filepath.Join(storagePath, "identity.txt")
			if err := os.WriteFile(rawPath, raw, 0600); err != nil {
				t.Fatal(err)
			}
			exec("INSERT INTO data(id,name,owner_id,raw_data_location,data_size,raw_content_hash) VALUES('identity-document','Identity Document',$1,$2,$3,$4)", first, "file://"+rawPath, len(raw), fmt.Sprintf("%x", sha256.Sum256(raw)))
			exec("INSERT INTO dataset_data(dataset_id,data_id) VALUES('identity-docs','identity-document')")
			document := "/api/v1/datasets/identity-docs/data/identity-document"
			mutationHeaders := map[string]string{"Content-Type": "application/json", "Origin": origin}
			registered := identitySandboxJSON(t, wire("POST", document+"/policy", `{"tenant_id":"identity-tenant","mode":"restricted"}`, owner, mutationHeaders, 200))
			revision := int64(registered["acl_revision"].(float64))
			wire("GET", document+"/raw", "", reader, nil, 403)
			granted := identitySandboxJSON(t, wire("POST", document+"/grants", fmt.Sprintf(`{"acl_revision":%d,"principal_kind":"group","principal_id":%q,"role":"viewer"}`, revision, group), owner, mutationHeaders, 200))
			revision = int64(granted["acl_revision"].(float64))
			assertRaw := func(cookie *http.Cookie, want int) {
				response := wire("GET", document+"/raw", "", cookie, nil, want)
				if want == 200 && string(response.body) != string(raw) {
					t.Fatalf("raw contents changed: %q", response.body)
				}
			}
			assertRaw(reader, 200)
			assertRaw(samlReader, 200)

			// SCIM rename changes mutable display identity, never the owner key.
			renamed := identitySandboxJSON(t, scim("PATCH", "/scim/v2/Users/"+first, `{"Operations":[{"op":"Replace","path":"userName","value":"renamed-owner@stored.test"}]}`, 200))
			if renamed["id"] != first || renamed["externalId"] != "owner-subject" || renamed["userName"] != "renamed-owner@stored.test" {
				t.Fatalf("rename identity changed: %v", renamed)
			}
			renamedOwner := oidcLogin("owner-subject", first, "renamed-owner@stored.test")
			assertRaw(renamedOwner, 200)
			var dataOwner, datasetOwner string
			if err := store.DB.QueryRow("SELECT owner_id FROM data WHERE id='identity-document'").Scan(&dataOwner); err != nil {
				t.Fatal(err)
			}
			if err := store.DB.QueryRow("SELECT owner_id FROM datasets WHERE id='identity-docs'").Scan(&datasetOwner); err != nil {
				t.Fatal(err)
			}
			if dataOwner != first || datasetOwner != first {
				t.Fatal("rename changed document ownership")
			}

			groupPatch := func(body string, want int) {
				etag := scim("GET", groupPath, "", 200).header.Get("ETag")
				if etag == "" {
					t.Fatal("group ETag absent")
				}
				wire("PATCH", groupPath, body, nil, map[string]string{"Authorization": "Bearer " + scimTestToken, "Content-Type": "application/scim+json", "If-Match": etag}, want)
			}
			groupPatch(fmt.Sprintf(`{"Operations":[{"op":"remove","path":%q}]}`, `members[value eq "`+second+`"]`), 200)
			assertRaw(reader, 403)
			assertRaw(samlReader, 403)
			groupPatch(fmt.Sprintf(`{"Operations":[{"op":"add","path":"members","value":[{"value":%q}]}]}`, second), 200)
			assertRaw(reader, 200)
			assertRaw(samlReader, 200)
			etag := scim("GET", groupPath, "", 200).header.Get("ETag")
			type patchOutcome struct {
				response identitySandboxReply
				err      error
			}
			outcomes := make(chan patchOutcome, 2)
			start := make(chan struct{})
			var wait sync.WaitGroup
			for _, name := range []string{"Concurrent A", "Concurrent B"} {
				wait.Add(1)
				go func(name string) {
					defer wait.Done()
					<-start
					response, err := identitySandboxHTTP(client, "PATCH", origin+groupPath, fmt.Sprintf(`{"Operations":[{"op":"replace","path":"displayName","value":%q}]}`, name), nil, map[string]string{"Authorization": "Bearer " + scimTestToken, "Content-Type": "application/scim+json", "If-Match": etag})
					outcomes <- patchOutcome{response, err}
				}(name)
			}
			close(start)
			wait.Wait()
			close(outcomes)
			winners, preconditions := 0, 0
			for outcome := range outcomes {
				if outcome.err != nil {
					t.Fatal(outcome.err)
				}
				switch outcome.response.status {
				case 200:
					winners++
				case 412:
					preconditions++
				default:
					t.Fatalf("concurrent PATCH=%d %s", outcome.response.status, outcome.response.body)
				}
			}
			if winners != 1 || preconditions != 1 {
				t.Fatalf("same ETag winners=%d preconditions=%d", winners, preconditions)
			}
			currentGroup := identitySandboxJSON(t, scim("GET", groupPath, "", 200))
			if len(currentGroup["members"].([]any)) != 1 {
				t.Fatal("concurrent rename lost membership")
			}

			// Native grant removal and restoration affect both verified provider sessions.
			revoked := identitySandboxJSON(t, wire("DELETE", document+"/grants/group/"+group, fmt.Sprintf(`{"acl_revision":%d}`, revision), renamedOwner, mutationHeaders, 200))
			revision = int64(revoked["acl_revision"].(float64))
			assertRaw(reader, 403)
			assertRaw(samlReader, 403)
			restored := identitySandboxJSON(t, wire("POST", document+"/grants", fmt.Sprintf(`{"acl_revision":%d,"principal_kind":"group","principal_id":%q,"role":"viewer"}`, revision, group), renamedOwner, mutationHeaders, 200))
			if _, ok := restored["acl_revision"].(float64); !ok {
				t.Fatal("restored grant response lacks numeric ACL revision")
			}
			assertRaw(reader, 200)

			pendingOIDC, pendingOIDCCookies := oidcStart("reader-subject")
			pendingSAML, pendingSAMLCookies := samlStart("reader-subject")
			rebuild() // same SQL, JWT secret, SP key/certificate, provider and TLS endpoint
			oidcCallback(pendingOIDC, pendingOIDCCookies, 401)
			samlCallback(pendingSAML, pendingSAMLCookies, 401)
			wire("GET", "/api/v1/auth/me", "", reader, nil, 200)
			assertRaw(reader, 200)
			assertRaw(samlReader, 200)
			freshSAML := samlLogin("reader-subject", second, "reader@stored.test")
			assertRaw(freshSAML, 200)
			wire("POST", "/api/v1/auth/logout", "", reader, map[string]string{"Origin": origin}, 204)
			wire("GET", "/api/v1/auth/me", "", reader, nil, 401)
			assertRaw(reader, 401)
			wire("GET", "/api/v1/auth/me", "", freshSAML, nil, 200)
			assertRaw(freshSAML, 200)
			scim("PATCH", "/scim/v2/Users/"+second, `{"Operations":[{"op":"Replace","path":"active","value":false}]}`, 200)
			wire("GET", "/api/v1/auth/me", "", freshSAML, nil, 401)
			assertRaw(freshSAML, 401)
			assertRaw(samlReader, 401)
			inactivePath, inactiveCookies := oidcStart("reader-subject")
			oidcCallback(inactivePath, inactiveCookies, 401)
			inactiveBody, inactiveSAMLCookies := samlStart("reader-subject")
			samlCallback(inactiveBody, inactiveSAMLCookies, 401)
			assertRaw(renamedOwner, 200)
			var count int
			if err := store.DB.QueryRow("SELECT COUNT(*) FROM users").Scan(&count); err != nil || count != 2 {
				t.Fatalf("identity split/merge after protocols: users=%d err=%v", count, err)
			}
			identityID, err := store.Lookup(ctx, "identity-directory", "owner-subject")
			if err != nil || identityID != first {
				t.Fatalf("persisted owner binding=%q err=%v", identityID, err)
			}
			identityID, err = store.Lookup(ctx, "identity-directory", "reader-subject")
			if err != nil || identityID != second {
				t.Fatalf("persisted reader binding=%q err=%v", identityID, err)
			}
			if err := store.DB.QueryRow("SELECT owner_id FROM data WHERE id='identity-document'").Scan(&dataOwner); err != nil || dataOwner != first {
				t.Fatalf("persisted owner changed=%q err=%v", dataOwner, err)
			}
			t.Log("Observed local TLS SCIM/OIDC/SAML/document lifecycle and handler-object reconstruction; no vendor certification or executable restart claimed")
		})
	}
}
