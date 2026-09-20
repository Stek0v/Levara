package chatimport

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// ParseOptions tunes adapter behaviour. Defaults match the project decision
// recorded 2026-09-20: reasoning is imported for CLI transcript sources.
type ParseOptions struct {
	IncludeReasoning bool
	MaxContentBytes  int
}

// DefaultParseOptions returns the recommended options.
func DefaultParseOptions() ParseOptions {
	return ParseOptions{IncludeReasoning: true, MaxContentBytes: 64 * 1024}
}

const (
	defaultTitleLen = 80
	systemPrefixes  = "<app-context>,<user-instructions>,<environment_context>,<ENVIRONMENT_CONTEXT>,<turn_aborted>,<multi_agent_role>,<multi_agent_mode>"
)

// ParseCodexRollout parses one Codex rollout JSONL transcript
// (~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl) into a Conversation.
//
// The rollout is a stream of typed records; only session_meta and
// response_item/* carry conversation content. Everything else (token counts,
// world state, UI events) is counted as skipped. Unknown or malformed records
// are skipped with a warning — foreign formats must not abort the import.
func ParseCodexRollout(r io.Reader, opts ParseOptions) (*Conversation, ParseStats, error) {
	if opts.MaxContentBytes <= 0 {
		opts = DefaultParseOptions()
	}
	stats := ParseStats{}
	conv := &Conversation{Platform: PlatformCodex}

	// Line-by-line JSONL parsing: a malformed line invalidates only itself.
	// json.Decoder would be unrecoverable after a syntax error mid-stream.
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 32*1024*1024)
	ordinal := 0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec codexRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			stats.Skipped++
			stats.AddWarning(fmt.Sprintf("skipped malformed JSON record: %v", err))
			continue
		}
		switch rec.Type {
		case "session_meta":
			var meta codexSessionMeta
			if err := json.Unmarshal(rec.Payload, &meta); err != nil {
				stats.AddWarning("session_meta record unparsable")
				continue
			}
			// July-2026 rollouts (cli ≈0.46) carry only `id`; later ones
			// carry both, with session_id authoritative.
			sid := firstNonEmpty(meta.SessionID, meta.ID)
			if sid == "" {
				stats.AddWarning("session_meta record without session_id")
				continue
			}
			conv.SessionID = sid
			conv.CreatedAt = meta.Timestamp
			conv.Cwd = meta.Cwd
			metaMap := map[string]string{
				"originator":  meta.Originator,
				"cli_version": meta.CLIVersion,
			}
			// source is a plain string in recent rollouts and an object
			// (subagent/thread spawn descriptor) in older ones.
			if s, ok := meta.Source.(string); ok {
				metaMap["source"] = s
			} else if meta.ThreadSource != "" {
				metaMap["source"] = meta.ThreadSource
			}
			if meta.AgentNickname != "" {
				metaMap["agent_nickname"] = meta.AgentNickname
			}
			if meta.ParentThreadID != "" {
				metaMap["parent_thread_id"] = meta.ParentThreadID
			}
			conv.Metadata = metaMap
		case "event_msg":
			// thread_settings_applied is the reliable model source; the rest
			// is UI noise.
			if model := codexEventModel(rec.Payload); model != "" && conv.Model == "" {
				conv.Model = model
			} else {
				stats.Skipped++
			}
		case "turn_context", "world_state", "token_usage_record":
			stats.Skipped++
		case "response_item":
			var item codexItem
			if err := json.Unmarshal(rec.Payload, &item); err != nil {
				stats.Skipped++
				stats.AddWarning(fmt.Sprintf("skipped unparsable response_item: %v", err))
				continue
			}
			msg, ok := codexItemToMessage(item, rec.Timestamp, ordinal, opts, &stats)
			if ok {
				msg.Model = firstNonEmpty(msg.Model, conv.Model)
				conv.Messages = append(conv.Messages, msg)
				ordinal++
			}
		default:
			stats.Skipped++
		}
	}
	if err := sc.Err(); err != nil {
		stats.AddWarning(fmt.Sprintf("record stream read stopped early: %v", err))
	}
	if conv.SessionID == "" {
		return nil, stats, fmt.Errorf("codex rollout has no session_meta record")
	}
	// thread_settings may arrive after early messages; backfill so every row
	// carries the session model uniformly.
	for i := range conv.Messages {
		if conv.Messages[i].Model == "" {
			conv.Messages[i].Model = conv.Model
		}
	}
	conv.Title = codexTitle(conv.Messages)
	stats.Messages = len(conv.Messages)
	return conv, stats, nil
}

func codexItemToMessage(item codexItem, ts string, ordinal int, opts ParseOptions, stats *ParseStats) (Message, bool) {
	fallbackID := fmt.Sprintf("seq-%d", ordinal)
	msg := Message{ExternalID: firstNonEmpty(item.ID, fallbackID), Ordinal: ordinal, CreatedAt: ts}

	switch item.Type {
	case "message":
		msg.Role = item.Role
		msg.Content = joinCodexText(item.Content)
		switch {
		case item.Role == "developer" && hasSystemPrefix(msg.Content):
			msg.Kind = KindSystem
		default:
			msg.Kind = KindText
		}
		if msg.Content == "" {
			stats.Skipped++
			return msg, false
		}
	case "reasoning":
		if !opts.IncludeReasoning {
			stats.Skipped++
			return msg, false
		}
		summary := joinCodexText(item.Summary)
		if summary == "" {
			// Encrypted-only reasoning carries no recoverable text.
			stats.Skipped++
			return msg, false
		}
		msg.Role = "assistant"
		msg.Kind = KindReasoning
		msg.Content = summary
	case "custom_tool_call", "function_call":
		msg.Role = "assistant"
		msg.Kind = KindToolCall
		msg.Content = item.Name + "\n" + firstNonEmpty(item.Input, rawAsString(item.Arguments))
		msg.Metadata = map[string]string{"tool_name": item.Name, "call_id": item.CallID}
	case "custom_tool_call_output", "function_call_output":
		msg.Role = "tool"
		msg.Kind = KindToolResult
		msg.Content = joinCodexOutput(item.Output)
		msg.Metadata = map[string]string{"call_id": item.CallID}
	case "agent_message":
		// Inter-agent communication (subagent spawns). Encrypted parts carry
		// no recoverable text; the plaintext headers survive.
		msg.Role = "agent"
		msg.Kind = KindText
		msg.Content = joinCodexText(item.Content)
		msg.Metadata = map[string]string{"author": item.Author, "recipient": item.Recipient}
		if msg.Content == "" {
			stats.Skipped++
			return msg, false
		}
	default:
		stats.Skipped++
		return msg, false
	}
	msg.Content = truncateContent(msg.Content, opts.MaxContentBytes)
	return msg, true
}

func codexEventModel(payload json.RawMessage) string {
	var ev struct {
		Type          string `json:"type"`
		ThreadSetting struct {
			Model string `json:"model"`
		} `json:"thread_settings"`
	}
	if err := json.Unmarshal(payload, &ev); err != nil || ev.Type != "thread_settings_applied" {
		return ""
	}
	return ev.ThreadSetting.Model
}

// joinCodexText concatenates text content parts; image parts become a
// placeholder so the message is still visible in prose.
func joinCodexText(parts []codexContent) string {
	var b strings.Builder
	for _, p := range parts {
		switch p.Type {
		case "input_text", "output_text", "summary_text", "text":
			b.WriteString(p.Text)
		case "input_image", "image":
			b.WriteString("[image]")
		}
	}
	return b.String()
}

func hasSystemPrefix(content string) bool {
	for _, prefix := range strings.Split(systemPrefixes, ",") {
		if strings.HasPrefix(content, prefix) {
			return true
		}
	}
	return false
}

func codexTitle(messages []Message) string {
	for _, m := range messages {
		if m.Role == "user" && m.Kind == KindText && m.Content != "" {
			line := strings.TrimSpace(strings.SplitN(m.Content, "\n", 2)[0])
			if len(line) > defaultTitleLen {
				line = line[:defaultTitleLen]
			}
			return line
		}
	}
	return ""
}

func truncateContent(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("\n…[truncated %d bytes]", len(s)-max)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// codexRecord is the rollout envelope; codexItem is the response_item union.
type codexRecord struct {
	Timestamp string          `json:"timestamp"`
	Ordinal   int             `json:"ordinal"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type codexSessionMeta struct {
	SessionID      string `json:"session_id"`
	ID             string `json:"id"`
	Timestamp      string `json:"timestamp"`
	Cwd            string `json:"cwd"`
	Originator     string `json:"originator"`
	CLIVersion     string `json:"cli_version"`
	Source         any    `json:"source"`
	ThreadSource   string `json:"thread_source"`
	AgentNickname  string `json:"agent_nickname"`
	ParentThreadID string `json:"parent_thread_id"`
}

type codexContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type codexItem struct {
	Type             string          `json:"type"`
	ID               string          `json:"id"`
	CallID           string          `json:"call_id"`
	Name             string          `json:"name"`
	Author           string          `json:"author"`
	Recipient        string          `json:"recipient"`
	Input            string          `json:"input"`
	Arguments        json.RawMessage `json:"arguments"`
	Role             string          `json:"role"`
	Content          []codexContent  `json:"content"`
	Summary          []codexContent  `json:"summary"`
	Output           json.RawMessage `json:"output"`
	EncryptedContent string          `json:"encrypted_content"`
}

// joinCodexOutput handles both observed shapes: a content-part array (recent
// rollouts) and a plain string (older ones).
func joinCodexOutput(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var parts []codexContent
	if err := json.Unmarshal(raw, &parts); err == nil {
		return joinCodexText(parts)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return ""
}

// rawAsString returns a raw JSON field as text: a JSON string stays itself,
// structured values are compacted back to JSON. function_call.arguments
// appears both as an encoded string and as a nested object.
func rawAsString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err == nil {
		return buf.String()
	}
	return ""
}
