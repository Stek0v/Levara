package chatimport

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"
)

// Artifact is a non-code document produced inside a conversation: a file
// written by the agent (Write tool, apply_patch Add File) with a document
// extension. Code files are deliberately excluded per the project decision
// (2026-09-20) — they stay in the transcript, not in the artifact layer.
type Artifact struct {
	FileName  string // original basename, e.g. "plan.md"
	FilePath  string // original full path from the tool call
	Title     string // first markdown heading or basename
	Content   string // full file content
	SourceID  string // external_id of the producing message
	SessionID string
}

// documentExtensions is the non-code allowlist for artifact extraction.
var documentExtensions = map[string]bool{
	".md": true, ".markdown": true, ".txt": true, ".rst": true, ".adoc": true,
}

// ExtractArtifacts walks tool-call messages and returns full documents the
// agent wrote: Claude Code "Write" tool inputs and Codex apply_patch
// "Add File" sections. Partial updates (Edit, apply_patch Update File) are
// skipped — they cannot be reconstructed into a whole document.
func ExtractArtifacts(conv *Conversation, opts ParseOptions) []Artifact {
	if conv == nil {
		return nil
	}
	if opts.MaxContentBytes <= 0 {
		opts = DefaultParseOptions()
	}
	var out []Artifact
	for _, m := range conv.Messages {
		if m.Kind != KindToolCall {
			continue
		}
		switch m.Metadata["tool_name"] {
		case "Write":
			out = append(out, claudeCodeWriteArtifacts(m, conv.SessionID, opts)...)
		case "apply_patch":
			out = append(out, applyPatchArtifacts(m, conv.SessionID, opts)...)
		}
	}
	return out
}

// claudeCodeWriteArtifacts parses a Write tool call whose content is
// "Write\n{json input}".
func claudeCodeWriteArtifacts(m Message, sessionID string, opts ParseOptions) []Artifact {
	body := strings.TrimSpace(strings.TrimPrefix(m.Content, "Write"))
	var in struct {
		FilePath string `json:"file_path"`
		Content  string `json:"content"`
	}
	if err := json.Unmarshal([]byte(body), &in); err != nil || in.FilePath == "" {
		return nil
	}
	if !isDocumentPath(in.FilePath) {
		return nil
	}
	return []Artifact{newArtifact(in.FilePath, in.Content, m.ExternalID, sessionID, opts)}
}

// applyPatchArtifacts extracts Add File sections from a codex apply_patch
// input:
//
//	*** Begin Patch
//	*** Add File: docs/plan.md
//	+content line
//	*** End Patch
func applyPatchArtifacts(m Message, sessionID string, opts ParseOptions) []Artifact {
	var out []Artifact
	var currentPath string
	var current []string
	flush := func() {
		if currentPath == "" || !isDocumentPath(currentPath) {
			currentPath, current = "", nil
			return
		}
		var b strings.Builder
		for _, line := range current {
			b.WriteString(strings.TrimPrefix(line, "+"))
			b.WriteString("\n")
		}
		out = append(out, newArtifact(currentPath, b.String(), m.ExternalID, sessionID, opts))
		currentPath, current = "", nil
	}
	for _, line := range strings.Split(m.Content, "\n") {
		switch {
		case strings.HasPrefix(line, "*** Add File: "):
			flush()
			currentPath = strings.TrimSpace(strings.TrimPrefix(line, "*** Add File: "))
		case strings.HasPrefix(line, "*** Update File: "), strings.HasPrefix(line, "*** Delete File: "), strings.HasPrefix(line, "*** End Patch"), strings.HasPrefix(line, "*** Begin Patch"):
			flush()
		default:
			if currentPath != "" {
				current = append(current, line)
			}
		}
	}
	flush()
	return out
}

func newArtifact(filePath, content, sourceID, sessionID string, opts ParseOptions) Artifact {
	content = truncateContent(strings.TrimRight(content, "\n"), opts.MaxContentBytes)
	return Artifact{
		FileName:  path.Base(filePath),
		FilePath:  filePath,
		Title:     artifactTitle(content, filePath),
		Content:   content,
		SourceID:  sourceID,
		SessionID: sessionID,
	}
}

func artifactTitle(content, filePath string) string {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			return strings.TrimSpace(strings.TrimLeft(trimmed, "# "))
		}
	}
	return path.Base(filePath)
}

func isDocumentPath(p string) bool {
	return documentExtensions[strings.ToLower(path.Ext(p))]
}

// RenderArtifactMarkdown wraps an artifact into a self-contained document
// for the RAG sink, with provenance the conversation render cannot provide.
func RenderArtifactMarkdown(a Artifact, conv *Conversation) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", a.Title)
	fmt.Fprintf(&b, "- file: %s\n", a.FilePath)
	fmt.Fprintf(&b, "- source: %s session %s (message %s)\n", conv.Platform, conv.SessionID, a.SourceID)
	if conv.CreatedAt != "" {
		fmt.Fprintf(&b, "- created: %s\n", conv.CreatedAt)
	}
	b.WriteString("\n")
	b.WriteString(strings.ToValidUTF8(strings.ReplaceAll(a.Content, "\x00", "\uFFFD"), "\uFFFD"))
	b.WriteString("\n")
	return b.String()
}
