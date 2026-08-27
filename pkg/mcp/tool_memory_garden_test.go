package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestToolMemoryGardenFindsScopedReviewOnlyIssues(t *testing.T) {
	deps := setupSaveRecallMemoryDB(t)
	now := time.Now().UTC()
	insert := func(id, key, value, owner, updated, verification, task, receipts string) {
		t.Helper()
		if _, err := deps.db.Exec(`INSERT INTO memories(id,key,value,type,owner_id,collection_name,room,hall,verification_status,source_task_id,source_receipt_ids,created_at,updated_at)
			VALUES(?,?,?,'project',?,'levara','memory','fact',?,?,?,?,?)`, id, key, value, owner, verification, task, receipts, updated, updated); err != nil {
			t.Fatal(err)
		}
	}
	insert("a1", "duplicate-a", "Same   Value", "alice", now.AddDate(-2, 0, 0).Format(time.RFC3339), "", "", "[]")
	insert("a2", "duplicate-b", "same value", "alice", now.Format(time.RFC3339), "verified", "task-1", "[\"receipt-1\"]")
	insert("a3", "choice", "alice choice", "alice", now.Format(time.RFC3339), "verified", "", "[]")
	insert("shared", "choice", "shared choice", "", now.Format(time.RFC3339), "verified", "", "[]")
	insert("bob-private", "bob-only-key", "same value", "bob", now.Format(time.RFC3339), "", "", "[]")

	var before int
	if err := deps.db.QueryRow(`SELECT COUNT(*) FROM memories`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), UserIDKey, "alice")
	got := ToolMemoryGarden(ctx, deps, map[string]any{"collection": "levara", "stale_after_days": float64(30)})
	if got.IsError {
		t.Fatal(got.Content[0].Text)
	}
	if strings.Contains(got.Content[0].Text, "bob-private") || strings.Contains(got.Content[0].Text, "bob-only-key") || strings.Contains(got.Content[0].Text, "Same   Value") {
		t.Fatalf("garden leaked another owner or a memory value: %s", got.Content[0].Text)
	}
	var out struct{ Summary map[string]int }
	if err := json.Unmarshal([]byte(got.Content[0].Text), &out); err != nil {
		t.Fatal(err)
	}
	for category, want := range map[string]int{"duplicate": 1, "conflict": 1, "stale": 1, "weak_provenance": 1} {
		if out.Summary[category] != want {
			t.Fatalf("%s=%d want %d; output=%s", category, out.Summary[category], want, got.Content[0].Text)
		}
	}
	var after int
	if err := deps.db.QueryRow(`SELECT COUNT(*) FROM memories`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("garden mutated memories: before=%d after=%d", before, after)
	}
}

func TestToolMemoryGardenValidatesBounds(t *testing.T) {
	deps := setupSaveRecallMemoryDB(t)
	for _, args := range []map[string]any{
		{},
		{"collection": "levara", "stale_after_days": float64(0)},
		{"collection": "levara", "limit": float64(201)},
	} {
		if got := ToolMemoryGarden(context.Background(), deps, args); !got.IsError {
			t.Fatalf("args=%+v accepted", args)
		}
	}
}
