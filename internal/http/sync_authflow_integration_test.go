package http

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/adaptor"
	_ "github.com/ncruces/go-sqlite3/driver"
)

// End-to-end auth flow for cross-instance sync: a remote Levara instance with
// requireAuth=true is fronted by JWTMiddleware, and the local client drives
// pull/push through the real sync helpers. Exercises every combination of
// {valid token, no token} × {pull, push} × {version match, mismatch}, mirroring
// the Mac(:8081 auth-gated) ↔ Pi(:8090) topology.

const syncTestSecret = "integration-secret-0123456789abcd"

// newSyncIntegDB builds a sqlite DB with the full sync schema (memories +
// interactions + graph). The caller owns provider state (set once per test).
func newSyncIntegDB(t *testing.T, name string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(t.TempDir(), name+".db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		CREATE TABLE memories (
			id TEXT PRIMARY KEY, key TEXT NOT NULL, value TEXT NOT NULL,
			type TEXT NOT NULL DEFAULT 'project', owner_id TEXT NOT NULL DEFAULT '',
			collection_name TEXT NOT NULL DEFAULT '', room TEXT NOT NULL DEFAULT '',
			hall TEXT NOT NULL DEFAULT '', is_pinned BOOLEAN NOT NULL DEFAULT FALSE,
			pin_priority INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
			UNIQUE(key, owner_id)
		);
		CREATE TABLE interactions (
			id TEXT PRIMARY KEY, session_id TEXT, user_id TEXT, query TEXT,
			response TEXT, search_type TEXT, created_at TEXT NOT NULL
		);
		CREATE TABLE graph_nodes (
			id TEXT PRIMARY KEY, name TEXT, type TEXT, description TEXT,
			properties TEXT, dataset_id TEXT NOT NULL DEFAULT ''
		);
		CREATE TABLE graph_edges (
			id TEXT PRIMARY KEY, source_id TEXT, target_id TEXT,
			relationship_name TEXT, properties TEXT, valid_from TEXT, valid_until TEXT,
			superseded_by TEXT NOT NULL DEFAULT '', confidence REAL NOT NULL DEFAULT 1.0,
			dataset_id TEXT NOT NULL DEFAULT ''
		);
	`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	return db
}

// startAuthRemote spins up an auth-protected remote instance over httptest and
// returns the base URL (including /api/v1, matching the real sync remote_url).
func startAuthRemote(t *testing.T, db *sql.DB, version string) string {
	t.Helper()
	app := fiber.New()
	api := app.Group("/api/v1")
	api.Use(JWTMiddleware(syncTestSecret, true)) // requireAuth=true
	RegisterSyncAPI(api, APIConfig{DB: db, Version: version, EmbedModel: "potion-256"})
	srv := httptest.NewServer(adaptor.FiberApp(app))
	t.Cleanup(srv.Close)
	return srv.URL + "/api/v1"
}

func seedMemory(t *testing.T, db *sql.DB, key, val string) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO memories (id, key, value, type, owner_id, collection_name, room, hall,
			is_pinned, pin_priority, created_at, updated_at)
		VALUES ('id-'||?, ?, ?, 'project', 'user-1', 'levara', 'sync', 'event',
			FALSE, 0, '2026-06-04T00:00:00Z', '2026-06-04T01:00:00Z')`, key, key, val); err != nil {
		t.Fatal(err)
	}
}

func memoryExists(t *testing.T, db *sql.DB, key string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM memories WHERE key = ?`, key).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

func TestSyncAuthFlowEndToEnd(t *testing.T) {
	SetDBProvider(DBSQLite)
	defer SetDBProvider(DBPostgres)

	validToken := createJWT("user-1", "user@example.com", syncTestSecret)
	if validToken == "" {
		t.Fatal("createJWT returned empty token")
	}

	// ── 1. Raw auth enforcement on the remote manifest endpoint ──
	t.Run("manifest endpoint enforces auth", func(t *testing.T) {
		remoteDB := newSyncIntegDB(t, "remote")
		url := startAuthRemote(t, remoteDB, "remoteSHA")

		// no token → 401
		resp, err := http.Get(url + "/sync/manifest")
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("no-token manifest status=%d, want 401", resp.StatusCode)
		}

		// valid token → 200
		req, _ := http.NewRequest(http.MethodGet, url+"/sync/manifest", nil)
		req.Header.Set("Authorization", "Bearer "+validToken)
		resp2, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp2.Body.Close()
		if resp2.StatusCode != http.StatusOK {
			t.Fatalf("valid-token manifest status=%d, want 200", resp2.StatusCode)
		}
	})

	// ── 2. Manifest helper threads the token and surfaces version ──
	t.Run("SyncManifestFromRemote with valid token returns version", func(t *testing.T) {
		remoteDB := newSyncIntegDB(t, "remote")
		url := startAuthRemote(t, remoteDB, "remoteSHA")

		m, err := SyncManifestFromRemote(url, validToken)
		if err != nil {
			t.Fatalf("manifest fetch: %v", err)
		}
		if m.Version != "remoteSHA" {
			t.Fatalf("manifest version=%q, want remoteSHA", m.Version)
		}
	})

	// ── 3. PULL: remote → local ──
	t.Run("pull with valid token imports data", func(t *testing.T) {
		remoteDB := newSyncIntegDB(t, "remote")
		seedMemory(t, remoteDB, "pull.key", "from remote")
		url := startAuthRemote(t, remoteDB, "v1")

		localDB := newSyncIntegDB(t, "local")
		localCfg := APIConfig{DB: localDB, SyncToken: validToken}
		res := SyncPull(localCfg, url, []string{"memories"}, "")

		if _, bad := res["memories_error"]; bad {
			t.Fatalf("unexpected pull error: %v", res["memories_error"])
		}
		if !memoryExists(t, localDB, "pull.key") {
			t.Fatalf("pull did not import memory: results=%v", res)
		}
	})

	t.Run("pull without token imports nothing", func(t *testing.T) {
		remoteDB := newSyncIntegDB(t, "remote")
		seedMemory(t, remoteDB, "pull.noauth", "from remote")
		url := startAuthRemote(t, remoteDB, "v1")

		localDB := newSyncIntegDB(t, "local")
		localCfg := APIConfig{DB: localDB, SyncToken: ""} // no token
		SyncPull(localCfg, url, []string{"memories"}, "")

		if memoryExists(t, localDB, "pull.noauth") {
			t.Fatal("pull without token leaked data past auth")
		}
	})

	// ── 4. PUSH: local → remote ──
	t.Run("push with valid token exports data", func(t *testing.T) {
		remoteDB := newSyncIntegDB(t, "remote")
		url := startAuthRemote(t, remoteDB, "v1")

		localDB := newSyncIntegDB(t, "local")
		seedMemory(t, localDB, "push.key", "to remote")
		localCfg := APIConfig{DB: localDB, SyncToken: validToken}
		res := syncPush(context.Background(), localCfg, url, []string{"memories"}, "")

		if _, bad := res["memories_error"]; bad {
			t.Fatalf("unexpected push error: %v", res["memories_error"])
		}
		if !memoryExists(t, remoteDB, "push.key") {
			t.Fatalf("push did not reach remote: results=%v", res)
		}
	})

	t.Run("push without token reaches nothing", func(t *testing.T) {
		remoteDB := newSyncIntegDB(t, "remote")
		url := startAuthRemote(t, remoteDB, "v1")

		localDB := newSyncIntegDB(t, "local")
		seedMemory(t, localDB, "push.noauth", "to remote")
		localCfg := APIConfig{DB: localDB, SyncToken: ""} // no token
		syncPush(context.Background(), localCfg, url, []string{"memories"}, "")

		if memoryExists(t, remoteDB, "push.noauth") {
			t.Fatal("push without token wrote past remote auth")
		}
	})

	// ── 5. Version skew via DoSync (warn-and-continue) ──
	t.Run("version mismatch warns but still syncs", func(t *testing.T) {
		remoteDB := newSyncIntegDB(t, "remote")
		seedMemory(t, remoteDB, "skew.key", "from remote")
		url := startAuthRemote(t, remoteDB, "remoteSHA")

		localDB := newSyncIntegDB(t, "local")
		h := &mcpHandler{cfg: APIConfig{DB: localDB, SyncToken: validToken, Version: "localSHA"}}
		result, _, err := h.DoSync(context.Background(), url, "pull", []string{"memories"}, "", nil)
		if err != nil {
			t.Fatalf("DoSync: %v", err)
		}
		if _, ok := result["version_warning"]; !ok {
			t.Fatalf("expected version_warning on skew, got %v", result)
		}
		if !memoryExists(t, localDB, "skew.key") {
			t.Fatal("version mismatch blocked the sync (should warn-and-continue)")
		}
	})

	t.Run("version match produces no warning", func(t *testing.T) {
		remoteDB := newSyncIntegDB(t, "remote")
		seedMemory(t, remoteDB, "match.key", "from remote")
		url := startAuthRemote(t, remoteDB, "sameSHA")

		localDB := newSyncIntegDB(t, "local")
		h := &mcpHandler{cfg: APIConfig{DB: localDB, SyncToken: validToken, Version: "sameSHA"}}
		result, _, err := h.DoSync(context.Background(), url, "pull", []string{"memories"}, "", nil)
		if err != nil {
			t.Fatalf("DoSync: %v", err)
		}
		if _, ok := result["version_warning"]; ok {
			t.Fatalf("unexpected version_warning on match: %v", result["version_warning"])
		}
		if !memoryExists(t, localDB, "match.key") {
			t.Fatal("matched-version sync imported nothing")
		}
	})
}
