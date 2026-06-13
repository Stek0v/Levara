package mcp

// Sync tool: bidirectional data sync between Levara instances.
// Migrated in F-4 wave 3q — the last remaining full-body tool in
// internal/http/mcp.go. One DoSync Deps method absorbs all the sync
// helpers (SyncPull, syncPush, syncPullCollections, syncPushCollections,
// SyncManifestFromRemote) so pkg/mcp stays free of APIConfig and
// *store.CollectionManager.

import (
	"context"
	"fmt"
)

// ToolSync orchestrates a bidirectional sync with a remote Levara instance.
//
// Args:
//   - remote_url (required): e.g. "http://10.23.0.53:8080/api/v1"
//   - direction: "pull" (default) or "push"
//   - since: ISO-8601 timestamp — only sync records updated after this
//   - types: ["memories", "graph", "collections"] — default all
//   - collections: collection names for the collections type
//
// Error branches: missing remote_url → IsError; nil DB → IsError;
// manifest-fetch failure → IsError. Per-type errors are folded into
// the result JSON under "<type>_error" keys, matching pre-refactor
// behaviour.
func ToolSync(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	remoteURL, _ := args["remote_url"].(string)
	if remoteURL == "" {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: 'remote_url' required (e.g., http://10.23.0.53:8080/api/v1)"}},
			IsError: true,
		}
	}

	direction, _ := args["direction"].(string)
	if direction == "" {
		direction = "pull"
	}
	since, _ := args["since"].(string)

	var types []string
	if typesRaw, ok := args["types"].([]any); ok {
		for _, t := range typesRaw {
			if s, ok := t.(string); ok {
				types = append(types, s)
			}
		}
	}

	var collectionNames []string
	if collsRaw, ok := args["collections"].([]any); ok {
		for _, c := range collsRaw {
			if s, ok := c.(string); ok && s != "" {
				collectionNames = append(collectionNames, s)
			}
		}
	}

	if deps.DB() == nil {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: database not configured"}},
			IsError: true,
		}
	}

	result, manifest, err := deps.DoSync(ctx, remoteURL, direction, types, since, collectionNames)
	if err != nil {
		return ToolResult{
			Content: []Content{{Type: "text", Text: fmt.Sprintf("Error: %s", err.Error())}},
			IsError: true,
		}
	}

	result["remote_manifest"] = manifest
	deps.LogHeartbeat("sync", map[string]any{
		"direction": direction,
		"remote":    remoteURL,
		"types":     types,
	})

	return jsonResult(result)
}
