package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestToolMemoryScaffoldBlockOnlyRendersSelectedApprovedProposals(t *testing.T) {
	deps := setupSaveRecallMemoryDB(t)
	if _, err := deps.db.Exec(`CREATE TABLE memory_scaffold_proposals (id TEXT PRIMARY KEY, collection_name TEXT NOT NULL, summary TEXT NOT NULL, proposed_change TEXT NOT NULL, status TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	insert := func(id, collection, summary, change, status string) {
		t.Helper()
		if _, err := deps.db.Exec(`INSERT INTO memory_scaffold_proposals(id,collection_name,summary,proposed_change,status) VALUES(?,?,?,?,?)`, id, collection, summary, change, status); err != nil {
			t.Fatal(err)
		}
	}
	insert("approved", "levara", "consult memory", "Recall before saving durable memory.", "approved")
	insert("fallback", "levara", "Use room and hall.", "", "approved")
	insert("open", "levara", "must not render", "must not render", "open")
	insert("other", "other", "must not render", "must not render", "approved")
	var before int
	if err := deps.db.QueryRow(`SELECT COUNT(*) FROM memory_scaffold_proposals`).Scan(&before); err != nil {
		t.Fatal(err)
	}

	got := ToolMemoryScaffoldBlock(context.Background(), deps, map[string]any{"collection": "levara", "target_file": "AGENTS.md", "proposal_ids": []any{"approved", "fallback"}})
	if got.IsError {
		t.Fatal(got.Content[0].Text)
	}
	var out struct {
		TargetFile string `json:"target_file"`
		Block      string `json:"block"`
	}
	if err := json.Unmarshal([]byte(got.Content[0].Text), &out); err != nil {
		t.Fatal(err)
	}
	if out.TargetFile != "AGENTS.md" || !strings.Contains(out.Block, "Recall before saving durable memory.") || !strings.Contains(out.Block, "Use room and hall.") || strings.Contains(out.Block, "must not render") {
		t.Fatalf("unexpected preview: %s", got.Content[0].Text)
	}
	var after int
	if err := deps.db.QueryRow(`SELECT COUNT(*) FROM memory_scaffold_proposals`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("preview mutated proposals: before=%d after=%d", before, after)
	}
}

func TestToolMemoryScaffoldBlockRejectsInvalidOrUnapprovedSelection(t *testing.T) {
	deps := setupSaveRecallMemoryDB(t)
	if _, err := deps.db.Exec(`CREATE TABLE memory_scaffold_proposals (id TEXT PRIMARY KEY, collection_name TEXT NOT NULL, summary TEXT NOT NULL, proposed_change TEXT NOT NULL, status TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := deps.db.Exec(`INSERT INTO memory_scaffold_proposals(id,collection_name,summary,proposed_change,status) VALUES('open','levara','summary','change','open')`); err != nil {
		t.Fatal(err)
	}
	for _, args := range []map[string]any{
		{}, {"collection": "levara", "target_file": "README.md", "proposal_ids": []any{"open"}}, {"collection": "levara", "target_file": "CLAUDE.md", "proposal_ids": []any{"open"}},
	} {
		if got := ToolMemoryScaffoldBlock(context.Background(), deps, args); !got.IsError {
			t.Fatalf("args=%+v accepted", args)
		}
	}
}
