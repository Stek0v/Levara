package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestToolMemoryMarkdownDigestExportsOnlySelectedVerifiedScopedMemories(t *testing.T) {
	deps := setupSaveRecallMemoryDB(t)
	insert := func(id, key, value, owner, hall, verification, task, receipts, superseded string) {
		t.Helper()
		if _, err := deps.db.Exec(`INSERT INTO memories(id,key,value,type,owner_id,collection_name,room,hall,verification_status,source_task_id,source_receipt_ids,superseded_by,created_at,updated_at)
			VALUES(?,?,?,'project',?,'levara','memory',?,?,?,?,?,?,?)`, id, key, value, owner, hall, verification, task, receipts, superseded, time.Now().UTC().Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
	}
	insert("decision", "chosen-decision", "Use Levara.\nDo not create a second source.", "alice", "decision", "verified", "task-1", `["receipt-1"]`, "")
	insert("discovery", "verified-discovery", "Verified finding", "", "discovery", "verified", "", "", "")
	insert("unverified", "unverified", "must not export", "alice", "decision", "pending", "", "", "")
	insert("fact", "fact", "must not export", "alice", "fact", "verified", "", "", "")
	insert("old", "old", "must not export", "alice", "decision", "verified", "", "", "replacement")
	insert("bob", "bob", "must not export", "bob", "decision", "verified", "", "", "")
	var before int
	if err := deps.db.QueryRow(`SELECT COUNT(*) FROM memories`).Scan(&before); err != nil {
		t.Fatal(err)
	}

	ctx := context.WithValue(context.Background(), UserIDKey, "alice")
	got := ToolMemoryMarkdownDigest(ctx, deps, map[string]any{"collection": "levara", "memory_ids": []any{"decision", "discovery", "unverified", "fact", "old", "bob"}})
	if got.IsError {
		t.Fatal(got.Content[0].Text)
	}
	var out struct {
		Count    int    `json:"count"`
		Markdown string `json:"markdown"`
	}
	if err := json.Unmarshal([]byte(got.Content[0].Text), &out); err != nil {
		t.Fatal(err)
	}
	if out.Count != 2 || !strings.Contains(out.Markdown, "Levara remains the source of truth") || !strings.Contains(out.Markdown, "Source task: task-1") || !strings.Contains(out.Markdown, `Receipts: ["receipt-1"]`) || !strings.Contains(out.Markdown, "> Use Levara.") {
		t.Fatalf("unexpected digest: %s", got.Content[0].Text)
	}
	for _, forbidden := range []string{"must not export", "## bob"} {
		if strings.Contains(out.Markdown, forbidden) {
			t.Fatalf("digest leaked excluded memory: %s", out.Markdown)
		}
	}
	var after int
	if err := deps.db.QueryRow(`SELECT COUNT(*) FROM memories`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("digest mutated memories: before=%d after=%d", before, after)
	}
}

func TestToolMemoryMarkdownDigestRequiresSelectedKeys(t *testing.T) {
	deps := setupSaveRecallMemoryDB(t)
	for _, args := range []map[string]any{
		{}, {"collection": "levara"}, {"collection": "levara", "memory_ids": []any{}}, {"collection": "levara", "memory_ids": []any{""}},
	} {
		if got := ToolMemoryMarkdownDigest(context.Background(), deps, args); !got.IsError {
			t.Fatalf("args=%+v accepted", args)
		}
	}
}

func TestMemoryMarkdownDigestOmitsLegacyNullReceipts(t *testing.T) {
	got := memoryMarkdownDigestMarkdown("levara", []memoryMarkdownDigestRow{{Key: "decision", Hall: "decision", Room: "memory", Verification: "verified", ReceiptJSON: "null"}})
	if strings.Contains(got, "Receipts:") {
		t.Fatalf("legacy null receipts leaked into digest: %s", got)
	}
}
