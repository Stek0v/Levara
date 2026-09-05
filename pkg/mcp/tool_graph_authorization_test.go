package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stek0v/levara/pkg/sqlcompat"
)

// Exercise the same MCP tool SQL with SQLite's positional rewrite and native
// PostgreSQL parameters. HTTP tests separately cover the real dataset policy.
func graphACLFixture(t *testing.T, dialect string) (Deps, *fakeDeps) {
	t.Helper()
	previous := sqlcompat.CurrentProvider()
	t.Cleanup(func() { sqlcompat.SetProvider(previous) })
	sqlcompat.SetProvider(sqlcompat.SQLite)
	if dialect == "postgres" {
		sqlcompat.SetProvider(sqlcompat.Postgres)
	}
	var deps Deps
	var fake *fakeDeps
	if dialect == "postgres" {
		fake = &fakeDeps{db: openPostgresMemoryTestDB(t)}
		deps = &postgresMemoryDeps{fakeDeps: fake}
	} else {
		db, err := sql.Open("sqlite3", t.TempDir()+"/graph.db")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		fake = &fakeDeps{db: db}
		deps = fake
	}
	validityType := "TEXT"
	if dialect == "postgres" {
		validityType = "TIMESTAMPTZ"
	}
	for _, query := range []string{
		`CREATE TABLE users(id TEXT PRIMARY KEY, is_active BOOLEAN, is_superuser BOOLEAN)`,
		`INSERT INTO users VALUES ('alice',TRUE,FALSE),('admin',TRUE,TRUE),('inactive',FALSE,TRUE)`,
		`CREATE TABLE graph_nodes(id TEXT PRIMARY KEY, name TEXT, dataset_id TEXT NOT NULL DEFAULT '', updated_at TEXT)`,
		fmt.Sprintf(`CREATE TABLE graph_edges(id TEXT PRIMARY KEY, source_id TEXT, target_id TEXT, relationship_name TEXT, properties TEXT, valid_from %s, valid_until %s, superseded_by TEXT, confidence REAL, dataset_id TEXT NOT NULL DEFAULT '', updated_at TEXT)`, validityType, validityType),
		`CREATE TABLE graph_communities(id TEXT PRIMARY KEY, level INTEGER, parent_id TEXT, member_count INTEGER, summary TEXT)`,
		`CREATE TABLE community_members(id TEXT PRIMARY KEY, community_id TEXT, node_id TEXT)`,
		`INSERT INTO graph_communities VALUES ('private-summary',0,'',2,'foreign confidential summary')`,
	} {
		if _, err := fake.db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	for _, dataset := range []string{"owned", "public", "shared", "foreign", ""} {
		for _, suffix := range []string{"entity", "target"} {
			if _, err := fake.db.Exec(deps.Q(`INSERT INTO graph_nodes(id,name,dataset_id) VALUES($1,$2,$3)`), dataset+"-"+suffix, suffix, dataset); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := fake.db.Exec(deps.Q(`INSERT INTO graph_edges(id,source_id,target_id,relationship_name,properties,superseded_by,confidence,dataset_id,updated_at) VALUES($1,$2,$3,'rel','{}','',1,$4,'2026-01-01')`), dataset+"-edge", dataset+"-entity", dataset+"-target", dataset); err != nil {
			t.Fatal(err)
		}
	}
	for _, edge := range []struct{ id, source, target, dataset string }{
		{"foreign-edge-owned-endpoints", "owned-entity", "owned-target", "foreign"},
		{"foreign-target", "owned-entity", "foreign-target", "owned"},
		{"foreign-source", "foreign-target", "owned-entity", "owned"},
		{"missing-target", "owned-entity", "absent", "owned"},
		{"global-edge-owned-endpoints", "owned-entity", "owned-target", ""},
	} {
		if _, err := fake.db.Exec(deps.Q(`INSERT INTO graph_edges(id,source_id,target_id,relationship_name,properties,superseded_by,confidence,dataset_id,updated_at) VALUES($1,$2,$3,'private-rel','{}','',1,$4,'2026-02-01')`), edge.id, edge.source, edge.target, edge.dataset); err != nil {
			t.Fatal(err)
		}
	}
	return deps, fake
}

func TestGraphACLQueryEntity(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			deps, fake := graphACLFixture(t, dialect)
			fake.allowedDatasetIDs = []string{"owned", "public", "shared"}
			ctx := context.WithValue(context.Background(), UserIDKey, "alice")
			for _, dataset := range []string{"", "owned", "public", "shared"} {
				t.Run("dataset="+dataset, func(t *testing.T) {
					for _, asOf := range []string{"", "2026-01-15T00:00:00Z"} {
						got := ToolQueryEntity(ctx, deps, map[string]any{"name": "entity", "dataset_id": dataset, "as_of": asOf})
						if got.IsError {
							t.Fatalf("allowed query: %+v", got)
						}
						var result struct {
							NodeIDs []string         `json:"node_ids"`
							Edges   []map[string]any `json:"edges"`
						}
						if err := json.Unmarshal([]byte(got.Content[0].Text), &result); err != nil {
							t.Fatal(err)
						}
						want := []string{"owned", "public", "shared"}
						if dataset != "" {
							want = []string{dataset}
						}
						if len(result.NodeIDs) != len(want) || len(result.Edges) != len(want) {
							t.Errorf("node/edge disclosure: %+v", result)
						}
						for _, id := range result.NodeIDs {
							if !graphACLContains(want, strings.TrimSuffix(id, "-entity")) {
								t.Errorf("foreign node: %s", id)
							}
						}
						for _, edge := range result.Edges {
							if !graphACLContains(want, strings.TrimSuffix(edge["id"].(string), "-edge")) {
								t.Errorf("foreign edge/endpoint: %+v", edge)
							}
						}
					}
				})
			}
			if got := ToolQueryEntity(ctx, deps, map[string]any{"name": "entity", "dataset_id": "foreign", "actor_id": "admin"}); !got.IsError {
				t.Errorf("explicit foreign dataset accepted: %+v", got)
			}
			fake.allowedDatasetIDs = []string{}
			if got := ToolQueryEntity(ctx, deps, map[string]any{"name": "entity"}); got.IsError || !strings.Contains(got.Content[0].Text, "No entity found") {
				t.Errorf("empty scope leaked graph: %+v", got)
			}
			fake.allowedDatasetIDs = nil
			if got := ToolQueryEntity(context.Background(), deps, map[string]any{"name": "entity"}); got.IsError || !strings.Contains(got.Content[0].Text, "foreign-entity") {
				t.Errorf("unrestricted compatibility lost: %+v", got)
			}
		})
	}
}

func TestGraphACLCommunities(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			deps, fake := graphACLFixture(t, dialect)
			ctx := context.WithValue(context.Background(), UserIDKey, "alice")
			for _, allowed := range [][]string{{"owned", "public", "shared"}, {}} {
				fake.allowedDatasetIDs = allowed
				got := ToolListCommunities(ctx, deps, map[string]any{"dataset_id": "owned"})
				if !got.IsError || strings.Contains(got.Content[0].Text, "foreign confidential") {
					t.Errorf("global summary exposed: %+v", got)
				}
			}
			fake.allowedDatasetIDs = nil
			for _, user := range []string{"inactive", "missing", "alice"} {
				if got := ToolListCommunities(context.WithValue(context.Background(), UserIDKey, user), deps, map[string]any{"actor_id": "admin"}); !got.IsError {
					t.Errorf("unverified admin %s read communities: %+v", user, got)
				}
				if got := ToolQueryEntity(context.WithValue(context.Background(), UserIDKey, user), deps, map[string]any{"name": "entity"}); !got.IsError {
					t.Errorf("unverified admin %s read graph: %+v", user, got)
				}
			}
			admin := context.WithValue(context.Background(), UserIDKey, "admin")
			if got := ToolListCommunities(admin, deps, map[string]any{}); got.IsError || len(decodeCommunities(t, got)) != 1 {
				t.Errorf("active admin communities: %+v", got)
			}
			if got := ToolQueryEntity(admin, deps, map[string]any{"name": "entity"}); got.IsError || !strings.Contains(got.Content[0].Text, "foreign-entity") {
				t.Errorf("active admin graph: %+v", got)
			}
			got := ToolListCommunities(context.Background(), deps, map[string]any{})
			if got.IsError || len(decodeCommunities(t, got)) != 1 {
				t.Errorf("unrestricted communities: %+v", got)
			}
			if _, err := fake.db.Exec(`DROP TABLE graph_communities`); err != nil {
				t.Fatal(err)
			}
			got = ToolListCommunities(context.Background(), deps, map[string]any{})
			if got.IsError || len(decodeCommunities(t, got)) != 0 {
				t.Errorf("SQL error shape changed: %+v", got)
			}
			fake.allowedDatasetIDs = []string{}
			if got := ToolListCommunities(ctx, deps, map[string]any{}); !got.IsError {
				t.Errorf("failed policy must deny: %+v", got)
			}
		})
	}
}

func TestGraphACLPrune(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			deps, fake := graphACLFixture(t, dialect)
			if _, err := fake.db.Exec(`UPDATE graph_edges SET superseded_by='replacement', valid_until='2000-01-01' WHERE id='foreign-edge'`); err != nil {
				t.Fatal(err)
			}
			for _, tc := range []struct{ user, permissions string }{{"alice", "write"}, {"inactive", "write"}, {"missing", "write"}, {"admin", "read"}} {
				for _, dry := range []bool{true, false} {
					ctx := context.WithValue(context.Background(), UserIDKey, tc.user)
					ctx = context.WithValue(ctx, ContextKey("mcp_api_key_permissions"), tc.permissions)
					got := ToolPruneGraph(ctx, deps, map[string]any{"dry_run": dry, "actor_id": "admin"})
					if !got.IsError || strings.Contains(got.Content[0].Text, "edges_would_delete") {
						t.Errorf("unauthorized preview/mutation user=%s dry=%v: %+v", tc.user, dry, got)
					}
				}
			}
			var n int
			if err := fake.db.QueryRow(`SELECT COUNT(*) FROM graph_edges WHERE id='foreign-edge'`).Scan(&n); err != nil || n != 1 {
				t.Fatalf("unauthorized prune changed foreign graph: count=%d error=%v", n, err)
			}
			{
				ctx := context.WithValue(context.Background(), UserIDKey, "admin")
				ctx = context.WithValue(ctx, ContextKey("mcp_api_key_permissions"), "write")
				got := ToolPruneGraph(ctx, deps, map[string]any{"dry_run": true})
				if got.IsError || !strings.Contains(got.Content[0].Text, `"edges_would_delete": 1`) {
					t.Errorf("admin preview: %+v", got)
				}
				got = ToolPruneGraph(ctx, deps, map[string]any{"dry_run": false})
				if got.IsError {
					t.Errorf("admin mutation: %+v", got)
				}
				if err := fake.db.QueryRow(`SELECT COUNT(*) FROM graph_edges WHERE id='foreign-edge'`).Scan(&n); err != nil || n != 0 {
					t.Errorf("admin prune count=%d error=%v", n, err)
				}
			}
			if _, err := fake.db.Exec(`DROP TABLE users`); err != nil {
				t.Fatal(err)
			}
			if got := ToolPruneGraph(context.WithValue(context.Background(), UserIDKey, "admin"), deps, map[string]any{}); !got.IsError {
				t.Errorf("admin SQL failure allowed: %+v", got)
			}
		})
	}
}

func graphACLContains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func TestGraphACLExplicitDatasetExcludesOtherReadableEndpoints(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			deps, fake := graphACLFixture(t, dialect)
			if _, err := fake.db.Exec(`INSERT INTO graph_edges(id,source_id,target_id,relationship_name,properties,superseded_by,confidence,dataset_id) VALUES('readable-cross','owned-entity','shared-target','rel','{}','',1,'owned')`); err != nil {
				t.Fatal(err)
			}
			fake.allowedDatasetIDs = []string{"owned", "shared", "public"}
			ctx := context.WithValue(context.Background(), UserIDKey, "alice")
			got := ToolQueryEntity(ctx, deps, map[string]any{"name": "entity"})
			if got.IsError || !strings.Contains(got.Content[0].Text, "readable-cross") {
				t.Fatalf("readable cross-dataset edge missing: %+v", got)
			}
			for _, user := range []string{"alice", "admin", ""} {
				ctx = context.WithValue(context.Background(), UserIDKey, user)
				if user != "alice" {
					fake.allowedDatasetIDs = nil
				}
				got = ToolQueryEntity(ctx, deps, map[string]any{"name": "entity", "dataset_id": "owned"})
				if got.IsError || strings.Contains(got.Content[0].Text, "readable-cross") || !strings.Contains(got.Content[0].Text, "owned-edge") {
					t.Errorf("explicit dataset mixed context for %q: %+v", user, got)
				}
			}
		})
	}
}
