// mcp_palace.go — Memory-palace MCP handlers: wake_up, pin/unpin, query_entity,
// agent diaries, and the controlled "hall" vocabulary.
//
// Inspired by milla-jovovich/mempalace's Wings/Rooms/Halls metaphor: rooms are
// sub-topics within a collection, halls classify the genre of a memory (fact,
// event, decision, ...). Combined with structural filters this raises recall
// precision substantially over flat metadata search.
package http

import (
	"context"

	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/mcp"
)

// Hall vocabulary, ChunkMetaMatches, and IsValidHall live in pkg/mcp now
// (F-4 wave 1a) — see pkg/mcp/hall.go.

// ── wake_up ──

// toolWakeUp is a thin shim over mcp.ToolWakeUp (F-4 wave 3f).
func (h *mcpHandler) toolWakeUp(ctx context.Context, args map[string]any) mcpToolResult {
	return mcp.ToolWakeUp(ctx, h, args)
}

// ── pin / unpin ──

// toolPinMemory / toolUnpinMemory are thin shims over pkg/mcp (F-4 wave 3f).
func (h *mcpHandler) toolPinMemory(ctx context.Context, args map[string]any) mcpToolResult {
	return mcp.ToolPinMemory(ctx, h, args)
}

func (h *mcpHandler) toolUnpinMemory(ctx context.Context, args map[string]any) mcpToolResult {
	return mcp.ToolUnpinMemory(ctx, h, args)
}

func (h *mcpHandler) toolDeleteMemory(ctx context.Context, args map[string]any) mcpToolResult {
	return mcp.ToolDeleteMemory(ctx, h, args)
}

// ── query_entity ──

// toolQueryEntity is a thin shim over mcp.ToolQueryEntity (F-4 wave 3h).
func (h *mcpHandler) toolQueryEntity(ctx context.Context, args map[string]any) mcpToolResult {
	return mcp.ToolQueryEntity(ctx, h, args)
}

func (h *mcpHandler) GraphDatasetIDs(ctx context.Context) ([]string, error) {
	return documentSQLPolicy(h.cfg).GraphDatasetIDs(ctx, workspaceActorFromMCP(ctx))
}

func (h *mcpHandler) GraphAssertionsAllowed(ctx context.Context, assertions []mcp.GraphAssertion) bool {
	actor, ok := ctx.Value(searchActorKey{}).(accesspkg.Actor)
	if !ok {
		actor = workspaceActorFromMCP(ctx)
		ctx = context.WithValue(ctx, searchActorKey{}, actor)
	}
	policy := documentSQLPolicy(h.cfg)
	for _, assertion := range assertions {
		source, err := decodeSearchDocumentSource(assertion.Properties)
		if err != nil {
			return false
		}
		source.DatasetID = assertion.DatasetID
		if source.DocumentID == "" && source.DatasetID != "" {
			registered, err := policy.HasRegisteredDocuments(ctx, source.DatasetID)
			if err != nil || registered {
				return false
			}
			decision, err := policy.AuthorizeDataset(ctx, actor, source.DatasetID, accesspkg.ActionRead)
			if err != nil || !decision.Allowed {
				return false
			}
			trackSearchSource(ctx, searchDocumentSource{DatasetID: source.DatasetID, DatasetMetadataOnly: true})
			continue
		}
		source.Derived = true
		allowed, err := searchDocumentAllowed(ctx, h.cfg, actor, source)
		if err != nil || !allowed {
			return false
		}
		trackSearchSource(ctx, source)
	}
	return len(assertions) > 0
}

// ── Agent diaries ──

// DiaryOwnerPrefix and DiaryOwner moved to pkg/mcp/util.go.

// toolDiaryWrite / toolDiaryRead are thin shims over pkg/mcp (F-4 wave 3g).
func (h *mcpHandler) toolDiaryWrite(ctx context.Context, args map[string]any) mcpToolResult {
	return mcp.ToolDiaryWrite(ctx, h, args)
}

func (h *mcpHandler) toolDiaryRead(ctx context.Context, args map[string]any) mcpToolResult {
	return mcp.ToolDiaryRead(ctx, h, args)
}
