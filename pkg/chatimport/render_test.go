package chatimport

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestRenderConversationMarkdownGolden(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "codex-rollout.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	conv, _, err := ParseCodexRollout(bytes.NewReader(raw), DefaultParseOptions())
	if err != nil {
		t.Fatal(err)
	}
	got := RenderConversationMarkdown(conv)

	goldenPath := filepath.Join("testdata", "codex-rollout-rendered.md")
	if *update {
		if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("golden file missing (run go test -update once and review it): %v", err)
	}
	if got != string(want) {
		t.Fatalf("rendered markdown drifted from golden:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}

	// Structure invariants the search sink depends on.
	for _, needle := range []string{
		"# [codex] Как устроен WAL в SQLite?",
		"- session: 11111111-2222-3333-4444-555555555555",
		"- cli_version: 0.50.0",
		"## [1] user · text",
		"## [2] assistant · reasoning",
		"## [4] assistant · tool_call: exec",
		"## [5] tool · tool_result",
	} {
		if !bytes.Contains([]byte(got), []byte(needle)) {
			t.Fatalf("rendered markdown missing %q", needle)
		}
	}
}

func TestRenderNilConversation(t *testing.T) {
	if got := RenderConversationMarkdown(nil); got != "" {
		t.Fatalf("nil render = %q, want empty", got)
	}
}
