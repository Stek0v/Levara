package http

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stek0v/levara/pkg/graph"
	"github.com/stek0v/levara/pkg/orchestrator"
)

func TestGraphTemporalEpisodeHistory(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		type episode struct {
			ID, Target, Relation, From, Until, Successor, Properties string
		}
		read := func(source, dataset, relation string) []episode {
			t.Helper()
			query, args := QArgs(`SELECT id,target_id,relationship_name,CAST(valid_from AS TEXT),COALESCE(CAST(valid_until AS TEXT),''),superseded_by,CAST(properties AS TEXT)
			 FROM graph_edges WHERE source_id=$1 AND dataset_id=$2 AND LOWER(relationship_name)=LOWER($3) ORDER BY id`, source, dataset, relation)
			rows, err := f.db.QueryContext(ctx, query, args...)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			var out []episode
			for rows.Next() {
				var e episode
				if err := rows.Scan(&e.ID, &e.Target, &e.Relation, &e.From, &e.Until, &e.Successor, &e.Properties); err != nil {
					t.Fatal(err)
				}
				for _, raw := range []string{e.From, e.Until} {
					if raw == "" {
						continue
					}
					var stamp time.Time
					var parseErr error
					for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05.999999999-07"} {
						stamp, parseErr = time.Parse(layout, raw)
						if parseErr == nil {
							break
						}
					}
					if parseErr != nil || stamp.Nanosecond()%int(time.Microsecond) != 0 {
						t.Fatalf("new episode timestamp lacks microsecond alignment: %q err=%v", raw, parseErr)
					}
				}
				out = append(out, e)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			return out
		}
		write := func(dataset string, nodes []graph.DedupNode, edges ...graph.DedupEdge) {
			t.Helper()
			n, e, err := orchestrator.UpsertGraphToPostgres(ctx, f.db, dataset, nodes, edges)
			if err != nil || n != len(nodes) || e != len(edges) {
				t.Fatalf("graph upsert: nodes=%d edges=%d err=%v", n, e, err)
			}
		}
		seed := func(source string) {
			t.Helper()
			write("alpha", []graph.DedupNode{{ID: source, Name: source, Type: "Entity"}})
		}
		edge := func(source, target, relation string) graph.DedupEdge {
			return graph.DedupEdge{SourceID: source, TargetID: target, RelationshipName: relation, EdgeText: target}
		}
		chain := func(source, dataset, relation string, wantEpisodes int) {
			t.Helper()
			query, args := QArgs(`SELECT COUNT(*),SUM(CASE WHEN valid_until IS NULL THEN 1 ELSE 0 END),
			 SUM(CASE WHEN valid_until < valid_from THEN 1 ELSE 0 END)
			 FROM graph_edges WHERE source_id=$1 AND dataset_id=$2 AND LOWER(relationship_name)=LOWER($3)`, source, dataset, relation)
			var count, active, reversed int
			if err := f.db.QueryRowContext(ctx, query, args...).Scan(&count, &active, &reversed); err != nil || count != wantEpisodes || active != 1 || reversed != 0 {
				t.Fatalf("episode chain count=%d active=%d reversed=%d err=%v", count, active, reversed, err)
			}
			query, args = QArgs(`SELECT COUNT(*) FROM graph_edges old JOIN graph_edges next ON next.id=old.superseded_by
			 WHERE old.source_id=$1 AND old.dataset_id=$2 AND LOWER(old.relationship_name)=LOWER($3)
			 AND old.valid_until IS NOT NULL AND old.valid_until=next.valid_from
			 AND old.source_id=next.source_id AND old.dataset_id=next.dataset_id
			 AND LOWER(old.relationship_name)=LOWER(next.relationship_name)`, source, dataset, relation)
			var adjacent int
			if err := f.db.QueryRowContext(ctx, query, args...).Scan(&adjacent); err != nil || adjacent != wantEpisodes-1 {
				t.Fatalf("episode adjacency=%d want=%d err=%v", adjacent, wantEpisodes-1, err)
			}
		}
		t.Run("normalized_bound_timestamp", func(t *testing.T) {
			const source = "precision-source"
			seed(source)
			write("alpha", nil, edge(source, "A", "owns"))
			write("alpha", nil, edge(source, "B", "owns"))
			read(source, "alpha", "owns") // Actual writer intervals must be microsecond aligned.
			transition := time.Date(2026, 10, 6, 12, 0, 0, 123100600, time.UTC).Round(time.Microsecond)
			expected := time.Date(2026, 10, 6, 12, 0, 0, 123101000, time.UTC)
			if !transition.Equal(expected) {
				t.Fatalf("explicit normalization changed: %s want=%s", transition, expected)
			}
			// This proves the chosen normalized time.Time binding, not raw legacy
			// nanosecond imports, whose driver precision can differ.
			f.exec("INSERT INTO graph_edges(id,source_id,target_id,relationship_name,dataset_id,valid_from,valid_until) VALUES('precision-old','normalized-oracle','old','owns','alpha',$1,$2)",
				expected.Add(-time.Millisecond), transition)
			f.exec("INSERT INTO graph_edges(id,source_id,target_id,relationship_name,dataset_id,valid_from) VALUES('precision-new','normalized-oracle','new','owns','alpha',$1)", transition)
			query, args := QArgs("SELECT target_id,CAST(valid_from AS TEXT),COALESCE(CAST(valid_until AS TEXT),'') FROM graph_edges WHERE source_id='normalized-oracle' ORDER BY target_id")
			rows, err := f.db.QueryContext(ctx, query, args...)
			if err != nil {
				t.Fatal(err)
			}
			type interval struct {
				target      string
				from, until time.Time
			}
			var intervals []interval
			for rows.Next() {
				var target, fromRaw, untilRaw string
				if err := rows.Scan(&target, &fromRaw, &untilRaw); err != nil {
					_ = rows.Close()
					t.Fatal(err)
				}
				parse := func(raw string) time.Time {
					t.Helper()
					if raw == "" {
						return time.Time{}
					}
					for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05.999999999-07"} {
						if stamp, err := time.Parse(layout, raw); err == nil {
							return stamp
						}
					}
					_ = rows.Close()
					t.Fatalf("unparseable stored timestamp: %q", raw)
					return time.Time{}
				}
				intervals = append(intervals, interval{target, parse(fromRaw), parse(untilRaw)})
			}
			rowErr := rows.Err()
			closeErr := rows.Close()
			if rowErr != nil || closeErr != nil || len(intervals) != 2 {
				t.Fatalf("stored intervals=%v row_err=%v close_err=%v", intervals, rowErr, closeErr)
			}
			if intervals[0].target != "new" || !intervals[0].from.Equal(expected) || !intervals[0].until.IsZero() ||
				intervals[1].target != "old" || !intervals[1].from.Equal(expected.Add(-time.Millisecond)) || !intervals[1].until.Equal(expected) {
				t.Fatalf("normalized time.Time bindings changed exact stored instants: %+v want transition=%s", intervals, expected)
			}
			// Evaluate independently parsed instants: SQLite TEXT inequalities
			// compare formats lexically and are not a temporal precision oracle.
			for _, tc := range []struct {
				at   time.Time
				want []string
			}{
				{expected.Add(-time.Microsecond), []string{"old"}},
				{expected, []string{"new", "old"}},
				{expected.Add(time.Microsecond), []string{"new"}},
			} {
				var got []string
				for _, bound := range intervals {
					if !tc.at.Before(bound.from) && (bound.until.IsZero() || !tc.at.After(bound.until)) {
						got = append(got, bound.target)
					}
				}
				if !reflect.DeepEqual(got, tc.want) {
					t.Fatalf("parsed inclusive interval at=%s got=%v want=%v", tc.at, got, tc.want)
				}
			}
		})
		t.Run("A_B_A_retry", func(t *testing.T) {
			const source = "episode-source"
			seed(source)
			write("alpha", nil, edge(source, "A", "assigned_to"))
			first := read(source, "alpha", "assigned_to")[0]
			write("alpha", nil, edge(source, "B", "assigned_to"))
			closed := read(source, "alpha", "assigned_to")
			returned := edge(source, "A", "assigned_to")
			returned.EdgeText = "return to A with fresh evidence"
			write("alpha", nil, returned)
			beforeRetry := read(source, "alpha", "assigned_to")
			chain(source, "alpha", "assigned_to", 3)
			var activeID string
			for _, e := range beforeRetry {
				if e.ID == first.ID && (e.From != first.From || e.Until == "" || e.Successor == "") {
					t.Fatalf("old A episode reopened: %+v", e)
				}
				if e.Until == "" {
					activeID = e.ID
					if e.Target != "A" || e.ID == first.ID {
						t.Fatalf("return-to-A reused old episode: %+v", e)
					}
				}
			}
			for _, old := range closed {
				if old.ID != first.ID {
					continue
				}
				for _, now := range beforeRetry {
					if now.ID == old.ID && now != old {
						t.Fatalf("closed A changed after recurrence: before=%+v after=%+v", old, now)
					}
				}
			}
			write("alpha", nil, returned)
			if got := read(source, "alpha", "assigned_to"); !reflect.DeepEqual(got, beforeRetry) || activeID == "" {
				t.Fatalf("active retry changed episode identity/intervals: before=%+v after=%+v", beforeRetry, got)
			}
		})
		t.Run("mixed_case", func(t *testing.T) {
			const source = "case-source"
			seed(source)
			write("alpha", nil, edge(source, "A", "Assigned_To"))
			id := read(source, "alpha", "assigned_to")[0].ID
			write("alpha", nil, edge(source, "A", "ASSIGNED_TO"))
			if got := read(source, "alpha", "assigned_to"); len(got) != 1 || got[0].ID != id || got[0].Until != "" {
				t.Fatalf("mixed-case active retry: %+v", got)
			}
			write("alpha", nil, edge(source, "B", "assigned_to"))
			write("alpha", nil, edge(source, "A", "ASSIGNED_TO"))
			chain(source, "alpha", "assigned_to", 3)
		})
		t.Run("nonexclusive_coexistence", func(t *testing.T) {
			const source = "coexist-source"
			seed(source)
			write("alpha", nil, edge(source, "A", "knows"), edge(source, "B", "knows"))
			before := read(source, "alpha", "knows")
			write("alpha", nil, edge(source, "A", "knows"))
			if got := read(source, "alpha", "knows"); len(got) != 2 || !reflect.DeepEqual(got, before) {
				t.Fatalf("nonexclusive retry/coexistence changed: %+v", got)
			}
			for _, e := range before {
				if e.Until != "" || e.Successor != "" {
					t.Fatalf("nonexclusive edge superseded: %+v", e)
				}
			}
		})
		t.Run("dataset_isolation", func(t *testing.T) {
			const source = "dataset-source"
			seed(source)
			write("alpha", nil, edge(source, "A", "owns"))
			alpha := read(source, "alpha", "owns")
			write("beta", nil, edge(source, "A", "owns"))
			beta := read(source, "beta", "owns")
			if len(beta) != 1 || beta[0].ID == alpha[0].ID || !reflect.DeepEqual(alpha, read(source, "alpha", "owns")) {
				t.Fatalf("foreign legacy ID overwritten: alpha=%+v beta=%+v", alpha, beta)
			}
			write("beta", nil, edge(source, "B", "owns"))
			chain(source, "beta", "owns", 2)
			if !reflect.DeepEqual(alpha, read(source, "alpha", "owns")) {
				t.Fatal("other dataset transition superseded alpha")
			}
			write("alpha", nil, edge(source, "A", "knows"))
			alphaCoexisting := read(source, "alpha", "knows")
			write("beta", nil, edge(source, "A", "knows"))
			write("beta", nil, edge(source, "A", "knows"))
			betaCoexisting := read(source, "beta", "knows")
			if len(betaCoexisting) != 1 || betaCoexisting[0].ID == alphaCoexisting[0].ID || !reflect.DeepEqual(alphaCoexisting, read(source, "alpha", "knows")) {
				t.Fatalf("nonexclusive foreign ID/retry changed: alpha=%+v beta=%+v", alphaCoexisting, betaCoexisting)
			}
		})
		t.Run("missing_source_rollback", func(t *testing.T) {
			_, _, err := orchestrator.UpsertGraphToPostgres(ctx, f.db, "alpha", []graph.DedupNode{{ID: "aaa-transient", Name: "Transient", Type: "Entity"}}, []graph.DedupEdge{edge("zzz-missing", "A", "owns")})
			if err == nil {
				t.Fatal("missing exclusive source accepted")
			}
			var count int
			if err := f.db.QueryRow("SELECT COUNT(*) FROM graph_nodes WHERE id='aaa-transient'").Scan(&count); err != nil || count != 0 {
				t.Fatalf("missing source leaked node: count=%d err=%v", count, err)
			}
		})
		t.Run("batch_failure_rollback", func(t *testing.T) {
			const source = "rollback-source"
			seed(source)
			write("alpha", nil, edge(source, "A", "owns"))
			before := read(source, "alpha", "owns")
			if GetDBProvider() == DBSQLite {
				f.exec(`CREATE TRIGGER reject_episode BEFORE INSERT ON graph_edges WHEN NEW.target_id='bad'
				 BEGIN SELECT RAISE(ABORT,'reject episode'); END`)
				defer f.exec("DROP TRIGGER reject_episode")
			} else {
				f.exec(`CREATE FUNCTION reject_episode() RETURNS trigger LANGUAGE plpgsql AS $$
				 BEGIN IF NEW.target_id='bad' THEN RAISE EXCEPTION 'reject episode'; END IF; RETURN NEW; END; $$`)
				f.exec("CREATE TRIGGER reject_episode BEFORE INSERT ON graph_edges FOR EACH ROW EXECUTE FUNCTION reject_episode()")
				defer f.exec("DROP FUNCTION reject_episode() CASCADE")
			}
			_, _, err := orchestrator.UpsertGraphToPostgres(ctx, f.db, "alpha", []graph.DedupNode{{ID: source, Name: "Changed", Type: "Entity"}}, []graph.DedupEdge{edge(source, "B", "owns"), edge(source, "bad", "owns")})
			if err == nil || !reflect.DeepEqual(before, read(source, "alpha", "owns")) {
				t.Fatalf("failed batch changed episode history: err=%v", err)
			}
			var name string
			query, args := QArgs("SELECT name FROM graph_nodes WHERE id=$1", source)
			if err := f.db.QueryRow(query, args...).Scan(&name); err != nil || name != source {
				t.Fatalf("failed batch changed node: name=%q err=%v", name, err)
			}
		})
		t.Run("concurrent_edge_only", func(t *testing.T) {
			const source = "concurrent-source"
			seed(source)
			write("alpha", nil, edge(source, "start", "owns"))
			writers := []*sql.DB{f.db, f.db}
			var blocker *sql.Tx
			var pids [2]int
			if GetDBProvider() == DBPostgres {
				f.db.SetMaxOpenConns(2)
				var searchPath string
				if err := f.db.QueryRow("SHOW search_path").Scan(&searchPath); err != nil {
					t.Fatal(err)
				}
				for i := range writers {
					config, err := pgx.ParseConfig(os.Getenv("LEVARA_TEST_POSTGRES_DSN"))
					if err != nil {
						t.Fatal(err)
					}
					config.RuntimeParams["search_path"] = searchPath
					writers[i] = stdlib.OpenDB(*config)
					writers[i].SetMaxOpenConns(1)
					defer writers[i].Close()
					if err := writers[i].QueryRow("SELECT pg_backend_pid()").Scan(&pids[i]); err != nil {
						t.Fatal(err)
					}
				}
				if pids[0] == pids[1] {
					t.Fatal("PostgreSQL writers share a connection")
				}
				var err error
				blocker, err = f.db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer blocker.Rollback()
				if _, err := blocker.ExecContext(ctx, "UPDATE graph_nodes SET id=id WHERE id=$1", source); err != nil {
					t.Fatal(err)
				}
			} else {
				// SQLite uses the fixture's single pool connection; this proves pool
				// serialization, not independent-connection contention.
				f.db.SetMaxOpenConns(1)
			}
			start := make(chan struct{})
			results := make(chan error, 2)
			for i, target := range []string{"B", "C"} {
				go func(db *sql.DB, target string) {
					<-start
					_, _, err := orchestrator.UpsertGraphToPostgres(ctx, db, "alpha", nil, []graph.DedupEdge{edge(source, target, "owns")})
					results <- err
				}(writers[i], target)
			}
			close(start)
			if blocker != nil {
				waitCtx, stop := context.WithTimeout(ctx, 3*time.Second)
				defer stop()
				ticker := time.NewTicker(time.Millisecond)
				defer ticker.Stop()
				for {
					var waiting int
					if err := f.db.QueryRowContext(waitCtx, "SELECT COUNT(*) FROM pg_stat_activity WHERE pid IN ($1,$2) AND wait_event_type='Lock'", pids[0], pids[1]).Scan(&waiting); err != nil {
						t.Fatal(err)
					}
					if waiting == 2 {
						break
					}
					select {
					case <-waitCtx.Done():
						t.Fatal("independent writers did not both wait on source authority")
					case <-ticker.C:
					}
				}
				if err := blocker.Commit(); err != nil {
					t.Fatal(err)
				}
			}
			for range writers {
				if err := <-results; err != nil {
					t.Fatal(err)
				}
			}
			chain(source, "alpha", "owns", 3)
		})
		t.Run("crossed_node_source_batches", func(t *testing.T) {
			seed("cross-A")
			seed("cross-B")
			start := make(chan struct{})
			results := make(chan error, 2)
			for i := range 2 {
				go func(i int) {
					<-start
					node, source := "cross-A", "cross-B"
					if i == 1 {
						node, source = source, node
					}
					_, _, err := orchestrator.UpsertGraphToPostgres(ctx, f.db, "alpha", []graph.DedupNode{{ID: node, Name: node, Type: "Entity"}}, []graph.DedupEdge{edge(source, fmt.Sprint(i), "owns")})
					results <- err
				}(i)
			}
			close(start)
			for range 2 {
				if err := <-results; err != nil {
					t.Fatal(err)
				}
			}
			chain("cross-A", "alpha", "owns", 1)
			chain("cross-B", "alpha", "owns", 1)
		})
	})
}
