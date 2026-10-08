# Tasks

## 1. Global admission and completed response lifetime

- [x] 1.1 Close I100 with existing active-admin/no-tenant admission and protected MCP list response; preserve trusted local and JSON-RPC shapes. DoD: both legacy/latest actual transports deny peer/selected tenant/forged actor, allow active unscoped admin, and work with one SQL connection. Root runs `go test -p=1 -ldflags=-w -count=1 -race ./internal/http -run 'Community.*Authority|GraphACLMCPTransports' -skip '^TestMemoryREST'`; preflight disposable PostgreSQL P, no provider.
- [x] 1.2 Cap shared completed-body observer at verified credential expiry; acquire transfer fence on cancelable context and retain it until actual Close. DoD: partial-drain expiry, demotion/revoked key before handoff, canceled pool1 acquisition and repeated Close checks pass on both SQL. Document exact completed-response limit. Root runs `go test -p=1 -ldflags=-w -count=1 -race ./internal/http -run 'Community.*Authority|ProtectedResponse|FencedResponse|WorkspaceReadResponseFence|GraphPathDocumentAuthorization|GlobalSearchRejects' -skip '^TestMemoryREST'`; preflight P.

## 2. Immutable build proof and SQL publication

- [x] 2.1 Add additive mirrored community publication fields with populated migration tests; no legacy auto-verification. DoD: generation/proof defaults agree on both schemas and invalid/missing/over-budget assertions remain unverified. Command: native new CommunityPublication migration/proof tests; preflight P, local stub model only.
- [x] 2.2 Materialize detection/prompt node, edge and endpoint inputs once; preserve complete topology proof and union used child proofs. DoD: mixed sources, mutated node metadata, inactive edges, invalid endpoint, parent propagation and missing model cases covered; prompts never reread an unproved source. Command: `go test -p=1 -ldflags=-w -count=1 -race ./pkg/community ./internal/http -run 'Community.*(Snapshot|Provenance|Publication)' -skip '^TestMemoryREST'`; preflight P/local model M.
- [x] 2.3 Replace communities/members/summaries/proof atomically after source and graph revalidation; fix placeholder/conflict/error/rollback and incremental paths. DoD: injected later SQL failure preserves full prior publication, independent PostgreSQL writers reject stale snapshots, cancellation is observed, pool1 works, no short-ID panic; PostgreSQL graph/data pruning waits on nodes before owning edges (independent pools and actual lock barrier); document concurrency/proof limits. Command: native CommunityPublication/Incremental race tests; preflight P/M.

## 3. Authoritative consumers and derived vectors

- [x] 3.1 Resolve MCP/REST/general summary collection hits through matching current SQL generation and validate recorded dependencies before model/egress. DoD: retirement/revision/share revoke of one source hides whole dependent aggregate, stale vector generation and legacy unverified rows cannot leak, selected tenant stays denied, anonymous legacy compatibility explicit. Command: `go test -p=1 -ldflags=-w -count=1 -race ./pkg/mcp ./internal/http -run 'Community|Global' -skip '^TestMemoryREST'`; preflight P/M, local embed stub.
- [x] 3.2 Embed only committed SQL summaries; preserve degradation when model/embed unavailable and ignore late/stale vector writes. DoD: backend failure after SQL commit never exposes old generation; successful rebuild retrieves authoritative SQL text. Update community/source lifecycle guide. Command: native CommunityPublication vector/provider tests; preflight P/local M.

## 4. Integration acceptance

- [x] 4.1 Independent current combined review, targeted original T16 Community/Global/Graph race suite, fresh frozen `make test-commit`, `make contract-check`, and `openspec validate scope-community-publications --strict`. DoD: actual terminal exits, immutable logs/hashes/revisions, all native tests without PostgreSQL skips, issue/backlog/evidence updates. Do not accept T16 from admission alone or silently accept missing original T15 Neo4j parity. Exclude additional REST owner probes as the user directed. No production operation; preflight P/M.

## Acceptance

Own native SQL/global publication change: 8/8 complete. Root observed race427/0/0; full3942/0/2 S0–S4; docs/community/pipeline race117/0/2 after generated-only schema update; standalone contract and OpenSpec strict exit0; independent current source/log audit. Initial399/16/0 and contract drift failures retained in [roadmap evidence](../../../docs/product/decomposition-evidence-2026-10-05.md). Original roadmap T16 remains open pending T15/Neo4j I98; no source-scoped partition, cross-store atomicity, physical vector deletion, deployment or archive claim.
