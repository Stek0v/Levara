package mcp

import (
	"context"
	"fmt"
	"testing"
)

func TestRecallHistoricalCandidates(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			base := &fakeDeps{}
			var deps Deps = base
			if dialect == "sqlite" {
				base = setupSaveRecallMemoryDB(t)
				deps = base
			} else {
				base.db = openPostgresMemoryTestDB(t)
				createTaskTestSchema(t, base.db)
				deps = &postgresMemoryDeps{fakeDeps: base}
			}
			base.embedAvailable = true
			base.embedFn = func(context.Context, string) ([]float32, error) { return []float32{1}, nil }
			base.searchFn = func(string, []float32, int) ([]SearchResult, error) {
				return []SearchResult{{ID: "current", Score: 1}}, nil
			}
			for _, row := range []struct{ id, value, parent, child, owner, collection, room, hall string }{
				{"current", "Сегодня руководит Мария.", "previous", "", "alice", "project", "memory", "fact"},
				{"previous", "Раньше руководила Ольга.", "oldest", "current", "alice", "project", "memory", "fact"},
				{"oldest", "До Ольги руководил Илья.", "", "previous", "alice", "project", "memory", "fact"},
				{"foreign", "Раньше руководила Ольга.", "", "", "bob", "project", "memory", "fact"},
				{"other", "Раньше руководила Ольга.", "", "", "alice", "other", "memory", "fact"},
			} {
				_, err := deps.DB().Exec(deps.Q(`INSERT INTO memories(id,key,value,type,owner_id,collection_name,room,hall,created_at,updated_at,supersedes_memory_id,superseded_by)
					VALUES($1,$2,$3,'project',$4,$5,$6,$7,'','',$8,$9)`), row.id, row.id, row.value, row.owner, row.collection, row.room, row.hall, row.parent, row.child)
				if err != nil {
					t.Fatal(err)
				}
			}
			ctx := context.WithValue(context.Background(), UserIDKey, "alice")
			args := map[string]any{"query": "Ольга", "collection": "project", "room": "memory", "hall": "fact", "include_superseded": true}
			check := func(want int) []map[string]any {
				t.Helper()
				result := ToolRecallMemory(ctx, deps, args)
				if result.IsError {
					t.Fatal(result.Content)
				}
				rows := decodeRecallResults(t, result)
				if len(rows) != want {
					t.Fatalf("query %v got %+v, want %d", args, rows, want)
				}
				return rows
			}
			rows := check(3)
			if rows[0]["id"] != "previous" {
				t.Fatalf("literal historical source must not be suppressed: %+v", rows)
			}
			args["query"] = "история смены начальников" // no literal match; follow semantic current hit.
			rows = check(3)
			if rows[0]["id"] != "current" || rows[1]["id"] != "previous" || rows[2]["id"] != "oldest" {
				t.Fatalf("semantic chain: %+v", rows)
			}
			args["include_superseded"] = false
			check(1)
			args["include_superseded"] = true
			for _, change := range []string{"owner_id='bob'", "owner_id=''", "collection_name='other'", "room='other'", "hall='decision'", "superseded_by='forged'"} {
				t.Run(change, func(t *testing.T) {
					if change == "collection_name='other'" {
						delete(args, "collection")
					}
					if _, err := deps.DB().Exec("UPDATE memories SET " + change + " WHERE id='previous'"); err != nil {
						t.Fatal(err)
					}
					check(1)
					args["collection"] = "project"
					if _, err := deps.DB().Exec(`UPDATE memories SET owner_id='alice',collection_name='project',room='memory',hall='fact',superseded_by='current' WHERE id='previous'`); err != nil {
						t.Fatal(err)
					}
				})
			}
			// A corrupt reciprocal cycle is bounded by visited identities.
			if _, err := deps.DB().Exec(`UPDATE memories SET supersedes_memory_id='current' WHERE id='oldest'`); err != nil {
				t.Fatal(err)
			}
			if _, err := deps.DB().Exec(`UPDATE memories SET superseded_by='oldest' WHERE id='current'`); err != nil {
				t.Fatal(err)
			}
			check(3)
			if _, err := deps.DB().Exec(`DROP TABLE memories`); err != nil {
				t.Fatal(err)
			}
			if got := ToolRecallMemory(ctx, deps, args); !got.IsError {
				t.Fatal("SQL failure disguised as partial audit")
			}
		})
	}
}

func TestRecallHistoricalCandidatesBounded(t *testing.T) {
	deps := setupSaveRecallMemoryDB(t)
	deps.embedAvailable = true
	deps.embedFn = func(context.Context, string) ([]float32, error) { return []float32{1}, nil }
	deps.searchFn = func(string, []float32, int) ([]SearchResult, error) { return []SearchResult{{ID: "m0", Score: 1}}, nil }
	for i := 0; i < 25; i++ {
		parent, child := fmt.Sprintf("m%d", i+1), ""
		if i > 0 {
			child = fmt.Sprintf("m%d", i-1)
		}
		_, err := deps.db.Exec(`INSERT INTO memories(id,key,value,type,owner_id,collection_name,room,hall,created_at,updated_at,supersedes_memory_id,superseded_by) VALUES(?,?,'past event','project','','project','memory','fact','','',?,?)`, fmt.Sprintf("m%d", i), fmt.Sprintf("m%d", i), parent, child)
		if err != nil {
			t.Fatal(err)
		}
	}
	got := ToolRecallMemory(context.Background(), deps, map[string]any{"query": "semantic only", "collection": "project", "include_superseded": true})
	if got.IsError {
		t.Fatal(got.Content)
	}
	if rows := decodeRecallResults(t, got); len(rows) != recallMemorySQLLimit {
		t.Fatalf("want bounded %d history rows, got %d", recallMemorySQLLimit, len(rows))
	}
}

func TestRecallHistoricalHydrationFailure(t *testing.T) {
	deps := setupSaveRecallMemoryDB(t)
	deps.embedAvailable = true
	deps.embedFn = func(context.Context, string) ([]float32, error) {
		// The literal query succeeds; hydration fails after semantic search.
		if err := deps.db.Close(); err != nil {
			t.Fatal(err)
		}
		return []float32{1}, nil
	}
	deps.searchFn = func(string, []float32, int) ([]SearchResult, error) {
		return []SearchResult{{ID: "current", Score: 1}}, nil
	}
	got := ToolRecallMemory(context.Background(), deps, map[string]any{"query": "semantic only", "include_superseded": true})
	if !got.IsError {
		t.Fatal("SQL hydration failure disguised as successful empty history")
	}
}
