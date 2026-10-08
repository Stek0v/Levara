package mcp

import (
	"context"
	"encoding/json"
	"reflect"
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

func TestMemoryMarkdownDigestValidatedSaveAndScope(t *testing.T) {
	for _, dialect := range []struct {
		name string
		pg   bool
	}{{"sqlite", false}, {"postgres", true}} {
		t.Run(dialect.name, func(t *testing.T) {
			deps, ctx := memoryCommitEvidenceFixture(t, dialect.pg)
			if dialect.pg {
				for _, col := range []string{"created_at", "updated_at"} {
					if _, err := deps.DB().Exec("ALTER TABLE memories ALTER COLUMN " + col + " TYPE TIMESTAMPTZ USING " + col + "::timestamptz"); err != nil {
						t.Fatal(err)
					}
				}
			}
			taskID, receiptID := memoryCommitOwnedTaskReceipt(t, deps, ctx)
			var taskOwner, taskCollection, revision, receiptOwner, receiptTask, receiptStatus, receiptRevision string
			var exit int
			if err := deps.DB().QueryRow(deps.Q("SELECT owner_id,collection_name,current_workspace_revision FROM tasks WHERE id=$1"), taskID).Scan(&taskOwner, &taskCollection, &revision); err != nil {
				t.Fatal(err)
			}
			if err := deps.DB().QueryRow(deps.Q("SELECT owner_id,task_id,status,workspace_revision,exit_code FROM task_receipts WHERE id=$1"), receiptID).Scan(&receiptOwner, &receiptTask, &receiptStatus, &receiptRevision, &exit); err != nil {
				t.Fatal(err)
			}
			if taskOwner != "owner-a" || receiptOwner != taskOwner || taskCollection != "levara" || receiptTask != taskID || revision != "rev-1" || receiptRevision != revision || receiptStatus != "pass" || exit != 0 {
				t.Fatalf("fixture has no current owned pass evidence: task=%s/%s/%s receipt=%s/%s/%s/%s exit=%d", taskOwner, taskCollection, revision, receiptOwner, receiptTask, receiptStatus, receiptRevision, exit)
			}
			type published struct{ id, key, value, hall, updated string }
			var validated []published
			receipts, err := json.Marshal([]string{receiptID})
			if err != nil {
				t.Fatal(err)
			}
			for _, hall := range []string{"decision", "discovery"} {
				row := published{key: "validated-" + hall, value: "Evidence-backed " + hall + ".\nPreserve this publication.", hall: hall}
				result := ToolSaveMemory(ctx, deps, map[string]any{
					"key": row.key, "value": row.value, "collection": "levara", "room": "memory", "hall": hall,
					"source_task_id": taskID, "source_receipt_ids": []any{receiptID}, "verification_status": "verified",
				})
				if result.IsError {
					t.Fatal(toolResultText(result))
				}
				var verification, savedTask, savedReceipts string
				if err := deps.DB().QueryRow(deps.Q("SELECT id,verification_status,source_task_id,source_receipt_ids,updated_at FROM memories WHERE key=$1 AND owner_id=$2 AND collection_name=$3"), row.key, "owner-a", "levara").Scan(&row.id, &verification, &savedTask, &savedReceipts, &row.updated); err != nil {
					t.Fatal(err)
				}
				if verification != "receipt-validated" || savedTask != taskID || savedReceipts != string(receipts) {
					t.Fatalf("actual writer did not validate/preserve source evidence: label=%q task=%q receipts=%q", verification, savedTask, savedReceipts)
				}
				if _, err := time.Parse(time.RFC3339Nano, row.updated); err != nil {
					t.Fatalf("stored freshness is not RFC3339: %q: %v", row.updated, err)
				}
				validated = append(validated, row)
			}
			// No evidence means a caller hint cannot certify the publication.
			hint := ToolSaveMemory(ctx, deps, map[string]any{"key": "caller-hint", "value": "EXCLUDED_CALLER_HINT", "collection": "levara", "room": "memory", "hall": "decision", "verification_status": "receipt-validated"})
			if hint.IsError {
				t.Fatal(toolResultText(hint))
			}
			var hintID, hintStatus string
			if err := deps.DB().QueryRow("SELECT id,verification_status FROM memories WHERE key='caller-hint'").Scan(&hintID, &hintStatus); err != nil || hintStatus != "unverified" {
				t.Fatalf("caller certified memory without evidence: label=%q err=%v", hintStatus, err)
			}
			for _, row := range []struct{ id, owner, collection, hall, label, retired string }{
				{"legacy-own", "owner-a", "levara", "decision", "verified", ""},
				{"legacy-shared", "", "levara", "discovery", "verified", ""},
				{"validated-shared", "", "levara", "decision", "receipt-validated", ""},
				{"foreign", "owner-b", "levara", "decision", "receipt-validated", ""},
				{"sibling", "owner-a", "other", "discovery", "receipt-validated", ""},
				{"retired", "owner-a", "levara", "decision", "receipt-validated", "replacement"},
				{"unverified", "owner-a", "levara", "discovery", "unverified", ""},
				{"other-hall", "owner-a", "levara", "fact", "receipt-validated", ""},
				{"not-selected", "owner-a", "levara", "discovery", "receipt-validated", ""},
			} {
				value := "CONTROL_" + row.id
				if _, err := deps.DB().Exec(deps.Q("INSERT INTO memories(id,key,value,type,owner_id,collection_name,room,hall,verification_status,superseded_by,created_at,updated_at) VALUES($1,$2,$3,'project',$4,$5,'memory',$6,$7,$8,$9,$10)"), row.id, row.id, value, row.owner, row.collection, row.hall, row.label, row.retired, "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"); err != nil {
					t.Fatal(err)
				}
			}
			// Compare complete SQL rows, including source evidence and ledger events.
			snapshot := func(t *testing.T) map[string]string {
				t.Helper()
				out := map[string]string{}
				for _, table := range []string{"memories", "tasks", "task_receipts", "task_events"} {
					rows, err := deps.DB().Query("SELECT * FROM " + table + " ORDER BY id")
					if err != nil {
						t.Fatal(err)
					}
					cols, err := rows.Columns()
					if err != nil {
						_ = rows.Close()
						t.Fatal(err)
					}
					var records [][]any
					for rows.Next() {
						values, pointers := make([]any, len(cols)), make([]any, len(cols))
						for i := range values {
							pointers[i] = &values[i]
						}
						if err := rows.Scan(pointers...); err != nil {
							_ = rows.Close()
							t.Fatal(err)
						}
						records = append(records, values)
					}
					err = rows.Err()
					_ = rows.Close()
					if err != nil {
						t.Fatal(err)
					}
					raw, err := json.Marshal(records)
					if err != nil {
						t.Fatal(err)
					}
					out[table] = string(raw)
				}
				return out
			}
			type digest struct {
				Collection  string `json:"collection"`
				GeneratedAt string `json:"generated_at"`
				Count       int    `json:"count"`
				Markdown    string `json:"markdown"`
			}
			read := func(t *testing.T, ids []any) digest {
				t.Helper()
				before := snapshot(t)
				got := ToolMemoryMarkdownDigest(ctx, deps, map[string]any{"collection": "levara", "memory_ids": ids})
				if !reflect.DeepEqual(snapshot(t), before) {
					t.Fatal("read-only digest changed memory or evidence rows")
				}
				if got.IsError || len(got.Content) != 1 || got.Content[0].Type != "text" {
					t.Fatalf("digest error/result shape: %+v", got)
				}
				var out digest
				if err := json.Unmarshal([]byte(got.Content[0].Text), &out); err != nil {
					t.Fatal(err)
				}
				if out.Collection != "levara" {
					t.Fatalf("digest collection=%q", out.Collection)
				}
				if _, err := time.Parse(time.RFC3339, out.GeneratedAt); err != nil {
					t.Fatalf("digest generated_at=%q: %v", out.GeneratedAt, err)
				}
				return out
			}
			t.Run("actual-validated-save", func(t *testing.T) {
				got := read(t, []any{validated[0].id, " " + validated[0].id + " ", validated[1].id, validated[1].id})
				if got.Count != 2 {
					t.Fatalf("actual receipt-validated decision/discovery missing from explicit digest: count=%d markdown=%s", got.Count, got.Markdown)
				}
				if strings.Count(got.Markdown, "- Verification: receipt-validated\n") != 2 || strings.Count(got.Markdown, "- Source task: "+taskID+"\n") != 2 || strings.Count(got.Markdown, "- Receipts: "+string(receipts)+"\n") != 2 {
					t.Fatalf("digest changed stored label or provenance: %s", got.Markdown)
				}
				for _, row := range validated {
					heading := "## " + row.key + "\n"
					if strings.Count(got.Markdown, heading) != 1 {
						t.Errorf("digest missing/duplicating selected memory %q: %s", row.key, got.Markdown)
						continue
					}
					card := strings.SplitN(got.Markdown, heading, 2)[1]
					if next := strings.Index(card, "\n## "); next >= 0 {
						card = card[:next]
					}
					for _, text := range []string{"- Kind: " + row.hall + "\n", "- Room: memory\n", "- Freshness: updated " + row.updated + "\n", "> Evidence-backed " + row.hall + ".\n> Preserve this publication.\n"} {
						if strings.Count(card, text) != 1 {
							t.Errorf("digest changed stored detail %q in %s: %s", text, row.key, card)
						}
					}
				}
			})
			t.Run("eligible-own-shared-and-exclusions", func(t *testing.T) {
				got := read(t, []any{validated[0].id, validated[1].id, "legacy-own", "legacy-shared", "validated-shared", "foreign", "sibling", "retired", "unverified", "other-hall", hintID, "missing", "legacy-own"})
				if got.Count != 5 || strings.Count(got.Markdown, "- Verification: verified\n") != 2 || strings.Count(got.Markdown, "- Verification: receipt-validated\n") != 3 {
					t.Errorf("eligibility/legacy label compatibility mismatch: count=%d markdown=%s", got.Count, got.Markdown)
				}
				for _, id := range []string{"legacy-own", "legacy-shared", "validated-shared"} {
					if strings.Count(got.Markdown, "## "+id+"\n") != 1 || !strings.Contains(got.Markdown, "> CONTROL_"+id+"\n") {
						t.Errorf("eligible control missing/duplicated: %s", id)
					}
				}
				for _, id := range []string{"foreign", "sibling", "retired", "unverified", "other-hall", "not-selected"} {
					if strings.Contains(got.Markdown, "CONTROL_"+id) {
						t.Errorf("excluded %s exported: %s", id, got.Markdown)
					}
				}
				if strings.Contains(got.Markdown, "EXCLUDED_CALLER_HINT") {
					t.Error("caller hint exported without evidence")
				}
				hundred := make([]any, memoryMarkdownDigestMaxKeys)
				for i := range hundred {
					hundred[i] = "legacy-own"
				}
				if got := read(t, hundred); got.Count != 1 || strings.Count(got.Markdown, "## legacy-own\n") != 1 {
					t.Errorf("100 valid duplicate IDs changed compatibility: %+v", got)
				}
				if got := read(t, []any{"missing"}); got.Count != 0 {
					t.Errorf("unknown explicit ID exported records: %+v", got)
				}
			})
			t.Run("malformed-and-existing-limit-errors", func(t *testing.T) {
				tooMany := make([]any, memoryMarkdownDigestMaxKeys+1)
				for i := range tooMany {
					tooMany[i] = "legacy-own"
				}
				before := snapshot(t)
				for _, raw := range []any{nil, "legacy-own", []string{"legacy-own"}, []any{}, []any{""}, []any{"  "}, []any{nil}, []any{7}, []any{true}, []any{map[string]any{}}, tooMany} {
					got := ToolMemoryMarkdownDigest(ctx, deps, map[string]any{"collection": "levara", "memory_ids": raw})
					want := "'memory_ids' must contain non-empty strings"
					if items, ok := raw.([]any); !ok || len(items) == 0 || len(items) > memoryMarkdownDigestMaxKeys {
						want = "'memory_ids' must contain 1-100 memory IDs"
					}
					if !got.IsError || len(got.Content) != 1 || got.Content[0].Type != "text" || !strings.Contains(got.Content[0].Text, want) {
						t.Errorf("invalid selector %T/%v changed error contract: %+v", raw, raw, got)
					}
				}
				if !reflect.DeepEqual(snapshot(t), before) {
					t.Error("invalid digest selector changed SQL state")
				}
			})
		})
	}
}
