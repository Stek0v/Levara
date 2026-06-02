# Levara Memory Consolidation (System2 layer) — Design

**Date:** 2026-06-01
**Status:** Approved (design), pending implementation plan
**Author:** stek0v + Claude

## Context

Triggered by analysis of Tencent's "Hy-Memory" announcement (forwarded via a
Telegram ML channel; install command and headline numbers — −70% stored
memories, +45% info density, −35% tokens, +20% update speed — are unverified
marketing). The underlying architectural concepts are sound and map to a real
Levara gap.

Levara today is strong at **storage + retrieval + temporal graph** (HNSW +
BM25 + RRF, knowledge graph with `valid_until`/`superseded_by` supersession on
whitelisted edge relations, `room × hall` memory palace, communities,
graphrank, rerank, runreg). It lacks a **"System2" memory-lifecycle layer**: a
background process that consolidates, abstracts, and compacts memories over
time. Memory-palace records (`save_memory`) never evolve — `dedup` runs only at
insert time, `prune` is manual. Multi-day single-agent sessions therefore grow
raw storage without bound and add noise to `recall`.

This spec covers the **first** of four identified gaps: **Memory
consolidation / compaction**. The other three (salience + decay, explicit
memory tiering, token-aware context assembly) are deferred to separate specs;
this design is their foundation.

## Goals

- Periodically compress a collection's memory: collapse clusters of
  near-duplicate / closely-related memories into a single record.
- Halt unbounded raw-storage growth in long sessions; raise information density
  and reduce noise in `recall`.
- Be fully reversible and auditable — never silently lose a memory.
- Reuse existing Levara subsystems; introduce as few new ones as possible.

## Non-goals

- Does **not** touch the cognify graph's edge supersession (it has its own).
- Does **not** touch `pinned` memories or singletons.
- Does **not** implement salience/decay scoring, tiering, or context-assembly
  compression (separate specs).

## Decisions (from brainstorming)

| Decision | Choice | Rationale |
|---|---|---|
| Engine | **Hybrid**: deterministic merge + LLM abstraction | Mechanical merge for near-dupes is cheap & safe; LLM only for nontrivial clusters above a similarity threshold — balances cost vs. quality. |
| Fate of originals | **Archive + supersede (reversible)** | Reuses the temporal graph mechanism (`superseded_by`/`valid_until`); originals live in cold storage, auditable, restorable. |
| Trigger | **Background janitor + explicit MCP tool** | Periodic/idle background pass for hands-off compaction; explicit `consolidate` tool for agent/human-driven runs. |

## Architecture

### Pipeline (5 stages)

```
scan → cluster → classify(gate) → [merge | abstract(LLM)] → commit(supersede+archive)
```

1. **scan** — select candidates within scope `(collection, room?, hall?)`,
   excluding `pinned` and already-superseded records.
2. **cluster** — build a kNN similarity graph over the candidates' existing
   HNSW vectors, keep edges with `cosine ≥ τ_low` (default τ_low = 0.85), run
   connected-components / Louvain (**reuse `pkg/community`**). Keep clusters of size ≥ 2.
3. **classify (deterministic gate)** — per cluster:
   - `cosine ≥ τ_high` (~0.97) → **mechanical merge**: keep the newest record,
     supersede the rest. No LLM.
   - `τ_low ≤ cosine < τ_high` → **LLM abstraction** (System2).
   - otherwise → leave untouched.
4. **abstract (LLM, DeepSeek)** — synthesize one semantic record from the
   cluster's values. Hard guardrails: forbidden to introduce facts / numbers /
   entities not present in the sources; a post-check asserts that numbers and
   entities from the sources are covered in the output. On LLM failure → skip
   the cluster (stays raw), no partial supersede.
5. **commit** — write the new record (`tier=semantic`, inherited `room`,
   `consolidated_from=[ids]`); mark sources `superseded_by=<new>`,
   `valid_until=now`, moved to cold archive (physically retained). All via WAL,
   reversible.

### Data model additions (memory record)

- `superseded_by` — id of the record that replaced this one.
- `consolidated_from []id` — provenance: source ids on a generated record.
- `consolidation_run_id` — run that produced/affected this record.
- `tier ∈ {raw, consolidated, semantic}`.

`recall` / `wake_up` hide superseded records by default; a new
`include_superseded=true` flag returns them. Reuses the existing temporal
mechanism (`valid_until` / `superseded_by`) already implemented for graph
edges — extended to palace records.

### Trigger & API

- **Background janitor** — built on `pkg/runreg` + the `indexerLoop` pattern:
  scans collections on schedule / on idle. Per-run LLM-call budget (analogous
  to `RERANK_BUDGET_MS`).
- **MCP tool `consolidate(collection, room?, hall?, dry_run=true)`** — explicit
  run. `dry_run` (default on early runs) returns proposed merges **without
  writing**.
- **MCP tool `consolidation_revert(run_id)`** — restores superseded sources,
  deletes generated semantic records. Full reversibility.

## Error handling

- LLM failure on a cluster → skip that cluster, leave sources raw (no partial
  supersede). The run continues with other clusters.
- Per-run LLM budget cap; on overrun, remaining clusters are left raw and the
  run reports the cap was hit (no silent truncation).
- Commit is atomic per cluster: either the new record is written and all
  sources superseded, or nothing changes for that cluster.

## Safety

- `pinned` memories are never scanned or superseded.
- Anti-hallucination coverage check on every LLM-abstracted record.
- Idempotency: a second pass does not re-merge already-consolidated records
  (`tier=semantic`/`consolidated` and superseded records are excluded from
  scan).
- `dry_run` default for first runs of a collection.

## Observability (Prometheus)

- `levara_consolidation_clusters_total`, `_merged_total`, `_abstracted_total`
- `levara_consolidation_memories_reclaimed`
- `levara_consolidation_llm_tokens_spent`
- `levara_consolidation_memories_before` / `_after`
- `levara_consolidation_char_density_before` / `_after`

The before/after counters let us **honestly validate** the Hy-Memory claims
(−70% records / +45% density) on our own data rather than trust the marketing
numbers.

## Testing

- Golden-cluster fixtures (known near-dup and related clusters).
- Unit tests on mechanical merge (threshold behavior, newest-wins).
- LLM-abstraction with a mock LLM + coverage assertions (numbers/entities
  preserved, no hallucinated facts).
- Round-trip `consolidate` → `consolidation_revert` restores exact prior state.
- Idempotency: running twice over a consolidated collection is a no-op.

## Reused subsystems

HNSW vectors (clustering input), `pkg/community` (Louvain), temporal supersede
(`valid_until`/`superseded_by`), `pkg/runreg` (run lifecycle), LLM client
(DeepSeek), WAL (reversibility), Prometheus telemetry. Net-new code is mostly
orchestration of existing parts.

## Deferred (future specs)

1. Salience + decay (recency/frequency/importance scoring; cold-archive policy).
2. Explicit memory tiering (working → episodic → semantic promotion rules).
3. Token-aware context assembly (System1: adaptive compression of returned
   context under a budget).
