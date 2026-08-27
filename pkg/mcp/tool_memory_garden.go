package mcp

import (
	"context"
	"sort"
	"strings"
	"time"
)

const memoryGardenScanLimit = 500

type memoryGardenRow struct {
	ID, Key, Value, UpdatedAt, Verification, SourceTaskID, ReceiptJSON string
}

// ToolMemoryGarden reports review findings from active memories. It never
// returns memory values and never changes memory, indexes, pins, or graph.
func ToolMemoryGarden(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	collection, _ := args["collection"].(string)
	if strings.TrimSpace(collection) == "" {
		return toolError("'collection' required")
	}
	staleAfterDays := 180
	if value, ok := args["stale_after_days"].(float64); ok {
		staleAfterDays = int(value)
	}
	if staleAfterDays < 1 || staleAfterDays > 3650 {
		return toolError("'stale_after_days' must be 1-3650")
	}
	limit := 100
	if value, ok := args["limit"].(float64); ok {
		limit = int(value)
	}
	if limit < 1 || limit > 200 {
		return toolError("'limit' must be 1-200")
	}
	db := deps.DB()
	if db == nil {
		return jsonResult(memoryGardenResult(collection, staleAfterDays, 0, false, nil))
	}

	rows, err := db.QueryContext(ctx, deps.Q(`SELECT id,key,value,updated_at,verification_status,source_task_id,source_receipt_ids
		FROM memories WHERE collection_name=$1 AND (owner_id=$2 OR owner_id='') AND superseded_by=''
		ORDER BY updated_at ASC,id ASC LIMIT $3`), collection, extractOwnerID(ctx), memoryGardenScanLimit+1)
	if err != nil {
		return toolError(err.Error())
	}
	defer rows.Close()
	memories := make([]memoryGardenRow, 0, memoryGardenScanLimit)
	for rows.Next() {
		var memory memoryGardenRow
		if err := rows.Scan(&memory.ID, &memory.Key, &memory.Value, &memory.UpdatedAt, &memory.Verification, &memory.SourceTaskID, &memory.ReceiptJSON); err != nil {
			continue
		}
		memories = append(memories, memory)
	}
	truncated := len(memories) > memoryGardenScanLimit
	if truncated {
		memories = memories[:memoryGardenScanLimit]
	}
	findings, findingsTruncated := memoryGardenFindings(memories, staleAfterDays, limit)
	return jsonResult(memoryGardenResult(collection, staleAfterDays, len(memories), truncated || findingsTruncated, findings))
}

func memoryGardenResult(collection string, staleAfterDays, scanned int, truncated bool, findings []map[string]any) map[string]any {
	summary := map[string]int{"duplicate": 0, "conflict": 0, "stale": 0, "weak_provenance": 0}
	for _, finding := range findings {
		summary[finding["category"].(string)]++
	}
	return map[string]any{
		"collection": collection, "stale_after_days": staleAfterDays, "scanned_memories": scanned, "truncated": truncated,
		"summary": summary, "findings": findings,
	}
}

func memoryGardenFindings(memories []memoryGardenRow, staleAfterDays, limit int) ([]map[string]any, bool) {
	byValue, byKey := map[string][]memoryGardenRow{}, map[string][]memoryGardenRow{}
	for _, memory := range memories {
		normalized := normalizeMemoryCommitText(strings.ToLower(memory.Value))
		byValue[normalized] = append(byValue[normalized], memory)
		byKey[memory.Key] = append(byKey[memory.Key], memory)
	}
	findings := make([]map[string]any, 0)
	for _, value := range sortedGardenKeys(byValue) {
		group := byValue[value]
		if len(group) > 1 {
			findings = append(findings, memoryGardenFinding("duplicate", "medium", group, "same normalized value"))
		}
	}
	for _, key := range sortedGardenKeys(byKey) {
		group := byKey[key]
		if len(group) > 1 && gardenDistinctValues(group) > 1 {
			findings = append(findings, memoryGardenFinding("conflict", "high", group, "same active key has different values"))
		}
	}
	cutoff := time.Now().Add(-time.Duration(staleAfterDays) * 24 * time.Hour)
	for _, memory := range memories {
		if updated, ok := parseMemoryGardenTime(memory.UpdatedAt); ok && updated.Before(cutoff) {
			findings = append(findings, memoryGardenFinding("stale", "low", []memoryGardenRow{memory}, "updated before stale threshold"))
		}
		if memory.Verification == "" && memory.SourceTaskID == "" && !gardenHasReceipts(memory.ReceiptJSON) {
			findings = append(findings, memoryGardenFinding("weak_provenance", "low", []memoryGardenRow{memory}, "no verification status, task, or receipt"))
		}
	}
	if len(findings) > limit {
		return findings[:limit], true
	}
	return findings, false
}

func memoryGardenFinding(category, severity string, memories []memoryGardenRow, reason string) map[string]any {
	ids, keys := make([]string, 0, len(memories)), make([]string, 0, len(memories))
	for _, memory := range memories {
		ids, keys = append(ids, memory.ID), append(keys, memory.Key)
	}
	return map[string]any{"category": category, "severity": severity, "memory_ids": ids, "keys": keys, "reason": reason}
}

func sortedGardenKeys(groups map[string][]memoryGardenRow) []string {
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func gardenDistinctValues(memories []memoryGardenRow) int {
	values := map[string]bool{}
	for _, memory := range memories {
		values[normalizeMemoryCommitText(strings.ToLower(memory.Value))] = true
	}
	return len(values)
}

func gardenHasReceipts(raw string) bool {
	return strings.Trim(strings.TrimSpace(raw), "[]\" ") != ""
}

func parseMemoryGardenTime(raw string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if value, err := time.Parse(layout, raw); err == nil {
			return value, true
		}
	}
	return time.Time{}, false
}
