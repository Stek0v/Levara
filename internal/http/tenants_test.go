package http

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	_ "github.com/ncruces/go-sqlite3/driver"
)

func TestTenantMiddlewareHeaderRequiresMembership(t *testing.T) {
	db := newTenantTestDB(t)

	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		c.Locals("user_id", "user-a")
		return c.Next()
	})
	app.Use(TenantMiddleware(AccessConfig{DB: db}))
	app.Get("/tenant", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"tenant_id": ResolveTenantID(c)})
	})

	req := httptest.NewRequest("GET", "/tenant", nil)
	req.Header.Set("X-Tenant-Id", "tenant-a")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
}

func TestTenantMiddlewareHeaderRejectsNonMember(t *testing.T) {
	db := newTenantTestDB(t)

	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		c.Locals("user_id", "user-a")
		return c.Next()
	})
	app.Use(TenantMiddleware(AccessConfig{DB: db}))
	app.Get("/tenant", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"tenant_id": ResolveTenantID(c)})
	})

	req := httptest.NewRequest("GET", "/tenant", nil)
	req.Header.Set("X-Tenant-Id", "tenant-b")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 403 {
		t.Fatalf("status=%d, want 403", resp.StatusCode)
	}
}

func TestTenantFilterSQLUsesBindArg(t *testing.T) {
	old := GetDBProvider()
	SetDBProvider(DBPostgres)
	t.Cleanup(func() { SetDBProvider(old) })

	tenantID := "tenant-a' OR 1=1 --"
	clause, args := TenantFilterSQL(tenantID, 3)
	if strings.Contains(clause, tenantID) {
		t.Fatalf("tenant filter clause contains raw tenant id: %s", clause)
	}
	if !strings.Contains(clause, "$3") {
		t.Fatalf("tenant filter clause=%q, want $3 placeholder", clause)
	}
	if len(args) != 1 || args[0] != tenantID {
		t.Fatalf("args=%v, want tenant id as bind arg", args)
	}
}

func TestTenantAdministrationRequiresOwnerOrSuperuser(t *testing.T) {
	db := newTenantAdminTestDB(t)
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(func(c *fiber.Ctx) error {
		if userID := c.Get("X-Test-User"); userID != "" {
			c.Locals("user_id", userID)
		}
		return c.Next()
	})
	RegisterTenantAPI(app, APIConfig{DB: db, RequireAuth: true})

	resp := tenantTestRequest(t, app, "GET", "/tenants", "member-a", nil)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("member list status=%d, want 200", resp.StatusCode)
	}
	var listed []struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if len(listed) != 1 || listed[0].ID != "tenant-a" {
		t.Fatalf("member tenant list=%v, want only tenant-a", listed)
	}

	resp = tenantTestRequest(t, app, "POST", "/tenants/tenant-b/users", "member-a", map[string]any{"user_id": "member-a"})
	if resp.StatusCode != fiber.StatusForbidden {
		t.Fatalf("foreign member add status=%d, want 403", resp.StatusCode)
	}
	_ = resp.Body.Close()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM user_tenant WHERE user_id = 'member-a' AND tenant_id = 'tenant-b'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("denied caller added itself to foreign tenant")
	}

	for _, actor := range []string{"owner-b", "root"} {
		resp = tenantTestRequest(t, app, "POST", "/tenants/tenant-b/users", actor, map[string]any{"user_id": "new-user"})
		if resp.StatusCode != fiber.StatusCreated {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("%s add status=%d body=%s, want 201", actor, resp.StatusCode, body)
		}
		_ = resp.Body.Close()
	}
}

func TestTenantCreateAndACLMutationsRequireAuthorizedActor(t *testing.T) {
	db := newTenantAdminTestDB(t)
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(func(c *fiber.Ctx) error {
		if userID := c.Get("X-Test-User"); userID != "" {
			c.Locals("user_id", userID)
		}
		return c.Next()
	})
	RegisterTenantAPI(app, APIConfig{DB: db, RequireAuth: true})

	resp := tenantTestRequest(t, app, "POST", "/tenants", "", map[string]any{"name": "anonymous-tenant"})
	if resp.StatusCode != fiber.StatusUnauthorized {
		t.Fatalf("anonymous create status=%d, want 401", resp.StatusCode)
	}
	_ = resp.Body.Close()

	grant := map[string]any{"principal_id": "member-a", "dataset_id": "dataset-b", "permission_type": "read"}
	resp = tenantTestRequest(t, app, "POST", "/acl", "member-a", grant)
	if resp.StatusCode != fiber.StatusForbidden {
		t.Fatalf("foreign ACL grant status=%d, want 403", resp.StatusCode)
	}
	_ = resp.Body.Close()

	resp = tenantTestRequest(t, app, "POST", "/acl", "owner-b", grant)
	if resp.StatusCode != fiber.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("owner ACL grant status=%d body=%s, want 201", resp.StatusCode, body)
	}
	_ = resp.Body.Close()
}

func tenantTestRequest(t *testing.T, app *fiber.App, method, path, userID string, payload any) *http.Response {
	t.Helper()
	var body *bytes.Reader
	if payload == nil {
		body = bytes.NewReader(nil)
	} else {
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(encoded)
	}
	req := httptest.NewRequest(method, path, body)
	req.Header.Set("Content-Type", "application/json")
	if userID != "" {
		req.Header.Set("X-Test-User", userID)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func newTenantAdminTestDB(t *testing.T) *sql.DB {
	t.Helper()
	old := GetDBProvider()
	SetDBProvider(DBSQLite)
	t.Cleanup(func() { SetDBProvider(old) })

	db, err := sql.Open("sqlite3", t.TempDir()+"/tenant-admin.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, stmt := range []string{
		`CREATE TABLE users (id TEXT PRIMARY KEY, is_superuser BOOLEAN DEFAULT FALSE)`,
		`CREATE TABLE tenants (id TEXT PRIMARY KEY, name TEXT UNIQUE, owner_id TEXT, created_at TEXT)`,
		`CREATE TABLE user_tenant (user_id TEXT, tenant_id TEXT, UNIQUE(user_id, tenant_id))`,
		`CREATE TABLE datasets (id TEXT PRIMARY KEY, owner_id TEXT)`,
		`CREATE TABLE dataset_shares (id TEXT PRIMARY KEY, dataset_id TEXT, user_id TEXT, role TEXT)`,
		`CREATE TABLE acl (id TEXT PRIMARY KEY, principal_id TEXT, dataset_id TEXT, permission_type TEXT, UNIQUE(principal_id, dataset_id, permission_type))`,
		`INSERT INTO users(id, is_superuser) VALUES ('owner-a', FALSE), ('owner-b', FALSE), ('member-a', FALSE), ('root', TRUE)`,
		`INSERT INTO tenants(id, name, owner_id, created_at) VALUES ('tenant-a', 'A', 'owner-a', '2026-01-01'), ('tenant-b', 'B', 'owner-b', '2026-01-02')`,
		`INSERT INTO user_tenant(user_id, tenant_id) VALUES ('owner-a', 'tenant-a'), ('member-a', 'tenant-a'), ('owner-b', 'tenant-b')`,
		`INSERT INTO datasets(id, owner_id) VALUES ('dataset-a', 'owner-a'), ('dataset-b', 'owner-b')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
	return db
}

func newTenantTestDB(t *testing.T) *sql.DB {
	t.Helper()
	old := GetDBProvider()
	SetDBProvider(DBSQLite)
	t.Cleanup(func() { SetDBProvider(old) })

	db, err := sql.Open("sqlite3", t.TempDir()+"/tenants.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, stmt := range []string{
		`CREATE TABLE user_tenant (user_id TEXT, tenant_id TEXT)`,
		`INSERT INTO user_tenant(user_id, tenant_id) VALUES ('user-a', 'tenant-a')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
	return db
}
