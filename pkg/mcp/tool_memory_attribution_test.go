package mcp

import (
	"context"
	"fmt"
	"reflect"
	"testing"
)

func TestRecallProvenanceLinkDoesNotInheritRetirement(t *testing.T) {
	for _, pg := range []bool{false, true} {
		t.Run(fmt.Sprint(pg), func(t *testing.T) {
			d, ctx := memoryCommitEvidenceFixture(t, pg)
			save := func(key, value, parent string) string {
				t.Helper()
				args := map[string]any{"key": key, "value": value, "collection": "levara", "room": "memory", "hall": "fact"}
				if parent != "" {
					args["supersedes_memory_id"] = parent
				}
				if got := ToolSaveMemory(ctx, d, args); got.IsError {
					t.Fatal(got.Content)
				}
				var id string
				if err := d.DB().QueryRow(d.Q(`SELECT id FROM memories WHERE key=$1 AND owner_id='owner-a' AND collection_name='levara' AND superseded_by=''`), key).Scan(&id); err != nil {
					t.Fatal(err)
				}
				return id
			}
			aID := save("attribution-original", "old fact", "")
			bID := save("attribution-provenance", "provenance-only statement", aID)
			var child, reason, retiredAt string
			if err := d.DB().QueryRow(d.Q(`SELECT superseded_by,supersession_reason,COALESCE(CAST(valid_until AS TEXT),'') FROM memories WHERE id=$1`), aID).Scan(&child, &reason, &retiredAt); err != nil {
				t.Fatal(err)
			}
			if child != "" || reason != "" || retiredAt != "" {
				t.Fatal("provenance-only save retired its predecessor")
			}
			if got := ToolSupersedeMemory(ctx, d, map[string]any{"old_memory_id": aID, "new_value": "replacement fact", "reason": "actual-A-to-C"}); got.IsError {
				t.Fatal(got.Content)
			}
			var cID string
			if err := d.DB().QueryRow(`SELECT id FROM memories WHERE key='attribution-original' AND owner_id='owner-a' AND superseded_by=''`).Scan(&cID); err != nil {
				t.Fatal(err)
			}
			if err := d.DB().QueryRow(d.Q(`SELECT superseded_by,supersession_reason,COALESCE(CAST(valid_until AS TEXT),'') FROM memories WHERE id=$1`), aID).Scan(&child, &reason, &retiredAt); err != nil {
				t.Fatal(err)
			}
			if child != cID || reason != "actual-A-to-C" || retiredAt == "" {
				t.Fatal("actual supersede did not establish SQL retirement")
			}
			var base *fakeDeps
			if pg {
				base = d.Deps.(*postgresMemoryDeps).fakeDeps
			} else {
				base = d.Deps.(*fakeDeps)
			}
			for _, mode := range []string{"sql", "vector", "historical"} {
				t.Run(mode, func(t *testing.T) {
					base.embedAvailable = mode != "sql"
					query, wantCalls := "attribution-provenance", 0
					if base.embedAvailable {
						query, wantCalls = "semantic-attribution-probe", 1
						var literalMatches int
						if err := d.DB().QueryRow(d.Q(`SELECT COUNT(*) FROM memories WHERE key LIKE $1 OR value LIKE $2`), "%"+query+"%", "%"+query+"%").Scan(&literalMatches); err != nil {
							t.Fatal(err)
						}
						if literalMatches != 0 {
							t.Fatal("semantic probe can match SQL fallback")
						}
					}
					embedCalls, searchCalls := 0, 0
					base.embedFn = func(_ context.Context, q string) ([]float32, error) {
						embedCalls++
						if q != query {
							t.Fatalf("unexpected embedding query=%q", q)
						}
						return []float32{1}, nil
					}
					base.searchFn = func(collection string, _ []float32, k int) ([]SearchResult, error) {
						searchCalls++
						if collection != "_memories_levara" || k != 10 {
							t.Fatalf("unexpected search collection=%q k=%d", collection, k)
						}
						return []SearchResult{{ID: bID, Score: 1}}, nil
					}
					got := ToolRecallMemory(ctx, d, map[string]any{"query": query, "collection": "levara", "room": "memory", "hall": "fact", "include_superseded": mode == "historical"})
					if embedCalls != wantCalls || searchCalls != wantCalls {
						t.Fatalf("provider calls embed=%d search=%d want=%d", embedCalls, searchCalls, wantCalls)
					}
					if got.IsError {
						t.Fatal(got.Content)
					}
					rows := decodeRecallResults(t, got)
					if len(rows) != 1 || rows[0]["id"] != bID {
						t.Fatalf("provenance-only recall=%+v", rows)
					}
					row := rows[0]
					if row["supersession_state"] != "active" || row["supersedes_memory_id"] != aID || row["superseded_by"] != "" || row["supersession_reason"] != "" || row["superseded_at"] != "" {
						t.Errorf("provenance link inherited A-to-C retirement: %+v", row)
					}
				})
			}
			base.embedAvailable = false
			got := ToolRecallMemory(ctx, d, map[string]any{"query": "attribution-original", "collection": "levara"})
			if got.IsError {
				t.Fatal(got.Content)
			}
			rows := decodeRecallResults(t, got)
			if len(rows) != 1 || rows[0]["id"] != cID || rows[0]["supersession_reason"] != reason || rows[0]["superseded_at"] != retiredAt {
				t.Fatalf("actual replacement lost inherited metadata: %+v", rows)
			}
			var parent string
			if err := d.DB().QueryRow(d.Q(`SELECT supersedes_memory_id,superseded_by,supersession_reason,COALESCE(CAST(valid_until AS TEXT),'') FROM memories WHERE id=$1`), bID).Scan(&parent, &child, &reason, &retiredAt); err != nil {
				t.Fatal(err)
			}
			if parent != aID || child != "" || reason != "" || retiredAt != "" {
				t.Fatal("recall mutated provenance-only SQL state")
			}
		})
	}
}

func TestRecallRetirementMetadataNamespace(t *testing.T) {
	for _, pg := range []bool{false, true} {
		t.Run(fmt.Sprint(pg), func(t *testing.T) {
			for _, tc := range []struct {
				name, owner, parentOwner, parentCollection, parentChild string
				want                                                    bool
			}{
				{"own_reciprocal", "owner-a", "owner-a", "levara", "derived", true},
				{"shared_reciprocal", "", "", "levara", "derived", true},
				{"not_reciprocal", "owner-a", "owner-a", "levara", "other-child", false},
				{"shared_to_private", "owner-a", "", "levara", "derived", false},
				{"foreign_owner", "owner-a", "owner-b", "levara", "derived", false},
				{"sibling_collection", "owner-a", "owner-a", "other", "derived", false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					d, ctx := memoryCommitEvidenceFixture(t, pg)
					if _, err := d.DB().Exec(d.Q(`INSERT INTO memories(id,key,value,type,owner_id,collection_name,room,hall,created_at,updated_at,supersedes_memory_id) VALUES('derived','current-key','current value','project',$1,'levara','memory','fact','','','predecessor')`), tc.owner); err != nil {
						t.Fatal(err)
					}
					if _, err := d.DB().Exec(d.Q(`INSERT INTO memories(id,key,value,type,owner_id,collection_name,room,hall,created_at,updated_at,superseded_by,supersession_reason,valid_until) VALUES('predecessor','predecessor-key','old value','project',$1,$2,'memory','fact','','',$3,'private-retirement','2026-10-05T12:00:00Z')`), tc.parentOwner, tc.parentCollection, tc.parentChild); err != nil {
						t.Fatal(err)
					}
					got := ToolRecallMemory(ctx, d, map[string]any{"query": "current-key", "collection": "levara"})
					if got.IsError {
						t.Fatal(got.Content)
					}
					rows := decodeRecallResults(t, got)
					if len(rows) != 1 || rows[0]["id"] != "derived" {
						t.Fatalf("scoped recall=%+v", rows)
					}
					wantReason, wantAt := "", ""
					if tc.want {
						wantReason, wantAt = "private-retirement", "2026-10-05T12:00:00Z"
					}
					if rows[0]["supersession_reason"] != wantReason || rows[0]["superseded_at"] != wantAt {
						t.Fatalf("non-history namespace inherited retirement: %+v", rows[0])
					}
				})
			}
		})
	}
}

func TestRecallHistoricalLiteralBeyondSemanticCandidates(t *testing.T) {
	for _, pg := range []bool{false, true} {
		t.Run(fmt.Sprint(pg), func(t *testing.T) {
			d, ctx := memoryCommitEvidenceFixture(t, pg)
			var base *fakeDeps
			if pg {
				base = d.Deps.(*postgresMemoryDeps).fakeDeps
			} else {
				base = d.Deps.(*fakeDeps)
			}
			for _, r := range []struct{ id, value, parent, child, owner, collection, room, hall string }{
				{"literal-old", "obsoletekeyword old fact", "oldest", "current", "owner-a", "levara", "memory", "fact"},
				{"oldest", "prior statement", "", "literal-old", "owner-a", "levara", "memory", "fact"},
				{"current", "current statement", "literal-old", "", "owner-a", "levara", "memory", "fact"},
				{"foreign", "obsoletekeyword private", "", "", "owner-b", "levara", "memory", "fact"},
				{"sibling", "obsoletekeyword other project", "", "", "owner-a", "other", "memory", "fact"},
				{"room-control", "obsoletekeyword other room", "", "", "owner-a", "levara", "other", "fact"},
				{"hall-control", "obsoletekeyword other hall", "", "", "owner-a", "levara", "memory", "decision"},
			} {
				if _, err := d.DB().Exec(d.Q(`INSERT INTO memories(id,key,value,type,owner_id,collection_name,room,hall,created_at,updated_at,supersedes_memory_id,superseded_by) VALUES($1,$2,$3,'project',$4,$5,$6,$7,'','',$8,$9)`), r.id, r.id, r.value, r.owner, r.collection, r.room, r.hall, r.parent, r.child); err != nil {
					t.Fatal(err)
				}
			}
			var hits []SearchResult
			for i := 0; i < 10; i++ {
				id := fmt.Sprintf("semantic-%d", i)
				if _, err := d.DB().Exec(d.Q(`INSERT INTO memories(id,key,value,type,owner_id,collection_name,room,hall,created_at,updated_at) VALUES($1,$2,'unrelated active fact','project','owner-a','levara','memory','fact','','')`), id, id); err != nil {
					t.Fatal(err)
				}
				hits = append(hits, SearchResult{ID: id, Score: 1})
			}
			args := map[string]any{"query": "obsoletekeyword", "collection": "levara", "room": "memory", "hall": "fact", "include_superseded": true}
			for _, enabled := range []bool{false, true} {
				t.Run(fmt.Sprintf("embed=%v", enabled), func(t *testing.T) {
					base.embedAvailable = enabled
					embedCalls, searchCalls := 0, 0
					base.embedFn = func(_ context.Context, q string) ([]float32, error) {
						embedCalls++
						if !enabled || q != "obsoletekeyword" {
							t.Fatalf("unexpected embedding: %q", q)
						}
						return []float32{1}, nil
					}
					base.searchFn = func(collection string, _ []float32, k int) ([]SearchResult, error) {
						searchCalls++
						if !enabled || collection != "_memories_levara" || k != 10 {
							t.Fatalf("unexpected search collection=%q k=%d", collection, k)
						}
						return hits, nil
					}
					got := ToolRecallMemory(ctx, d, args)
					if got.IsError {
						t.Fatal(got.Content)
					}
					rows := decodeRecallResults(t, got)
					want := map[string]bool{"literal-old": true, "oldest": true}
					if enabled {
						for _, h := range hits {
							want[h.ID] = true
						}
					}
					gotIDs := map[string]bool{}
					for _, r := range rows {
						gotIDs[r["id"].(string)] = true
					}
					if len(rows) == 0 || rows[0]["id"] != "literal-old" || !reflect.DeepEqual(gotIDs, want) {
						t.Fatalf("literal history suppressed or leaked: %+v want=%v", rows, want)
					}
					wantCalls := 0
					if enabled {
						wantCalls = 1
					}
					if embedCalls != wantCalls || searchCalls != wantCalls {
						t.Fatalf("provider calls embed=%d search=%d want=%d", embedCalls, searchCalls, wantCalls)
					}
				})
			}
		})
	}
}
