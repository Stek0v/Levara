package chatimport

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func parseFixtureFile(t *testing.T, name string) *Conversation {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var conv *Conversation
	var err2 error
	if strings.HasPrefix(name, "claudecode") {
		conv, _, err2 = ParseClaudeCodeTranscript(bytes.NewReader(raw), DefaultParseOptions())
	} else {
		conv, _, err2 = ParseCodexRollout(bytes.NewReader(raw), DefaultParseOptions())
	}
	if err2 != nil {
		t.Fatal(err2)
	}
	return conv
}

func TestExtractArtifactsCodexApplyPatch(t *testing.T) {
	conv := parseFixtureFile(t, "codex-applypatch.jsonl")
	arts := ExtractArtifacts(conv, DefaultParseOptions())
	if len(arts) != 1 {
		t.Fatalf("artifacts = %d, want 1 (plan.md kept; main.go is code; legacy.md is a partial update)", len(arts))
	}
	a := arts[0]
	if a.FileName != "migration-plan.md" || a.FilePath != "docs/migration-plan.md" {
		t.Fatalf("artifact path wrong: %+v", a)
	}
	if a.Title != "План миграции на v2" {
		t.Fatalf("title = %q, want first heading", a.Title)
	}
	if !strings.HasPrefix(a.Content, "# План миграции на v2") || !strings.Contains(a.Content, "3. Переключить трафик") {
		t.Fatalf("content wrong: %q", a.Content)
	}
	if a.SourceID != "ctc_patch_1" || a.SessionID != "bbbb1111-2222-3333-4444-555555555555" {
		t.Fatalf("provenance lost: %+v", a)
	}
}

func TestExtractArtifactsClaudeCodeWrite(t *testing.T) {
	conv := parseFixtureFile(t, "claudecode-artifacts.jsonl")
	arts := ExtractArtifacts(conv, DefaultParseOptions())
	if len(arts) != 1 {
		t.Fatalf("artifacts = %d, want 1 (design.md kept; main.go is code)", len(arts))
	}
	a := arts[0]
	if a.FileName != "design.md" || a.Title != "Дизайн системы" {
		t.Fatalf("artifact wrong: %+v", a)
	}
	if a.SourceID != "a1#0" {
		t.Fatalf("source id = %q, want a1#0", a.SourceID)
	}
}

func TestExtractArtifactsNilAndEmpty(t *testing.T) {
	if got := ExtractArtifacts(nil, DefaultParseOptions()); got != nil {
		t.Fatalf("nil conversation gave %d artifacts", len(got))
	}
	conv := &Conversation{Platform: PlatformCodex, SessionID: "s", Messages: []Message{
		{ExternalID: "m1", Role: "user", Kind: KindText, Content: "no tools here"},
	}}
	if got := ExtractArtifacts(conv, DefaultParseOptions()); len(got) != 0 {
		t.Fatalf("plain messages gave %d artifacts", len(got))
	}
}

func TestRenderArtifactMarkdown(t *testing.T) {
	conv := parseFixtureFile(t, "codex-applypatch.jsonl")
	art := ExtractArtifacts(conv, DefaultParseOptions())[0]
	doc := RenderArtifactMarkdown(art, conv)
	for _, needle := range []string{
		"# План миграции на v2",
		"- file: docs/migration-plan.md",
		"- source: codex session bbbb1111-2222-3333-4444-555555555555 (message ctc_patch_1)",
		"3. Переключить трафик",
	} {
		if !strings.Contains(doc, needle) {
			t.Fatalf("rendered artifact missing %q:\n%s", needle, doc)
		}
	}
}
