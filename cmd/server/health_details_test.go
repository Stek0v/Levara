package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
)

func TestHealthDetailsHandlesUnavailablePostgresAndDisabledGRPC(t *testing.T) {
	t.Setenv("LLM_ENDPOINT", "")
	t.Setenv("WHISPER_ENDPOINT", "")
	t.Setenv("VISION_ENDPOINT", "")

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	registerHealthDetails(app, healthDeps{dbProvider: "postgres", pgDB: nil, grpcPort: 0})

	resp, err := app.Test(httptest.NewRequest("GET", "/health/details", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	var body struct {
		Services map[string]map[string]any `json:"services"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if got := body.Services["postgres"]["status"]; got != "unavailable" {
		t.Fatalf("postgres status=%v, want unavailable", got)
	}
	if got := body.Services["grpc"]["status"]; got != "disabled" {
		t.Fatalf("grpc status=%v, want disabled", got)
	}
	if got := body.Services["collections"]["status"]; got != "unavailable" {
		t.Fatalf("collections status=%v, want unavailable", got)
	}
}

func TestHealthDetailsProbesAuthenticatedLLMAndRerank(t *testing.T) {
	const apiKey = "health-test-secret"
	seen := make(chan string, 2)
	dependency := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.URL.Path + " " + r.Header.Get("Authorization")
		if r.URL.Path == "/v1/models" {
			_, _ = w.Write([]byte(`{"data":[{"id":"configured-model"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer dependency.Close()
	t.Setenv("LLM_ENDPOINT", dependency.URL+"/v1/")
	t.Setenv("LLM_MODEL", "configured-model")
	t.Setenv("LLM_API_KEY", apiKey)
	t.Setenv("WHISPER_ENDPOINT", "")
	t.Setenv("VISION_ENDPOINT", "")

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	registerHealthDetails(app, healthDeps{
		rerankEndpoint: dependency.URL + "/rerank/",
		rerankModel:    "rerank-model",
	})
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/health/details", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Services map[string]map[string]any `json:"services"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if got := body.Services["llm"]["status"]; got != "connected" {
		t.Fatalf("llm status=%v, want connected", got)
	}
	if got := body.Services["rerank"]["status"]; got != "connected" {
		t.Fatalf("rerank status=%v, want connected", got)
	}
	got := map[string]bool{<-seen: true, <-seen: true}
	if !got["/v1/models Bearer "+apiKey] {
		t.Fatalf("LLM probe did not send bearer token to /v1/models: %v", got)
	}
	if !got["/health "] {
		t.Fatalf("rerank probe leaked bearer token or used wrong path: %v", got)
	}
	raw, _ := json.Marshal(body)
	if string(raw) == "" || strings.Contains(string(raw), apiKey) {
		t.Fatal("health response leaked LLM API key")
	}
}

func TestHealthDetailsReportsProbeFailuresWithoutResponseBodies(t *testing.T) {
	const responseSecret = "provider-private-body"
	dependency := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, responseSecret, http.StatusUnauthorized)
	}))
	defer dependency.Close()
	t.Setenv("LLM_ENDPOINT", dependency.URL)
	t.Setenv("LLM_API_KEY", "wrong-key")
	t.Setenv("WHISPER_ENDPOINT", "")
	t.Setenv("VISION_ENDPOINT", "")

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	registerHealthDetails(app, healthDeps{rerankEndpoint: "://bad"})
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/health/details", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Services map[string]map[string]any `json:"services"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if got := body.Services["llm"]["status"]; got != "unavailable" {
		t.Fatalf("llm status=%v, want unavailable", got)
	}
	if got := body.Services["llm"]["http_status"]; got != float64(http.StatusUnauthorized) {
		t.Fatalf("llm http_status=%v, want 401", got)
	}
	if got := body.Services["rerank"]["status"]; got != "error" {
		t.Fatalf("rerank status=%v, want error", got)
	}
	raw, _ := json.Marshal(body)
	if strings.Contains(string(raw), responseSecret) {
		t.Fatal("health response leaked dependency response body")
	}
	if strings.Contains(string(raw), "://bad") {
		t.Fatal("health response leaked rejected endpoint")
	}
}

func TestDependencyProbeDoesNotForwardBearerOnRedirect(t *testing.T) {
	redirected := make(chan struct{}, 1)
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirected <- struct{}{}
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer redirect-secret" {
			t.Errorf("authorization=%q, want bearer on configured endpoint", got)
		}
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer source.Close()

	result := probeHTTPDependency(source.URL, "model", "models", "redirect-secret")
	if got := result["status"]; got != "unavailable" {
		t.Fatalf("status=%v, want unavailable for redirect", got)
	}
	select {
	case <-redirected:
		t.Fatal("health probe followed redirect and forwarded the request")
	default:
	}
}

func TestDependencyProbeTimesOut(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	oldClient := healthProbeClient
	healthProbeClient = &http.Client{Timeout: 5 * time.Millisecond, CheckRedirect: oldClient.CheckRedirect}
	defer func() { healthProbeClient = oldClient }()
	if got := probeHTTPDependency(server.URL, "model", "models", "")["status"]; got != "unreachable" {
		t.Fatalf("status=%v, want unreachable after timeout", got)
	}
}

func TestDependencyProbeURLHandlesFullEndpointPaths(t *testing.T) {
	tests := map[string]struct {
		endpoint string
		probe    string
		want     string
	}{
		"llm completion": {"https://example.test/v1/chat/completions/?token=ignored", "models", "https://example.test/v1/models"},
		"rerank":         {"http://127.0.0.1:9100/api/rerank/", "health", "http://127.0.0.1:9100/api/health"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := dependencyProbeURL(tt.endpoint, tt.probe)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("url=%q, want %q", got, tt.want)
			}
		})
	}
}

func TestDependencyProbeSanitizesDisplayedEndpoint(t *testing.T) {
	const querySecret = "query-secret-value"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"configured-model"}]}`))
	}))
	defer server.Close()

	result := probeHTTPDependency(server.URL+"/v1?api_key="+querySecret+"#fragment-secret", "configured-model", "models", "")
	if got := result["endpoint"]; got != server.URL+"/v1" {
		t.Fatalf("endpoint=%v, want sanitized URL", got)
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), querySecret) || strings.Contains(string(raw), "fragment-secret") {
		t.Fatalf("sanitized result leaked endpoint secret: %s", raw)
	}
}

func TestDependencyProbeOmitsRejectedEndpoint(t *testing.T) {
	const userInfoSecret = "userinfo-secret"
	result := probeHTTPDependency("http://user:"+userInfoSecret+"@example.test/v1", "model", "models", "")
	if got := result["status"]; got != "error" {
		t.Fatalf("status=%v, want error", got)
	}
	if _, ok := result["endpoint"]; ok {
		t.Fatalf("rejected endpoint was returned: %v", result["endpoint"])
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), userInfoSecret) || strings.Contains(string(raw), "user") {
		t.Fatalf("rejected endpoint leaked userinfo: %s", raw)
	}
}

func TestDependencyProbeChecksBoundedStandardModelList(t *testing.T) {
	tests := map[string]struct {
		body       string
		wantStatus string
		wantReason string
	}{
		"present": {
			body:       `{"data":[{"id":"other"},{"id":"configured-model"}]}`,
			wantStatus: "connected",
		},
		"absent": {
			body:       `{"data":[{"id":"other"}]}`,
			wantStatus: "unavailable",
			wantReason: "configured_model_not_listed",
		},
		"nonstandard": {
			body:       `{"ok":true}`,
			wantStatus: "unverified",
			wantReason: "model_list_unverified",
		},
		"malformed": {
			body:       `{"data":`,
			wantStatus: "unverified",
			wantReason: "model_list_unverified",
		},
		"oversized": {
			body:       `{"data":[],"padding":"` + strings.Repeat("x", maxDependencyProbeBody) + `"}`,
			wantStatus: "unverified",
			wantReason: "model_list_unverified",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()
			result := probeHTTPDependency(server.URL, "configured-model", "models", "")
			if got := result["status"]; got != tt.wantStatus {
				t.Fatalf("status=%v, want %s", got, tt.wantStatus)
			}
			gotReason, hasReason := result["reason"]
			if tt.wantReason == "" && hasReason {
				t.Fatalf("unexpected reason=%v", gotReason)
			}
			if tt.wantReason != "" && gotReason != tt.wantReason {
				t.Fatalf("reason=%v, want %q", gotReason, tt.wantReason)
			}
		})
	}
}

func TestDependencyProbeVerifiesRerankHealthBody(t *testing.T) {
	tests := map[string]struct {
		body       string
		wantStatus string
		wantReason string
	}{
		"ready":       {`{"ok":true}`, "connected", ""},
		"not ready":   {`{"ok":false}`, "unavailable", "health_not_ready"},
		"nonstandard": {`{"status":"ok"}`, "unverified", "health_response_unverified"},
		"malformed":   {`{"ok":`, "unverified", "health_response_unverified"},
		"oversized":   {`{"ok":true,"padding":"` + strings.Repeat("x", maxDependencyProbeBody) + `"}`, "unverified", "health_response_unverified"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()
			result := probeHTTPDependency(server.URL+"/rerank", "rerank-model", "health", "")
			if got := result["status"]; got != tt.wantStatus {
				t.Fatalf("status=%v, want %s", got, tt.wantStatus)
			}
			gotReason, hasReason := result["reason"]
			if tt.wantReason == "" && hasReason {
				t.Fatalf("unexpected reason=%v", gotReason)
			}
			if tt.wantReason != "" && gotReason != tt.wantReason {
				t.Fatalf("reason=%v, want %q", gotReason, tt.wantReason)
			}
		})
	}
}

func TestHealthDetailsDoesNotReportSQLiteAsPostgres(t *testing.T) {
	t.Setenv("LLM_ENDPOINT", "")
	t.Setenv("WHISPER_ENDPOINT", "")
	db, err := sql.Open("sqlite3", "file:"+t.TempDir()+"/health.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	registerHealthDetails(app, healthDeps{dbProvider: "sqlite", pgDB: db})
	resp, err := app.Test(httptest.NewRequest("GET", "/health/details", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Services map[string]map[string]any `json:"services"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if got := body.Services["database"]["status"]; got != "connected" {
		t.Fatalf("database status=%v, want connected", got)
	}
	if got := body.Services["postgres"]["status"]; got != "not_configured" {
		t.Fatalf("postgres status=%v, want not_configured", got)
	}
}
