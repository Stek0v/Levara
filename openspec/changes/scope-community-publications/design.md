# Design

## Context

See proposal.md. Existing community detection is global. The pipeline reaches it only from its unscoped local branch, but the database reads all datasets. Summarization rereads mutable graph rows; replacement ignores SQL errors and contains SQLite-only placeholders/conflict syntax. MCP list currently bypasses the shared global admission and protected response path. No new algorithm, dependency, interface or feature flag is needed.

## Goals / Non-Goals

**Goals:** preserve instance-wide global semantics, exact input provenance, SQL parity, and bounded actual-drain authority. Implement admission/lifetime first, then verified publication without claiming T16 complete from the first slice.

**Non-Goals:** project-specific community partitions, authenticated memify enablement, new external providers, Neo4j parity, production migrations/deployment, automatic legacy proof adoption.

## Decisions

- Reuse active-admin/no-tenant policy. A filtered membership list cannot make a global summary source scoped because both detection and parent summaries depend on other inputs.
- MCP list joins the existing protected result branch, with credential-capped construction context. Its SQL rows are materialized and closed before a transfer fence is acquired, preserving a one-connection pool.
- The shared completed-body sender caps verified expiry and acquires a transfer-owned fence on the original cancelable context. Only the completed body observer detaches from handler cancellation; release belongs to actual Close. Deadline-owned SQL transactions are insufficient because they auto-release before actual drain.
- Materialize one immutable graph snapshot with full detection-input assertions and exact prompt text inputs. Summaries use that snapshot instead of querying live rows again. Parent proof includes all incorporated child proof; topology proof includes the full detection input, not just the prompt's top-N members.
- Add mirrored generation, sources_json and lineage_verified columns to graph_communities. Preserve concrete native source revision/hash/publication assertions and graph snapshot assertions. Reuse existing source/resource/publication APIs and bounded recursive validation (256 distinct dependencies, depth16); unsupported workspace or legacy evidence remains unverified. No synthetic owner or document publication is invented for the builder.
- The new verified builder uses a full Louvain recompute over the immutable snapshot. The existing incremental API remains compatible; verified incremental optimization can reuse a caller-owned snapshot only when measured need justifies it. This avoids nested DB acquisition under a pool-one fence while preserving rebuild correctness.
- Persist native source assertions as a bounded JSON array, including a canonical input_sha256 digest binding each entry to the exact materialized detection input. Capture raw nested dependencies separately from derived document assertions: raw dependencies require source version/hash and resource revision; derived graph inputs additionally require the saved document collection/generation. Missing legacy identity yields explicit unverified local compatibility; known retired/invalid sources or SQL errors abort before model effects.
- SQL publication is one checked transaction after final source and graph-input revalidation. Acquire source/authority and document publication fences before graph nodes → edges SHARE locks. Graph/data cleanup takes nodes → edges SHARE ROW EXCLUSIVE before deletions, so both table and raw-upsert row conflicts have one order; dry runs do not acquire write locks. The existing source fence alone does not block graph writers. Concurrent builds use last-current-writer-wins; stale snapshots fail. Compute summaries before replacement; no asynchronous updates to an already published generation.
- MCP list carries SQL publication evidence internally in a json-ignored ToolResult field, so transport tracking uses the exact materialized proof and never adopts a later publication for earlier returned text. Existing document evidence is reused for all native dependencies; community generation/proof are rechecked through the same policy transaction before model and completed-body delivery. No new policy interface is needed.
- Native document lineage accepts the existing serializer's known empty workspace fields, while nonempty workspace evidence and persisted community proof remain strict. Legacy summary updates clear generation/proof/verified state instead of preserving evidence for changed text.
- Generic retrieval carries its server-selected collection in a json-ignored ScoredResult field through vector, lexical, hybrid and rerank stages. Authenticated summary collection records require publication proof even when indexed metadata omits its community marker.
- Vector metadata contains community_id and generation. Consumers resolve SQL summary/proof, authorize all dependencies through the existing document policy, and track them for model/egress fences. General search of the summary collection uses the same gate. Vectors remain rebuildable; no cross-store atomicity is claimed.

## Risks / Trade-offs

- Global detection proof can be large → conservative complete-input proof and explicit existing dependency budgets; no truncated verified proof. Measure before introducing per-community partition semantics.
- Long source/model work and graph locking → keep observer bounded, preserve cancellation, lock in a consistent order, and test independent PostgreSQL writers and pool1 SQLite. Do not release while a noncooperative provider still holds source text.
- Old summaries lack provenance → trusted anonymous compatibility only; authenticated readers fail closed until a provable rebuild. Missing LLM yields no invented summary; embedding failure leaves SQL usable and stale generations rejected.
- Shared sender affects other completed protected responses → targeted lifetime/acquisition tests plus their existing regressions and fresh full/contract checks.

## Migration Plan

Add defaults compatible with populated SQLite/PostgreSQL schemas; old rows remain unverified. Exercise migration and rollback in disposable native fixtures only. Public descriptors/result shapes remain stable; root owns generated contract checks. No deployment or live migration is part of this request.
