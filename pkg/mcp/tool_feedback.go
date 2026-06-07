package mcp

// Feedback + session-context tools.
// Extracted from deps.go during F-4 wave 3j-split.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
)

// feedbackQueryLogMaxLen caps how much of the query text we echo back in
// ToolAddFeedback's success message. The DB row still carries the full
// query; this limit only affects the human-readable response.
const feedbackQueryLogMaxLen = 50

// ToolAddFeedback records user feedback on a search result.
//
// Validation: query required, rating must be 1..5, DB must be configured
// (unlike other tools, feedback has no useful degraded mode — if the
// feedback table can't be written, the caller should know).
//
// The SQL insert is fire-and-forget: error return from ExecContext is
// ignored to match pre-refactor behavior, since the duplicate-id
// collision can be a benign retry.
func ToolAddFeedback(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	query, _ := args["query"].(string)
	if query == "" {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: 'query' required"}},
			IsError: true,
		}
	}
	rating := 0
	if r, ok := args["rating"].(float64); ok {
		rating = int(r)
	}
	if rating < 1 || rating > 5 {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: 'rating' must be 1-5"}},
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

	resultID, _ := args["result_id"].(string)
	collection, _ := args["collection"].(string)
	searchType, _ := args["search_type"].(string)
	comment, _ := args["comment"].(string)
	userID := ""
	if uid, ok := ctx.Value(UserIDKey).(string); ok {
		userID = uid
	}

	id := uuid.New().String()
	db.ExecContext(ctx, deps.Q(`
		INSERT INTO search_feedback (id, query, result_id, collection, search_type, rating, comment, user_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`), id, query, resultID, collection, searchType, rating, comment, userID)

	return ToolResult{Content: []Content{{
		Type: "text",
		Text: fmt.Sprintf("Feedback saved: rating=%d for query '%s'", rating, Truncate(query, feedbackQueryLogMaxLen)),
	}}}
}

// ToolGetFeedbackStats aggregates the search_feedback table.
//
// Optional "collection" filter scopes to one collection. Nil DB is not
// an error here — returns {"total":0} to match pre-refactor behavior
// (some deployments have MCP without feedback logging).
func ToolGetFeedbackStats(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	db := deps.DB()
	if db == nil {
		return ToolResult{Content: []Content{{Type: "text", Text: `{"total":0}`}}}
	}
	collection, _ := args["collection"].(string)

	var total int
	var avgRating float64
	var worstQuery string

	if collection != "" {
		db.QueryRowContext(ctx,
			deps.Q(`SELECT COUNT(*), COALESCE(AVG(rating),0) FROM search_feedback WHERE collection = $1`),
			collection).Scan(&total, &avgRating)
		db.QueryRowContext(ctx,
			deps.Q(`SELECT COALESCE(query,'') FROM search_feedback WHERE collection = $1 ORDER BY rating ASC LIMIT 1`),
			collection).Scan(&worstQuery)
	} else {
		db.QueryRowContext(ctx,
			deps.Q(`SELECT COUNT(*), COALESCE(AVG(rating),0) FROM search_feedback`)).Scan(&total, &avgRating)
		db.QueryRowContext(ctx,
			deps.Q(`SELECT COALESCE(query,'') FROM search_feedback ORDER BY rating ASC LIMIT 1`)).Scan(&worstQuery)
	}

	out, _ := json.MarshalIndent(map[string]any{
		"total":       total,
		"avg_rating":  avgRating,
		"worst_query": worstQuery,
		"collection":  collection,
	}, "", "  ")
	return ToolResult{Content: []Content{{Type: "text", Text: string(out)}}}
}

// ToolSetContext binds a default collection to the caller's session.
// Subsequent tool calls that omit the "collection" argument will use
// this default (see ResolveCollection).
//
// Unknown collection names are accepted with a "will be created when
// data is added" note — this lets clients pre-set a context before the
// first write. A nil session is treated as a client error (initialize
// was skipped).
func ToolSetContext(sess *Session, deps Deps, args map[string]any) ToolResult {
	collection, _ := args["collection"].(string)
	if collection == "" {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: 'collection' required"}},
			IsError: true,
		}
	}
	if sess == nil {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: no active session (send initialize first)"}},
			IsError: true,
		}
	}

	exists := deps.CollectionExists(collection)
	sess.DefaultCollection = collection

	status := "set"
	if !exists {
		status = "set (collection not yet created — will be used when data is added)"
	}
	return ToolResult{Content: []Content{{
		Type: "text",
		Text: fmt.Sprintf("Context %s: default collection = '%s'", status, collection),
	}}}
}
