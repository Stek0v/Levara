package mcp

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestConsolidationSyncLifecycleRevisions(t *testing.T) {
	for _, pg := range []bool{false, true} {
		t.Run(fmt.Sprint(pg), func(t *testing.T) {
			d, ctx := consolidationLifecycleFixture(t, pg)
			revision := func(id string) string {
				t.Helper()
				var stamp string
				if err := d.DB().QueryRow(d.Q("SELECT updated_at FROM memories WHERE id=$1"), id).Scan(&stamp); err != nil {
					t.Fatal(err)
				}
				return stamp
			}
			before := revision("a")
			s := &sqlStore{deps: d, collection: "levara"}
			if _, err := s.Candidates(ctx, "levara", "", ""); err != nil {
				t.Fatal(err)
			}
			if err := s.Apply(ctx, "sync-revisions", consolidationMixedActions()); err != nil {
				t.Fatal(err)
			}
			applied := revision("a")
			if err := s.Revert(ctx, "sync-revisions"); err != nil {
				t.Fatal(err)
			}
			reverted := revision("a")
			oldTime, err := consolidationTime(before)
			if err != nil {
				t.Fatal(err)
			}
			appliedTime, err := consolidationTime(applied)
			if err != nil {
				t.Fatal(err)
			}
			revertedTime, err := consolidationTime(reverted)
			if err != nil {
				t.Fatal(err)
			}
			if !appliedTime.After(oldTime) || !revertedTime.After(appliedTime) {
				t.Fatalf("lifecycle revisions not increasing: %s -> %s -> %s", before, applied, reverted)
			}
			if err := s.Revert(ctx, "sync-revisions"); err != nil {
				t.Fatal(err)
			}
			if revision("a") != reverted {
				t.Fatal("idempotent revert advanced revision")
			}
		})
	}
}

func TestConsolidationSyncRevisionFuturePrecision(t *testing.T) {
	for _, pg := range []bool{false, true} {
		t.Run(fmt.Sprint(pg), func(t *testing.T) {
			d, ctx := consolidationLifecycleFixture(t, pg)
			const future = "2099-01-01T00:00:00.123456789Z"
			if _, err := d.DB().Exec(d.Q("UPDATE memories SET updated_at=$1 WHERE id=$2"), future, "a"); err != nil {
				t.Fatal(err)
			}
			var prior string
			if err := d.DB().QueryRow("SELECT updated_at FROM memories WHERE id='a'").Scan(&prior); err != nil {
				t.Fatal(err)
			}
			s := &sqlStore{deps: d, collection: "levara"}
			if _, err := s.Candidates(ctx, "levara", "", ""); err != nil {
				t.Fatal(err)
			}
			started := time.Now().UTC()
			if err := s.Apply(ctx, "future-revision", consolidationMixedActions()); err != nil {
				t.Fatal(err)
			}
			ended := time.Now().UTC()
			var applied, retiredAt string
			if err := d.DB().QueryRow("SELECT updated_at,valid_until FROM memories WHERE id='a'").Scan(&applied, &retiredAt); err != nil {
				t.Fatal(err)
			}
			retired, err := consolidationTime(retiredAt)
			if err != nil || retired.Before(started.Truncate(time.Microsecond)) || retired.After(ended) {
				t.Fatalf("retirement time used a future revision: %s %v", retiredAt, err)
			}
			if err := s.Revert(ctx, "future-revision"); err != nil {
				t.Fatal(err)
			}
			var reverted string
			if err := d.DB().QueryRow("SELECT updated_at FROM memories WHERE id='a'").Scan(&reverted); err != nil {
				t.Fatal(err)
			}
			old, err := consolidationTime(prior)
			if err != nil {
				t.Fatal(err)
			}
			next, err := consolidationTime(applied)
			if err != nil {
				t.Fatal(err)
			}
			last, err := consolidationTime(reverted)
			if err != nil {
				t.Fatal(err)
			}
			if !next.After(old) || !last.After(next) || next.Nanosecond()%1000 != 0 || last.Nanosecond()%1000 != 0 {
				t.Fatalf("future revisions/precision: %s -> %s -> %s", prior, applied, reverted)
			}
		})
	}
}

func TestConsolidationSyncValidLegacyJournal(t *testing.T) {
	for _, pg := range []bool{false, true} {
		t.Run(fmt.Sprint(pg), func(t *testing.T) {
			d, ctx := consolidationLifecycleFixture(t, pg)
			s := &sqlStore{deps: d, collection: "levara"}
			original := consolidationSnapshot(t, d, true)
			if _, err := s.Candidates(ctx, "levara", "", ""); err != nil {
				t.Fatal(err)
			}
			if err := s.Apply(ctx, "legacy-valid", consolidationMixedActions()); err != nil {
				t.Fatal(err)
			}
			var raw string
			if err := d.DB().QueryRow("SELECT payload_json FROM consolidation_runs WHERE id='legacy-valid'").Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var journal []consolidationJournalRow
			if err := json.Unmarshal([]byte(raw), &journal); err != nil {
				t.Fatal(err)
			}
			// Model the historical producer: source retirement kept updated_at and
			// the retirement journal contained exactly its three lifecycle fields.
			for i := range journal {
				j := &journal[i]
				if j.Role != "source" {
					continue
				}
				if _, err := d.DB().Exec(d.Q("UPDATE memories SET updated_at=$1 WHERE id=$2"), j.Before["updated_at"], j.ID); err != nil {
					t.Fatal(err)
				}
				rows, err := d.DB().Query(d.Q("SELECT * FROM memories WHERE id=$1"), j.ID)
				if err != nil {
					t.Fatal(err)
				}
				all, err := scanConsolidationRows(rows)
				if err != nil {
					t.Fatal(err)
				}
				if len(all) != 1 {
					t.Fatal("legacy source missing")
				}
				j.After = consolidationHash(all[0])
				delete(j.Before, "updated_at")
			}
			encoded, err := json.Marshal(journal)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := d.DB().Exec(d.Q("UPDATE consolidation_runs SET payload_json=$1 WHERE id=$2"), string(encoded), "legacy-valid"); err != nil {
				t.Fatal(err)
			}
			if err := s.Revert(ctx, "legacy-valid"); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(original, consolidationSnapshot(t, d, true)) {
				t.Fatal("valid legacy journal did not restore contents")
			}
		})
	}
}
