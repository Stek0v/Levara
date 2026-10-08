# Proposal

## Why

Actual authenticated MCP probes show that different users and tenants read and overwrite the same agent diary when agent and collection match. Native PostgreSQL also rejects diary writes because the INSERT supplies integer zero to a BOOLEAN column.

## What Changes

- **BREAKING**: isolate authenticated diary entries by verified caller, selected tenant and normalized agent; stop exposing historical global agent diaries to authenticated callers.
- Preserve the anonymous trusted-local `agent:<name>` namespace and exported legacy helper. Do not infer owners or backfill historical rows.
- Use existing bounded SQL authority fences for authenticated reads and writes, with live credential, permission and tenant checks.
- Use a native SQL BOOLEAN literal on both database dialects; fail reads on scan/iteration errors.
- Close the confirmed alternate REST writer bypass: authenticated foreign/synthetic owner is rejected before persistence and publication; own omitted/explicit owner and anonymous local compatibility remain. Root owns this dependency.

Non-goals: imported-transcript authorization, distillation routing/cancellation, model factual quality, profile dispatch I05, automatic migration, deployment and an HTTP transfer-drain guarantee.

## Capabilities

### New Capabilities

- `chat/agent-diaries`: caller/tenant-scoped agent diaries, trusted-local compatibility and native SQL publication/read boundaries.

### Modified Capabilities

None; the current main-spec inventory is empty.

## Impact

`pkg/mcp/tool_diary.go`, focused MCP and real HTTP transport regressions, tool descriptors and guides. No new dependencies, tables, migrations or interfaces. Historical global rows remain stored and available through the trusted-local path; authenticated clients create new scoped entries. Root owns generated contracts. Local fixtures and stub credentials only; no production operations.
