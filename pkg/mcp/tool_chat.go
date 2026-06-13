package mcp

// Chat-history tools: save_chat, recall_chat, search_chats.
// Extracted from deps.go during F-4 wave 3j-split.

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

const (
	// recallChatLimit caps messages returned by ToolRecallChat.
	recallChatLimit = 100
	// searchChatsLimit caps results returned by ToolSearchChats.
	searchChatsLimit = 20
)

// ToolSaveChat persists a batch of chat messages to the interactions
// table, one row per message. The role → column mapping preserves
// pre-refactor semantics: user messages go into `query`, everything
// else (assistant, system, tool) goes into `response`.
//
// Nil DB is a hard error — chat storage is the tool's only purpose.
// Individual message inserts are fire-and-forget; the saved count
// reflects non-empty role+content pairs only.
func ToolSaveChat(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	sessionID, _ := args["session_id"].(string)
	if sessionID == "" {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: 'session_id' required"}},
			IsError: true,
		}
	}
	messagesRaw, ok := args["messages"].([]any)
	if !ok || len(messagesRaw) == 0 {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: 'messages' array required"}},
			IsError: true,
		}
	}
	db := deps.DB()
	if db == nil {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: database not configured"}},
			IsError: true,
		}
	}

	saved := 0
	insertSQL := deps.Q(`
		INSERT INTO interactions (id, session_id, user_id, query, response, search_type, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`)
	for _, msgRaw := range messagesRaw {
		msg, ok := msgRaw.(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		content, _ := msg["content"].(string)
		if role == "" || content == "" {
			continue
		}

		id := uuid.New().String()
		now := time.Now().UTC()

		query, response := "", ""
		if role == "user" {
			query = content
		} else {
			response = content
		}

		db.ExecContext(ctx, insertSQL, id, sessionID, "", query, response, "chat", now)
		saved++
	}

	return statusResult(true, fmt.Sprintf("Saved %d messages to session %s", saved, sessionID))
}

// ToolRecallChat returns the first recallChatLimit messages in the
// session's interaction history, ordered by created_at ASC (oldest
// first — matches pre-refactor, preserves chronological reading).
//
// Each DB row emits up to two ToolResult items: one for the query
// (role=user) and one for the response (role=assistant). Empty
// columns are skipped, so a user-only message produces one item.
//
// Nil DB returns "[]" rather than an error — matches the read-only
// contract other list tools use.
func ToolRecallChat(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	sessionID, _ := args["session_id"].(string)
	if sessionID == "" {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: 'session_id' required"}},
			IsError: true,
		}
	}
	db := deps.DB()
	if db == nil {
		return jsonResult(map[string]any{"session_id": sessionID, "messages": []any{}})
	}

	rows, err := db.QueryContext(ctx, deps.Q(fmt.Sprintf(`
		SELECT id, query, response, created_at FROM interactions
		WHERE session_id = $1 ORDER BY created_at LIMIT %d
	`, recallChatLimit)), sessionID)
	if err != nil {
		return jsonResult(map[string]any{"session_id": sessionID, "messages": []any{}})
	}
	defer rows.Close()

	var messages []map[string]any
	for rows.Next() {
		var id, query, response, ca string
		if err := rows.Scan(&id, &query, &response, &ca); err != nil {
			continue
		}
		if query != "" {
			messages = append(messages, map[string]any{
				"role": "user", "content": query, "created_at": ca,
			})
		}
		if response != "" {
			messages = append(messages, map[string]any{
				"role": "assistant", "content": response, "created_at": ca,
			})
		}
	}

	if len(messages) == 0 {
		return jsonResult(map[string]any{
			"session_id": sessionID,
			"messages":   []any{},
			"message":    "No messages found for session.",
		})
	}
	return jsonResult(map[string]any{"session_id": sessionID, "messages": messages})
}

// ToolSearchChats does a LIKE-based substring search across the
// query + response columns of interactions. Case-sensitivity follows
// the active DB dialect — Postgres ILIKE is not used here; callers
// who need case-insensitive search should pass the query in the
// expected casing or pre-filter.
//
// Results are ordered by created_at DESC (newest first) and capped
// at searchChatsLimit rows.
func ToolSearchChats(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	query, _ := args["query"].(string)
	if query == "" {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: 'query' required"}},
			IsError: true,
		}
	}
	db := deps.DB()
	if db == nil {
		return jsonResult(map[string]any{"results": []any{}})
	}

	pattern := "%" + query + "%"
	rows, err := db.QueryContext(ctx, deps.Q(fmt.Sprintf(`
		SELECT id, session_id, query, response, created_at FROM interactions
		WHERE query LIKE $1 OR response LIKE $2
		ORDER BY created_at DESC LIMIT %d
	`, searchChatsLimit)), pattern, pattern)
	if err != nil {
		return jsonResult(map[string]any{"results": []any{}})
	}
	defer rows.Close()

	var results []map[string]any
	for rows.Next() {
		var id, sid, q, r, ca string
		if err := rows.Scan(&id, &sid, &q, &r, &ca); err != nil {
			continue
		}
		results = append(results, map[string]any{
			"id": id, "session_id": sid, "query": q, "response": r, "created_at": ca,
			"snippet": firstNonEmpty(q, r), "role": "chat", "score": 1.0,
		})
	}

	if len(results) == 0 {
		return jsonResult(map[string]any{
			"results": []any{},
			"message": "No matching chats found.",
		})
	}
	return jsonResult(map[string]any{"results": results})
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
