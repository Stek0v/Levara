package mcp

import (
	"context"
	"testing"

	"github.com/stek0v/levara/pkg/consolidate"
)

func TestConsolidationApplyRejectsStalePin(t *testing.T) {
	d := setupConsolidateDB(t)
	seedDup(t, d, "old", "same fact", "2026-01-01T00:00:00Z")
	seedDup(t, d, "new", "same fact", "2026-01-02T00:00:00Z")
	s := &sqlStore{deps: d, collection: "levara"}
	if _, err := s.Candidates(context.Background(), "levara", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := d.db.Exec(`UPDATE memories SET is_pinned=TRUE WHERE id='old'`); err != nil {
		t.Fatal(err)
	}
	err := s.Apply(context.Background(), "stale-pin", []consolidate.Action{{Kind: consolidate.ActionMerge, SurvivorID: "new", SourceIDs: []string{"old"}}})
	if err == nil {
		t.Fatal("stale newly pinned candidate was retired")
	}
	var target string
	if err := d.db.QueryRow(`SELECT superseded_by FROM memories WHERE id='old'`).Scan(&target); err != nil {
		t.Fatal(err)
	}
	if target != "" {
		t.Fatalf("partial retirement=%q", target)
	}
}

func TestConsolidationRevertRejectsChangedSurvivor(t *testing.T) {
	d := setupConsolidateDB(t)
	seedDup(t, d, "old", "same fact", "2026-01-01T00:00:00Z")
	seedDup(t, d, "new", "same fact", "2026-01-02T00:00:00Z")
	s := &sqlStore{deps: d, collection: "levara"}
	if _, err := s.Candidates(context.Background(), "levara", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Apply(context.Background(), "stale-survivor", []consolidate.Action{{Kind: consolidate.ActionMerge, SurvivorID: "new", SourceIDs: []string{"old"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.db.Exec(`UPDATE memories SET value='later user edit' WHERE id='new'`); err != nil {
		t.Fatal(err)
	}
	if err := s.Revert(context.Background(), "stale-survivor"); err == nil {
		t.Fatal("revert ignored a later survivor edit without timestamp update")
	}
	var target string
	if err := d.db.QueryRow(`SELECT superseded_by FROM memories WHERE id='old'`).Scan(&target); err != nil {
		t.Fatal(err)
	}
	if target != "new" {
		t.Fatalf("partial reactivation=%q", target)
	}
}
