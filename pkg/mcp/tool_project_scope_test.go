package mcp

import (
	"context"
	"strings"
	"testing"
	"time"
)

type projectContextSpyDeps struct {
	Deps
	metaCalls int
	queries   int
	failQuery int
}

func (d *projectContextSpyDeps) CollectionMeta(name string) CollectionInfo {
	d.metaCalls++
	return CollectionInfo{Name: "SECRET_VECTOR", Records: 777, Dim: 999, Metric: "SECRET_METRIC"}
}

func (d *projectContextSpyDeps) Q(query string) string {
	d.queries++
	if d.queries == d.failQuery {
		return "SELECT missing_column FROM memories"
	}
	return d.Deps.Q(query)
}

func TestProjectContextScope(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			setup := func(t *testing.T) Deps {
				t.Helper()
				var d Deps
				if dialect == "sqlite" {
					d = setupProjectDB(t)
				} else {
					db := openPostgresMemoryTestDB(t)
					if _, err := db.Exec(`CREATE TABLE memories(id TEXT PRIMARY KEY,key TEXT,value TEXT,type TEXT DEFAULT 'fact',owner_id TEXT NOT NULL DEFAULT '',collection_name TEXT NOT NULL DEFAULT '',superseded_by TEXT NOT NULL DEFAULT '',valid_until TIMESTAMPTZ,updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()); CREATE TABLE graph_nodes(id TEXT PRIMARY KEY,name TEXT,type TEXT); CREATE TABLE interactions(id TEXT PRIMARY KEY,query TEXT,response TEXT,created_at TIMESTAMPTZ DEFAULT NOW())`); err != nil {
						t.Fatal(err)
					}
					d = &postgresMemoryDeps{fakeDeps: &fakeDeps{db: db}}
				}
				d.DB().SetMaxOpenConns(1)
				for _, row := range []struct{ id, owner, collection, value, retired string }{
					{"m1", "alice", "main", "ALICE_MAIN", ""}, {"m2", "", "main", "SHARED_MAIN", ""},
					{"m3", "bob", "main", "BOB_MAIN", ""}, {"m4", "alice", "main", "OLD_MAIN", "new"},
					{"r1", "alice", "related", "ALICE_RELATED", ""}, {"r2", "", "related", "SHARED_RELATED", ""},
					{"r3", "bob", "related", "BOB_RELATED", ""}, {"r4", "alice", "related", "OLD_RELATED", "new"},
					{"h1", "bob", "hidden", "SECRET_FOREIGN", ""}, {"s1", "alice", "sibling", "SIBLING_MAIN", ""},
					{"q1", "alice", "one' OR 1=1 --", "EXACT_QUOTED", ""},
				} {
					if _, err := d.DB().Exec(d.Q(`INSERT INTO memories(id,key,owner_id,collection_name,value,superseded_by) VALUES($1,$2,$3,$4,$5,$6)`), row.id, row.id, row.owner, row.collection, row.value, row.retired); err != nil {
						t.Fatal(err)
					}
				}
				for _, query := range []string{`INSERT INTO graph_nodes(id,name,type) VALUES('g1','secret','SECRET_GRAPH')`, `INSERT INTO interactions(id,query,response) VALUES('i1','SECRET_CHAT','SECRET_RESPONSE')`} {
					if _, err := d.DB().Exec(query); err != nil {
						t.Fatal(err)
					}
				}
				return d
			}
			for _, tc := range []struct {
				name, owner, collection string
				related                 []any
				want, absent            []string
			}{
				{"main-related", "alice", "main", []any{"related", "hidden", "missing"}, []string{"ALICE_MAIN", "SHARED_MAIN", "ALICE_RELATED", "SHARED_RELATED"}, nil},
				{"anonymous", "", "main", []any{"related"}, []string{"SHARED_MAIN", "SHARED_RELATED"}, []string{"ALICE_MAIN", "ALICE_RELATED"}},
				{"foreign-only", "alice", "hidden", nil, []string{"no memories"}, nil},
				{"unknown", "alice", "missing", nil, []string{"no memories"}, nil},
				{"literal-quoted", "alice", "one' OR 1=1 --", nil, []string{"EXACT_QUOTED"}, []string{"ALICE_MAIN", "SHARED_MAIN"}},
				{"invalid-related-items", "alice", "main", []any{nil, 42, "", "related"}, []string{"ALICE_MAIN", "ALICE_RELATED"}, nil},
			} {
				t.Run(tc.name, func(t *testing.T) {
					d := &projectContextSpyDeps{Deps: setup(t)}
					ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
					defer cancel()
					if tc.owner != "" {
						ctx = context.WithValue(ctx, UserIDKey, tc.owner)
					}
					got := ToolGetProjectContext(ctx, d, map[string]any{"collection": tc.collection, "include_related": tc.related, "owner_id": "bob", "actor_id": "bob"})
					if got.IsError {
						t.Fatalf("unexpected error: %+v", got)
					}
					structured, ok := got.StructuredContent.(map[string]any)
					if !ok || len(structured) != 2 || structured["collection"] != tc.collection {
						t.Fatalf("schema: %+v", got)
					}
					text, _ := structured["text"].(string)
					for _, want := range tc.want {
						if !strings.Contains(text, want) {
							t.Errorf("missing %q in %q", want, text)
						}
					}
					absent := append([]string{"BOB_MAIN", "BOB_RELATED", "OLD_MAIN", "OLD_RELATED", "SIBLING_MAIN", "SECRET_FOREIGN", "SECRET_VECTOR", "SECRET_METRIC", "SECRET_GRAPH", "SECRET_CHAT", "SECRET_RESPONSE"}, tc.absent...)
					for _, secret := range absent {
						if strings.Contains(text, secret) {
							t.Errorf("exposed %q in %q", secret, text)
						}
					}
					for _, section := range []string{"Collection Stats", "Key Entity Types", "Recent Interactions"} {
						at := strings.Index(text, "## "+section)
						if at < 0 {
							t.Errorf("missing %s", section)
							continue
						}
						tail := text[at+len("## "+section):]
						if next := strings.Index(tail, "## "); next >= 0 {
							tail = tail[:next]
						}
						if !strings.Contains(tail, "unavailable") {
							t.Errorf("%s lacks unavailable marker: %q", section, tail)
						}
					}
					if d.metaCalls != 0 {
						t.Errorf("unguarded vector metadata read %d times", d.metaCalls)
					}
					if d.DB().Stats().InUse != 0 {
						t.Fatal("connection retained")
					}
				})
			}
			for _, failure := range []string{"query", "related-query", "scan", "row-evaluation", "cancelled"} {
				t.Run(failure, func(t *testing.T) {
					base := setup(t)
					d := &projectContextSpyDeps{Deps: base}
					switch failure {
					case "query":
						d.failQuery = 1
					case "related-query":
						d.failQuery = 2
					case "scan":
						if _, err := base.DB().Exec(`ALTER TABLE memories RENAME TO memory_rows; CREATE VIEW memories AS SELECT id,key,value,NULL AS type,owner_id,collection_name,superseded_by,updated_at FROM memory_rows`); err != nil {
							t.Fatal(err)
						}
					case "row-evaluation":
						if _, err := base.DB().Exec(`ALTER TABLE memories RENAME TO memory_rows`); err != nil {
							t.Fatal(err)
						}
						expr := `json_extract('malformed', '$.x')`
						if dialect == "postgres" {
							expr = `CAST('bad-' || key AS INTEGER)::TEXT`
						}
						if _, err := base.DB().Exec(`CREATE VIEW memories AS SELECT id,key,CASE WHEN id='m1' THEN ` + expr + ` ELSE value END AS value,type,owner_id,collection_name,superseded_by,updated_at FROM memory_rows`); err != nil {
							t.Fatal(err)
						}
					}
					ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
					defer cancel()
					ctx = context.WithValue(ctx, UserIDKey, "alice")
					if failure == "cancelled" {
						cancel()
					}
					got := ToolGetProjectContext(ctx, d, map[string]any{"collection": "main", "include_related": []any{"related"}})
					if !got.IsError || got.StructuredContent != nil {
						t.Errorf("failure returned partial success: %+v", got)
					}
					for _, content := range got.Content {
						if strings.Contains(content.Text, "ALICE_") {
							t.Errorf("partial memory data: %+v", got)
						}
					}
					if base.DB().Stats().InUse != 0 {
						t.Fatal("connection retained after failure")
					}
				})
			}
		})
	}
	t.Run("nil-db", func(t *testing.T) {
		if got := ToolGetProjectContext(t.Context(), &fakeDeps{}, map[string]any{"collection": "main"}); !got.IsError {
			t.Fatalf("nil db returned success: %+v", got)
		}
	})
}
