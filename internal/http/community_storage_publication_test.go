package http

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/stek0v/levara/pkg/community"
	"reflect"
	"sort"
	"testing"
	"time"
)

func TestCommunityStoragePublication(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		for _, id := range []string{"a", "b", "c", "d"} {
			f.exec("INSERT INTO graph_nodes(id,name,type,description) VALUES($1,$1,'Entity','fixture description')", id)
		}
		dendro := func(id string, members ...string) community.Dendrogram {
			return community.Dendrogram{Resolution: 1, Levels: [][]community.Community{{{ID: id, Level: 0, Members: members, MemberCount: len(members), InternalWeight: 2}}}}
		}
		if err := community.ReplaceCommunities(ctx, f.db, dendro("prior", "a", "b", "c")); err != nil {
			t.Fatal(err)
		}
		f.exec("UPDATE graph_communities SET summary='prior summary',summary_embedding_id='prior vector' WHERE id='prior'")
		snapshot := func() string {
			t.Helper()
			var result [][][]any
			for _, query := range []string{"SELECT * FROM graph_communities ORDER BY id", "SELECT * FROM community_members ORDER BY community_id,node_id"} {
				rows, err := f.db.QueryContext(ctx, query)
				if err != nil {
					t.Fatal(err)
				}
				cols, err := rows.Columns()
				if err != nil {
					_ = rows.Close()
					t.Fatal(err)
				}
				var table [][]any
				for rows.Next() {
					values := make([]any, len(cols))
					dest := make([]any, len(cols))
					for i := range values {
						dest[i] = &values[i]
					}
					if err := rows.Scan(dest...); err != nil {
						_ = rows.Close()
						t.Fatal(err)
					}
					table = append(table, values)
				}
				err = rows.Err()
				closeErr := rows.Close()
				if err != nil || closeErr != nil {
					t.Fatalf("snapshot err=%v close=%v", err, closeErr)
				}
				result = append(result, table)
			}
			raw, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			return string(raw)
		}
		baseline := snapshot()
		reject := func(table, operation, condition string) func() {
			t.Helper()
			row := "NEW"
			if operation == "DELETE" {
				row = "OLD"
			}
			if GetDBProvider() == DBSQLite {
				f.exec(fmt.Sprintf("CREATE TRIGGER community_fixture_reject BEFORE %s ON %s WHEN %s BEGIN SELECT RAISE(ABORT,'community fixture rejection'); END", operation, table, condition))
				return func() { f.exec("DROP TRIGGER community_fixture_reject") }
			}
			f.exec(fmt.Sprintf("CREATE FUNCTION community_fixture_reject_fn() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF %s THEN RAISE EXCEPTION 'community fixture rejection'; END IF; RETURN %s; END $$", condition, row))
			f.exec(fmt.Sprintf("CREATE TRIGGER community_fixture_reject BEFORE %s ON %s FOR EACH ROW EXECUTE FUNCTION community_fixture_reject_fn()", operation, table))
			return func() {
				f.exec("DROP TRIGGER community_fixture_reject ON " + table)
				f.exec("DROP FUNCTION community_fixture_reject_fn()")
			}
		}
		for _, tc := range []struct{ name, table, operation, condition string }{
			{"delete_members", "community_members", "DELETE", "OLD.node_id='b'"},
			{"delete_communities", "graph_communities", "DELETE", "OLD.id='prior'"},
			{"latter_membership", "community_members", "INSERT", "NEW.node_id='bad'"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				cleanup := reject(tc.table, tc.operation, tc.condition)
				defer cleanup()
				if err := community.ReplaceCommunities(ctx, f.db, dendro("n", "a", "bad")); err == nil {
					t.Fatal("injected failure committed")
				}
				if got := snapshot(); got != baseline {
					t.Fatalf("rollback changed prior full rows: got=%s want=%s", got, baseline)
				}
			})
		}
		t.Run("latter_duplicate_short_id", func(t *testing.T) {
			next := dendro("n", "a")
			next.Levels[0] = append(next.Levels[0], next.Levels[0][0])
			if err := community.ReplaceCommunities(ctx, f.db, next); err == nil {
				t.Fatal("duplicate insertion committed")
			}
			if got := snapshot(); got != baseline {
				t.Fatalf("duplicate rollback changed prior rows: %s", got)
			}
		})
		t.Run("lookup_incremental", func(t *testing.T) {
			got, err := community.LookupCommunities(ctx, f.db, []string{"a", "b"}, 0)
			if err != nil || !reflect.DeepEqual(got, []string{"prior"}) {
				t.Fatalf("lookup=%v err=%v", got, err)
			}
			g := community.NewGraph([]string{"a", "b", "c"})
			g.AddEdge("a", "b", 1)
			g.AddEdge("b", "c", 1)
			if d, err := community.IncrementalUpdate(ctx, f.db, g, community.DefaultConfig()); err != nil || d == nil {
				t.Fatalf("incremental=%v err=%v", d, err)
			}
			f.exec("ALTER TABLE community_members RENAME TO unavailable_community_members")
			if _, err := community.IncrementalUpdate(ctx, f.db, g, community.DefaultConfig()); err == nil {
				t.Fatal("partition SQL failure became empty partition")
			}
			if _, err := community.LookupCommunities(ctx, f.db, []string{"a"}, 0); err == nil {
				t.Fatal("lookup SQL failure hidden")
			}
			f.exec("ALTER TABLE unavailable_community_members RENAME TO community_members")
		})
		t.Run("canceled", func(t *testing.T) {
			canceled, cancel := context.WithCancel(ctx)
			cancel()
			if err := community.ReplaceCommunities(canceled, f.db, dendro("cancel", "a")); err == nil {
				t.Fatal("canceled replacement committed")
			}
			if _, err := community.BuildGraphFromSQL(canceled, f.db); err == nil {
				t.Fatal("canceled graph build succeeded")
			}
			if _, err := community.LookupCommunities(canceled, f.db, []string{"a"}, 0); err == nil {
				t.Fatal("canceled lookup succeeded")
			}
			if _, err := community.IncrementalUpdate(canceled, f.db, community.NewGraph([]string{"a", "b", "c"}), community.DefaultConfig()); err == nil {
				t.Fatal("canceled partition load succeeded")
			}
			if got := snapshot(); got != baseline {
				t.Fatal("canceled call changed prior rows")
			}
		})
		t.Run("replace_positive_members_summary", func(t *testing.T) {
			if err := community.ReplaceCommunities(ctx, f.db, dendro("x", "a", "b", "a")); err != nil {
				t.Fatal(err)
			}
			var members int
			if err := f.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM community_members WHERE community_id='x'").Scan(&members); err != nil || members != 2 {
				t.Fatalf("members=%d err=%v", members, err)
			}
			got, err := community.LookupCommunities(ctx, f.db, []string{"a", "b"}, 0)
			if err != nil || !reflect.DeepEqual(got, []string{"x"}) {
				t.Fatalf("lookup=%v err=%v", got, err)
			}
			f.exec("UPDATE graph_communities SET generation='prior-generation', sources_json='[{\"prior\":true}]', lineage_verified=1 WHERE id='x'")
			provider := &recordingLLM{responses: []string{"new summary"}}
			if err := community.SummarizeHierarchy(ctx, dendro("x", "a", "b"), community.NewGraph([]string{"a", "b"}), community.SummarizeConfig{DB: f.db, LLMProvider: provider, LLMModel: "fixture", MinMembers: 2}); err != nil {
				t.Fatal(err)
			}
			var summary, generation, proof string
			var verified int
			if err := f.db.QueryRowContext(ctx, "SELECT summary,generation,sources_json,lineage_verified FROM graph_communities WHERE id='x'").Scan(&summary, &generation, &proof, &verified); err != nil || summary != "new summary" || generation != "" || proof != "[]" || verified != 0 {
				t.Fatalf("summary=%q generation=%q proof=%q verified=%d err=%v", summary, generation, proof, verified, err)
			}
		})
		t.Run("active_graph_pool_one", func(t *testing.T) {
			for _, e := range []struct{ id, src, dst, from, until string }{
				{"current", "a", "b", "2020-01-01T00:00:00.123100600Z", "2030-01-01T00:00:00.123100600Z"},
				{"future", "a", "c", "2030-01-01T00:00:00.123100600Z", "2040-01-01T00:00:00Z"},
				{"expired", "b", "c", "2000-01-01T00:00:00Z", "2020-01-01T00:00:00Z"},
			} {
				f.exec("INSERT INTO graph_edges(id,source_id,target_id,relationship_name,confidence,valid_from,valid_until) VALUES($1,$2,$3,'related_to',1,$4,$5)", e.id, e.src, e.dst, e.from, e.until)
			}
			f.exec("INSERT INTO graph_edges(id,source_id,target_id,relationship_name,confidence,valid_from) VALUES('open','c','d','related_to',1,NULL)")
			g, err := community.BuildGraphFromSQL(ctx, f.db)
			if err != nil {
				t.Fatal(err)
			}
			if g.NodeCount() != 4 {
				t.Fatalf("nodes=%d", g.NodeCount())
			}
			d := community.Louvain(g, community.DefaultConfig())
			var got [][]string
			for _, c := range d.Levels[0] {
				m := append([]string(nil), c.Members...)
				sort.Strings(m)
				got = append(got, m)
			}
			sort.Slice(got, func(i, j int) bool { return got[i][0] < got[j][0] })
			if !reflect.DeepEqual(got, [][]string{{"a", "b"}, {"c", "d"}}) {
				t.Fatalf("future/expired edge affected active graph: %v", got)
			}
			if GetDBProvider() == DBSQLite {
				f.exec("UPDATE graph_edges SET valid_from='malformed' WHERE id='future'")
				if _, err := community.BuildGraphFromSQL(ctx, f.db); err == nil {
					t.Fatal("malformed bound silently admitted")
				}
			}
		})
	})
}
