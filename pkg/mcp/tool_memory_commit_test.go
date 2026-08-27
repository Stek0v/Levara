package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stek0v/levara/pkg/memoryindex"
)

func TestToolMemoryCommitPreviewClassifiesWithoutMutation(t *testing.T) {
	deps := setupSaveRecallMemoryDB(t)
	ctx := context.WithValue(context.Background(), UserIDKey, "owner-a")
	if got := ToolSaveMemory(ctx, deps, map[string]any{"collection": "levara", "key": "runtime", "value": "old value", "room": "memory", "hall": "decision"}); got.IsError {
		t.Fatal(got.Content[0].Text)
	}
	var oldID string
	if err := deps.db.QueryRow(`SELECT id FROM memories WHERE key='runtime'`).Scan(&oldID); err != nil {
		t.Fatal(err)
	}
	var before int
	if err := deps.db.QueryRow(`SELECT COUNT(*) FROM memories`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	got := ToolMemoryCommitPreview(ctx, deps, map[string]any{"collection": "levara", "idempotency_key": "preview-1", "candidates": []any{
		map[string]any{"candidate_id": "duplicate", "key": "runtime", "value": " old   value ", "room": "memory", "hall": "decision"},
		map[string]any{"candidate_id": "conflict", "key": "runtime", "value": "new value", "room": "memory", "hall": "decision"},
		map[string]any{"candidate_id": "replace", "key": "runtime", "value": "new value", "room": "memory", "hall": "decision", "supersedes_memory_id": oldID},
		map[string]any{"candidate_id": "secret", "key": "unsafe", "value": "api_key=sk-test-secret", "room": "memory", "hall": "fact"},
	}})
	if got.IsError {
		t.Fatal(got.Content[0].Text)
	}
	var out struct {
		Items []struct {
			CandidateID string `json:"candidate_id"`
			Action      string `json:"action"`
			ReasonCode  string `json:"reason_code"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(got.Content[0].Text), &out); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"duplicate": "skip", "conflict": "conflict", "replace": "supersede", "secret": "reject"}
	for _, item := range out.Items {
		if want[item.CandidateID] != item.Action {
			t.Errorf("%s action=%q want %q", item.CandidateID, item.Action, want[item.CandidateID])
		}
	}
	var after int
	if err := deps.db.QueryRow(`SELECT COUNT(*) FROM memories`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("preview mutated memories: before=%d after=%d", before, after)
	}
}

func TestMemoryCommitToolProfileFeatureFlag(t *testing.T) {
	t.Setenv("LEVARA_MEMORY_COMMIT", "")
	if ToolAllowedForMode("memory", "memory_commit_preview") {
		t.Fatal("preview exposed while feature disabled")
	}
	t.Setenv("LEVARA_MEMORY_COMMIT", "true")
	for _, name := range []string{"memory_commit_preview", "memory_commit_apply"} {
		if !ToolAllowedForMode("memory", name) {
			t.Fatalf("%s missing while feature enabled", name)
		}
	}
}

func TestToolMemoryCommitPreviewDoesNotInspectAnotherOwnersMemory(t *testing.T) {
	deps := setupSaveRecallMemoryDB(t)
	ownerA := context.WithValue(context.Background(), UserIDKey, "owner-a")
	if got := ToolSaveMemory(ownerA, deps, map[string]any{"collection": "levara", "key": "private", "value": "owner-a value", "room": "memory", "hall": "fact"}); got.IsError {
		t.Fatal(got.Content[0].Text)
	}
	ownerB := context.WithValue(context.Background(), UserIDKey, "owner-b")
	got := ToolMemoryCommitPreview(ownerB, deps, map[string]any{"collection": "levara", "idempotency_key": "preview-owner-b", "candidates": []any{
		map[string]any{"candidate_id": "candidate", "key": "private", "value": "owner-b value", "room": "memory", "hall": "fact"},
	}})
	if got.IsError {
		t.Fatal(got.Content[0].Text)
	}
	if !strings.Contains(got.Content[0].Text, `"action": "add"`) || strings.Contains(got.Content[0].Text, "owner-a value") {
		t.Fatalf("preview leaked or conflicted with another owner: %s", got.Content[0].Text)
	}
}

func TestToolMemoryCommitApplyIsAtomicIdempotentAndQueuesIndexJobs(t *testing.T) {
	deps := setupSaveRecallMemoryDB(t)
	ctx := context.WithValue(context.Background(), UserIDKey, "owner-a")
	if got := ToolSaveMemory(ctx, deps, map[string]any{"collection": "levara", "key": "runtime", "value": "old", "room": "memory", "hall": "decision"}); got.IsError {
		t.Fatal(got.Content[0].Text)
	}
	var oldID string
	if err := deps.db.QueryRow(`SELECT id FROM memories WHERE key='runtime'`).Scan(&oldID); err != nil {
		t.Fatal(err)
	}
	outbox, err := memoryindex.NewStore(deps.db)
	if err != nil {
		t.Fatal(err)
	}
	deps.memoryIndexOutbox, deps.embedAvailable, deps.hasColls = outbox, true, true
	preview := ToolMemoryCommitPreview(ctx, deps, map[string]any{"collection": "levara", "idempotency_key": "apply-1", "candidates": []any{
		map[string]any{"candidate_id": "add", "key": "new", "value": "new value", "room": "memory", "hall": "fact"},
		map[string]any{"candidate_id": "replace", "key": "runtime", "value": "replacement", "room": "memory", "hall": "decision", "supersedes_memory_id": oldID},
	}})
	commitID, digest := memoryCommitIDAndDigest(t, preview)
	apply := ToolMemoryCommitApply(ctx, deps, map[string]any{"commit_id": commitID, "plan_digest": digest})
	if apply.IsError || !strings.Contains(apply.Content[0].Text, `"added": 1`) || !strings.Contains(apply.Content[0].Text, `"superseded": 1`) {
		t.Fatalf("apply=%+v", apply)
	}
	if retry := ToolMemoryCommitApply(ctx, deps, map[string]any{"commit_id": commitID, "plan_digest": digest}); retry.IsError {
		t.Fatalf("idempotent retry: %s", retry.Content[0].Text)
	}
	var active, archived, jobs int
	_ = deps.db.QueryRow(`SELECT COUNT(*) FROM memories WHERE superseded_by=''`).Scan(&active)
	_ = deps.db.QueryRow(`SELECT COUNT(*) FROM memories WHERE id=? AND superseded_by<>''`, oldID).Scan(&archived)
	_ = deps.db.QueryRow(`SELECT COUNT(*) FROM memory_index_jobs`).Scan(&jobs)
	if active != 2 || archived != 1 || jobs != 3 {
		t.Fatalf("active=%d archived=%d jobs=%d", active, archived, jobs)
	}
}

func TestToolMemoryCommitApplyRollsBackWhenPlanBecomesStale(t *testing.T) {
	deps := setupSaveRecallMemoryDB(t)
	ctx := context.WithValue(context.Background(), UserIDKey, "owner-a")
	if got := ToolSaveMemory(ctx, deps, map[string]any{"collection": "levara", "key": "runtime", "value": "old", "room": "memory", "hall": "decision"}); got.IsError {
		t.Fatal(got.Content[0].Text)
	}
	var oldID string
	if err := deps.db.QueryRow(`SELECT id FROM memories WHERE key='runtime'`).Scan(&oldID); err != nil {
		t.Fatal(err)
	}
	preview := ToolMemoryCommitPreview(ctx, deps, map[string]any{"collection": "levara", "idempotency_key": "stale-1", "candidates": []any{
		map[string]any{"candidate_id": "add", "key": "must-not-persist", "value": "value", "room": "memory", "hall": "fact"},
		map[string]any{"candidate_id": "replace", "key": "runtime", "value": "replacement", "room": "memory", "hall": "decision", "supersedes_memory_id": oldID},
	}})
	commitID, digest := memoryCommitIDAndDigest(t, preview)
	if got := ToolSaveMemory(ctx, deps, map[string]any{"collection": "levara", "key": "runtime", "value": "changed after preview", "room": "memory", "hall": "decision"}); got.IsError {
		t.Fatal(got.Content[0].Text)
	}
	if got := ToolMemoryCommitApply(ctx, deps, map[string]any{"commit_id": commitID, "plan_digest": digest}); !got.IsError || !strings.Contains(got.Content[0].Text, "stale") {
		t.Fatalf("apply=%+v", got)
	}
	var added int
	_ = deps.db.QueryRow(`SELECT COUNT(*) FROM memories WHERE key='must-not-persist'`).Scan(&added)
	if added != 0 {
		t.Fatalf("stale plan partially applied: rows=%d", added)
	}
}

func memoryCommitIDAndDigest(t *testing.T, got ToolResult) (string, string) {
	t.Helper()
	var out struct {
		CommitID   string `json:"commit_id"`
		PlanDigest string `json:"plan_digest"`
	}
	if got.IsError || json.Unmarshal([]byte(got.Content[0].Text), &out) != nil || out.CommitID == "" || out.PlanDigest == "" {
		t.Fatalf("preview=%+v", got)
	}
	return out.CommitID, out.PlanDigest
}
