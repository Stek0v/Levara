package mcp

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/stek0v/levara/pkg/consolidate"
)

func TestConsolidationAuthenticatedSharedLifecycle(t *testing.T) {
	for _, pg := range []bool{false, true} {
		t.Run(fmt.Sprint(pg), func(t *testing.T) {
			d, ctx := consolidationLifecycleFixture(t, pg)
			for _, row := range []struct{ id, owner, collection string }{{"s1", "", "levara"}, {"s2", "", "levara"}, {"f", "owner-b", "levara"}, {"other", "owner-a", "other"}} {
				if _, err := d.DB().Exec(d.Q(`INSERT INTO memories(id,key,value,type,owner_id,collection_name,room,hall,created_at,updated_at) VALUES($1,$2,'shared fact','user',$3,$4,'memory','fact',$5,$6)`), row.id, row.id, row.owner, row.collection, "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"); err != nil {
					t.Fatal(err)
				}
			}
			private := &sqlStore{deps: d, collection: "levara"}
			forged := context.WithValue(ctx, UserIDKey, "owner-b")
			rows, err := private.Candidates(forged, "levara", "", "")
			if err != nil || len(rows) != 4 {
				t.Fatalf("private scope: %v %+v", err, rows)
			}
			for _, r := range rows {
				if r.OwnerID != "owner-a" || r.Collection != "levara" || r.Type != "user" {
					t.Fatalf("classification=%+v", r)
				}
			}
			shared := &sqlStore{deps: d, collection: "levara", shared: true}
			if _, err := shared.Candidates(ctx, "levara", "", ""); err == nil {
				t.Fatal("ordinary caller accessed shared candidates")
			}
			if _, err := d.DB().Exec(`UPDATE users SET is_superuser=TRUE WHERE id='owner-a'`); err != nil {
				t.Fatal(err)
			}
			rows, err = shared.Candidates(ctx, "levara", "", "")
			if err != nil || len(rows) != 2 {
				t.Fatalf("admin shared: %v %+v", err, rows)
			}
			original := consolidationSnapshot(t, d, true)
			actions := []consolidate.Action{{Kind: consolidate.ActionAbstract, SourceIDs: []string{"s1", "s2"}, NewValue: "combined shared fact", Room: "memory", Hall: "fact"}}
			if err := shared.Apply(ctx, "shared-run", actions); err != nil {
				t.Fatal(err)
			}
			var owner, typ, collection, room, hall string
			if err := d.DB().QueryRow(`SELECT owner_id,type,collection_name,room,hall FROM memories WHERE tier='semantic'`).Scan(&owner, &typ, &collection, &room, &hall); err != nil {
				t.Fatal(err)
			}
			if owner != "" || typ != "user" || collection != "levara" || room != "memory" || hall != "fact" {
				t.Fatalf("abstract classification=%q/%q/%q/%q/%q", owner, typ, collection, room, hall)
			}
			applied := consolidationSnapshot(t, d)
			if err := private.Revert(ctx, "shared-run"); err == nil {
				t.Fatal("implicit admin revert reached shared run")
			}
			if _, err := d.DB().Exec(`UPDATE users SET is_superuser=FALSE WHERE id='owner-a'`); err != nil {
				t.Fatal(err)
			}
			if err := shared.Revert(ctx, "shared-run"); err == nil {
				t.Fatal("revoked admin reverted shared run")
			}
			if got := consolidationSnapshot(t, d); !reflect.DeepEqual(applied, got) {
				t.Fatal("denied shared revert changed memories")
			}
			if _, err := d.DB().Exec(`UPDATE users SET is_superuser=TRUE WHERE id='owner-a'`); err != nil {
				t.Fatal(err)
			}
			if err := shared.Revert(ctx, "shared-run"); err != nil {
				t.Fatal(err)
			}
			if got := consolidationSnapshot(t, d, true); !reflect.DeepEqual(original, got) {
				t.Fatal("shared revert did not restore exact namespace")
			}
		})
	}
}

func TestConsolidationIndexEffectsAtomic(t *testing.T) {
	for _, pg := range []bool{false, true} {
		t.Run(fmt.Sprint(pg), func(t *testing.T) {
			d, ctx := consolidationLifecycleFixture(t, pg)
			s := &sqlStore{deps: d, collection: "levara"}
			if _, err := s.Candidates(ctx, "levara", "", ""); err != nil {
				t.Fatal(err)
			}
			if err := s.Apply(ctx, "indexed", consolidationMixedActions()); err != nil {
				t.Fatal(err)
			}
			count := func() int {
				t.Helper()
				var n int
				if err := d.DB().QueryRow(`SELECT COUNT(*) FROM memory_index_jobs WHERE owner_id='owner-a' AND collection_name='levara' AND status='pending'`).Scan(&n); err != nil {
					t.Fatal(err)
				}
				return n
			}
			if got := count(); got != 4 {
				t.Fatalf("apply index effects=%d want4", got)
			}
			if err := s.Revert(ctx, "indexed"); err != nil {
				t.Fatal(err)
			}
			if got := count(); got != 8 {
				t.Fatalf("revert index effects=%d want8", got)
			}
			if err := s.Revert(ctx, "indexed"); err != nil {
				t.Fatal(err)
			}
			if got := count(); got != 8 {
				t.Fatalf("idempotent revert index effects=%d want8", got)
			}
		})
	}
}
