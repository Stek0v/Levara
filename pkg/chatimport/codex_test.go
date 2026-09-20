package chatimport

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

func TestParseCodexRolloutGolden(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "codex-rollout.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	conv, stats, err := ParseCodexRollout(bytes.NewReader(raw), DefaultParseOptions())
	if err != nil {
		t.Fatal(err)
	}

	goldenPath := filepath.Join("testdata", "codex-rollout.expected.json")
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(conv); err != nil {
		t.Fatal(err)
	}

	if *update {
		if err := os.WriteFile(goldenPath, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("golden file missing (run go test -update once and review it): %v", err)
	}
	if !jsonEqual(buf.Bytes(), want) {
		t.Fatalf("normalized conversation drifted from golden:\n--- got ---\n%s\n--- want ---\n%s", buf.String(), want)
	}

	if got := len(conv.Messages); got != 7 {
		t.Fatalf("messages = %d, want 7", got)
	}
	if stats.Skipped != 5 {
		t.Fatalf("skipped = %d, want 5 (encrypted reasoning, token_usage, turn_context, token_count, bad line)", stats.Skipped)
	}
	if len(stats.Warnings) != 1 {
		t.Fatalf("warnings = %v, want exactly the malformed-line warning", stats.Warnings)
	}
	if conv.Title != "Как устроен WAL в SQLite?" {
		t.Fatalf("title = %q", conv.Title)
	}
	if conv.Model != "gpt-5.2-codex" {
		t.Fatalf("model = %q", conv.Model)
	}
	// Message-level shape spot checks.
	m := conv.Messages[0]
	if m.Role != "developer" || m.Kind != KindSystem {
		t.Fatalf("first message role/kind = %s/%s, want developer/system", m.Role, m.Kind)
	}
	m = conv.Messages[2]
	if m.Kind != KindReasoning || m.Content == "" {
		t.Fatalf("reasoning message not normalized: %+v", m)
	}
	m = conv.Messages[4]
	if m.Kind != KindToolCall || m.Metadata["tool_name"] != "exec" {
		t.Fatalf("tool call not normalized: %+v", m)
	}
	m = conv.Messages[5]
	if m.Kind != KindToolResult || m.Content != "Script completed\nOutput:\nwal" {
		t.Fatalf("tool result not normalized: %+v", m)
	}
}

func TestParseCodexRolloutWithoutReasoning(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "codex-rollout.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	opts := DefaultParseOptions()
	opts.IncludeReasoning = false
	conv, stats, err := ParseCodexRollout(bytes.NewReader(raw), opts)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(conv.Messages); got != 6 {
		t.Fatalf("messages = %d, want 6 (reasoning excluded)", got)
	}
	if stats.Skipped != 6 {
		t.Fatalf("skipped = %d, want 6", stats.Skipped)
	}
}

func TestParseCodexRolloutRejectsNoSessionMeta(t *testing.T) {
	_, _, err := ParseCodexRollout(bytes.NewReader([]byte("{\"type\":\"turn_context\"}\n")), DefaultParseOptions())
	if err == nil {
		t.Fatal("expected error for rollout without session_meta")
	}
}

// jsonEqual compares two JSON documents structurally so golden files stay
// stable across key ordering and whitespace.
func jsonEqual(a, b []byte) bool {
	var va, vb any
	if err := json.Unmarshal(a, &va); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &vb); err != nil {
		return false
	}
	na, _ := json.Marshal(va)
	nb, _ := json.Marshal(vb)
	return bytes.Equal(na, nb)
}

// Old-format rollouts (≈2026-08): object source with subagent descriptor,
// string tool outputs, agent_message records.
func TestParseCodexRolloutOldFormat(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "codex-rollout-old.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	conv, stats, err := ParseCodexRollout(bytes.NewReader(raw), DefaultParseOptions())
	if err != nil {
		t.Fatal(err)
	}
	if conv.SessionID != "aaaa1111-bbbb-cccc-dddd-eeeeeeeeeeee" {
		t.Fatalf("session id = %q", conv.SessionID)
	}
	if conv.Metadata["source"] != "subagent" || conv.Metadata["agent_nickname"] != "Rawls" || conv.Metadata["parent_thread_id"] == "" {
		t.Fatalf("subagent metadata not captured: %v", conv.Metadata)
	}
	if got := len(conv.Messages); got != 4 {
		t.Fatalf("messages = %d, want 4 (user, agent_message, tool_call, string-output tool_result); encrypted-only agent_message skipped", got)
	}
	if stats.Skipped != 1 {
		t.Fatalf("skipped = %d, want 1", stats.Skipped)
	}
	agent := conv.Messages[1]
	if agent.Role != "agent" || agent.Metadata["recipient"] != "/root/edge_hunter" {
		t.Fatalf("agent_message not normalized: %+v", agent)
	}
	out := conv.Messages[3]
	if out.Content != "raw string output shape" {
		t.Fatalf("string output shape mishandled: %q", out.Content)
	}
	if conv.Title != "Проверь граничные случаи парсера" {
		t.Fatalf("title = %q", conv.Title)
	}
}
