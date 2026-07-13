package access

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	_ "github.com/ncruces/go-sqlite3/driver"
)

func TestAuthorizeRoutesWorkspace(t *testing.T) {
	db := newPolicyTestDB(t)
	policy := SQLPolicy{DB: db, Q: sqliteQ}
	ctx := context.Background()

	cases := []struct {
		name    string
		actor   Actor
		action  string
		allowed bool
		role    string
		reason  string
	}{
		{"owner write", Actor{UserID: "user-a"}, ActionWrite, true, RoleAdmin, "owner"},
		{"viewer read", Actor{UserID: "user-b"}, ActionRead, true, RoleViewer, "shared_viewer"},
		{"viewer write denied", Actor{UserID: "user-b"}, ActionWrite, false, RoleViewer, "role_insufficient"},
		{"api-key denied", Actor{UserID: "user-a", APIKeyPermissions: "read"}, ActionWrite, false, "", "api_key_permissions_denied"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, err := policy.Authorize(ctx, tc.actor, Resource{Kind: ResourceWorkspace, ID: "payments"}, tc.action)
			if err != nil {
				t.Fatal(err)
			}
			if d.Allowed != tc.allowed || d.Role != tc.role || d.Reason != tc.reason {
				t.Fatalf("decision=%+v, want allowed=%v role=%q reason=%q", d, tc.allowed, tc.role, tc.reason)
			}
		})
	}
}

func TestAuthorizeRoutesDataset(t *testing.T) {
	db := newPolicyTestDB(t)
	policy := SQLPolicy{DB: db, Q: sqliteQ}
	ctx := context.Background()

	cases := []struct {
		name    string
		actor   Actor
		action  string
		allowed bool
		role    string
		reason  string
	}{
		{"owner read", Actor{UserID: "user-a"}, ActionRead, true, RoleAdmin, "owner"},
		{"shared read", Actor{UserID: "user-b"}, ActionRead, true, RoleViewer, "shared_viewer"},
		{"viewer write denied", Actor{UserID: "user-b"}, ActionWrite, false, RoleViewer, "role_insufficient"},
		{"public read", Actor{UserID: "user-c"}, ActionRead, true, RoleViewer, "public"},
		{"public write denied", Actor{UserID: "user-c"}, ActionWrite, false, RoleViewer, "public_read_only"},
		{"foreign denied", Actor{UserID: "user-c"}, ActionRead, false, "", "denied"},
		{"api-key denied", Actor{UserID: "user-a", APIKeyPermissions: "read"}, ActionWrite, false, "", "api_key_permissions_denied"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			datasetID := "payments"
			if strings.HasPrefix(tc.name, "public") {
				datasetID = "public"
			}
			d, err := policy.Authorize(ctx, tc.actor, Resource{Kind: ResourceDataset, ID: datasetID}, tc.action)
			if err != nil {
				t.Fatal(err)
			}
			if d.Allowed != tc.allowed || d.Role != tc.role || d.Reason != tc.reason {
				t.Fatalf("decision=%+v, want allowed=%v role=%q reason=%q", d, tc.allowed, tc.role, tc.reason)
			}
		})
	}
}

func TestAuthorizeUnknownResourceKind(t *testing.T) {
	_, err := (SQLPolicy{}).Authorize(context.Background(), Actor{UserID: "user-a"}, Resource{Kind: "bogus", ID: "x"}, ActionRead)
	if err == nil {
		t.Fatal("want error for unknown resource kind, got nil")
	}
}

func TestIsTenantMember(t *testing.T) {
	db := newTenantMemberTestDB(t)
	policy := SQLPolicy{DB: db, Q: sqliteQ}
	ctx := context.Background()

	cases := []struct {
		name     string
		userID   string
		tenantID string
		want     bool
	}{
		{"member", "user-a", "tenant-a", true},
		{"non-member", "user-a", "tenant-b", false},
		{"empty user", "", "tenant-a", false},
		{"empty tenant", "user-a", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := policy.IsTenantMember(ctx, tc.userID, tc.tenantID)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("IsTenantMember(%q,%q)=%v, want %v", tc.userID, tc.tenantID, got, tc.want)
			}
		})
	}

	if got, err := (SQLPolicy{}).IsTenantMember(ctx, "user-a", "tenant-a"); err != nil || got {
		t.Fatalf("nil-db IsTenantMember=%v err=%v, want false nil", got, err)
	}
}

func newTenantMemberTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", t.TempDir()+"/tenant_member.db")
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
