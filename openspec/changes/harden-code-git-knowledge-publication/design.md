# Design

## Context

See proposal.md for motivation. Go extraction currently discards parser errors; JS/TS is detected without an analyzer; Python uses regex. Direct codify SQL writes ignore errors and hash endpoints differently from nodes. Existing native cognify ingestion and runCognifySources already own immutable source bytes, checked source generation, transfer guards and final versioned CAS. Existing Git wrappers own administrator/no-tenant admission, but ParseLog uses Background and CLI ignores result.isError.

## Goals / Non-Goals

Goals: retain a synchronous compatible successful code summary, publish static graphs through the established source lifecycle and honestly describe analyzer/repository outcomes. Non-goals: a new analyzer framework, JS/TS parser, Python compiler, repository semantic resolution, new graph publisher, cross-store atomicity or production deployment.

## Decisions

- Add a checked Go/Python analyzer entry while retaining the old AnalyzeCode adapter for internal callers. Use stdlib parser errors for Go; unsupported extensions are explicit public errors. Python remains a documented heuristic.
- Supply one concrete optional precomputed ExtractedGraph to orchestrator.Config. It replaces extraction/dedup stages with static Deduplicate, skipping LLM, semantic and temporal enrichment; existing guarded later stages and source publication remain shared. No new Deps interface or factory.
- Build one graph ID lookup with file module, qualified declarations and explicit external references. Never resolve an ambiguous bare name to an arbitrary declaration. Keep source provenance assigned by the pipeline, not caller metadata.
- ToolCodify uses existing PrepareCognify, ClaimPipelineAttempt and synchronous RunPipeline with a drained progress channel. Use code_knowledge when collection is omitted and immutable content ingestion semantics on repeat; analysis-only nilDB returns before persistence. Configured failures produce IsError; failed runs are not certified current. Source bytes may persist after derived indexing failure, as in native ingestion.
- HTTP uses the existing verified global administrator/no-tenant admission and protected dispatch. Cap credential deadline before work and reuse native source guards, checked write and successful-response fence; do not invent raw owner hints.
- Add contextual ParseLog while preserving its Background compatibility adapter. Treat unborn HEAD explicitly after repository verification, preserve invalid repository errors and filtered empty behavior. No global commit-hash deduplication is introduced.
- CLI Git commands check HTTP/JSON-RPC/ToolResult errors using existing HTTP/auth helpers, without a generic new client framework.

## Risks / Trade-offs

- Static reference nodes are syntactic relationships, not semantic call resolution → document the extraction limits and test collisions.
- Existing pipeline may suppress stage errors or attempt model work in an empty/static branch → focused failure/no-model checks and minimal error propagation fixes inside the same owned pipeline unit.
- SQL/vector effects are not atomic → existing publication eligibility/CAS remains authoritative; do not claim physical cleanup or cross-store rollback.
- Source ingestion may reuse immutable identical content → test observed repeat behavior rather than promise filename mutation or commit exactly-once behavior.
- Public admission tightens and unsupported inputs become explicit errors → update descriptor/docs/generated contracts; preserve supported output fields and nilDB local behavior.

## Migration Plan

No schema migration or new dependency. Verify native SQLite and dedicated PostgreSQL, unit/static pipeline errors and real MCP authority, then regenerate existing full/core contracts. No rollout is authorized here; reverting requires keeping existing source-publication safeguards, not restoring unrestricted raw writes. Original T18 remains dependency-bound until dedicated T15 Neo4j parity is fulfilled.
