package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/community"
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
		`CREATE TABLE graph_nodes(id TEXT PRIMARY KEY, name TEXT, dataset_id TEXT NOT NULL DEFAULT '', properties TEXT NOT NULL DEFAULT '{}', updated_at TEXT)`,
		fmt.Sprintf(`CREATE TABLE graph_edges(id TEXT PRIMARY KEY, source_id TEXT, target_id TEXT, relationship_name TEXT, properties TEXT, valid_from %s, valid_until %s, superseded_by TEXT, confidence REAL, dataset_id TEXT NOT NULL DEFAULT '', updated_at TEXT)`, validityType, validityType),
		`CREATE TABLE graph_communities(id TEXT PRIMARY KEY, level INTEGER, parent_id TEXT, member_count INTEGER, summary TEXT, generation TEXT NOT NULL DEFAULT '',sources_json TEXT NOT NULL DEFAULT '[]',lineage_verified INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE community_members(id TEXT PRIMARY KEY, community_id TEXT, node_id TEXT)`,
		`INSERT INTO graph_communities(id,level,parent_id,member_count,summary) VALUES ('private-summary',0,'',2,'foreign confidential summary')`,
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
			if got := ToolQueryEntity(ctx, deps, map[string]any{"name": "entity"}); got.IsError || !strings.Contains(got.Content[0].Text, `"node_ids": []`) {
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
			if got := ToolListCommunities(admin, deps, nil); got.IsError || len(decodeCommunities(t, got)) != 0 {
				t.Fatalf("admin accepted unverified legacy community: %+v", got)
			}
			// Minimal native unregistered document publication, scoped to this test.
			for _, query := range []string{
				"CREATE TABLE datasets(id TEXT PRIMARY KEY,owner_id TEXT)",
				"CREATE TABLE data(id TEXT PRIMARY KEY,source_revision BIGINT NOT NULL,raw_content_hash TEXT NOT NULL)",
				"CREATE TABLE dataset_data(dataset_id TEXT,data_id TEXT)",
				"CREATE TABLE document_resources(dataset_id TEXT,data_id TEXT,tenant_id TEXT,mode TEXT,acl_revision BIGINT,content_revision BIGINT,tombstoned BOOLEAN,hold BOOLEAN)",
				"CREATE TABLE document_index_publications(dataset_id TEXT,data_id TEXT,collection_name TEXT,content_revision BIGINT,generation TEXT,sources_json TEXT,requires_admin INTEGER,lineage_verified INTEGER,source_revision BIGINT,raw_content_hash TEXT,UNIQUE(dataset_id,data_id,collection_name))",
				"INSERT INTO datasets(id,owner_id) VALUES('native','admin')",
				"INSERT INTO dataset_data(dataset_id,data_id) VALUES('native','native-doc')",
			} {
				if _, err := fake.db.Exec(query); err != nil {
					t.Fatal(err)
				}
			}
			hash := strings.Repeat("e", 64)
			if _, err := fake.db.Exec(deps.Q("INSERT INTO data(id,source_revision,raw_content_hash) VALUES('native-doc',1,$1)"), hash); err != nil {
				t.Fatal(err)
			}
			nativeCtx, cancel := context.WithTimeout(admin, 3*time.Second)
			defer cancel()
			tx, err := fake.db.BeginTx(nativeCtx, nil)
			if err != nil {
				t.Fatal(err)
			}
			policy := access.SQLPolicy{DB: fake.db, Q: deps.Q}
			err = policy.WithReadTransaction(tx).CommitDocumentIndexVersioned(nativeCtx, access.Actor{UserID: "admin"}, access.DocumentRef{DatasetID: "native", DataID: "native-doc"}, 0, "docs", "native-doc-generation", access.DocumentPublicationLineage{SourcesJSON: "[]", SourceRevision: 1, RawContentHash: hash})
			if err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
			proof, _ := json.Marshal([]community.Source{{DatasetID: "native", DocumentID: "native-doc", Derived: true, Collection: "docs", Generation: "native-doc-generation", SourceRevision: 1, RawContentHash: hash, InputSHA256: strings.Repeat("f", 64)}})
			if _, err := fake.db.Exec(deps.Q("UPDATE graph_communities SET generation='native-community',sources_json=$1,lineage_verified=1 WHERE id='private-summary'"), string(proof)); err != nil {
				t.Fatal(err)
			}
			if got := ToolListCommunities(admin, deps, map[string]any{}); got.IsError || len(decodeCommunities(t, got)) != 1 {
				t.Errorf("active admin native communities: %+v", got)
			}
			selected := context.WithValue(admin, TenantIDKey, "native")
			if got := ToolListCommunities(selected, deps, nil); !got.IsError {
				t.Errorf("selected tenant accepted global publication: %+v", got)
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

func TestGraphACLLargeDatasetScopeUsesBoundedParameters(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			deps, fake := graphACLFixture(t, dialect)
			allowed := make([]string, 0, 23000)
			allowed = append(allowed, "owned")
			for i := 1; i < 23000; i++ {
				allowed = append(allowed, fmt.Sprintf("unused-%d", i))
			}
			fake.allowedDatasetIDs = allowed
			ctx := context.WithValue(context.Background(), UserIDKey, "alice")

			got := ToolQueryEntity(ctx, deps, map[string]any{"name": "entity", "limit": float64(1)})
			if got.IsError {
				t.Fatalf("large allowed scope: %+v", got)
			}
			var result struct {
				NodeIDs []string         `json:"node_ids"`
				Edges   []map[string]any `json:"edges"`
			}
			if err := json.Unmarshal([]byte(got.Content[0].Text), &result); err != nil {
				t.Fatal(err)
			}
			if len(result.Edges) != 1 || result.Edges[0]["id"] != "owned-edge" {
				t.Fatalf("large scope result: %+v", result)
			}

			got = ToolQueryEntity(ctx, deps, map[string]any{"name": "entity", "dataset_id": "owned", "limit": float64(1)})
			if got.IsError || !strings.Contains(got.Content[0].Text, "owned-edge") {
				t.Fatalf("explicit scope control: %+v", got)
			}
		})
	}
}

func TestGraphDatasetPredicateParameterCountIsBounded(t *testing.T) {
	allowed := make([]string, 23000)
	for i := range allowed {
		allowed[i] = fmt.Sprintf("dataset-%d", i)
	}
	for _, dialect := range []sqlcompat.Provider{sqlcompat.SQLite, sqlcompat.Postgres} {
		t.Run(string(dialect), func(t *testing.T) {
			previous := sqlcompat.CurrentProvider()
			t.Cleanup(func() { sqlcompat.SetProvider(previous) })
			sqlcompat.SetProvider(dialect)
			var args []any
			clause := graphDatasetPredicate("dataset_id", allowed, &args)
			if len(args) != 1 {
				t.Fatalf("parameter count=%d want=1", len(args))
			}
			if !strings.Contains(clause, "$1") || strings.Contains(clause, "$2") {
				t.Fatalf("unexpected clause: %s", clause)
			}
			var decoded []string
			if err := json.Unmarshal([]byte(args[0].(string)), &decoded); err != nil || len(decoded) != len(allowed) {
				t.Fatalf("encoded allowlist len=%d error=%v", len(decoded), err)
			}
		})
	}
}

func TestToolQueryEntityReportsNodeResolutionSQLError(t *testing.T) {
	deps := setupQueryEntityDB(t)
	if _, err := deps.db.Exec(`DROP TABLE graph_nodes`); err != nil {
		t.Fatal(err)
	}
	got := ToolQueryEntity(context.Background(), deps, map[string]any{"name": "entity"})
	if !got.IsError || !strings.Contains(strings.ToLower(got.Content[0].Text), "graph_nodes") {
		t.Fatalf("SQL failure was reported as not-found: %+v", got)
	}
}

func TestGraphACLQueryEntityTemporalBounds(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			deps, fake := graphACLFixture(t, dialect)
			// Unzoned SQL timestamp text denotes UTC in the SQLite fixture.
			// Match that interpretation for this PostgreSQL session only.
			if dialect == "postgres" {
				if _, err := fake.db.Exec("SET TIME ZONE 'UTC'"); err != nil {
					t.Fatal(err)
				}
			}
			fake.allowedDatasetIDs = []string{"owned"}
			ctx := context.WithValue(context.Background(), UserIDKey, "alice")
			if _, err := fake.db.Exec("DELETE FROM graph_edges WHERE id <> 'owned-edge'"); err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC().Truncate(time.Second)
			past := now.Add(-24 * time.Hour).Format(time.RFC3339)
			future := now.Add(24 * time.Hour).Format(time.RFC3339)
			expired := now.Add(-time.Second).Format(time.RFC3339)
			const start = "2026-01-15T12:00:00Z"
			const end = "2026-01-15T13:00:00Z"
			for _, tc := range []struct {
				name        string
				from, until any
				asOf        string
				want        int
			}{
				{"unbounded_current", nil, nil, "", 1},
				{"past_open_current", past, nil, "", 1},
				{"future_open_current", future, nil, "", 0},
				{"future_open_snapshot", future, nil, future, 1},
				{"future_upper_current", nil, future, "", 1},
				{"same_day_expired_rfc3339", nil, expired, "", 0},
				{"window_before_start", start, end, "2026-01-15T11:59:59Z", 0},
				{"window_exact_start", start, end, start, 1},
				{"window_before_end", start, end, "2026-01-15T12:59:59Z", 1},
				{"window_exact_end", start, end, end, 0},
				{"sql_text_with_rfc3339_snapshot", "2026-01-15 12:00:00", "2026-01-15 13:00:00", "2026-01-15T12:30:00Z", 1},
				{"sql_text_exact_end", "2026-01-15 12:00:00", "2026-01-15 13:00:00", end, 0},
				{"offset_equal_start", start, end, "2026-01-15T14:00:00+02:00", 1},
				{"offset_equal_end", start, end, "2026-01-15T15:00:00+02:00", 0},
				{"stored_offset_same_instant", "2026-01-15T14:00:00+02:00", "2026-01-15T15:00:00+02:00", "2026-01-15T12:30:00Z", 1},
				{"microseconds_exact_start", "2026-01-15T12:00:00.123100Z", "2026-01-15T12:00:00.123400Z", "2026-01-15T12:00:00.123100Z", 1},
				{"microseconds_interior", "2026-01-15T12:00:00.123100Z", "2026-01-15T12:00:00.123400Z", "2026-01-15T12:00:00.123200Z", 1},
				{"microseconds_exact_end", "2026-01-15T12:00:00.123100Z", "2026-01-15T12:00:00.123400Z", "2026-01-15T12:00:00.123400Z", 0},
				{"microseconds_offset_interior", "2026-01-15T14:00:00.123100+02:00", "2026-01-15T14:00:00.123400+02:00", "2026-01-15T12:00:00.123200Z", 1},
				{"nanoseconds_round_to_start", "2026-01-15T12:00:00.123100Z", "2026-01-15T12:00:00.123400Z", "2026-01-15T12:00:00.123099600Z", 1},
				{"nanoseconds_round_below_end", "2026-01-15T12:00:00.123100Z", "2026-01-15T12:00:00.123400Z", "2026-01-15T12:00:00.123399400Z", 1},
				{"nanoseconds_round_to_end", "2026-01-15T12:00:00.123100Z", "2026-01-15T12:00:00.123400Z", "2026-01-15T12:00:00.123399600Z", 0},
				{"stored_nanoseconds_round_to_start", "2026-01-15T12:00:00.123100400Z", "2026-01-15T12:00:00.123400400Z", "2026-01-15T12:00:00.123100Z", 1},
				{"snapshot_even_tie_down_inside", "2026-01-15T12:00:00.123100Z", "2026-01-15T12:00:00.123101Z", "2026-01-15T12:00:00.123100500Z", 1},
				{"snapshot_odd_tie_up_to_end", "2026-01-15T12:00:00.123101Z", "2026-01-15T12:00:00.123102Z", "2026-01-15T12:00:00.123101500Z", 0},
				{"stored_start_even_tie_down", "2026-01-15T12:00:00.123100500Z", "2026-01-15T12:00:00.123102Z", "2026-01-15T12:00:00.123100Z", 1},
				{"stored_start_odd_tie_up", "2026-01-15T12:00:00.123101500Z", "2026-01-15T12:00:00.123103Z", "2026-01-15T12:00:00.123101Z", 0},
				{"stored_end_even_tie_down", "2026-01-15T12:00:00.123099Z", "2026-01-15T12:00:00.123100500Z", "2026-01-15T12:00:00.123100Z", 0},
				{"stored_end_odd_tie_up", "2026-01-15T12:00:00.123100Z", "2026-01-15T12:00:00.123101500Z", "2026-01-15T12:00:00.123101Z", 1},
				{"snapshot_tie_carries_second", "2026-01-15T12:00:00.999999Z", "2026-01-15T12:00:01.000001Z", "2026-01-15T12:00:00.999999500Z", 1},
			} {
				t.Run(tc.name, func(t *testing.T) {
					if _, err := fake.db.Exec(deps.Q("UPDATE graph_edges SET valid_from=$1,valid_until=$2 WHERE id='owned-edge'"), tc.from, tc.until); err != nil {
						t.Fatal(err)
					}
					got := ToolQueryEntity(ctx, deps, map[string]any{"name": "entity", "dataset_id": "owned", "as_of": tc.asOf})
					if got.IsError || len(got.Content) != 1 {
						t.Fatalf("temporal query failed: %+v", got)
					}
					var result struct {
						Edges []struct {
							ID string `json:"id"`
						} `json:"edges"`
					}
					if err := json.Unmarshal([]byte(got.Content[0].Text), &result); err != nil {
						t.Fatal(err)
					}
					if len(result.Edges) != tc.want || (tc.want == 1 && result.Edges[0].ID != "owned-edge") {
						t.Fatalf("edges=%+v want=%d from=%v until=%v as_of=%q", result.Edges, tc.want, tc.from, tc.until, tc.asOf)
					}
				})
			}
		})
	}
}

func TestGraphACLQueryEntityAsOfInputContract(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			deps, fake := graphACLFixture(t, dialect)
			fake.allowedDatasetIDs = []string{"owned"}
			ctx := context.WithValue(context.Background(), UserIDKey, "alice")
			if _, err := fake.db.Exec("DELETE FROM graph_edges WHERE id <> 'owned-edge'"); err != nil {
				t.Fatal(err)
			}
			// The fully-open edge would be returned by any accidental fallback
			// to current mode, or by NULL comparisons that bypass bad input.
			for _, tc := range []struct {
				name              string
				value             any
				supplied, invalid bool
			}{
				{"omitted_current", nil, false, false},
				{"empty_current", "", true, false},
				{"valid_offset", "2026-01-15T14:30:00+02:00", true, false},
				{"valid_nanoseconds", "2026-01-15T12:30:00.123456789Z", true, false},
				{"malformed", "not-a-timestamp", true, true},
				{"invalid_date", "2026-02-30T12:00:00Z", true, true},
				{"date_without_time", "2026-01-15", true, true},
				{"number", float64(42), true, true},
				{"null", nil, true, true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					args := map[string]any{"name": "entity", "dataset_id": "owned"}
					if tc.supplied {
						args["as_of"] = tc.value
					}
					got := ToolQueryEntity(ctx, deps, args)
					if tc.invalid {
						if !got.IsError || len(got.Content) != 1 || !strings.Contains(got.Content[0].Text, "as_of must be an RFC3339 timestamp string") || strings.Contains(got.Content[0].Text, "owned-edge") {
							t.Fatalf("invalid timestamp disclosed data or fell back: %+v", got)
						}
						return
					}
					if got.IsError || len(got.Content) != 1 {
						t.Fatalf("valid timestamp rejected: %+v", got)
					}
					var result struct {
						AsOf  string `json:"as_of"`
						Edges []struct {
							ID string `json:"id"`
						} `json:"edges"`
					}
					if err := json.Unmarshal([]byte(got.Content[0].Text), &result); err != nil {
						t.Fatal(err)
					}
					expectedEcho, _ := tc.value.(string)
					if result.AsOf != expectedEcho || len(result.Edges) != 1 || result.Edges[0].ID != "owned-edge" {
						t.Fatalf("valid timestamp echo or open edge lost: %+v", result)
					}
				})
			}
		})
	}
}

func TestGraphACLQueryEntityTemporalPaging(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			deps, fake := graphACLFixture(t, dialect)
			fake.allowedDatasetIDs = []string{"owned"}
			ctx := context.WithValue(context.Background(), UserIDKey, "alice")
			if _, err := fake.db.Exec("DELETE FROM graph_edges WHERE id <> 'owned-edge'"); err != nil {
				t.Fatal(err)
			}
			future := time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339)
			for i := 0; i < 129; i++ {
				if _, err := fake.db.Exec(deps.Q(`INSERT INTO graph_edges(id,source_id,target_id,relationship_name,properties,valid_from,superseded_by,confidence,dataset_id,updated_at)
					VALUES($1,'owned-entity','owned-target','future','{}',$2,'',1,'owned','2026-01-01')`), fmt.Sprintf("a-future-%03d", i), future); err != nil {
					t.Fatal(err)
				}
			}
			if dialect == "sqlite" {
				// SQLite TEXT can contain malformed bounds; PostgreSQL's column
				// type rejects them before publication. Keep SQLite fail-closed.
				for _, column := range []string{"valid_from", "valid_until"} {
					if _, err := fake.db.Exec(`INSERT INTO graph_edges(id,source_id,target_id,relationship_name,properties,` + column + `,superseded_by,confidence,dataset_id,updated_at)
						VALUES('a-invalid-` + column + `','owned-entity','owned-target','invalid','{}','not-a-time','',1,'owned','2026-01-01')`); err != nil {
						t.Fatal(err)
					}
				}
			}
			got := ToolQueryEntity(ctx, deps, map[string]any{"name": "entity", "dataset_id": "owned", "limit": float64(1)})
			if got.IsError || len(got.Content) != 1 {
				t.Fatalf("paged temporal query failed: %+v", got)
			}
			var result struct {
				Edges []struct {
					ID string `json:"id"`
				} `json:"edges"`
			}
			if err := json.Unmarshal([]byte(got.Content[0].Text), &result); err != nil {
				t.Fatal(err)
			}
			if len(result.Edges) != 1 || result.Edges[0].ID != "owned-edge" {
				t.Fatalf("hidden temporal candidates starved later valid edge: %+v", result)
			}
		})
	}
}
