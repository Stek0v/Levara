package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const memoryMarkdownDigestMaxKeys = 100

type memoryMarkdownDigestRow struct {
	Key, Value, Room, Hall, Verification, SourceTaskID, ReceiptJSON, SupersedesID, UpdatedAt string
}

// ToolMemoryMarkdownDigest renders selected, verified decisions and discoveries
// as Markdown. It is an export view only: Levara remains the source of truth.
func ToolMemoryMarkdownDigest(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	collection, _ := args["collection"].(string)
	if strings.TrimSpace(collection) == "" {
		return toolError("'collection' required")
	}
	memoryIDs, err := memoryMarkdownDigestIDs(args["memory_ids"])
	if err != nil {
		return toolError(err.Error())
	}
	if deps.DB() == nil {
		return jsonResult(memoryMarkdownDigestResult(collection, nil))
	}

	placeholders := make([]string, len(memoryIDs))
	params := make([]any, 0, len(memoryIDs)+2)
	params = append(params, collection, extractOwnerID(ctx))
	for i, memoryID := range memoryIDs {
		placeholders[i] = fmt.Sprintf("$%d", i+3)
		params = append(params, memoryID)
	}
	query := fmt.Sprintf(`SELECT key,value,room,hall,verification_status,source_task_id,source_receipt_ids,supersedes_memory_id,updated_at
		FROM memories WHERE collection_name=$1 AND (owner_id=$2 OR owner_id='') AND superseded_by=''
		AND verification_status='verified' AND hall IN ('decision','discovery') AND id IN (%s)
		ORDER BY updated_at DESC,key ASC`, strings.Join(placeholders, ","))
	rows, err := deps.DB().QueryContext(ctx, deps.Q(query), params...)
	if err != nil {
		return toolError(err.Error())
	}
	defer rows.Close()
	memories := make([]memoryMarkdownDigestRow, 0, len(memoryIDs))
	for rows.Next() {
		var memory memoryMarkdownDigestRow
		if err := rows.Scan(&memory.Key, &memory.Value, &memory.Room, &memory.Hall, &memory.Verification, &memory.SourceTaskID, &memory.ReceiptJSON, &memory.SupersedesID, &memory.UpdatedAt); err != nil {
			return toolError(err.Error())
		}
		memories = append(memories, memory)
	}
	if err := rows.Err(); err != nil {
		return toolError(err.Error())
	}
	return jsonResult(memoryMarkdownDigestResult(collection, memories))
}

func memoryMarkdownDigestIDs(raw any) ([]string, error) {
	items, ok := raw.([]any)
	if !ok || len(items) == 0 || len(items) > memoryMarkdownDigestMaxKeys {
		return nil, fmt.Errorf("'memory_ids' must contain 1-%d memory IDs", memoryMarkdownDigestMaxKeys)
	}
	seen := make(map[string]bool, len(items))
	memoryIDs := make([]string, 0, len(items))
	for _, item := range items {
		memoryID, ok := item.(string)
		memoryID = strings.TrimSpace(memoryID)
		if !ok || memoryID == "" {
			return nil, fmt.Errorf("'memory_ids' must contain non-empty strings")
		}
		if !seen[memoryID] {
			seen[memoryID] = true
			memoryIDs = append(memoryIDs, memoryID)
		}
	}
	return memoryIDs, nil
}

func memoryMarkdownDigestResult(collection string, memories []memoryMarkdownDigestRow) map[string]any {
	return map[string]any{
		"collection": collection, "generated_at": time.Now().UTC().Format(time.RFC3339), "count": len(memories),
		"markdown": memoryMarkdownDigestMarkdown(collection, memories),
	}
}

func memoryMarkdownDigestMarkdown(collection string, memories []memoryMarkdownDigestRow) string {
	var out strings.Builder
	fmt.Fprintf(&out, "# Levara Memory Digest\n\n> Read-only export from Levara. Levara remains the source of truth.\n\nCollection: %s\n", memoryMarkdownDigestText(collection))
	if len(memories) == 0 {
		out.WriteString("\nNo verified decisions or discoveries matched the selected memory IDs.\n")
		return out.String()
	}
	for _, memory := range memories {
		fmt.Fprintf(&out, "\n## %s\n\n- Kind: %s\n- Room: %s\n- Verification: %s\n- Freshness: updated %s\n", memoryMarkdownDigestText(memory.Key), memoryMarkdownDigestText(memory.Hall), memoryMarkdownDigestText(memory.Room), memoryMarkdownDigestText(memory.Verification), memoryMarkdownDigestText(memory.UpdatedAt))
		if memory.SourceTaskID != "" {
			fmt.Fprintf(&out, "- Source task: %s\n", memoryMarkdownDigestText(memory.SourceTaskID))
		}
		if memory.ReceiptJSON != "" && memory.ReceiptJSON != "[]" && memory.ReceiptJSON != "null" {
			fmt.Fprintf(&out, "- Receipts: %s\n", memoryMarkdownDigestText(memory.ReceiptJSON))
		}
		if memory.SupersedesID != "" {
			fmt.Fprintf(&out, "- Supersedes: %s\n", memoryMarkdownDigestText(memory.SupersedesID))
		}
		for _, line := range strings.Split(strings.ReplaceAll(memory.Value, "\r\n", "\n"), "\n") {
			fmt.Fprintf(&out, "> %s\n", line)
		}
	}
	return out.String()
}

func memoryMarkdownDigestText(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(value), "\r", " "), "\n", " ")
}
