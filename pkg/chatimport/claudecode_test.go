package chatimport

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func parseClaudeFixture(t *testing.T, opts ParseOptions) (*Conversation, ParseStats) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "claudecode-transcript.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	conv, stats, err := ParseClaudeCodeTranscript(bytes.NewReader(raw), opts)
	if err != nil {
		t.Fatal(err)
	}
	return conv, stats
}

func TestParseClaudeCodeGolden(t *testing.T) {
	conv, stats := parseClaudeFixture(t, DefaultParseOptions())

	goldenPath := filepath.Join("testdata", "claudecode-transcript.expected.json")
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

	if conv.Platform != PlatformClaudeCode || conv.SessionID != "aaaa0000-0000-0000-0000-000000000001" {
		t.Fatalf("identity wrong: %s/%s", conv.Platform, conv.SessionID)
	}
	if conv.Title != "Проверка workflow ревью" {
		t.Fatalf("title = %q, want summary record text", conv.Title)
	}
	if conv.Model != "claude-sonnet-4-5" {
		t.Fatalf("model = %q", conv.Model)
	}
	if conv.Metadata["git_branch"] != "main" || conv.Metadata["cli_version"] != "2.0.14" {
		t.Fatalf("metadata wrong: %v", conv.Metadata)
	}
	if got := len(conv.Messages); got != 7 {
		t.Fatalf("messages = %d, want 7", got)
	}
	if stats.Skipped != 5 {
		t.Fatalf("skipped = %d, want 5 (summary, redacted_thinking, attachment, queue-operation, bad line)", stats.Skipped)
	}
	if len(stats.Warnings) != 1 {
		t.Fatalf("warnings = %v, want malformed-line warning only", stats.Warnings)
	}

	byID := map[string]Message{}
	for _, m := range conv.Messages {
		byID[m.ExternalID] = m
	}
	if m := byID["a-1#0"]; m.Kind != KindReasoning || m.Role != "assistant" {
		t.Fatalf("thinking block: %+v", m)
	}
	if m := byID["a-1#1"]; m.Kind != KindToolCall || m.Metadata["tool_name"] != "Bash" || m.Metadata["call_id"] != "toolu_1" {
		t.Fatalf("tool_use block: %+v", m)
	}
	if m := byID["u-tool-1#0"]; m.Kind != KindToolResult || m.Role != "tool" || m.Metadata["call_id"] != "toolu_1" {
		t.Fatalf("tool_result block: %+v", m)
	}
	if !strings.Contains(byID["u-tool-1#0"].Content, "diff --git a/main.go") {
		t.Fatalf("tool_result content lost: %q", byID["u-tool-1#0"].Content)
	}
	if m := byID["s-1"]; m.Kind != KindSystem || m.Role != "system" {
		t.Fatalf("system record: %+v", m)
	}
	// The redacted_thinking sidechain record produced no messages, so no
	// message carries the sidechain flag here — verified via absence.
	for _, m := range conv.Messages {
		if _, ok := m.Metadata["sidechain"]; ok {
			t.Fatalf("unexpected sidechain marker: %+v", m)
		}
	}
}

func TestParseClaudeCodeWithoutReasoning(t *testing.T) {
	opts := DefaultParseOptions()
	opts.IncludeReasoning = false
	conv, stats := parseClaudeFixture(t, opts)
	if got := len(conv.Messages); got != 6 {
		t.Fatalf("messages = %d, want 6 (thinking excluded)", got)
	}
	if stats.Skipped != 6 {
		t.Fatalf("skipped = %d, want 6", stats.Skipped)
	}
}

func TestParseClaudeCodeRejectsNoSession(t *testing.T) {
	_, _, err := ParseClaudeCodeTranscript(bytes.NewReader([]byte("{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":\"hi\"}}\n")), DefaultParseOptions())
	if err == nil {
		t.Fatal("expected error for transcript without sessionId")
	}
}
