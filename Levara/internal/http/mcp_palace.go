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

// ── query_entity ──

// toolQueryEntity is a thin shim over mcp.ToolQueryEntity (F-4 wave 3h).
func (h *mcpHandler) toolQueryEntity(ctx context.Context, args map[string]any) mcpToolResult {
	return mcp.ToolQueryEntity(ctx, h, args)
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

