package mcp

import (
	"context"
	"testing"
)

func dataACLDeps(t *testing.T) *fakeDeps {
	t.Helper()
	d := setupAddTestDB(t)
	for _, q := range []string{
		`CREATE TABLE users(id TEXT PRIMARY KEY, is_active BOOLEAN, is_superuser BOOLEAN)`,
		`CREATE TABLE dataset_shares(id TEXT PRIMARY KEY, dataset_id TEXT, user_id TEXT, role TEXT)`,
		`INSERT INTO users VALUES ('alice',TRUE,FALSE),('bob',TRUE,FALSE),('admin',TRUE,TRUE),('inactive',FALSE,TRUE)`,
		`CREATE TABLE graph_nodes(id TEXT)`, `CREATE TABLE graph_edges(id TEXT)`,
		`INSERT INTO datasets(id,name,owner_id) VALUES ('a','alice-docs','alice'),('b','bob-docs','bob'),('public','public','')`,
	} {
		if _, err := d.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func TestDocumentACLDeleteAndPrune(t *testing.T) {
	for _, user := range []string{"alice", "bob", "inactive"} {
		t.Run("prune/"+user, func(t *testing.T) {
			d := dataACLDeps(t)
			r := ToolPrune(context.WithValue(context.Background(), UserIDKey, user), d)
			if !r.IsError {
				t.Errorf("non-admin prune succeeded: %+v", r)
			}
			var n int
			if err := d.db.QueryRow(`SELECT COUNT(*) FROM datasets`).Scan(&n); err != nil || n != 3 {
				t.Errorf("dataset rows=%d err=%v, want 3", n, err)
			}
		})
	}
	for _, id := range []string{"b", "public"} {
		t.Run("delete/"+id, func(t *testing.T) {
			d := dataACLDeps(t)
			r := ToolDelete(context.WithValue(context.Background(), UserIDKey, "alice"), d, map[string]any{"dataset_id": id})
			if !r.IsError {
				t.Errorf("foreign/public delete succeeded: %+v", r)
			}
		})
	}
	t.Run("SQL-error", func(t *testing.T) {
		d := dataACLDeps(t)
		d.db.Close()
		if r := ToolDelete(context.Background(), d, map[string]any{"dataset_id": "a"}); !r.IsError {
			t.Fatal("closed database delete reported success")
		}
	})
	t.Run("prune-rollback", func(t *testing.T) {
		d := dataACLDeps(t)
		if _, err := d.db.Exec(`CREATE TRIGGER stop_prune BEFORE DELETE ON datasets BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
			t.Fatal(err)
		}
		if _, err := d.db.Exec(`INSERT INTO data(id,name) VALUES('doc','doc')`); err != nil {
			t.Fatal(err)
		}
		if r := ToolPrune(context.Background(), d); !r.IsError {
			t.Error("prune reported success on SQL failure")
		}
		var n int
		if err := d.db.QueryRow(`SELECT COUNT(*) FROM data`).Scan(&n); err != nil || n != 1 {
			t.Errorf("partial prune, remaining=%d err=%v", n, err)
		}
	})
}

func TestDocumentACLAddOwnerAndDataset(t *testing.T) {
	d := dataACLDeps(t)
	ctx := context.WithValue(context.Background(), UserIDKey, "alice")
	r := ToolAdd(ctx, d, map[string]any{"data": "private text", "dataset_name": "alice-docs"})
	if r.IsError {
		t.Fatalf("add: %+v", r)
	}
	var owner, ds string
	if err := d.db.QueryRow(`SELECT d.owner_id,dd.dataset_id FROM data d JOIN dataset_data dd ON d.id=dd.data_id`).Scan(&owner, &ds); err != nil {
		t.Fatal(err)
	}
	if owner != "alice" || ds != "a" {
		t.Errorf("owner=%q dataset=%q want alice/a", owner, ds)
	}
	r = ToolAdd(ctx, d, map[string]any{"data": "must not write", "dataset_name": "bob-docs"})
	if !r.IsError {
		t.Error("add wrote to foreign dataset")
	}
}

func TestDocumentACLListDataFilters(t *testing.T) {
	d := dataACLDeps(t)
	for _, q := range []string{
		`INSERT INTO data(id,name,extension,room,tags,created_at) VALUES ('one','visible','.txt','docs','["tag"]','2026-01-01'),('two','secret','.txt','docs','["tag"]','2026-01-02')`,
		`INSERT INTO dataset_data VALUES('a','one'),('b','two')`,
	} {
		if _, err := d.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for _, allowed := range [][]string{{"a"}, {}} {
		d.allowedDatasetIDs = allowed
		for _, args := range []map[string]any{{"room": "docs"}, {"tags": []any{"tag"}}} {
			items := parseListDataContent(t, ToolListData(context.Background(), d, args))
			if len(items) != len(allowed) {
				t.Errorf("allowed=%v args=%v returned=%v", allowed, args, items)
			}
			for _, item := range items {
				if item["id"] != "one" {
					t.Errorf("private document leaked: %v", item)
				}
			}
		}
	}
}

func TestDocumentACLRoleAndAPIKeyMatrix(t *testing.T) {
	for _, tc := range []struct {
		name, user, role, permissions string
		deny                          bool
	}{
		{"owner", "alice", "", "", false},
		{"viewer", "bob", "viewer", "", true},
		{"editor", "bob", "editor", "write", false},
		{"admin-share", "bob", "admin", "write", false},
		{"instance-admin", "admin", "", "", false},
		{"read-key-owner", "alice", "", "read", true},
		{"inactive-owner", "inactive", "", "", true},
		{"standalone", "", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := dataACLDeps(t)
			if tc.role != "" {
				if _, err := d.db.Exec(`INSERT INTO dataset_shares VALUES('grant','a','bob',?)`, tc.role); err != nil {
					t.Fatal(err)
				}
			}
			ctx := context.WithValue(context.Background(), UserIDKey, tc.user)
			ctx = context.WithValue(ctx, ContextKey("mcp_api_key_permissions"), tc.permissions)
			got := ToolDelete(ctx, d, map[string]any{"dataset_id": "a", "actor_id": "admin", "owner_id": "alice"})
			if got.IsError != tc.deny {
				t.Errorf("deny=%v got=%+v", tc.deny, got)
			}
		})
	}
	t.Run("active-instance-admin-prune", func(t *testing.T) {
		d := dataACLDeps(t)
		if got := ToolPrune(context.WithValue(context.Background(), UserIDKey, "admin"), d); got.IsError {
			t.Fatalf("admin prune denied: %+v", got)
		}
	})
	t.Run("read-key-add-before-file-write", func(t *testing.T) {
		d := dataACLDeps(t)
		ctx := context.WithValue(context.Background(), UserIDKey, "alice")
		ctx = context.WithValue(ctx, ContextKey("mcp_api_key_permissions"), "read")
		if got := ToolAdd(ctx, d, map[string]any{"data": "must not persist", "dataset_name": "new"}); !got.IsError {
			t.Fatal("read-only key wrote new document")
		}
		var n int
		if err := d.db.QueryRow(`SELECT COUNT(*) FROM data`).Scan(&n); err != nil || n != 0 {
			t.Fatalf("data rows=%d err=%v", n, err)
		}
	})
}

func TestDocumentACLListDataKeepsIDsAndNamesSeparate(t *testing.T) {
	d := dataACLDeps(t)
	if _, err := d.db.Exec(`UPDATE datasets SET name=CASE id WHEN 'a' THEN 'b' WHEN 'b' THEN 'a' ELSE name END`); err != nil {
		t.Fatal(err)
	}
	d.collections = []string{"a", "b", "unrelated"}
	d.allowedDatasetIDs = []string{"a"}
	items := parseListDataContent(t, ToolListData(context.Background(), d, map[string]any{}))
	seen := map[string]bool{}
	for _, item := range items {
		switch item["type"] {
		case "dataset":
			if item["id"] != "a" {
				t.Errorf("collection-name alias leaked foreign dataset: %v", item)
			}
			seen["dataset"] = true
		case "vector_collection":
			if item["collection"] != "b" {
				t.Errorf("dataset-ID alias leaked foreign collection: %v", item)
			}
			seen["collection"] = true
		}
	}
	if len(items) != 2 || !seen["dataset"] || !seen["collection"] {
		t.Errorf("owned dataset and its collection must remain visible: %v", items)
	}
	d.allowedDatasetIDs = []string{}
	if items := parseListDataContent(t, ToolListData(context.Background(), d, map[string]any{})); len(items) != 0 {
		t.Errorf("empty permission set returned %v", items)
	}
	d.allowedDatasetIDs = nil
	if items := parseListDataContent(t, ToolListData(context.Background(), d, map[string]any{})); len(items) != 6 {
		t.Errorf("unrestricted listing changed: %v", items)
	}
}
