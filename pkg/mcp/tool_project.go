package mcp

// Project-context and embedding-drift tools: get_project_context + check_drift.
// Migrated in F-4 wave 3o. Both tools read collection metadata through the new
// CollectionMeta(name) Deps method added in this wave — avoids leaking
// internal/store.CollectionMeta into pkg/mcp.

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/stek0v/levara/pkg/embcontract"
)

// ToolGetProjectContext summarizes current caller/shared memories in a project.
// Auxiliary sources without proven project/access scope are explicitly unavailable.
// A failed read returns an error without publishing the partially assembled summary.
func ToolGetProjectContext(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	collection, _ := args["collection"].(string)
	if collection == "" {
		return toolError("'collection' required")
	}
	db := deps.DB()
	if db == nil {
		return toolError("database not configured")
	}
	ownerID := extractOwnerID(ctx)
	var sb strings.Builder
	var aggregates *ProjectContextAggregates
	if provider, ok := deps.(ProjectContextAggregateProvider); ok {
		value, err := provider.ProjectContextAggregates(ctx, collection)
		if err != nil {
			return toolError("read project aggregates: " + err.Error())
		}
		aggregates = &value
	}

	writeMemories := func(name string, limit int, compact bool) error {
		rows, err := db.QueryContext(ctx, deps.Q(`SELECT key, value, type FROM memories
			WHERE collection_name = $1 AND (owner_id = $2 OR owner_id = '')
			AND superseded_by = '' AND valid_until IS NULL ORDER BY updated_at DESC LIMIT $3`), name, ownerID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		count := 0
		for rows.Next() {
			var key, value, typ string
			if err := rows.Scan(&key, &value, &typ); err != nil {
				return err
			}
			if compact {
				fmt.Fprintf(&sb, "- %s: %s\n", key, Truncate(value, 100))
			} else {
				fmt.Fprintf(&sb, "- [%s] %s: %s\n", typ, key, Truncate(value, 200))
			}
			count++
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if count == 0 {
			sb.WriteString("- (no memories saved for this collection)\n")
		}
		sb.WriteString("\n")
		return nil
	}

	sb.WriteString("## Collection Stats\n")
	if aggregates == nil {
		sb.WriteString("- unavailable: vector statistics have no proven caller access scope\n\n")
	} else {
		fmt.Fprintf(&sb, "- Accessible published documents: %d\n\n", aggregates.PublishedDocuments)
	}
	sb.WriteString("## Project Memories\n")
	if err := writeMemories(collection, 20, false); err != nil {
		return toolError("read project memories: " + err.Error())
	}
	sb.WriteString("## Key Entity Types\n")
	if aggregates == nil {
		sb.WriteString("- unavailable: graph sources have no proven project/access scope\n\n")
	} else if len(aggregates.EntityTypes) == 0 {
		sb.WriteString("- (none)\n\n")
	} else {
		types := make([]string, 0, len(aggregates.EntityTypes))
		for typ := range aggregates.EntityTypes {
			types = append(types, typ)
		}
		sort.Strings(types)
		for _, typ := range types {
			fmt.Fprintf(&sb, "- %s: %d\n", typ, aggregates.EntityTypes[typ])
		}
		sb.WriteString("\n")
	}
	sb.WriteString("## Recent Interactions\n")
	if aggregates == nil {
		sb.WriteString("- unavailable: interactions have no project collection provenance\n")
	} else if len(aggregates.RecentInteractions) == 0 {
		sb.WriteString("- (none)\n")
	} else {
		for _, interaction := range aggregates.RecentInteractions {
			fmt.Fprintf(&sb, "- %s → %s\n", Truncate(interaction.Query, 100), Truncate(interaction.Response, 100))
		}
	}

	if related, ok := args["include_related"].([]any); ok && len(related) > 0 {
		sb.WriteString("\n## Related Projects\n")
		for _, r := range related {
			name, ok := r.(string)
			if !ok || name == "" {
				continue
			}
			fmt.Fprintf(&sb, "\n### %s\n", name)
			if err := writeMemories(name, 3, true); err != nil {
				return toolError("read related project memories: " + err.Error())
			}
		}
	}
	return jsonResult(map[string]any{"collection": collection, "text": sb.String()})
}

// driftResult mirrors embed.DriftCheckResult JSON shape without importing
// pkg/embed into pkg/mcp. The JSON keys are identical — callers parsing
// the output of toolCheckDrift before and after the migration see no diff.
type driftResult struct {
	Collection      string `json:"collection"`
	ExpectedModel   string `json:"expected_model"`
	ExpectedDim     int    `json:"expected_dim"`
	ExpectedVersion string `json:"expected_version,omitempty"`
	ActualModel     string `json:"actual_model"`
	ActualDim       int    `json:"actual_dim"`
	ActualVersion   string `json:"actual_version,omitempty"`
	IsDrifted       bool   `json:"is_drifted"`
	RecordCount     int    `json:"record_count"`
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
// Returns an empty drifted array when nothing is drifted.
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
	currentContract := embcontract.FromEnv(currentModel, currentDim, "cosine").Normalized()
	currentVersion := ""
	if !currentContract.Empty() {
		currentVersion = currentContract.Fingerprint()
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
		if currentVersion != "" && m.EmbedVersion != "" && m.EmbedVersion != currentVersion {
			isDrifted = true
		}
		if isDrifted {
			results = append(results, driftResult{
				Collection:      name,
				ExpectedModel:   currentModel,
				ExpectedDim:     currentDim,
				ExpectedVersion: currentVersion,
				ActualModel:     m.EmbedModel,
				ActualDim:       m.Dim,
				ActualVersion:   m.EmbedVersion,
				IsDrifted:       true,
				RecordCount:     m.Records,
			})
		}
	}

	if results == nil {
		results = []driftResult{}
	}
	return jsonResult(map[string]any{
		"current_model":   currentModel,
		"current_dim":     currentDim,
		"current_version": currentVersion,
		"drifted":         results,
	})
}
