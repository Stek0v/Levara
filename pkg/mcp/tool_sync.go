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
	"strings"
)

// ToolSync orchestrates a bidirectional sync with a remote Levara instance.
//
// Args:
//   - remote_url (required): e.g. "http://10.23.0.53:8080/api/v1"
//   - direction: "pull" (default) or "push"
//   - since: ISO-8601 timestamp — sync records updated at or after this
//   - types: memories/interactions/graph by default; collections is explicit opt-in
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

	types, err := syncStringList(args, "types")
	if err != nil {
		return toolError(err.Error())
	}
	collectionNames, err := syncStringList(args, "collections")
	if err != nil {
		return toolError(err.Error())
	}
	types, collectionNames, err = ValidateSyncSelectors(types, collectionNames)
	if err != nil {
		return toolError(err.Error())
	}

	if deps.DB() == nil {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: database not configured"}},
			IsError: true,
		}
	}

	result, manifest, err := deps.DoSync(ctx, remoteURL, direction, types, since, collectionNames)
	if err != nil {
		deps.LogHeartbeat("sync", map[string]any{"direction": direction, "types": types, "status": "error", "error": err.Error()})
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
		"status":    result["status"],
		"result":    result,
	})

	return jsonResult(result)
}

// ValidateSyncSelectors validates before any remote I/O and removes duplicate
// selectors without expanding an omitted/empty types list into collection sync.
func ValidateSyncSelectors(types, collections []string) ([]string, []string, error) {
	var selected, names []string
	seenTypes, seenNames := make(map[string]bool), make(map[string]bool)
	needsCollections := false
	for _, kind := range types {
		switch kind {
		case "memories", "interactions", "graph":
		case "collections":
			needsCollections = true
		default:
			return nil, nil, fmt.Errorf("unsupported sync type %q", kind)
		}
		if !seenTypes[kind] {
			selected = append(selected, kind)
			seenTypes[kind] = true
		}
	}
	for _, name := range collections {
		if strings.TrimSpace(name) == "" {
			return nil, nil, fmt.Errorf("collection names must not be blank")
		}
		if !seenNames[name] {
			names = append(names, name)
			seenNames[name] = true
		}
	}
	if needsCollections && len(names) == 0 {
		return nil, nil, fmt.Errorf("collections sync requires nonempty collection names")
	}
	return selected, names, nil
}

func syncStringList(args map[string]any, key string) ([]string, error) {
	raw, present := args[key]
	if !present {
		return nil, nil
	}
	switch values := raw.(type) {
	case []string:
		return append([]string(nil), values...), nil
	case []any:
		var out []string
		for _, value := range values {
			text, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("%s must be an array of strings", key)
			}
			out = append(out, text)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("%s must be an array of strings", key)
	}
}
