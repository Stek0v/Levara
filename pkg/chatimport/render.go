package chatimport

import (
	"fmt"
	"sort"
	"strings"
)

// RenderConversationMarkdown renders a normalized conversation as one
// self-contained markdown document for the RAG sink: a metadata header
// followed by one section per message, labeled by role and kind so chunk
// metadata survives into search results even before room/tags filtering.
func RenderConversationMarkdown(conv *Conversation) string {
	if conv == nil {
		return ""
	}
	// The server's /add extractor rejects NUL bytes and invalid UTF-8
	// (binary exec outputs carry both); neutralize before rendering.
	sanitize := func(s string) string {
		return strings.ToValidUTF8(strings.ReplaceAll(s, "\x00", "\uFFFD"), "\uFFFD")
	}
	var b strings.Builder
	title := conv.Title
	if title == "" {
		title = conv.SessionID
	}
	fmt.Fprintf(&b, "# [%s] %s\n\n", conv.Platform, title)
	fmt.Fprintf(&b, "- session: %s\n", conv.SessionID)
	fmt.Fprintf(&b, "- platform: %s\n", conv.Platform)
	if conv.Model != "" {
		fmt.Fprintf(&b, "- model: %s\n", conv.Model)
	}
	if conv.CreatedAt != "" {
		fmt.Fprintf(&b, "- created: %s\n", conv.CreatedAt)
	}
	if conv.Cwd != "" {
		fmt.Fprintf(&b, "- cwd: %s\n", conv.Cwd)
	}
	for _, k := range sortedKeys(conv.Metadata) {
		fmt.Fprintf(&b, "- %s: %s\n", k, conv.Metadata[k])
	}
	b.WriteString("\n")

	for _, m := range conv.Messages {
		m.Content = strings.ToValidUTF8(m.Content, "\uFFFD")
		if m.Kind == KindSystem {
			// Harness boilerplate adds search noise; keep it visible but
			// compact.
			fmt.Fprintf(&b, "## [%d] system\n\n<details>\n\n%s\n\n</details>\n\n", m.Ordinal, sanitize(m.Content))
			continue
		}
		label := string(m.Kind)
		if m.Kind == KindToolCall && m.Metadata["tool_name"] != "" {
			label = "tool_call: " + m.Metadata["tool_name"]
		}
		fmt.Fprintf(&b, "## [%d] %s · %s\n\n", m.Ordinal, m.Role, label)
		switch m.Kind {
		case KindReasoning:
			for _, line := range strings.Split(sanitize(m.Content), "\n") {
				b.WriteString("> " + line + "\n")
			}
			b.WriteString("\n")
		case KindToolCall, KindToolResult:
			lang := ""
			if m.Kind == KindToolCall {
				lang = "json"
			}
			fmt.Fprintf(&b, "```%s\n%s\n```\n\n", lang, sanitize(m.Content))
		default:
			b.WriteString(sanitize(m.Content))
			b.WriteString("\n\n")
		}
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
