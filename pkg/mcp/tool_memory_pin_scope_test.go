package mcp

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestToolMemoryPinCollectionScope(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var deps Deps
			if dialect == "sqlite" {
				deps = setupMemoryTestDB(t)
			} else {
				db := openPostgresMemoryTestDB(t)
				if _, err := db.Exec(`CREATE TABLE memories (
					id TEXT PRIMARY KEY, key TEXT NOT NULL, value TEXT, type TEXT,
					owner_id TEXT NOT NULL DEFAULT '', collection_name TEXT NOT NULL DEFAULT '',
					room TEXT, hall TEXT, is_pinned BOOLEAN NOT NULL DEFAULT FALSE,
					pin_priority INTEGER NOT NULL DEFAULT 0,
					created_at TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL,
					UNIQUE(key, owner_id, collection_name)
				)`); err != nil {
					t.Fatal(err)
				}
				deps = &postgresMemoryDeps{fakeDeps: &fakeDeps{db: db}}
			}
			testMemoryPinCollectionScope(t, deps, dialect)
		})
	}
}

func testMemoryPinCollectionScope(t *testing.T, deps Deps, dialect string) {
	t.Helper()
	const oldTimestamp = "2020-01-02T03:04:05Z"
	fixture := []struct{ id, key, owner, collection string }{
		{"own-one", "dup", "alice", "one"},
		{"shared-one", "dup", "", "one"},
		{"own-two", "dup", "alice", "two"},
		{"shared-two", "dup", "", "two"},
		{"foreign-one", "dup", "bob", "one"},
		{"other-key", "other", "alice", "one"},
		{"own-spaces", "dup", "alice", " one "},
		{"own-quoted", "dup", "alice", "one' OR 1=1 --"},
		{"foreign-only", "foreign-only", "bob", "one"},
	}
	type pinState struct {
		pinned    bool
		priority  int
		updatedAt string
	}
	snapshot := func(t *testing.T) map[string]pinState {
		t.Helper()
		rows, err := deps.DB().Query(`SELECT id, is_pinned, pin_priority, updated_at FROM memories`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		states := make(map[string]pinState)
		for rows.Next() {
			var id string
			var state pinState
			if err := rows.Scan(&id, &state.pinned, &state.priority, &state.updatedAt); err != nil {
				t.Fatal(err)
			}
			states[id] = state
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return states
	}

	for _, operation := range []struct {
		name string
		call func(context.Context, Deps, map[string]any) ToolResult
	}{
		{"pin", ToolPinMemory}, {"unpin", ToolUnpinMemory},
	} {
		t.Run(operation.name, func(t *testing.T) {
			cases := []struct {
				name, key, owner string
				collection       any
				present, invalid bool
				defaultPriority  bool
				repeat           bool
				changed          []string
			}{
				{name: "selected", collection: "one", present: true, changed: []string{"own-one", "shared-one"}},
				{name: "default-priority-repeat-unpin", collection: "one", present: true, defaultPriority: true, repeat: true, changed: []string{"own-one", "shared-one"}},
				{name: "missing-collection", collection: "unknown", present: true},
				{name: "missing-key", key: "ghost", collection: "one", present: true},
				{name: "foreign-only", key: "foreign-only", collection: "one", present: true},
				{name: "anonymous-shared-only", owner: "anonymous", collection: "one", present: true, changed: []string{"shared-one"}},
				{name: "literal-spaces", collection: " one ", present: true, changed: []string{"own-spaces"}},
				{name: "literal-quoted", collection: "one' OR 1=1 --", present: true, changed: []string{"own-quoted"}},
				{name: "omitted", changed: []string{"own-one", "shared-one", "own-two", "shared-two", "own-spaces", "own-quoted"}},
				{name: "empty", collection: "", present: true, changed: []string{"own-one", "shared-one", "own-two", "shared-two", "own-spaces", "own-quoted"}},
				{name: "null", collection: nil, present: true, invalid: true},
				{name: "number", collection: float64(1), present: true, invalid: true},
				{name: "bool", collection: true, present: true, invalid: true},
				{name: "array", collection: []any{"one"}, present: true, invalid: true},
				{name: "object", collection: map[string]any{"name": "one"}, present: true, invalid: true},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					if _, err := deps.DB().Exec(`DELETE FROM memories`); err != nil {
						t.Fatal(err)
					}
					initialPinned := operation.name == "unpin"
					for _, row := range fixture {
						if dialect == "sqlite" {
							pinned := 0
							if initialPinned {
								pinned = 1
							}
							seedMemory(t, deps.DB(), row.id, row.key, "value", "project", row.owner, row.collection, "memory", "fact", pinned, 3)
						} else if _, err := deps.DB().Exec(deps.Q(`INSERT INTO memories
							(id, key, owner_id, collection_name, is_pinned, pin_priority, created_at, updated_at)
							VALUES ($1, $2, $3, $4, $5, 3, $6, $7)`),
							row.id, row.key, row.owner, row.collection, initialPinned, oldTimestamp, oldTimestamp); err != nil {
							t.Fatal(err)
						}
					}
					if _, err := deps.DB().Exec(deps.Q(`UPDATE memories SET updated_at = $1`), oldTimestamp); err != nil {
						t.Fatal(err)
					}
					before := snapshot(t)
					key := tc.key
					if key == "" {
						key = "dup"
					}
					args := map[string]any{"key": key, "owner_id": "bob", "actor_id": "bob"}
					priority := 8
					if tc.defaultPriority {
						priority = 1
					} else {
						args["priority"] = float64(priority)
					}
					if tc.present {
						args["collection"] = tc.collection
					}
					ctx := context.Background()
					if tc.owner != "anonymous" {
						ctx = context.WithValue(ctx, UserIDKey, "alice")
					}
					result := operation.call(ctx, deps, args)
					wantError := tc.invalid || (operation.name == "pin" && len(tc.changed) == 0)
					if result.IsError != wantError {
						t.Errorf("IsError = %v, want %v: %+v", result.IsError, wantError, result)
					}
					if tc.invalid {
						if len(result.Content) != 1 || !strings.Contains(result.Content[0].Text, "'collection' must be a string") {
							t.Errorf("invalid selector error = %+v", result)
						}
					} else if wantError {
						if len(result.Content) != 1 || result.Content[0].Text != "No memory matched key "+key {
							t.Errorf("missing pin error changed: %+v", result)
						}
					} else {
						message := "Unpinned " + key
						if operation.name == "pin" {
							message = fmt.Sprintf("Pinned %s (priority=%d)", key, priority)
						}
						if len(result.Content) != 1 || !strings.Contains(result.Content[0].Text, message) {
							t.Errorf("success message changed: %+v", result)
						}
					}
					if tc.repeat && operation.name == "unpin" {
						if got := operation.call(ctx, deps, args); got.IsError {
							t.Errorf("repeat unpin failed: %+v", got)
						}
					}
					after := snapshot(t)
					changed := make(map[string]bool)
					for _, id := range tc.changed {
						changed[id] = true
					}
					for id, old := range before {
						state, ok := after[id]
						if !ok {
							t.Errorf("row %s disappeared", id)
							continue
						}
						if !changed[id] {
							if state != old {
								t.Errorf("control row %s changed: before=%+v after=%+v", id, old, state)
							}
							continue
						}
						wantPinned := operation.name == "pin"
						wantPriority := 0
						if wantPinned {
							wantPriority = priority
						}
						if state.pinned != wantPinned || state.priority != wantPriority || state.updatedAt == old.updatedAt {
							t.Errorf("selected row %s = %+v, want pinned=%v priority=%d and updated timestamp", id, state, wantPinned, wantPriority)
						}
					}
					if len(after) != len(before) {
						t.Errorf("row count changed: before=%d after=%d", len(before), len(after))
					}
				})
			}
		})
	}
	t.Run("database-failure", func(t *testing.T) {
		if _, err := deps.DB().Exec(`DROP TABLE memories`); err != nil {
			t.Fatal(err)
		}
		for _, call := range []func(context.Context, Deps, map[string]any) ToolResult{ToolPinMemory, ToolUnpinMemory} {
			if got := call(context.Background(), deps, map[string]any{"key": "dup", "collection": "one"}); !got.IsError {
				t.Errorf("database failure accepted: %+v", got)
			}
		}
	})
}
