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

func newPolicyTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", t.TempDir()+"/access.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, stmt := range []string{
		`CREATE TABLE users (id TEXT PRIMARY KEY, is_superuser INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE datasets (id TEXT PRIMARY KEY, owner_id TEXT)`,
		`CREATE TABLE dataset_shares (id TEXT PRIMARY KEY, dataset_id TEXT, user_id TEXT, role TEXT)`,
		`INSERT INTO users(id, is_superuser) VALUES ('user-a', 0), ('user-b', 0), ('user-c', 0), ('root', 1)`,
		`INSERT INTO datasets(id, owner_id) VALUES ('payments', 'user-a')`,
		`INSERT INTO datasets(id, owner_id) VALUES ('owned-b', 'user-b')`,
		`INSERT INTO datasets(id, owner_id) VALUES ('public', '')`,
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
