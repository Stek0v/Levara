package chatimport

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	// Read-only access to Cursor's state.vscdb; the server already ships
	// this driver and the CLI inherits it via this package.
	_ "github.com/ncruces/go-sqlite3/driver"
)

// ParseCursorChats reads conversations from a Cursor state.vscdb SQLite
// database (globalStorage or workspaceStorage). The format is undocumented
// and version-dependent — this adapter is experimental and fail-soft:
// unknown bubbles are skipped with counters, never fatal.
//
// Layout: cursorDiskKV rows keyed
//   - "composerData:<composerId>"  → conversation header (name, createdAt ms)
//   - "bubbleId:<composerId>:<bubbleId>" → one message bubble
//     (type 1 = user, type 2 = assistant; text, allThinkingBlocks,
//     toolFormerData for tool activity)
//
// Bubble rows carry no reliable timestamp; ordering follows insertion order
// (rowid), which matches chronological arrival.
func ParseCursorChats(dbPath string, opts ParseOptions) ([]Conversation, ParseStats, error) {
	if opts.MaxContentBytes <= 0 {
		opts = DefaultParseOptions()
	}
	stats := ParseStats{}
	db, err := sql.Open("sqlite3", "file:"+dbPath+"?mode=ro&immutable=1")
	if err != nil {
		return nil, stats, fmt.Errorf("cursor: open %s: %w", dbPath, err)
	}
	defer db.Close()

	// Workspace state.vscdb files exist for every workspace; most predate
	// chat storage and have no cursorDiskKV table. They are empty sources,
	// not failures.
	var table string
	if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='cursorDiskKV'`).Scan(&table); err != nil {
		if err == sql.ErrNoRows {
			return nil, stats, nil
		}
		return nil, stats, fmt.Errorf("cursor: inspect %s: %w", dbPath, err)
	}

	byComposer := map[string]*Conversation{}
	var order []string

	headerRows, err := db.Query(`SELECT value FROM cursorDiskKV WHERE key LIKE 'composerData:%' ORDER BY rowid`)
	if err != nil {
		return nil, stats, fmt.Errorf("cursor: query composers: %w", err)
	}
	defer headerRows.Close()
	for headerRows.Next() {
		var value string
		if err := headerRows.Scan(&value); err != nil {
			stats.AddWarning("cursor: unreadable composer header")
			continue
		}
		var h struct {
			ComposerID string `json:"composerId"`
			Name       string `json:"name"`
			CreatedAt  int64  `json:"createdAt"`
			Cwd        string `json:"currentDir"`
		}
		if err := json.Unmarshal([]byte(value), &h); err != nil || h.ComposerID == "" {
			stats.Skipped++
			continue
		}
		conv := &Conversation{Platform: PlatformCursor, SessionID: h.ComposerID, Title: h.Name, Cwd: h.Cwd}
		if h.CreatedAt > 0 {
			conv.CreatedAt = time.UnixMilli(h.CreatedAt).UTC().Format(time.RFC3339)
		}
		byComposer[h.ComposerID] = conv
		order = append(order, h.ComposerID)
	}
	if err := headerRows.Err(); err != nil {
		return nil, stats, fmt.Errorf("cursor: composers read: %w", err)
	}

	ordinals := map[string]int{}
	bubbleRows, err := db.Query(`SELECT key, value FROM cursorDiskKV WHERE key LIKE 'bubbleId:%' ORDER BY rowid`)
	if err != nil {
		return nil, stats, fmt.Errorf("cursor: query bubbles: %w", err)
	}
	defer bubbleRows.Close()
	for bubbleRows.Next() {
		var key, value string
		if err := bubbleRows.Scan(&key, &value); err != nil {
			stats.Skipped++
			continue
		}
		// bubbleId:<composerId>:<bubbleId>
		parts := strings.SplitN(key, ":", 3)
		if len(parts) != 3 {
			stats.Skipped++
			continue
		}
		conv, ok := byComposer[parts[1]]
		if !ok {
			// Bubble without a header — Cursor keeps some orphans.
			stats.Skipped++
			continue
		}
		var b struct {
			BubbleID     string          `json:"bubbleId"`
			Type         int             `json:"type"`
			Text         string          `json:"text"`
			Thinking     json.RawMessage `json:"allThinkingBlocks"`
			ToolFormer   json.RawMessage `json:"toolFormerData"`
			ServerBubble string          `json:"serverBubbleId"`
		}
		if err := json.Unmarshal([]byte(value), &b); err != nil {
			stats.Skipped++
			continue
		}
		externalID := firstNonEmpty(b.BubbleID, parts[2])
		base := Message{ExternalID: externalID, CreatedAt: conv.CreatedAt}
		if b.Type == 1 {
			base.Role, base.Kind = "user", KindText
		} else {
			base.Role, base.Kind = "assistant", KindText
		}
		if strings.TrimSpace(b.Text) != "" {
			base.Content = truncateContent(b.Text, opts.MaxContentBytes)
			base.Ordinal = ordinals[conv.SessionID]
			conv.Messages = append(conv.Messages, base)
			ordinals[conv.SessionID]++
		}
		// Thinking blocks carry their own text when present. Each block gets
		// an indexed suffix — they share the bubble's id otherwise and the
		// uniqueness constraint would silently drop all but the first.
		if opts.IncludeReasoning {
			for ti, th := range decodeThinkingBlocks(b.Thinking, opts) {
				m := Message{
					ExternalID: fmt.Sprintf("%s#t%d", externalID, ti),
					Role:       "assistant", Kind: KindReasoning,
					Content: th, CreatedAt: conv.CreatedAt,
					Ordinal: ordinals[conv.SessionID],
				}
				conv.Messages = append(conv.Messages, m)
				ordinals[conv.SessionID]++
			}
		} else if len(b.Thinking) > 4 {
			stats.Skipped++
		}
		if strings.TrimSpace(b.Text) == "" && len(b.Thinking) <= 4 {
			// Context/tool-only bubbles (toolFormerData without prose) stay
			// out of the experimental adapter.
			stats.Skipped++
		}
	}
	if err := bubbleRows.Err(); err != nil {
		return nil, stats, fmt.Errorf("cursor: bubbles read: %w", err)
	}

	convs := make([]Conversation, 0, len(order))
	for _, id := range order {
		conv := byComposer[id]
		if len(conv.Messages) == 0 {
			stats.Skipped++
			continue
		}
		if conv.Title == "" {
			conv.Title = codexTitle(conv.Messages)
		}
		convs = append(convs, *conv)
	}
	stats.Messages = 0
	for _, c := range convs {
		stats.Messages += len(c.Messages)
	}
	return convs, stats, nil
}

// decodeThinkingBlocks extracts plaintext thinking from
// allThinkingBlocks — the field is present on every bubble but observed
// empty locally, so the shape is handled defensively.
func decodeThinkingBlocks(raw json.RawMessage, opts ParseOptions) []string {
	if len(raw) == 0 {
		return nil
	}
	var blocks []map[string]any
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil
	}
	var out []string
	for _, blk := range blocks {
		text, _ := blk["text"].(string)
		if strings.TrimSpace(text) == "" {
			continue
		}
		out = append(out, truncateContent(text, opts.MaxContentBytes))
	}
	return out
}
