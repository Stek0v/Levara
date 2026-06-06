package http

import (
	"database/sql"
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
