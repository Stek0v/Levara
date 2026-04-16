package mcp

// Per-agent diary tools: diary_write, diary_read.
// Extracted from deps.go during F-4 wave 3j-split.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// diaryReadLimit caps the number of entries ToolDiaryRead returns.
const diaryReadLimit = 100

// ToolDiaryWrite inserts or updates a diary memory scoped to a
// subagent (owner_id = "agent:<agent>"). Upsert semantics: same key
// under the same owner replaces value + collection + updated_at.
//
// The original pre-refactor SQL reused placeholders ($3, $5, $7 in
// both VALUES and DO UPDATE SET). The rewrite here uses unique
// placeholders ($8, $9, $10) instead, which means we don't need
// QArgs on the Deps interface — consistent with wave 3f's pin/unpin
// simplification. Behavior is byte-for-byte identical.
func ToolDiaryWrite(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	db := deps.DB()
	if db == nil {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: database not configured"}},
			IsError: true,
		}
	}
	agent, _ := args["agent"].(string)
	key, _ := args["key"].(string)
	value, _ := args["value"].(string)
	if agent == "" || key == "" || value == "" {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: 'agent', 'key', 'value' required"}},
			IsError: true,
		}
	}
	collectionName, _ := args["collection"].(string)
	owner := DiaryOwner(agent)
	id := uuid.New().String()
	now := time.Now().UTC().Format(time.RFC3339)

	// value / collectionName / now repeat verbatim in the args slice
	// ($8/$9/$10) to keep the DO UPDATE SET clause readable without
	// QArgs placeholder-reuse machinery.
	_, err := db.ExecContext(ctx, deps.Q(`
		INSERT INTO memories (id, key, value, type, owner_id, collection_name, room, hall, is_pinned, pin_priority, created_at, updated_at)
		VALUES ($1, $2, $3, 'diary', $4, $5, '', '', 0, 0, $6, $7)
		ON CONFLICT(key, owner_id) DO UPDATE SET value = $8, collection_name = $9, updated_at = $10
	`),
		id, key, value, owner, collectionName, now, now,
		value, collectionName, now)
	if err != nil {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: " + err.Error()}},
			IsError: true,
		}
	}
	return ToolResult{Content: []Content{{
		Type: "text",
		Text: fmt.Sprintf("Diary[%s] wrote %s", agent, key),
	}}}
}

// ToolDiaryRead returns diary entries for a subagent, optionally
// filtered by query substring (matches key OR value) and/or
// collection. Nil DB returns "[]" rather than an error.
func ToolDiaryRead(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	db := deps.DB()
	if db == nil {
		return ToolResult{Content: []Content{{Type: "text", Text: "[]"}}}
	}
	agent, _ := args["agent"].(string)
	if agent == "" {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: 'agent' required"}},
			IsError: true,
		}
	}
	owner := DiaryOwner(agent)
	queryStr, _ := args["query"].(string)
	collectionName, _ := args["collection"].(string)

	var conds []string
	var qargs []any
	pos := 1
	conds = append(conds, fmt.Sprintf("owner_id = $%d", pos))
	qargs = append(qargs, owner)
	pos++
	if queryStr != "" {
		pat := "%" + queryStr + "%"
		conds = append(conds, fmt.Sprintf("(key LIKE $%d OR value LIKE $%d)", pos, pos+1))
		qargs = append(qargs, pat, pat)
		pos += 2
	}
	if collectionName != "" {
		conds = append(conds, fmt.Sprintf("collection_name = $%d", pos))
		qargs = append(qargs, collectionName)
		pos++
	}
	sqlStr := fmt.Sprintf(`
		SELECT key, value, created_at, updated_at FROM memories
		WHERE %s ORDER BY updated_at DESC LIMIT %d
	`, strings.Join(conds, " AND "), diaryReadLimit)

	rows, err := db.QueryContext(ctx, deps.Q(sqlStr), qargs...)
	if err != nil {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: " + err.Error()}},
			IsError: true,
		}
	}
	defer rows.Close()

	var entries []map[string]any
	for rows.Next() {
		var k, v, ca, ua string
		if err := rows.Scan(&k, &v, &ca, &ua); err != nil {
			continue
		}
		entries = append(entries, map[string]any{
			"key": k, "value": v, "created_at": ca, "updated_at": ua,
		})
	}
	if entries == nil {
		return ToolResult{Content: []Content{{
			Type: "text",
			Text: fmt.Sprintf("Diary[%s] is empty", agent),
		}}}
	}
	out, _ := json.MarshalIndent(entries, "", "  ")
	return ToolResult{Content: []Content{{Type: "text", Text: string(out)}}}
}
