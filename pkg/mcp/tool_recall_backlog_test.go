package mcp

import (
	"context"
	"testing"
	"time"

	"github.com/stek0v/levara/pkg/memoryindex"
)

func TestToolRecallMemoryDoesNotWaitForIndexBacklog(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var base *fakeDeps
			var deps Deps
			if dialect == "sqlite" {
				base = setupSaveRecallMemoryDB(t)
				deps = base
			} else {
				base = &fakeDeps{db: openPostgresMemoryTestDB(t)}
				createTaskTestSchema(t, base.db)
				deps = &postgresMemoryDeps{fakeDeps: base}
			}
			outbox, err := memoryindex.NewStore(base.db)
			if err != nil {
				t.Fatal(err)
			}
			base.memoryIndexOutbox = outbox
			for _, status := range []memoryindex.Status{memoryindex.Pending, memoryindex.Running, memoryindex.Failed} {
				job, err := outbox.Enqueue(context.Background(), memoryindex.Job{
					MemoryID: string(status), Operation: "upsert_vector", Digest: string(status),
					Collection: "project", OwnerID: "alice",
				})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := base.db.Exec(deps.Q("UPDATE memory_index_jobs SET status=$1 WHERE id=$2"), string(status), job.ID); err != nil {
					t.Fatal(err)
				}
			}
			if outbox.WaitReady(context.Background(), "project", "alice", 0) {
				t.Fatal("fixture must retain same-scope unfinished index jobs")
			}
			for _, row := range []struct{ id, value, owner, successor string }{
				{"first", "authoritative first value", "alice", ""},
				{"second", "authoritative second value", "alice", ""},
				{"foreign", "authoritative foreign value", "bob", ""},
				{"historical", "historical lexical needle", "alice", "first"},
			} {
				_, err := base.db.Exec(deps.Q(`INSERT INTO memories
					(id,key,value,type,owner_id,collection_name,room,hall,created_at,updated_at,superseded_by)
					VALUES($1,$2,$3,'project',$4,'project','memory','fact','','',$5)`),
					row.id, row.id, row.value, row.owner, row.successor)
				if err != nil {
					t.Fatal(err)
				}
			}
			for _, mode := range []string{"semantic", "sql", "history"} {
				t.Run(mode, func(t *testing.T) {
					embedCalls, searchCalls := 0, 0
					base.embedAvailable = mode == "semantic"
					base.embedFn = func(ctx context.Context, query string) ([]float32, error) {
						embedCalls++
						return []float32{1}, ctx.Err()
					}
					base.searchFn = func(collection string, vector []float32, topK int) ([]SearchResult, error) {
						searchCalls++
						if collection != "_memories_project" {
							t.Fatalf("collection=%s", collection)
						}
						// Reverse SQL insertion order, include a foreign candidate,
						// and let SQL supply current values rather than vector metadata.
						return []SearchResult{{ID: "second", Score: 1}, {ID: "foreign", Score: .9}, {ID: "first", Score: .8}}, nil
					}
					args := map[string]any{"collection": "project", "room": "memory", "hall": "fact"}
					want := []string{"second", "first"}
					switch mode {
					case "semantic":
						args["query"] = "nonliteral semantic probe"
					case "sql":
						args["query"] = "authoritative first"
						want = []string{"first"}
					case "history":
						args["query"] = "historical lexical needle"
						args["include_superseded"] = true
						want = []string{"historical"}
					}
					// The old implicit 200 ms queue wait exhausts this caller's
					// budget before it can reach authoritative SQL hydration.
					ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), UserIDKey, "alice"), 150*time.Millisecond)
					defer cancel()
					got := ToolRecallMemory(ctx, deps, args)
					if got.IsError || ctx.Err() != nil {
						t.Fatalf("recall unavailable during backlog: result=%+v context=%v", got, ctx.Err())
					}
					rows := decodeRecallResults(t, got)
					if len(rows) != len(want) {
						t.Fatalf("rows=%+v want IDs=%v", rows, want)
					}
					for i, id := range want {
						if rows[i]["id"] != id || rows[i]["owner_id"] != "alice" {
							t.Fatalf("rank/authority mismatch: rows=%+v want IDs=%v", rows, want)
						}
					}
					wantCalls := 0
					if mode == "semantic" {
						wantCalls = 1
						if rows[0]["value"] != "authoritative second value" {
							t.Fatalf("SQL hydration lost: %+v", rows)
						}
					}
					if embedCalls != wantCalls || searchCalls != wantCalls {
						t.Fatalf("embed/search calls=%d/%d want=%d", embedCalls, searchCalls, wantCalls)
					}
					if outbox.WaitReady(context.Background(), "project", "alice", 0) {
						t.Fatal("recall must not drain or rewrite the backlog")
					}
				})
			}
		})
	}
}
