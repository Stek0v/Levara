package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestMemoryProvenanceSurfaces(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var deps Deps
			if dialect == "sqlite" {
				deps = setupMemoryTestDB(t)
			} else {
				db := openPostgresMemoryTestDB(t)
				_, err := db.Exec(`CREATE TABLE memories (
					id TEXT PRIMARY KEY, key TEXT, value TEXT, type TEXT DEFAULT 'project',
					owner_id TEXT DEFAULT '', collection_name TEXT, room TEXT, hall TEXT,
					is_pinned BOOLEAN DEFAULT FALSE, pin_priority INTEGER DEFAULT 0,
					created_at TEXT DEFAULT '', updated_at TEXT DEFAULT '', superseded_by TEXT DEFAULT '', valid_until TIMESTAMPTZ,
					verification_status TEXT, source_task_id TEXT, source_receipt_ids TEXT)`)
				if err != nil {
					t.Fatal(err)
				}
				deps = &postgresMemoryDeps{fakeDeps: &fakeDeps{db: db}}
			}
			for _, row := range []struct{ key, owner, collection, retired string }{
				{"own", "alice", "project", ""}, {"shared", "", "project", ""},
				{"legacy", "alice", "project", ""}, {"foreign", "bob", "project", ""},
				{"other", "alice", "other", ""}, {"retired", "alice", "project", "replacement"},
			} {
				_, err := deps.DB().Exec(deps.Q(`INSERT INTO memories (id,key,value,type,owner_id,collection_name,room,hall,is_pinned,pin_priority,created_at,updated_at,superseded_by,verification_status,source_task_id,source_receipt_ids)
					VALUES($1,$2,$3,'project',$4,$5,'memory','fact',TRUE,10,'','',$6,'unverified','task-1','["receipt-1"]')`),
					row.key, row.key, "source "+row.key, row.owner, row.collection, row.retired)
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := deps.DB().Exec(`UPDATE memories SET source_receipt_ids=NULL,source_task_id=NULL,verification_status=NULL WHERE key='legacy'`); err != nil {
				t.Fatal(err)
			}
			ctx := context.WithValue(context.Background(), UserIDKey, "alice")
			for _, tool := range []struct {
				name, field string
				call        func(context.Context, Deps, map[string]any) ToolResult
			}{
				{"list", "memories", ToolListMemories}, {"wake", "pinned", ToolWakeUp},
			} {
				t.Run(tool.name, func(t *testing.T) {
					result := tool.call(ctx, deps, map[string]any{"collection": "project", "max_tokens": float64(2000)})
					if result.IsError {
						t.Fatal(result.Content)
					}
					var payload map[string]json.RawMessage
					if err := json.Unmarshal([]byte(result.Content[0].Text), &payload); err != nil {
						t.Fatal(err)
					}
					var rows []map[string]any
					if err := json.Unmarshal(payload[tool.field], &rows); err != nil {
						t.Fatal(err)
					}
					if len(rows) != 3 {
						t.Fatalf("expected own/shared/legacy only: %+v", rows)
					}
					for _, row := range rows {
						key := row["key"].(string)
						if key != "own" && key != "shared" && key != "legacy" {
							t.Fatalf("scope leak: %+v", row)
						}
						receipts, ok := row["source_receipt_ids"].([]any)
						if !ok {
							t.Fatalf("missing/non-array provenance: %+v", row)
						}
						if key == "legacy" {
							if len(receipts) != 0 || row["source_task_id"] != "" || row["verification_status"] != "unverified" {
								t.Fatalf("legacy provenance: %+v", row)
							}
						} else if row["source_task_id"] != "task-1" || row["verification_status"] != "unverified" || len(receipts) != 1 || receipts[0] != "receipt-1" {
							t.Fatalf("changed provenance: %+v", row)
						}
					}
				})
			}
		})
	}
}

func TestWakeUpBudgetKeepsProvenanceTogether(t *testing.T) {
	deps := setupMemoryTestDB(t)
	for i, key := range []string{"high", "low"} {
		seedMemory(t, deps.db, key, key, strings.Repeat("fact ", 20), "project", "alice", "project", "memory", "fact", 1, 10-i)
		if _, err := deps.db.Exec(`UPDATE memories SET verification_status='unverified',source_task_id='task-1',source_receipt_ids='["receipt-1"]' WHERE key=?`, key); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.WithValue(context.Background(), UserIDKey, "alice")
	result := ToolWakeUp(ctx, deps, map[string]any{"collection": "project", "max_tokens": float64(200)})
	if len(result.Content[0].Text) > 800 {
		t.Fatalf("budget exceeded: %d", len(result.Content[0].Text))
	}
	var payload struct {
		Pinned []map[string]any `json:"pinned"`
	}
	if err := json.Unmarshal([]byte(result.Content[0].Text), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Pinned) != 1 || payload.Pinned[0]["key"] != "high" {
		t.Fatalf("expected only highest-priority complete record: %+v", payload.Pinned)
	}
	if payload.Pinned[0]["verification_status"] != "unverified" || payload.Pinned[0]["source_task_id"] != "task-1" || payload.Pinned[0]["source_receipt_ids"] == nil {
		t.Fatalf("provenance stripped to fit: %+v", payload.Pinned)
	}
}
