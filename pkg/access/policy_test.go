package access

import (
	"context"
	"database/sql"
	"regexp"
	"testing"

	_ "github.com/ncruces/go-sqlite3/driver"
)

func TestAuthorizeWorkspaceRoles(t *testing.T) {
	db := newPolicyTestDB(t)
	policy := SQLPolicy{DB: db, Q: sqliteQ}
	ctx := context.Background()

	cases := []struct {
		name    string
		req     WorkspaceRequest
		allowed bool
		role    string
		reason  string
	}{
		{
			name:    "owner write",
			req:     WorkspaceRequest{UserID: "user-a", ProjectID: "payments", Action: ActionWrite},
			allowed: true,
			role:    RoleAdmin,
			reason:  "owner",
		},
		{
			name:    "viewer read",
			req:     WorkspaceRequest{UserID: "user-b", ProjectID: "payments", Action: ActionRead},
			allowed: true,
			role:    RoleViewer,
			reason:  "shared_viewer",
		},
		{
			name:    "viewer write denied",
			req:     WorkspaceRequest{UserID: "user-b", ProjectID: "payments", Action: ActionWrite},
			allowed: false,
			role:    RoleViewer,
			reason:  "role_insufficient",
		},
		{
			name:    "foreign denied",
			req:     WorkspaceRequest{UserID: "user-c", ProjectID: "payments", Action: ActionRead},
			allowed: false,
			reason:  "denied",
		},
		{
			name:    "superuser bypass",
			req:     WorkspaceRequest{UserID: "root", ProjectID: "payments", Action: ActionWrite},
			allowed: true,
			role:    RoleAdmin,
			reason:  "superuser",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decision, err := policy.AuthorizeWorkspace(ctx, tc.req)
			if err != nil {
				t.Fatal(err)
			}
			if decision.Allowed != tc.allowed || decision.Role != tc.role || decision.Reason != tc.reason {
				t.Fatalf("decision=%+v, want allowed=%v role=%q reason=%q", decision, tc.allowed, tc.role, tc.reason)
			}
		})
	}
}

func TestAuthorizeWorkspaceAPIKeyPermissions(t *testing.T) {
	policy := SQLPolicy{}
	decision, err := policy.AuthorizeWorkspace(context.Background(), WorkspaceRequest{
		UserID:            "user-a",
		ProjectID:         "payments",
		Action:            ActionWrite,
		APIKeyPermissions: "read",
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Allowed || decision.APIKeyAllowed || decision.Reason != "api_key_permissions_denied" {
		t.Fatalf("decision=%+v, want API key denial", decision)
	}
}

func TestAuthorizeWorkspaceDevMode(t *testing.T) {
	decision, err := (SQLPolicy{}).AuthorizeWorkspace(context.Background(), WorkspaceRequest{ProjectID: "payments", Action: ActionRead})
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Allowed || !decision.DevMode || decision.Role != RoleAdmin || decision.Reason != "dev_mode" {
		t.Fatalf("decision=%+v, want dev-mode admin allow", decision)
	}
}

func TestAllowedDatasetIDs(t *testing.T) {
	db := newPolicyTestDB(t)
	policy := SQLPolicy{DB: db, Q: sqliteQ, QA: sqliteQArgs}

	got := policy.AllowedDatasetIDs(context.Background(), "user-b")
	want := map[string]bool{"public": true, "payments": true, "owned-b": true}
	if len(got) != len(want) {
		t.Fatalf("allowed=%v, want %d ids", got, len(want))
	}
	for _, id := range got {
		if !want[id] {
			t.Fatalf("unexpected dataset %q in %v", id, got)
		}
	}

	if got := policy.AllowedDatasetIDs(context.Background(), "root"); got != nil {
		t.Fatalf("superuser allowed=%v, want nil no-filter", got)
	}
	if got := policy.AllowedDatasetIDs(context.Background(), ""); got != nil {
		t.Fatalf("anonymous allowed=%v, want nil no-filter", got)
	}
	if got := (SQLPolicy{}).AllowedDatasetIDs(context.Background(), "user-b"); got != nil {
		t.Fatalf("no-db allowed=%v, want nil no-filter", got)
	}
}

func TestAllowedDatasetIDsFailsClosedOnQueryError(t *testing.T) {
	db, err := sql.Open("sqlite3", t.TempDir()+"/broken.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE users (id TEXT PRIMARY KEY, is_superuser INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(id, is_superuser) VALUES ('user-a', 0)`); err != nil {
		t.Fatal(err)
	}

	got := (SQLPolicy{DB: db, Q: sqliteQ, QA: sqliteQArgs}).AllowedDatasetIDs(context.Background(), "user-a")
	if got == nil || len(got) != 0 {
		t.Fatalf("broken ACL query returned %v, want non-nil empty deny-all filter", got)
	}
}

func TestVisibleDatasetIDs(t *testing.T) {
	db := newPolicyTestDB(t)
	policy := SQLPolicy{DB: db, Q: sqliteQ, QA: sqliteQArgs}
	ctx := context.Background()

	got, err := policy.VisibleDatasetIDs(ctx, "user-b")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"owned-b", "payments", "public"}
	if len(got) != len(want) {
		t.Fatalf("visible=%v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("visible=%v, want ordered %v", got, want)
		}
	}

	got, err = policy.VisibleDatasetIDs(ctx, "root")
	if err != nil {
		t.Fatal(err)
	}
	want = []string{"owned-b", "payments", "public"}
	if len(got) != len(want) {
		t.Fatalf("superuser visible=%v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("superuser visible=%v, want ordered %v", got, want)
		}
	}

	got, err = policy.VisibleDatasetIDs(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("anonymous visible=%v, want all %v", got, want)
	}

	got, err = (SQLPolicy{}).VisibleDatasetIDs(ctx, "user-b")
	if err != nil || got != nil {
		t.Fatalf("nil-db visible=%v err=%v, want nil nil", got, err)
	}
}

func TestListVisibleDatasets(t *testing.T) {
	db := newPolicyTestDB(t)
	for _, stmt := range []string{
		`CREATE TABLE dataset_data (dataset_id TEXT, data_id TEXT)`,
		`INSERT INTO dataset_data(dataset_id, data_id) VALUES ('payments', 'd1'), ('payments', 'd2'), ('owned-b', 'd3')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
	policy := SQLPolicy{DB: db, Q: sqliteQ, QA: sqliteQArgs}
	ctx := context.Background()

	got, err := policy.ListVisibleDatasets(ctx, "user-b")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"payments": 2, "owned-b": 1, "public": 0}
	if len(got) != len(want) {
		t.Fatalf("visible datasets=%+v, want %d", got, len(want))
	}
	for _, d := range got {
		if want[d.ID] != d.RecordCount {
			t.Fatalf("dataset %s record_count=%d, want %d; all=%+v", d.ID, d.RecordCount, want[d.ID], got)
		}
	}

	got, err = policy.ListVisibleDatasets(ctx, "root")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("superuser visible datasets=%+v, want all 3", got)
	}

	got, err = policy.ListVisibleDatasets(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("anonymous visible datasets=%+v, want all 3", got)
	}

	got, err = (SQLPolicy{}).ListVisibleDatasets(ctx, "user-b")
	if err != nil || got != nil {
		t.Fatalf("nil-db visible datasets=%+v err=%v, want nil nil", got, err)
	}
}

func TestCanAccessDataset(t *testing.T) {
	db := newPolicyTestDB(t)
	policy := SQLPolicy{DB: db, Q: sqliteQ, QA: sqliteQArgs}
	ctx := context.Background()

	cases := []struct {
		name      string
		userID    string
		datasetID string
		want      bool
	}{
		{"owner", "user-a", "payments", true},
		{"shared", "user-b", "payments", true},
		{"foreign", "user-c", "payments", false},
		{"public owner", "user-c", "public", true},
		{"anonymous dev mode", "", "payments", true},
		{"missing keeps legacy public behavior", "user-c", "missing", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := policy.CanAccessDataset(ctx, tc.datasetID, tc.userID); got != tc.want {
				t.Fatalf("CanAccessDataset=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestCanUseDatasetForUpload(t *testing.T) {
	db := newPolicyTestDB(t)
	policy := SQLPolicy{DB: db, Q: sqliteQ}
	ctx := context.Background()

	cases := []struct {
		name      string
		userID    string
		datasetID string
		want      bool
	}{
		{"owner", "user-a", "payments", true},
		{"shared", "user-b", "payments", true},
		{"foreign", "user-c", "payments", false},
		{"public", "user-c", "public", true},
		{"missing allowed for create", "user-c", "new-dataset", true},
		{"anonymous dev mode", "", "payments", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := policy.CanUseDatasetForUpload(ctx, tc.datasetID, tc.userID)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("CanUseDatasetForUpload=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestCanManageDatasetShares(t *testing.T) {
	db := newPolicyTestDB(t)
	// user-d holds an admin share on payments; user-b is only a viewer.
	if _, err := db.Exec(`INSERT INTO dataset_shares(id, dataset_id, user_id, role) VALUES ('share-d', 'payments', 'user-d', 'admin')`); err != nil {
		t.Fatal(err)
	}
	policy := SQLPolicy{DB: db, Q: sqliteQ}
	ctx := context.Background()

	cases := []struct {
		name      string
		granterID string
		want      bool
	}{
		{"owner manages", "user-a", true},
		{"admin share manages", "user-d", true},
		{"viewer cannot manage", "user-b", false},
		{"foreigner cannot manage", "user-c", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := policy.CanManageDatasetShares(ctx, "payments", tc.granterID); got != tc.want {
				t.Fatalf("CanManageDatasetShares(payments, %q)=%v, want %v", tc.granterID, got, tc.want)
			}
		})
	}

	// No DB means sharing is unavailable; handlers special-case this earlier.
	if (SQLPolicy{}).CanManageDatasetShares(ctx, "payments", "user-a") {
		t.Fatal("nil-db CanManageDatasetShares must be false")
	}
}

func TestShareGrantRevokePolicyMethods(t *testing.T) {
	db := newPolicyTestDB(t)
	if _, err := db.Exec(`INSERT INTO dataset_shares(id, dataset_id, user_id, role) VALUES ('share-d', 'payments', 'user-d', 'admin')`); err != nil {
		t.Fatal(err)
	}
	policy := SQLPolicy{DB: db, Q: sqliteQ}
	ctx := context.Background()

	if !policy.CanGrantDatasetShare(ctx, "payments", "user-a") {
		t.Fatal("owner should be able to grant dataset shares")
	}
	if !policy.CanRevokeDatasetShare(ctx, "payments", "user-d") {
		t.Fatal("admin-share holder should be able to revoke dataset shares")
	}
	if policy.CanGrantDatasetShare(ctx, "payments", "user-b") {
		t.Fatal("viewer should not be able to grant dataset shares")
	}
	if policy.CanRevokeDatasetShare(ctx, "payments", "user-c") {
		t.Fatal("foreign user should not be able to revoke dataset shares")
	}
}

func TestResolveUserID(t *testing.T) {
	db := newPolicyTestDB(t)
	policy := SQLPolicy{DB: db, Q: sqliteQ}
	ctx := context.Background()

	if got, err := policy.ResolveUserID(ctx, "explicit", "user-b@example.com"); err != nil || got != "explicit" {
		t.Fatalf("explicit ResolveUserID=%q err=%v, want explicit nil", got, err)
	}
	if got, err := policy.ResolveUserID(ctx, "", "user-b@example.com"); err != nil || got != "user-b" {
		t.Fatalf("email ResolveUserID=%q err=%v, want user-b nil", got, err)
	}
	if got, err := policy.ResolveUserID(ctx, "", "missing@example.com"); err != nil || got != "" {
		t.Fatalf("missing ResolveUserID=%q err=%v, want empty nil", got, err)
	}
	if got, err := (SQLPolicy{}).ResolveUserID(ctx, "", "user-b@example.com"); err != nil || got != "" {
		t.Fatalf("nil-db ResolveUserID=%q err=%v, want empty nil", got, err)
	}
}

func TestValidRole(t *testing.T) {
	for _, role := range []string{RoleAdmin, RoleEditor, RoleViewer, "ADMIN"} {
		if !ValidRole(role) {
			t.Fatalf("ValidRole(%q)=false, want true", role)
		}
	}
	for _, role := range []string{"", "owner", "reader", "delete"} {
		if ValidRole(role) {
			t.Fatalf("ValidRole(%q)=true, want false", role)
		}
	}
}

func TestIsSuperuser(t *testing.T) {
	db := newPolicyTestDB(t)
	policy := SQLPolicy{DB: db, Q: sqliteQ}
	ctx := context.Background()

	cases := []struct {
		name   string
		userID string
		want   bool
	}{
		{"superuser", "root", true},
		{"regular user", "user-a", false},
		{"missing user is not super", "ghost", false},
		{"empty user is not super", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := policy.IsSuperuser(ctx, tc.userID)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("IsSuperuser(%q)=%v, want %v", tc.userID, got, tc.want)
			}
		})
	}

	// A nil DB never queries and never errors — dev/single-user mode.
	if got, err := (SQLPolicy{}).IsSuperuser(ctx, "root"); got || err != nil {
		t.Fatalf("nil-db IsSuperuser=%v,%v want false,nil", got, err)
	}
}

func TestIsActive(t *testing.T) {
	db := newPolicyTestDB(t)
	policy := SQLPolicy{DB: db, Q: sqliteQ}
	ctx := context.Background()

	// Deactivate user-a so the gate has an inactive row to find.
	if _, err := db.Exec(`UPDATE users SET is_active = 0 WHERE id = 'user-a'`); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		userID string
		want   bool
	}{
		{"active user", "user-b", true},
		{"deactivated user", "user-a", false},
		{"missing user fails open active", "ghost", true},
		{"empty user is active", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := policy.IsActive(ctx, tc.userID)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("IsActive(%q)=%v, want %v", tc.userID, got, tc.want)
			}
		})
	}

	// A nil DB never queries and treats everyone as active — dev/single-user mode.
	if got, err := (SQLPolicy{}).IsActive(ctx, "user-a"); !got || err != nil {
		t.Fatalf("nil-db IsActive=%v,%v want true,nil", got, err)
	}
}

func newPolicyTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", t.TempDir()+"/access.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, stmt := range []string{
		`CREATE TABLE users (id TEXT PRIMARY KEY, email TEXT DEFAULT '', is_active INTEGER NOT NULL DEFAULT 1, is_superuser INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE datasets (id TEXT PRIMARY KEY, name TEXT DEFAULT '', owner_id TEXT, created_at TEXT DEFAULT '')`,
		`CREATE TABLE dataset_shares (id TEXT PRIMARY KEY, dataset_id TEXT, user_id TEXT, role TEXT)`,
		`INSERT INTO users(id, email, is_superuser) VALUES ('user-a', 'user-a@example.com', 0), ('user-b', 'user-b@example.com', 0), ('user-c', 'user-c@example.com', 0), ('root', 'root@example.com', 1)`,
		`INSERT INTO datasets(id, name, owner_id, created_at) VALUES ('payments', 'Payments', 'user-a', '2026-01-01T00:00:00Z')`,
		`INSERT INTO datasets(id, name, owner_id, created_at) VALUES ('owned-b', 'Owned B', 'user-b', '2026-01-02T00:00:00Z')`,
		`INSERT INTO datasets(id, name, owner_id, created_at) VALUES ('public', 'Public', '', '2026-01-03T00:00:00Z')`,
		`INSERT INTO dataset_shares(id, dataset_id, user_id, role) VALUES ('share-b', 'payments', 'user-b', 'viewer')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
	return db
}

var sqlitePlaceholderRe = regexp.MustCompile(`\$\d+`)

func sqliteQ(query string) string {
	return sqlitePlaceholderRe.ReplaceAllString(query, "?")
}

func sqliteQArgs(query string, args ...any) (string, []any) {
	matches := sqlitePlaceholderRe.FindAllString(query, -1)
	out := make([]any, 0, len(matches))
	for _, m := range matches {
		if m == "$1" && len(args) >= 1 {
			out = append(out, args[0])
		}
	}
	return sqliteQ(query), out
}
