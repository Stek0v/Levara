package mcp

import (
	"context"
	"testing"
)

func TestToolMemoryIdentityRoomHall(t *testing.T) {
	for _, dialect := range []struct {
		name     string
		postgres bool
	}{{"sqlite", false}, {"postgres", true}} {
		t.Run(dialect.name, func(t *testing.T) {
			deps, ctx := memoryCommitEvidenceFixture(t, dialect.postgres)
			for _, seed := range []struct {
				owner, collection, value string
			}{
				{"owner-a", "levara", "original"},
				{"owner-a", "sibling", "other collection"},
				{"owner-b", "levara", "other owner"},
			} {
				result := ToolSaveMemory(context.WithValue(ctx, UserIDKey, seed.owner), deps, map[string]any{
					"key": "same-key", "value": seed.value, "collection": seed.collection,
					"room": "memory", "hall": "fact",
				})
				if result.IsError {
					t.Fatalf("seed %s/%s: %+v", seed.owner, seed.collection, result)
				}
			}

			const sentinel = "2000-01-01T00:00:00Z"
			if _, err := deps.DB().Exec(deps.Q(`UPDATE memories SET created_at = $1, updated_at = $2 WHERE key = $3`), sentinel, sentinel, "same-key"); err != nil {
				t.Fatal(err)
			}

			type row struct {
				owner, collection, value, room, hall, created, updated string
			}
			readRows := func() map[string]row {
				t.Helper()
				rows, err := deps.DB().Query(deps.Q(`SELECT id, owner_id, collection_name, value, room, hall, created_at, updated_at FROM memories WHERE key = $1`), "same-key")
				if err != nil {
					t.Fatal(err)
				}
				defer rows.Close()
				out := make(map[string]row)
				for rows.Next() {
					var id string
					var r row
					if err := rows.Scan(&id, &r.owner, &r.collection, &r.value, &r.room, &r.hall, &r.created, &r.updated); err != nil {
						t.Fatal(err)
					}
					out[id] = r
				}
				if err := rows.Err(); err != nil {
					t.Fatal(err)
				}
				return out
			}
			before := readRows()
			if len(before) != 3 {
				t.Fatalf("seed identities = %d, want 3", len(before))
			}
			var canonicalID string
			for id, r := range before {
				if r.owner == "owner-a" && r.collection == "levara" {
					canonicalID = id
				}
			}
			if canonicalID == "" {
				t.Fatal("selected identity missing")
			}

			for _, update := range []struct {
				name, value, room, hall string
				omitClassification      bool
			}{
				{name: "changed", value: "updated", room: "deploy", hall: "decision"},
				{name: "explicit empty", value: "legacy empty"},
				{name: "restore classification", value: "classified again", room: "memory", hall: "fact"},
				{name: "omitted", value: "legacy omitted", omitClassification: true},
			} {
				args := map[string]any{"key": "same-key", "value": update.value, "collection": "levara"}
				if !update.omitClassification {
					args["room"], args["hall"] = update.room, update.hall
				}
				result := ToolSaveMemory(ctx, deps, args)
				if result.IsError {
					t.Fatalf("%s save: %+v", update.name, result)
				}
				after := readRows()
				if len(after) != len(before) {
					t.Fatalf("%s identity count = %d, want %d", update.name, len(after), len(before))
				}
				selected, found := after[canonicalID]
				if !found || selected.owner != "owner-a" || selected.collection != "levara" || selected.value != update.value || selected.room != update.room || selected.hall != update.hall || selected.created != before[canonicalID].created || selected.updated == sentinel {
					t.Fatalf("%s canonical identity changed or classification not updated: id=%q row=%+v", update.name, canonicalID, selected)
				}
				for id, r := range before {
					if id != canonicalID && after[id] != r {
						t.Fatalf("%s changed sibling %q: before=%+v after=%+v", update.name, id, r, after[id])
					}
				}
			}

			beforeInvalid := readRows()
			result := ToolSaveMemory(ctx, deps, map[string]any{
				"key": "same-key", "value": "must not replace", "collection": "levara",
				"room": "auth", "hall": "unknown-hall",
			})
			if !result.IsError {
				t.Fatalf("unknown hall accepted: %+v", result)
			}
			afterInvalid := readRows()
			if len(afterInvalid) != len(beforeInvalid) {
				t.Fatalf("unknown hall changed identity count: %d != %d", len(afterInvalid), len(beforeInvalid))
			}
			for id, r := range beforeInvalid {
				if afterInvalid[id] != r {
					t.Fatalf("unknown hall mutated %q: before=%+v after=%+v", id, r, afterInvalid[id])
				}
			}
		})
	}
}
