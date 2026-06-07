package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
)

// The sync manifest must carry the build version so a pull/push can detect
// instance version skew (warn-and-continue policy lives in DoSync).
func TestSyncManifestIncludesVersion(t *testing.T) {
	app := fiber.New()
	RegisterSyncAPI(app, APIConfig{Version: "abc1234", EmbedModel: "potion-256"})

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/sync/manifest", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var m syncManifest
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatal(err)
	}
	if m.Version != "abc1234" {
		t.Fatalf("manifest version=%q, want abc1234", m.Version)
	}
}

// syncAuthGet/syncAuthPost attach Authorization: Bearer only when the token
// is non-empty, preserving the original unauthenticated behaviour otherwise.
func TestSyncAuthHeaderInjection(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := srv.Client()

	cases := []struct {
		name  string
		token string
		want  string
	}{
		{"with token", "tok-123", "Bearer tok-123"},
		{"empty token", "", ""},
	}
	for _, tc := range cases {
		t.Run("GET "+tc.name, func(t *testing.T) {
			gotAuth = "sentinel"
			resp, err := syncAuthGet(client, srv.URL, tc.token)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if gotAuth != tc.want {
				t.Fatalf("GET Authorization=%q, want %q", gotAuth, tc.want)
			}
		})
		t.Run("POST "+tc.name, func(t *testing.T) {
			gotAuth = "sentinel"
			resp, err := syncAuthPost(client, srv.URL, "application/json", "{}", tc.token)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if gotAuth != tc.want {
				t.Fatalf("POST Authorization=%q, want %q", gotAuth, tc.want)
			}
		})
	}
}
