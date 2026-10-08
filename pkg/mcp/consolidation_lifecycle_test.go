package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stek0v/levara/pkg/consolidate"
	"github.com/stek0v/levara/pkg/memoryindex"
)

func consolidationLifecycleFixture(t *testing.T, pg bool) (*memoryCommitEvidenceDeps, context.Context) {
	t.Helper()
	d, parent := memoryCommitEvidenceFixture(t, pg)
	d.DB().SetMaxOpenConns(1)
	for _, stmt := range []string{
		`ALTER TABLE memories ADD COLUMN consolidated_from TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE memories ADD COLUMN consolidation_run_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE memories ADD COLUMN tier TEXT NOT NULL DEFAULT 'raw'`,
		`CREATE TABLE document_index_publications(id TEXT)`,
		`CREATE TABLE document_pipeline_statuses(id TEXT)`,
	} {
		if _, err := d.DB().Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	outbox, err := memoryindex.NewStore(d.DB())
	if err != nil {
		t.Fatal(err)
	}
	switch base := d.Deps.(type) {
	case *fakeDeps:
		base.memoryIndexOutbox = outbox
	case *postgresMemoryDeps:
		base.memoryIndexOutbox = outbox
	default:
		t.Fatalf("unexpected fixture %T", base)
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	t.Cleanup(cancel)
	for _, id := range []string{"a", "b", "c", "d"} {
		if _, err := d.DB().Exec(d.Q(`INSERT INTO memories(id,key,value,type,owner_id,collection_name,room,hall,created_at,updated_at) VALUES($1,$2,$3,'user','owner-a','levara','memory','fact',$4,$5)`), id, id, "private "+id, "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	return d, ctx
}

func consolidationMixedActions() []consolidate.Action {
	return []consolidate.Action{{Kind: consolidate.ActionMerge, SourceIDs: []string{"a"}, SurvivorID: "b"}, {Kind: consolidate.ActionAbstract, SourceIDs: []string{"c", "d"}, NewValue: "combined private fact", Room: "memory", Hall: "fact"}}
}

func consolidationSnapshot(t *testing.T, d Deps, ignoreRevision ...bool) map[string]string {
	t.Helper()
	rows, err := d.DB().Query(`SELECT * FROM memories`)
	if err != nil {
		t.Fatal(err)
	}
	all, err := scanConsolidationRows(rows)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, r := range all {
		if len(ignoreRevision) > 0 && ignoreRevision[0] {
			delete(r, "updated_at")
		}
		out[rowString(r, "id")] = consolidationHash(r)
	}
	return out
}

func TestConsolidationCapturedPlanGuards(t *testing.T) {
	for _, pg := range []bool{false, true} {
		t.Run(fmt.Sprint(pg), func(t *testing.T) {
			for _, mutation := range []string{
				`UPDATE memories SET is_pinned=TRUE WHERE id='a'`, `UPDATE memories SET value='later' WHERE id='a'`,
				`UPDATE memories SET source_task_id='later-proof' WHERE id='a'`, `UPDATE memories SET source_receipt_ids='["later"]' WHERE id='b'`,
				`UPDATE memories SET type='project' WHERE id='a'`, `UPDATE memories SET room='other' WHERE id='a'`,
				`UPDATE memories SET hall='decision' WHERE id='a'`, `UPDATE memories SET owner_id='owner-b' WHERE id='a'`,
				`UPDATE memories SET collection_name='other' WHERE id='a'`, `UPDATE memories SET superseded_by='later' WHERE id='a'`,
				`UPDATE memories SET tier='semantic' WHERE id='a'`, `DELETE FROM memories WHERE id='b'`,
			} {
				t.Run(mutation, func(t *testing.T) {
					d, ctx := consolidationLifecycleFixture(t, pg)
					s := &sqlStore{deps: d, collection: "levara"}
					if _, err := s.Candidates(ctx, "levara", "", ""); err != nil {
						t.Fatal(err)
					}
					if _, err := d.DB().Exec(mutation); err != nil {
						t.Fatal(err)
					}
					before := consolidationSnapshot(t, d)
					if err := s.Apply(ctx, "stale", consolidationMixedActions()); err == nil {
						t.Fatal("stale plan accepted")
					}
					if got := consolidationSnapshot(t, d); !reflect.DeepEqual(before, got) {
						t.Fatal("failed apply changed memories")
					}
					var runs, jobs int
					if err := d.DB().QueryRow(`SELECT COUNT(*) FROM consolidation_runs`).Scan(&runs); err != nil {
						t.Fatal(err)
					}
					if err := d.DB().QueryRow(`SELECT COUNT(*) FROM memory_index_jobs`).Scan(&jobs); err != nil {
						t.Fatal(err)
					}
					if runs != 0 || jobs != 0 {
						t.Fatalf("partial journal/outbox: %d/%d", runs, jobs)
					}
				})
			}
			for _, kind := range []string{"uncaptured", "overlap", "duplicate-run", "revoked", "outbox-failure"} {
				t.Run(kind, func(t *testing.T) {
					d, ctx := consolidationLifecycleFixture(t, pg)
					s := &sqlStore{deps: d, collection: "levara"}
					if _, err := s.Candidates(ctx, "levara", "", ""); err != nil {
						t.Fatal(err)
					}
					actions := consolidationMixedActions()
					switch kind {
					case "uncaptured":
						actions[0].SourceIDs = []string{"missing"}
					case "overlap":
						actions[1].SourceIDs = []string{"a", "c"}
					case "duplicate-run":
						if err := s.Apply(ctx, "run", actions); err != nil {
							t.Fatal(err)
						}
						if err := s.Revert(ctx, "run"); err != nil {
							t.Fatal(err)
						}
						if _, err := s.Candidates(ctx, "levara", "", ""); err != nil {
							t.Fatal(err)
						}
					case "revoked":
						if _, err := d.DB().Exec(`UPDATE users SET is_active=FALSE WHERE id='owner-a'`); err != nil {
							t.Fatal(err)
						}
					case "outbox-failure":
						if _, err := d.DB().Exec(`DROP TABLE memory_index_jobs`); err != nil {
							t.Fatal(err)
						}
					}
					before := consolidationSnapshot(t, d)
					if err := s.Apply(ctx, "run", actions); err == nil {
						t.Fatal("invalid plan accepted")
					}
					if got := consolidationSnapshot(t, d); !reflect.DeepEqual(before, got) {
						t.Fatal("failed apply changed memories")
					}
				})
			}
		})
	}
}

func TestConsolidationJournalRevertGuards(t *testing.T) {
	for _, pg := range []bool{false, true} {
		t.Run(fmt.Sprint(pg), func(t *testing.T) {
			for _, kind := range []string{"unchanged", "source-pin", "source-proof", "survivor-value", "generated-value", "truncated", "role-mismatch", "before-tamper", "foreign-owner", "legacy-run"} {
				t.Run(kind, func(t *testing.T) {
					d, ctx := consolidationLifecycleFixture(t, pg)
					s := &sqlStore{deps: d, collection: "levara"}
					original := consolidationSnapshot(t, d, true)
					if _, err := s.Candidates(ctx, "levara", "", ""); err != nil {
						t.Fatal(err)
					}
					if err := s.Apply(ctx, "mixed", consolidationMixedActions()); err != nil {
						t.Fatal(err)
					}
					var raw string
					if err := d.DB().QueryRow(`SELECT payload_json FROM consolidation_runs WHERE id='mixed'`).Scan(&raw); err != nil {
						t.Fatal(err)
					}
					if strings.Contains(raw, "private") {
						t.Fatal("journal copied private source text")
					}
					var journal []consolidationJournalRow
					if err := json.Unmarshal([]byte(raw), &journal); err != nil {
						t.Fatal(err)
					}
					runID := "mixed"
					switch kind {
					case "source-pin":
						_, err := d.DB().Exec(`UPDATE memories SET is_pinned=TRUE WHERE id='a'`)
						if err != nil {
							t.Fatal(err)
						}
					case "source-proof":
						_, err := d.DB().Exec(`UPDATE memories SET verification_status='verified' WHERE id='c'`)
						if err != nil {
							t.Fatal(err)
						}
					case "survivor-value":
						_, err := d.DB().Exec(`UPDATE memories SET value='later edit' WHERE id='b'`)
						if err != nil {
							t.Fatal(err)
						}
					case "generated-value":
						_, err := d.DB().Exec(`UPDATE memories SET value='later edit' WHERE tier='semantic'`)
						if err != nil {
							t.Fatal(err)
						}
					case "truncated":
						for _, j := range journal {
							if j.Role == "survivor" {
								journal = []consolidationJournalRow{j}
								break
							}
						}
					case "role-mismatch":
						for i := range journal {
							if journal[i].Role == "generated" {
								journal[i].Role = "survivor"
							}
						}
					case "before-tamper":
						for i := range journal {
							if journal[i].Role == "source" {
								journal[i].Before["consolidation_run_id"] = "corrupt-prior-state"
								break
							}
						}
					case "foreign-owner":
						d.actor.UserID = "owner-b"
					case "legacy-run":
						runID = "legacy"
					}
					if kind == "truncated" || kind == "role-mismatch" || kind == "before-tamper" {
						raw, err := json.Marshal(journal)
						if err != nil {
							t.Fatal(err)
						}
						if _, err := d.DB().Exec(d.Q(`UPDATE consolidation_runs SET payload_json=$1 WHERE id='mixed'`), string(raw)); err != nil {
							t.Fatal(err)
						}
					}
					before := consolidationSnapshot(t, d)
					err := s.Revert(ctx, runID)
					if kind == "unchanged" {
						if err != nil {
							t.Fatal(err)
						}
						if got := consolidationSnapshot(t, d, true); !reflect.DeepEqual(original, got) {
							t.Fatal("revert did not restore full originals")
						}
						if err := s.Revert(ctx, runID); err != nil {
							t.Fatal("authorized idempotent revert:", err)
						}
					} else {
						if err == nil {
							t.Fatal("unsafe revert accepted")
						}
						if got := consolidationSnapshot(t, d); !reflect.DeepEqual(before, got) {
							t.Fatal("failed revert changed memories")
						}
						var status string
						if err := d.DB().QueryRow(`SELECT status FROM consolidation_runs WHERE id='mixed'`).Scan(&status); err != nil {
							t.Fatal(err)
						}
						if status != "applied" {
							t.Fatalf("partial journal transition=%s", status)
						}
					}
				})
			}
		})
	}
}

func TestConsolidationProviderFenceActualDrain(t *testing.T) {
	for _, pg := range []bool{false, true} {
		t.Run(fmt.Sprint(pg), func(t *testing.T) {
			d, parent := consolidationLifecycleFixture(t, pg)
			s := &sqlStore{deps: d, collection: "levara"}
			if _, err := s.Candidates(parent, "levara", "", ""); err != nil {
				t.Fatal(err)
			}
			var schema string
			if pg {
				if err := d.DB().QueryRow(`SELECT current_schema()`).Scan(&schema); err != nil {
					t.Fatal(err)
				}
			}
			d.DB().SetMaxOpenConns(2)
			revoker, err := d.DB().Conn(parent)
			if err != nil {
				t.Fatal(err)
			}
			defer revoker.Close()
			if pg {
				conn, err := d.DB().Conn(parent)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := conn.ExecContext(parent, "SET search_path TO "+schema); err != nil {
					t.Fatal(err)
				}
				if err := conn.Close(); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(parent)
			release, err := s.providerFence(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			cancel() // observer ended; a non-cooperative transfer has not drained
			done := make(chan error, 1)
			go func() {
				_, err := revoker.ExecContext(parent, `UPDATE users SET is_active=FALSE WHERE id='owner-a'`)
				done <- err
			}()
			select {
			case err := <-done:
				t.Fatalf("revocation passed before actual drain: %v", err)
			case <-time.After(100 * time.Millisecond):
			}
			release()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("revocation did not resume after release")
			}
		})
	}
}
