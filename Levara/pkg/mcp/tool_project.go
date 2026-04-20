package mcp

// Project-context and embedding-drift tools: get_project_context + check_drift.
// Migrated in F-4 wave 3o. Both tools read collection metadata through the new
// CollectionMeta(name) Deps method added in this wave — avoids leaking
// internal/store.CollectionMeta into pkg/mcp.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ToolGetProjectContext assembles a context summary for a collection:
// collection stats, recent memories, graph entity-type counts, and
// recent interactions. Optional "include_related" arg appends summaries
// of sibling collections.
//
// Error branch: missing collection arg → IsError.
// Non-error branches: any section that errors (DB nil, missing table) is
// silently skipped — a partial context is more useful than an error.
func ToolGetProjectContext(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	collection, _ := args["collection"].(string)
	if collection == "" {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: 'collection' required"}},
			IsError: true,
		}
	}

	var sb strings.Builder
	db := deps.DB()

	// 1. Collection stats
	sb.WriteString("## Collection Stats\n")
	meta := deps.CollectionMeta(collection)
	if meta.Records > 0 || meta.Dim > 0 {
		sb.WriteString(fmt.Sprintf("- Name: %s\n- Records: %d\n- Dimension: %d\n- Metric: %s\n\n",
			meta.Name, meta.Records, meta.Dim, meta.Metric))
	} else {
		sb.WriteString(fmt.Sprintf("- Collection '%s' not found (no vectors indexed yet)\n\n", collection))
	}

	// 2. Memories
	sb.WriteString("## Project Memories\n")
	if db != nil {
		rows, err := db.QueryContext(ctx,
			deps.Q(`SELECT key, value, type FROM memories
			 WHERE collection_name = $1 ORDER BY updated_at DESC LIMIT 20`), collection)
		if err == nil {
			defer rows.Close()
			count := 0
			for rows.Next() {
				var key, value, typ string
				rows.Scan(&key, &value, &typ)
				sb.WriteString(fmt.Sprintf("- [%s] %s: %s\n", typ, key, Truncate(value, 200)))
				count++
			}
			if count == 0 {
				sb.WriteString("- (no memories saved for this collection)\n")
			}
			sb.WriteString("\n")
		}
	}

	// 3. Graph entities (top types)
	sb.WriteString("## Key Entity Types\n")
	if db != nil {
		rows, err := db.QueryContext(ctx,
			deps.Q(`SELECT type, COUNT(*) as cnt FROM graph_nodes GROUP BY type ORDER BY cnt DESC LIMIT 10`))
		if err == nil {
			defer rows.Close()
			count := 0
			for rows.Next() {
				var typ string
				var cnt int
				rows.Scan(&typ, &cnt)
				sb.WriteString(fmt.Sprintf("- %s: %d entities\n", typ, cnt))
				count++
			}
			if count == 0 {
				sb.WriteString("- (no entities extracted yet)\n")
			}
			sb.WriteString("\n")
		}
	}

	// 4. Recent interactions
	sb.WriteString("## Recent Interactions\n")
	if db != nil {
		rows, err := db.QueryContext(ctx,
			deps.Q(`SELECT query, response, created_at FROM interactions
			 ORDER BY created_at DESC LIMIT 5`))
		if err == nil {
			defer rows.Close()
			count := 0
			for rows.Next() {
				var query, response, createdAt string
				rows.Scan(&query, &response, &createdAt)
				sb.WriteString(fmt.Sprintf("- Q: %s\n  A: %s\n", Truncate(query, 100), Truncate(response, 150)))
				count++
			}
			if count == 0 {
				sb.WriteString("- (no interactions recorded)\n")
			}
		}
	}

	// 5. Related projects (compact summaries)
	if related, ok := args["include_related"].([]any); ok && len(related) > 0 {
		sb.WriteString("\n## Related Projects\n")
		for _, r := range related {
			relColl, ok := r.(string)
			if !ok || relColl == "" {
				continue
			}
			sb.WriteString(fmt.Sprintf("\n### %s\n", relColl))
			relMeta := deps.CollectionMeta(relColl)
			if relMeta.Records > 0 || relMeta.Dim > 0 {
				sb.WriteString(fmt.Sprintf("- Records: %d, Dim: %d\n", relMeta.Records, relMeta.Dim))
			} else {
				sb.WriteString("- (no vectors)\n")
			}
			if db != nil {
				relRows, err := db.QueryContext(ctx,
					deps.Q(`SELECT key, value FROM memories WHERE collection_name = $1 ORDER BY updated_at DESC LIMIT 3`), relColl)
				if err == nil {
					defer relRows.Close()
					for relRows.Next() {
						var key, value string
						relRows.Scan(&key, &value)
						sb.WriteString(fmt.Sprintf("- %s: %s\n", key, Truncate(value, 100)))
					}
				}
			}
		}
	}

	return ToolResult{Content: []Content{{Type: "text", Text: sb.String()}}}
}

// driftResult mirrors embed.DriftCheckResult JSON shape without importing
// pkg/embed into pkg/mcp. The JSON keys are identical — callers parsing
// the output of toolCheckDrift before and after the migration see no diff.
type driftResult struct {
	Collection    string `json:"collection"`
	ExpectedModel string `json:"expected_model"`
	ExpectedDim   int    `json:"expected_dim"`
	ActualModel   string `json:"actual_model"`
	ActualDim     int    `json:"actual_dim"`
	IsDrifted     bool   `json:"is_drifted"`
	RecordCount   int    `json:"record_count"`
}

// ToolCheckDrift reports embedding model drift across all non-empty,
// non-internal collections. "Drift" means the collection was indexed with
// a different model or dimension than the current deployment config.
//
// Algorithm matches embed.CheckDrift exactly: iterate collections, skip
// empty and "_"-prefixed, compare model+dim. Uses Deps.CollectionMeta
// instead of the *store.CollectionManager pointer, and derives currentDim
// by scanning collections for the first non-zero dim rather than relying
// on a deployment-level constant (mirrors the two-step logic in the
// pre-refactor mcpHandler.toolCheckDrift).
//
// Returns "[]" (empty JSON array, not IsError) when nothing is drifted.
func ToolCheckDrift(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	// T6: narrow accessor — ToolCheckDrift only needs the model name.
	currentModel := deps.EmbedModel()

	// Find the representative dimension by looking at the first collection
	// that reports a non-zero dim. This matches the pre-refactor two-step
	// approach where dim was probed before the authoritative CheckDrift call.
	currentDim := 0
	for _, name := range deps.ListCollections() {
		m := deps.CollectionMeta(name)
		if m.Dim > 0 {
			currentDim = m.Dim
			break
		}
	}

	var results []driftResult
	for _, name := range deps.ListCollections() {
		// Skip internal collections (matching embed.CheckDrift behaviour).
		if strings.HasPrefix(name, "_") {
			continue
		}
		m := deps.CollectionMeta(name)
		if m.Records == 0 {
			continue
		}
		isDrifted := false
		if m.EmbedModel != "" && m.EmbedModel != currentModel {
			isDrifted = true
		}
		if currentDim > 0 && m.Dim > 0 && m.Dim != currentDim {
			isDrifted = true
		}
		if isDrifted {
			results = append(results, driftResult{
				Collection:    name,
				ExpectedModel: currentModel,
				ExpectedDim:   currentDim,
				ActualModel:   m.EmbedModel,
				ActualDim:     m.Dim,
				IsDrifted:     true,
				RecordCount:   m.Records,
			})
		}
	}

	if results == nil {
		results = []driftResult{}
	}
	out, _ := json.MarshalIndent(results, "", "  ")
	return ToolResult{Content: []Content{{Type: "text", Text: string(out)}}}
}
