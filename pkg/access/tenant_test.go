package access

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/ncruces/go-sqlite3/driver"
)

func TestDefaultTenantForUser(t *testing.T) {
	db := newDefaultTenantTestDB(t)
	policy := SQLPolicy{DB: db, Q: sqliteQ}
	ctx := context.Background()

	if got, err := policy.DefaultTenantForUser(ctx, "user-single"); err != nil || got != "tenant-a" {
		t.Fatalf("single membership = %q err=%v, want tenant-a nil", got, err)
	}

	// Multi-membership: any of the user's tenants is acceptable (LIMIT 1), but it
	// must be one the user actually belongs to and never empty/erroring.
	got, err := policy.DefaultTenantForUser(ctx, "user-multi")
	if err != nil {
		t.Fatalf("multi membership err=%v", err)
	}
	if got != "tenant-a" && got != "tenant-b" {
		t.Fatalf("multi membership = %q, want one of tenant-a/tenant-b", got)
	}

	if got, err := policy.DefaultTenantForUser(ctx, "user-none"); err != nil || got != "" {
		t.Fatalf("no membership = %q err=%v, want empty nil", got, err)
	}
	if got, err := policy.DefaultTenantForUser(ctx, ""); err != nil || got != "" {
		t.Fatalf("empty user = %q err=%v, want empty nil", got, err)
	}
	if got, err := (SQLPolicy{}).DefaultTenantForUser(ctx, "user-single"); err != nil || got != "" {
		t.Fatalf("nil-db = %q err=%v, want empty nil", got, err)
	}
}

func TestTenantOwnerFilterSQL(t *testing.T) {
	if clause, args := TenantOwnerFilterSQL("", 3, false); clause != "" || args != nil {
		t.Fatalf("empty tenant = (%q, %v), want empty/nil", clause, args)
	}

	clause, args := TenantOwnerFilterSQL("tenant-a", 3, false)
	want := " AND owner_id IN (SELECT user_id FROM user_tenant WHERE tenant_id = $3)"
	if clause != want {
		t.Fatalf("positional clause = %q, want %q", clause, want)
	}
	if len(args) != 1 || args[0] != "tenant-a" {
		t.Fatalf("args = %v, want [tenant-a]", args)
	}

	clause, _ = TenantOwnerFilterSQL("tenant-a", 3, true)
	wantSQLite := " AND owner_id IN (SELECT user_id FROM user_tenant WHERE tenant_id = ?)"
	if clause != wantSQLite {
		t.Fatalf("sqlite clause = %q, want %q", clause, wantSQLite)
	}

	// startIdx <= 0 clamps to $1 so the fragment never emits an invalid $0.
	clause, _ = TenantOwnerFilterSQL("tenant-a", 0, false)
	wantClamped := " AND owner_id IN (SELECT user_id FROM user_tenant WHERE tenant_id = $1)"
	if clause != wantClamped {
		t.Fatalf("clamped clause = %q, want %q", clause, wantClamped)
	}
}

func TestCanManageTenant(t *testing.T) {
	db := newDefaultTenantTestDB(t)
	policy := SQLPolicy{DB: db, Q: sqliteQ}
	ctx := context.Background()

	for _, tc := range []struct {
		name     string
		userID   string
		tenantID string
		want     bool
	}{
		{name: "owner", userID: "owner-a", tenantID: "tenant-a", want: true},
		{name: "superuser", userID: "root", tenantID: "tenant-a", want: true},
		{name: "member is not manager", userID: "user-single", tenantID: "tenant-a"},
		{name: "foreign owner", userID: "owner-b", tenantID: "tenant-a"},
		{name: "missing tenant", userID: "owner-a", tenantID: "missing"},
		{name: "anonymous", tenantID: "tenant-a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := policy.CanManageTenant(ctx, tc.userID, tc.tenantID)
			if err != nil {
				t.Fatalf("CanManageTenant: %v", err)
			}
			if got != tc.want {
				t.Fatalf("CanManageTenant(%q, %q) = %v, want %v", tc.userID, tc.tenantID, got, tc.want)
			}
		})
	}
}

func newDefaultTenantTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", t.TempDir()+"/default_tenant.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, stmt := range []string{
		`CREATE TABLE user_tenant (user_id TEXT, tenant_id TEXT)`,
		`CREATE TABLE tenants (id TEXT PRIMARY KEY, owner_id TEXT)`,
		`CREATE TABLE users (id TEXT PRIMARY KEY, is_superuser BOOLEAN DEFAULT FALSE)`,
		`INSERT INTO tenants(id, owner_id) VALUES ('tenant-a', 'owner-a'), ('tenant-b', 'owner-b')`,
		`INSERT INTO users(id, is_superuser) VALUES ('owner-a', FALSE), ('owner-b', FALSE), ('user-single', FALSE), ('user-multi', FALSE), ('root', TRUE)`,
		`INSERT INTO user_tenant(user_id, tenant_id) VALUES ('user-single', 'tenant-a')`,
		`INSERT INTO user_tenant(user_id, tenant_id) VALUES ('user-multi', 'tenant-a')`,
		`INSERT INTO user_tenant(user_id, tenant_id) VALUES ('user-multi', 'tenant-b')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
	return db
}
