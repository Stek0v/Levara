package http

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
)

// An alternate writer must not bypass the namespace enforced by diary tools.
func TestMemoryRESTCannotOverwriteAuthenticatedDiary(t *testing.T) {
	t.Setenv("LEVARA_MCP_TOOLSET", "full")
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		h := asyncAuthorityHandler(f)
		app := asyncAuthorityApp(h, nil)
		api := app.Group("/api/v1", func(c *fiber.Ctx) error {
			c.Locals("auth_db", &DBRef{DB: f.db})
			return c.Next()
		}, JWTMiddleware(h.cfg.JWTSecret, true), APIKeyPermissionMiddleware(), TenantMiddleware(AccessConfig{DB: f.db}))
		RegisterMemoryAPI(api, h.cfg)
		for _, transport := range []struct{ name, path string }{{"legacy", "/mcp"}, {"latest", latestMCPPath}} {
			t.Run(transport.name, func(t *testing.T) {
				collection := "rest-diary-" + transport.name
				args := map[string]any{"agent": "reviewer", "collection": collection, "key": "entry", "value": "peer-private"}
				written := asyncAuthorityRPC(t, app, h, transport.path, "peer", "diary_write", args)
				if written.IsError {
					t.Fatalf("public diary seed failed: %s", diaryScopeRaw(written))
				}
				var id, namespace string
				if err := f.db.QueryRow(Q("SELECT id,owner_id FROM memories WHERE key=$1 AND collection_name=$2"), "entry", collection).Scan(&id, &namespace); err != nil {
					t.Fatal(err)
				}
				before := diaryScopeSnapshot(t, f)
				body, err := json.Marshal(map[string]any{"key": "entry", "value": "REST-forged-text", "owner_id": namespace, "collection_name": collection})
				if err != nil {
					t.Fatal(err)
				}
				req := httptest.NewRequest("POST", "/api/v1/memories", strings.NewReader(string(body)))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", "Bearer "+createJWT("owner", "owner@test.invalid", h.cfg.JWTSecret))
				req.Header.Set("X-Tenant-Id", "a")
				resp, err := app.Test(req, 10000)
				if err != nil {
					t.Fatal(err)
				}
				raw, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
				if resp.StatusCode != 403 {
					t.Errorf("forged REST owner must be forbidden: status=%d body=%s victim=%s", resp.StatusCode, raw, id)
				}
				if !reflect.DeepEqual(before, diaryScopeSnapshot(t, f)) {
					t.Error("forged REST owner changed persisted memories")
				}
				read := asyncAuthorityRPC(t, app, h, transport.path, "peer", "diary_read", map[string]any{"agent": "reviewer", "collection": collection})
				entries := diaryScopeEntries(t, read)
				if len(entries) != 1 || entries[0].Value != "peer-private" {
					t.Errorf("victim diary read accepted forged REST text: %+v", entries)
				}
			})
		}
	})
}

func TestMemoryRESTOwnerControls(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		for _, requireAuth := range []bool{true, false} {
			cfg := f.cfg
			cfg.RequireAuth = requireAuth
			cfg.JWTSecret = "rest-owner-controls"
			app := fiber.New(fiber.Config{DisableStartupMessage: true})
			api := app.Group("/api/v1", func(c *fiber.Ctx) error {
				c.Locals("auth_db", &DBRef{DB: f.db})
				return c.Next()
			}, JWTMiddleware(cfg.JWTSecret, requireAuth), APIKeyPermissionMiddleware())
			RegisterMemoryAPI(api, cfg)
			post := func(user, owner, value string) (int, string) {
				t.Helper()
				body, err := json.Marshal(map[string]any{"key": "control", "value": value, "owner_id": owner, "collection_name": "rest-owner-controls"})
				if err != nil {
					t.Fatal(err)
				}
				req := httptest.NewRequest("POST", "/api/v1/memories", strings.NewReader(string(body)))
				req.Header.Set("Content-Type", "application/json")
				if user != "" {
					req.Header.Set("Authorization", "Bearer "+createJWT(user, user+"@test.invalid", cfg.JWTSecret))
				}
				resp, err := app.Test(req, 10000)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				raw, err := io.ReadAll(resp.Body)
				if err != nil {
					t.Fatal(err)
				}
				return resp.StatusCode, string(raw)
			}
			status, raw := post("peer", "", "own-first")
			if status != 201 {
				t.Fatalf("own omitted owner failed requireAuth=%v: %d %s", requireAuth, status, raw)
			}
			var first struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal([]byte(raw), &first); err != nil || first.ID == "" {
				t.Fatalf("invalid save response: %s error=%v", raw, err)
			}
			status, raw = post("peer", "peer", "own-updated")
			var updated struct {
				ID string `json:"id"`
			}
			err := json.Unmarshal([]byte(raw), &updated)
			if status != 201 || err != nil || updated.ID != first.ID {
				t.Fatalf("own explicit owner lost canonical ID: status=%d body=%s error=%v", status, raw, err)
			}
			var owner, value string
			if err := f.db.QueryRow(Q("SELECT owner_id,value FROM memories WHERE id=$1"), first.ID).Scan(&owner, &value); err != nil || owner != "peer" || value != "own-updated" {
				t.Fatalf("own persisted control: owner=%q value=%q error=%v", owner, value, err)
			}
			before := diaryScopeSnapshot(t, f)
			for _, forged := range []string{"owner", "agent:reviewer", diaryPolicyNamespace("peer", "a", "reviewer")} {
				status, raw = post("peer", forged, "forged")
				if status != 403 {
					t.Errorf("JWT forged owner accepted requireAuth=%v owner=%q: %d %s", requireAuth, forged, status, raw)
				}
			}
			if !reflect.DeepEqual(before, diaryScopeSnapshot(t, f)) {
				t.Error("rejected writer changed controls")
			}
			if !requireAuth {
				status, raw = post("", "agent:local", "local-compatible")
				if status != 201 {
					t.Fatalf("anonymous local explicit owner compatibility: %d %s", status, raw)
				}
				var local string
				if err := f.db.QueryRow(Q("SELECT value FROM memories WHERE key=$1 AND owner_id=$2 AND collection_name=$3"), "control", "agent:local", "rest-owner-controls").Scan(&local); err != nil || local != "local-compatible" {
					t.Fatalf("local persistence: value=%q error=%v", local, err)
				}
			} else {
				status, raw = post("", "peer", "anonymous-forged")
				if status != 401 {
					t.Errorf("required authentication accepted anonymous: %d %s", status, raw)
				}
			}
		}
	})
}
