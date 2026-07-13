package access

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	_ "github.com/ncruces/go-sqlite3/driver"
)

// newProvisioningTestDB builds the users / datasets / tenants / user_tenant
// surface the provisioner and the activation gate share. user-a is an active
// owner of the "payments" dataset and a member of tenant t1; root is an active
// superuser; t1/t2/t3 are tenants.
func newProvisioningTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", t.TempDir()+"/provision.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, stmt := range []string{
		`CREATE TABLE users (id TEXT PRIMARY KEY, is_active INTEGER NOT NULL DEFAULT 1, is_superuser INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE datasets (id TEXT PRIMARY KEY, owner_id TEXT)`,
		`CREATE TABLE dataset_shares (id TEXT PRIMARY KEY, dataset_id TEXT, user_id TEXT, role TEXT)`,
		`CREATE TABLE tenants (id TEXT PRIMARY KEY)`,
		`CREATE TABLE user_tenant (user_id TEXT, tenant_id TEXT, PRIMARY KEY(user_id, tenant_id))`,
		`INSERT INTO users(id, is_active, is_superuser) VALUES ('user-a', 1, 0), ('root', 1, 1)`,
		`INSERT INTO datasets(id, owner_id) VALUES ('payments', 'user-a')`,
		`INSERT INTO tenants(id) VALUES ('t1'), ('t2'), ('t3')`,
		`INSERT INTO user_tenant(user_id, tenant_id) VALUES ('user-a', 't1')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
	return db
}

func TestProvisionerDeactivationDeniesThroughPolicy(t *testing.T) {
	db := newProvisioningTestDB(t)
	policy := SQLPolicy{DB: db, Q: sqliteQ}
	prov := SQLProvisioner{DB: db, Q: sqliteQ}
	ctx := context.Background()

	wsReq := WorkspaceRequest{UserID: "user-a", ProjectID: "payments", Action: ActionWrite}
	dsActor := Actor{UserID: "user-a"}
	dsRes := Resource{Kind: ResourceDataset, ID: "payments"}

	// Active owner is allowed through both facades.
	if d, err := policy.AuthorizeWorkspace(ctx, wsReq); err != nil || !d.Allowed || d.Reason != "owner" {
		t.Fatalf("pre-deactivation workspace decision=%+v err=%v, want owner allow", d, err)
	}
	if d, err := policy.Authorize(ctx, dsActor, dsRes, ActionRead); err != nil || !d.Allowed || d.Reason != "owner" {
		t.Fatalf("pre-deactivation dataset decision=%+v err=%v, want owner allow", d, err)
	}

	// Deactivate through the provisioner; the shared policy layer now denies both.
	if err := prov.DeactivateUser(ctx, "user-a"); err != nil {
		t.Fatalf("DeactivateUser err=%v", err)
	}
	if d, err := policy.AuthorizeWorkspace(ctx, wsReq); err != nil || d.Allowed || d.Reason != "user_inactive" {
		t.Fatalf("post-deactivation workspace decision=%+v err=%v, want user_inactive deny", d, err)
	}
	if d, err := policy.Authorize(ctx, dsActor, dsRes, ActionRead); err != nil || d.Allowed || d.Reason != "user_inactive" {
		t.Fatalf("post-deactivation dataset decision=%+v err=%v, want user_inactive deny", d, err)
	}

	// Reactivating via ProvisionUser restores access.
	if err := prov.ProvisionUser(ctx, ProvisionedUser{UserID: "user-a", Active: true}); err != nil {
		t.Fatalf("ProvisionUser reactivate err=%v", err)
	}
	if d, err := policy.AuthorizeWorkspace(ctx, wsReq); err != nil || !d.Allowed || d.Reason != "owner" {
		t.Fatalf("post-reactivation workspace decision=%+v err=%v, want owner allow", d, err)
	}
}

func TestProvisionerDeactivatesSuperuser(t *testing.T) {
	db := newProvisioningTestDB(t)
	policy := SQLPolicy{DB: db, Q: sqliteQ}
	prov := SQLProvisioner{DB: db, Q: sqliteQ}
	ctx := context.Background()

	req := WorkspaceRequest{UserID: "root", ProjectID: "payments", Action: ActionWrite}

	// The superuser bypass applies while active...
	if d, err := policy.AuthorizeWorkspace(ctx, req); err != nil || !d.Allowed || d.Reason != "superuser" {
		t.Fatalf("active superuser decision=%+v err=%v, want superuser allow", d, err)
	}
	// ...but the activation gate runs first, so a deactivated superuser is denied.
	if err := prov.DeactivateUser(ctx, "root"); err != nil {
		t.Fatalf("DeactivateUser(root) err=%v", err)
	}
	if d, err := policy.AuthorizeWorkspace(ctx, req); err != nil || d.Allowed || d.Reason != "user_inactive" {
		t.Fatalf("deactivated superuser decision=%+v err=%v, want user_inactive deny", d, err)
	}
}

func TestProvisionerUnknownUser(t *testing.T) {
	db := newProvisioningTestDB(t)
	prov := SQLProvisioner{DB: db, Q: sqliteQ}
	ctx := context.Background()

	if err := prov.DeactivateUser(ctx, "ghost"); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("DeactivateUser(ghost) err=%v, want ErrUserNotFound", err)
	}
	if err := prov.ProvisionUser(ctx, ProvisionedUser{UserID: "ghost", Active: true}); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("ProvisionUser(ghost) err=%v, want ErrUserNotFound", err)
	}
	if err := prov.ProvisionUser(ctx, ProvisionedUser{Active: true}); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("ProvisionUser(empty id) err=%v, want ErrUserNotFound", err)
	}
}

func TestProvisionerTenantMembershipSync(t *testing.T) {
	db := newProvisioningTestDB(t)
	policy := SQLPolicy{DB: db, Q: sqliteQ}
	prov := SQLProvisioner{DB: db, Q: sqliteQ}
	ctx := context.Background()

	member := func(tenant string) bool {
		t.Helper()
		ok, err := policy.IsTenantMember(ctx, "user-a", tenant)
		if err != nil {
			t.Fatalf("IsTenantMember(%q) err=%v", tenant, err)
		}
		return ok
	}

	// Seeded with t1 only.
	if !member("t1") || member("t2") || member("t3") {
		t.Fatalf("initial membership wrong: t1=%v t2=%v t3=%v", member("t1"), member("t2"), member("t3"))
	}

	// Add t2 (keep t1).
	if err := prov.SyncTenantMembership(ctx, "user-a", []string{"t1", "t2"}); err != nil {
		t.Fatalf("sync add err=%v", err)
	}
	if !member("t1") || !member("t2") {
		t.Fatalf("after add: t1=%v t2=%v, want both true", member("t1"), member("t2"))
	}

	// Drop t1 (keep t2).
	if err := prov.SyncTenantMembership(ctx, "user-a", []string{"t2"}); err != nil {
		t.Fatalf("sync drop err=%v", err)
	}
	if member("t1") || !member("t2") {
		t.Fatalf("after drop: t1=%v t2=%v, want false,true", member("t1"), member("t2"))
	}

	// Empty (non-nil) set clears all memberships.
	if err := prov.SyncTenantMembership(ctx, "user-a", []string{}); err != nil {
		t.Fatalf("sync clear err=%v", err)
	}
	if member("t2") {
		t.Fatalf("after clear: t2 still a member")
	}

	// ProvisionUser with a non-nil TenantIDs reconciles memberships too.
	if err := prov.ProvisionUser(ctx, ProvisionedUser{UserID: "user-a", Active: true, TenantIDs: []string{"t3"}}); err != nil {
		t.Fatalf("ProvisionUser with tenants err=%v", err)
	}
	if !member("t3") || member("t1") || member("t2") {
		t.Fatalf("after provision: t1=%v t2=%v t3=%v, want only t3", member("t1"), member("t2"), member("t3"))
	}
}

func TestNoopProvisionerRejects(t *testing.T) {
	var prov Provisioner = NoopProvisioner{}
	ctx := context.Background()
	if err := prov.ProvisionUser(ctx, ProvisionedUser{UserID: "user-a"}); !errors.Is(err, ErrProvisioningDisabled) {
		t.Fatalf("ProvisionUser err=%v, want ErrProvisioningDisabled", err)
	}
	if err := prov.DeactivateUser(ctx, "user-a"); !errors.Is(err, ErrProvisioningDisabled) {
		t.Fatalf("DeactivateUser err=%v, want ErrProvisioningDisabled", err)
	}
	if err := prov.SyncTenantMembership(ctx, "user-a", nil); !errors.Is(err, ErrProvisioningDisabled) {
		t.Fatalf("SyncTenantMembership err=%v, want ErrProvisioningDisabled", err)
	}
}

func TestSQLProvisionerNoDB(t *testing.T) {
	prov := SQLProvisioner{}
	ctx := context.Background()
	if err := prov.ProvisionUser(ctx, ProvisionedUser{UserID: "user-a"}); !errors.Is(err, ErrProvisioningNoDB) {
		t.Fatalf("ProvisionUser err=%v, want ErrProvisioningNoDB", err)
	}
	if err := prov.DeactivateUser(ctx, "user-a"); !errors.Is(err, ErrProvisioningNoDB) {
		t.Fatalf("DeactivateUser err=%v, want ErrProvisioningNoDB", err)
	}
	if err := prov.SyncTenantMembership(ctx, "user-a", nil); !errors.Is(err, ErrProvisioningNoDB) {
		t.Fatalf("SyncTenantMembership err=%v, want ErrProvisioningNoDB", err)
	}
}
