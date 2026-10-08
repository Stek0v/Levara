# Design

## Context

See proposal.md. Diary identity is currently key + global agent owner + collection. Verified HTTP actors already carry caller, selected tenant and credential metadata; routing arguments are not authority. Both MCP transports dispatch the same handlers. Existing trusted-local unit fixtures contain only memories.

## Goals / Non-Goals

Implement caller/tenant isolation without DDL, dependency or interface changes. Keep existing response/error structures and legacy helper. This is a SQL-read/publication authority boundary; it does not hold authority through HTTP transfer drain or change profile availability.

## Decisions

- Use only `deps.MetadataActor(ctx)` for caller and tenant. Anonymous legacy access requires explicitly trusted local authority, no caller and no tenant. A nonempty caller always follows credential validation even if a local flag is present; missing proof fails closed.
- Authenticated owner encoding is `diary:v1:` plus base64url JSON tuple of caller, selected tenant and trimmed agent. Structured encoding prevents delimiter collisions; the different prefix is disjoint from every possible historical `agent:<name>`. Preserve exported DiaryOwner unchanged. Reject whitespace-only agent names.
- Reuse native bounded `SQLPolicy.BeginMetadataWrite` and `BeginTransferFenceTx`; use the returned transaction for diary operations and live credential/membership checks. Reads require read permission, writes require write permission; API keys with empty permissions fail closed. Preserve caller cancellation and ensure rollback/release on every error. A write rechecks before commit; a read closes rows and rechecks before returning. Reusing memoryCommitBegin would require unrelated tables and incorrectly require write permission for reads.
- Replace the pin literal with FALSE. Check scan and iteration errors instead of silently returning partial success. Keep same-owner upsert canonical ID and agent/collection filtering.

## Alternate writer dependency

Actual native real-JWT probes reproduced a REST POST writer bypass: caller-controlled owner_id overwrote a peer's newly scoped diary and the peer subsequently read the forged value. Root adds a narrow REST owner guard: authenticated callers may omit owner or supply their own ID; foreign and synthetic namespaces are rejected before SQL, outbox or publication. RequireAuth=false does not exempt a request with an authenticated caller. Explicit owner remains compatible only for anonymous local requests. This changes no hall validation, DDL, REST read filtering or transfer-drain guarantees. Acceptance requires full victim-row preservation, real diary reads, own canonical upsert controls and both SQL/transports.

## Risks / Trade-offs

- Authenticated legacy visibility changes → no automatic ownership assignment; keep stored rows and explicit trusted-local compatibility, describe the boundary.
- Broad existing SQL authority fences serialize short operations → use the installed policy, bounded request-derived contexts and pool=1 regressions; no new lock framework.
- SQL guard ends before ordinary HTTP transfer drains → do not claim post-return revocation protection.
- Historical global ownership is unknowable → no backfill or legacy fallback, including for administrators.

## Migration Plan

Local implementation and validation only. No schema migration or live rollout. Authenticated callers create new scoped diaries; old global rows remain untouched. Rollback preserves all rows but restores insecure global visibility, so it is not an authorization strategy.
