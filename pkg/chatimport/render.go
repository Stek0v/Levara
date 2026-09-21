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
			// Harness boilerplate (permissions, app-context, skills) is pure
			// search noise — measured live: it outranked real content in the
			// chat-imports collection. The raw layer keeps every message;
			// the RAG document simply omits the boilerplate.
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

// RagSegment is one independently cognifiable piece of a conversation.
// Large sessions (megabytes of rendered markdown) exceed the cognify
// budget as a single document; segmenting preserves search quality —
// every piece is findable, nothing is skipped.
type RagSegment struct {
	Index   int    // 1-based part number
	Name    string // deterministic item name suffix
	Content string // rendered markdown for this segment
}

// RenderConversationSegments renders a conversation as one or more
// segments, each at most maxBytes of markdown. Small conversations
// produce a single segment (name = ""); large ones are split at message
// boundaries with the metadata header repeated on each part.
func RenderConversationSegments(conv *Conversation, maxBytes int) []RagSegment {
	full := RenderConversationMarkdown(conv)
	if maxBytes <= 0 || len(full) <= maxBytes {
		return []RagSegment{{Index: 1, Name: "", Content: full}}
	}

	// Build the metadata header once; repeat on each segment for
	// standalone searchability.
	var header strings.Builder
	fmt.Fprintf(&header, "# [%s] %s (part %%d)\n\n", conv.Platform, conv.Title)
	fmt.Fprintf(&header, "- session: %s\n", conv.SessionID)
	fmt.Fprintf(&header, "- platform: %s\n", conv.Platform)
	if conv.Model != "" {
		fmt.Fprintf(&header, "- model: %s\n", conv.Model)
	}
	fmt.Fprintf(&header, "- segment: %%d\n")
	header.WriteString("\n")

	var segments []RagSegment
	var body strings.Builder
	segIdx := 0
	flush := func() {
		segIdx++
		content := fmt.Sprintf(header.String(), segIdx, segIdx) + body.String()
		segments = append(segments, RagSegment{
			Index:   segIdx,
			Name:    fmt.Sprintf("-part%03d", segIdx),
			Content: content,
		})
		body.Reset()
	}

	// Iterate messages, accumulating until the budget.
	headerLen := 200 // approximate
	for _, m := range conv.Messages {
		if m.Kind == KindSystem {
			continue
		}
		line := renderMessageLine(m)
		if body.Len()+len(line)+headerLen > maxBytes && body.Len() > 0 {
			flush()
		}
		body.WriteString(line)
		body.WriteString("\n\n")
	}
	if body.Len() > 0 {
		flush()
	}
	return segments
}

// renderMessageLine renders one message without the conversation header.
func renderMessageLine(m Message) string {
	sanitize := func(s string) string {
		return strings.ToValidUTF8(strings.ReplaceAll(s, "\x00", "\uFFFD"), "\uFFFD")
	}
	label := string(m.Kind)
	if m.Kind == KindToolCall && m.Metadata["tool_name"] != "" {
		label = "tool_call: " + m.Metadata["tool_name"]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "## %s · %s\n\n", m.Role, label)
	switch m.Kind {
	case KindReasoning:
		for _, line := range strings.Split(sanitize(m.Content), "\n") {
			b.WriteString("> " + line + "\n")
		}
		b.WriteString("\n")
	case KindToolCall, KindToolResult:
		fmt.Fprintf(&b, "```\n%s\n```\n\n", sanitize(m.Content))
	default:
		b.WriteString(sanitize(m.Content))
		b.WriteString("\n\n")
	}
	return b.String()
}
