package chatimport

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// ParseClaudeCodeTranscript parses one Claude Code session transcript
// (~/.claude/projects/<project-slug>/<session-uuid>.jsonl) into a Conversation.
//
// Every line is a wrapper record; conversation content lives in user and
// assistant records whose `message` field is an Anthropic API message.
// Each content block becomes its own normalized message so reasoning, tool
// calls and prose stay separately addressable. Unknown record types
// (attachment, queue-operation, last-prompt, …) and malformed lines are
// counted as skipped — the import must not abort.
func ParseClaudeCodeTranscript(r io.Reader, opts ParseOptions) (*Conversation, ParseStats, error) {
	if opts.MaxContentBytes <= 0 {
		opts = DefaultParseOptions()
	}
	stats := ParseStats{}
	conv := &Conversation{Platform: PlatformClaudeCode}

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 32*1024*1024)
	ordinal := 0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec claudeCodeRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			stats.Skipped++
			stats.AddWarning(fmt.Sprintf("skipped malformed JSON record: %v", err))
			continue
		}
		if conv.SessionID == "" && rec.SessionID != "" {
			conv.SessionID = rec.SessionID
		}
		if conv.CreatedAt == "" && rec.Timestamp != "" {
			conv.CreatedAt = rec.Timestamp
		}
		if conv.Cwd == "" && rec.Cwd != "" {
			conv.Cwd = rec.Cwd
		}
		if rec.GitBranch != "" || rec.Version != "" {
			setDefault(&conv.Metadata, "git_branch", rec.GitBranch)
			setDefault(&conv.Metadata, "cli_version", rec.Version)
		}
		sidechain := rec.IsSidechain

		switch rec.Type {
		case "summary":
			// Claude Code titles arrive as summary records; first wins.
			if conv.Title == "" && rec.Summary != "" {
				conv.Title = rec.Summary
			}
			stats.Skipped++
		case "user", "assistant":
			msgs := claudeCodeMessage(rec, opts, &stats)
			for _, m := range msgs {
				m.Ordinal = ordinal
				m.Model = firstNonEmpty(m.Model, rec.Message.Model, conv.Model)
				if m.Model != "" && conv.Model == "" {
					conv.Model = m.Model
				}
				if sidechain {
					setMeta(&m, "sidechain", "true")
				}
				conv.Messages = append(conv.Messages, m)
				ordinal++
			}
		case "system":
			text := claudeCodeText(rec.Content)
			if text != "" {
				conv.Messages = append(conv.Messages, Message{
					ExternalID: rec.UUID, Ordinal: ordinal, Role: "system", Kind: KindSystem,
					Content: truncateContent(text, opts.MaxContentBytes), CreatedAt: rec.Timestamp,
					Model: conv.Model,
				})
				ordinal++
			} else {
				stats.Skipped++
			}
		default:
			stats.Skipped++
		}
	}
	if err := sc.Err(); err != nil {
		stats.AddWarning(fmt.Sprintf("record stream read stopped early: %v", err))
	}
	if conv.SessionID == "" {
		return nil, stats, fmt.Errorf("claude-code transcript has no sessionId")
	}
	if conv.Title == "" {
		conv.Title = codexTitle(conv.Messages)
	}
	stats.Messages = len(conv.Messages)
	return conv, stats, nil
}

// claudeCodeMessage expands one wrapper record into zero or more normalized
// messages. Block index keeps external ids unique when a record carries
// several blocks.
func claudeCodeMessage(rec claudeCodeRecord, opts ParseOptions, stats *ParseStats) []Message {
	var out []Message
	blockIdx := 0
	emit := func(m Message) {
		m.ExternalID = fmt.Sprintf("%s#%d", rec.UUID, blockIdx)
		out = append(out, m)
		blockIdx++
	}

	content := claudeCodeAny(rec.Message.Content)
	role := rec.Type // user | assistant

	switch v := content.(type) {
	case string:
		if v == "" {
			stats.Skipped++
			return out
		}
		emit(Message{Role: role, Kind: KindText, Content: truncateContent(v, opts.MaxContentBytes), CreatedAt: rec.Timestamp})
		return out
	case []any:
		// Block-by-block handling below.
		_ = v
	default:
		stats.Skipped++
		return out
	}

	blocks, _ := content.([]any)
	for _, raw := range blocks {
		blk, ok := raw.(map[string]any)
		if !ok {
			stats.Skipped++
			continue
		}
		blkType, _ := blk["type"].(string)
		ts, _ := blk["text"].(string)
		switch blkType {
		case "text":
			if ts == "" {
				stats.Skipped++
				continue
			}
			emit(Message{Role: role, Kind: KindText, Content: truncateContent(ts, opts.MaxContentBytes), CreatedAt: rec.Timestamp})
		case "thinking":
			if !opts.IncludeReasoning {
				stats.Skipped++
				continue
			}
			th, _ := blk["thinking"].(string)
			if th == "" {
				stats.Skipped++
				continue
			}
			emit(Message{Role: "assistant", Kind: KindReasoning, Content: truncateContent(th, opts.MaxContentBytes), CreatedAt: rec.Timestamp})
		case "redacted_thinking":
			stats.Skipped++
		case "tool_use":
			name, _ := blk["name"].(string)
			id, _ := blk["id"].(string)
			input, err := json.Marshal(blk["input"])
			if err != nil {
				input = []byte("{}")
			}
			emit(Message{
				Role: "assistant", Kind: KindToolCall, CreatedAt: rec.Timestamp,
				Content:  truncateContent(name+"\n"+string(input), opts.MaxContentBytes),
				Metadata: map[string]string{"tool_name": name, "call_id": id},
			})
		case "tool_result":
			toolUseID, _ := blk["tool_use_id"].(string)
			msg := Message{Role: "tool", Kind: KindToolResult, CreatedAt: rec.Timestamp,
				Metadata: map[string]string{"call_id": toolUseID}}
			msg.Content = claudeCodeAnyText(blk["content"])
			if isErr, _ := blk["is_error"].(bool); isErr {
				msg.Metadata["is_error"] = "true"
			}
			if msg.Content == "" {
				stats.Skipped++
				continue
			}
			msg.Content = truncateContent(msg.Content, opts.MaxContentBytes)
			emit(msg)
		case "image":
			emit(Message{Role: role, Kind: KindText, Content: "[image]", CreatedAt: rec.Timestamp})
		default:
			stats.Skipped++
		}
	}
	return out
}

// claudeCodeAny decodes a raw JSON field that may be a string or a block
// array. Returns string, []any, or nil.
func claudeCodeAny(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var arr []any
	if err := json.Unmarshal(raw, &arr); err == nil {
		return arr
	}
	return nil
}

// claudeCodeText extracts prose from a raw field shaped as a string or a
// content-block array (system records use both shapes across versions).
func claudeCodeText(raw json.RawMessage) string {
	switch v := claudeCodeAny(raw).(type) {
	case string:
		return v
	case []any:
		var b strings.Builder
		for _, part := range v {
			if pm, ok := part.(map[string]any); ok {
				if t, ok := pm["text"].(string); ok {
					b.WriteString(t)
				}
			}
		}
		return b.String()
	}
	return ""
}

// claudeCodeAnyText is claudeCodeText for already-decoded values.
func claudeCodeAnyText(v any) string {
	switch c := v.(type) {
	case string:
		return c
	case []any:
		var b strings.Builder
		for _, part := range c {
			if pm, ok := part.(map[string]any); ok {
				if t, ok := pm["text"].(string); ok {
					b.WriteString(t)
				}
			}
		}
		return b.String()
	}
	return ""
}

func setMeta(m *Message, k, v string) {
	if m.Metadata == nil {
		m.Metadata = map[string]string{}
	}
	m.Metadata[k] = v
}

func setDefault(m *map[string]string, k, v string) {
	if v == "" {
		return
	}
	if *m == nil {
		*m = map[string]string{}
	}
	if _, exists := (*m)[k]; !exists {
		(*m)[k] = v
	}
}

// claudeCodeRecord is the transcript envelope. Message is the Anthropic API
// message; Content on the envelope belongs to system records.
type claudeCodeRecord struct {
	Type        string          `json:"type"`
	UUID        string          `json:"uuid"`
	ParentUUID  string          `json:"parentUuid"`
	SessionID   string          `json:"sessionId"`
	Timestamp   string          `json:"timestamp"`
	Cwd         string          `json:"cwd"`
	Version     string          `json:"version"`
	GitBranch   string          `json:"gitBranch"`
	IsSidechain bool            `json:"isSidechain"`
	Summary     string          `json:"summary"`
	Content     json.RawMessage `json:"content"`
	Message     claudeCodeMsg   `json:"message"`
}

type claudeCodeMsg struct {
	Role    string          `json:"role"`
	Model   string          `json:"model"`
	Content json.RawMessage `json:"content"`
}
