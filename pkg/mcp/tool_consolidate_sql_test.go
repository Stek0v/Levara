package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/stek0v/levara/pkg/consolidate"
)

func TestConsolidationSQLApplyRevert(t *testing.T) {
	for _, dialect := range []struct {
		name, rejectedAbstract, foreignKey string
		postgres                           bool
	}{
		{"sqlite", "CHECK constraint failed", "FOREIGN KEY constraint failed", false},
		{"postgres", "violates check constraint", "violates foreign key constraint", true},
	} {
		t.Run(dialect.name, func(t *testing.T) {
			var deps Deps
			if dialect.postgres {
				db := openPostgresMemoryTestDB(t)
				if _, err := db.Exec(`CREATE TABLE memories (
					id TEXT PRIMARY KEY, key TEXT, value TEXT CHECK(value <> 'blocked abstract'), type TEXT, owner_id TEXT DEFAULT '',
					collection_name TEXT DEFAULT '', room TEXT DEFAULT '', hall TEXT DEFAULT '',
					is_pinned BOOLEAN DEFAULT FALSE, pin_priority INTEGER DEFAULT 0,
					created_at TIMESTAMPTZ, updated_at TIMESTAMPTZ,
					superseded_by TEXT DEFAULT '', valid_until TIMESTAMPTZ,
					consolidated_from TEXT DEFAULT '', consolidation_run_id TEXT DEFAULT '',
					tier TEXT DEFAULT 'raw', UNIQUE(key, owner_id, collection_name)
				)`); err != nil {
					t.Fatal(err)
				}
				deps = &postgresMemoryDeps{fakeDeps: &fakeDeps{db: db}}
			} else {
				deps = setupConsolidateDB(t)
				deps.DB().SetMaxOpenConns(1)
				deps.DB().SetMaxIdleConns(1)
				if _, err := deps.DB().Exec(`PRAGMA foreign_keys=ON`); err != nil {
					t.Fatal(err)
				}
			}
			db := deps.DB()
			ctx := context.Background()
			for _, id := range []string{"merge-old", "merge-new", "abstract-a", "abstract-b", "control"} {
				collection, pinned, priority := "levara", false, 0
				if id == "control" {
					collection, pinned, priority = "other", true, 7
				}
				if _, err := db.Exec(deps.Q(`INSERT INTO memories
					(id, key, value, type, collection_name, room, hall, is_pinned, pin_priority, created_at, updated_at)
					VALUES ($1, $2, $3, 'project', $4, 'memory', 'fact', $5, $6, $7, $8)`),
					id, "key-"+id, "value-"+id, collection, pinned, priority,
					"2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"); err != nil {
					t.Fatalf("seed %s: %v", id, err)
				}
			}

			type row struct {
				key, value, typ, owner, collection, room, hall string
				created, updated, superseded, run, tier        string
				validUntil                                     sql.NullString
				pinned                                         bool
				priority                                       int
			}
			readRows := func() map[string]row {
				t.Helper()
				rows, err := db.Query(`SELECT id, key, value, type, owner_id, collection_name, room, hall,
					created_at, updated_at, superseded_by, consolidation_run_id, tier, valid_until, is_pinned, pin_priority FROM memories`)
				if err != nil {
					t.Fatal(err)
				}
				defer rows.Close()
				out := make(map[string]row)
				for rows.Next() {
					var id string
					var r row
					if err := rows.Scan(&id, &r.key, &r.value, &r.typ, &r.owner, &r.collection, &r.room, &r.hall,
						&r.created, &r.updated, &r.superseded, &r.run, &r.tier, &r.validUntil, &r.pinned, &r.priority); err != nil {
						t.Fatal(err)
					}
					out[id] = r
				}
				if err := rows.Err(); err != nil {
					t.Fatal(err)
				}
				return out
			}
			original := readRows()
			s := &sqlStore{deps: deps, collection: "levara"}
			actions := []consolidate.Action{
				{Kind: consolidate.ActionMerge, SourceIDs: []string{"merge-old"}, SurvivorID: "merge-new"},
				{Kind: consolidate.ActionAbstract, SourceIDs: []string{"abstract-a", "abstract-b"}, NewValue: "synthesized fact", Room: "memory", Hall: "fact"},
			}
			if _, err := s.Candidates(ctx, "levara", "", ""); err != nil {
				t.Fatal(err)
			}
			if err := s.Apply(ctx, "mixed-run", actions); err != nil {
				t.Fatalf("mixed apply: %v", err)
			}
			applied := readRows()
			if len(applied) != len(original)+1 {
				t.Fatalf("mixed apply rows = %d, want %d", len(applied), len(original)+1)
			}
			var abstractID string
			for id, r := range applied {
				if _, exists := original[id]; !exists {
					abstractID = id
					if r.pinned || r.priority != 0 || r.tier != "semantic" || r.run != "mixed-run" || r.collection != "levara" || r.value != actions[1].NewValue {
						t.Fatalf("generated abstract: %+v", r)
					}
				}
			}
			if abstractID == "" {
				t.Fatal("generated abstract missing")
			}
			var lineageJSON string
			if err := db.QueryRow(deps.Q(`SELECT consolidated_from FROM memories WHERE id=$1`), abstractID).Scan(&lineageJSON); err != nil {
				t.Fatal(err)
			}
			var lineage []string
			if err := json.Unmarshal([]byte(lineageJSON), &lineage); err != nil || !reflect.DeepEqual(lineage, actions[1].SourceIDs) {
				t.Fatalf("abstract lineage = %q, error=%v", lineageJSON, err)
			}
			for _, source := range []struct{ id, target string }{
				{"merge-old", "merge-new"}, {"abstract-a", abstractID}, {"abstract-b", abstractID},
			} {
				r := applied[source.id]
				if r.superseded != source.target || r.run != "mixed-run" || !r.validUntil.Valid || r.validUntil.String == "" {
					t.Fatalf("source %s not retired: %+v", source.id, r)
				}
			}
			for _, id := range []string{"merge-new", "control"} {
				if applied[id] != original[id] {
					t.Fatalf("mixed apply changed %s: before=%+v after=%+v", id, original[id], applied[id])
				}
			}

			if _, err := db.Exec(`CREATE TABLE abstract_reference (memory_id TEXT REFERENCES memories(id))`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(deps.Q(`INSERT INTO abstract_reference (memory_id) VALUES ($1)`), abstractID); err != nil {
				t.Fatal(err)
			}
			if err := s.Revert(ctx, "mixed-run"); err == nil || !strings.Contains(err.Error(), dialect.foreignKey) {
				t.Fatalf("forced revert error = %v, want foreign-key deletion failure", err)
			}
			if got := readRows(); !reflect.DeepEqual(got, applied) {
				t.Fatalf("failed revert left partial state: before=%+v after=%+v", applied, got)
			}
			if _, err := db.Exec(`DELETE FROM abstract_reference`); err != nil {
				t.Fatal(err)
			}
			if err := s.Revert(ctx, "mixed-run"); err != nil {
				t.Fatalf("successful revert: %v", err)
			}
			got := readRows()
			for _, id := range []string{"merge-old", "abstract-a", "abstract-b"} {
				r := original[id]
				if got[id].updated == applied[id].updated || got[id].updated == r.updated {
					t.Fatalf("revert did not advance revision for %s", id)
				}
				r.updated = got[id].updated
				original[id] = r
			}
			if !reflect.DeepEqual(got, original) {
				t.Fatalf("revert did not restore originals/remove abstract: before=%+v after=%+v", original, got)
			}

			// Fail the later abstract INSERT after the earlier merge UPDATE.
			actions[1].NewValue = "blocked abstract"
			if _, err := s.Candidates(ctx, "levara", "", ""); err != nil {
				t.Fatal(err)
			}
			if err := s.Apply(ctx, "failed-run", actions); err == nil || !strings.Contains(err.Error(), dialect.rejectedAbstract) {
				t.Fatalf("forced apply error = %v, want abstract constraint failure", err)
			}
			if got := readRows(); !reflect.DeepEqual(got, original) {
				t.Fatalf("failed apply left partial state: before=%+v after=%+v", original, got)
			}
		})
	}
}
