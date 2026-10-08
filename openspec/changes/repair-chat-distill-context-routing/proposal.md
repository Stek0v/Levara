# Proposal

## Why

Chat distillation drops caller cancellation/deadlines after loading a transcript, and dispatch does not honor the advertised session collection default. Whitespace-only messages acquire role labels and unnecessarily invoke the model.

## What Changes

- Keep synchronous model, SQL and derived indexing work within the caller's bounded operation context; reject a late provider result after cancellation.
- Apply explicit collection first, then the session default; retain the existing empty memory namespace without a session default.
- Reject genuinely empty/whitespace transcripts before invoking a provider. Preserve existing nonempty tool/system rendering.
- Preserve evidence reset, canonical identity, ordinary provenance, preview/error shapes and unrelated memory-save indexing behavior.

## Capabilities

### New Capabilities

- `chat/distillation-lifecycle`: request cancellation, meaningful input and collection routing for synchronous chat distillation.

### Modified Capabilities

None; the current main-spec inventory is empty.

## Impact

Bounded changes to pkg/mcp/tool_chat_distill.go, a context-aware indexing helper in tool_save_recall_memory.go, internal/http/mcp.go dispatch, focused native SQL/MCP tests, descriptors and guides. No dependencies, interfaces, schema migration or deployment. Imported transcript ownership is a separate product decision being clarified; this change does not certify that boundary or overall T08 completion.
