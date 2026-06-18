# DCD VSA Router Task List

Date: 2026-06-18

Status: active.

## Phase 0: Baseline Before Architecture Changes

- [x] Create implementation plan.
- [x] Add deterministic DCD/VSA baseline eval harness.
- [x] Cover at least 13 cases per core scenario.
- [x] Compare `global_vsa_first`, `oracle_route_vsa`, `wrong_route_vsa`,
  and `empty_route_vsa`.
- [x] Persist baseline JSON to `benchmark/results/dcd_vsa_baseline_latest.json`.
- [x] Persist markdown report to `docs/dcd-vsa-baseline-report.md`.
- [x] Add load baseline harness for opt-in profiles.
- [x] Run and record baseline before production routing changes.

## Phase 1: Route Metadata Model

- [x] Design SQLite/Postgres schema for domains, collections, and documents.
- [x] Add owner/team/dataset access constraints.
- [x] Add route metadata to graph facts and chunks.
- [x] Add idempotent migrations.
- [x] Regenerate API/architecture contract artifacts.
- [x] Test owner/team isolation for route rows.

## Phase 2: Route Candidate Resolver

- [x] Implement deterministic alias and exact-match resolver.
- [x] Implement BM25 route index over names, descriptions, aliases.
- [ ] Add optional VSA/embedding route index.
- [ ] Add optional LLM fallback with structured output and timeout.
- [x] Add route confidence scoring.
- [x] Add route candidate limit and confidence threshold.
- [x] Add observe-only debug metadata through `searchHandler`.
- [x] Gate DCD route resolution after query-type routing for graph/VSA-capable strategies.

## Phase 3: VSA Scope Integration

- [ ] Add routed search scope to VSA graph context retrieval.
- [ ] Apply route filters only when route confidence is sufficient.
- [x] Add route confidence boost inside VSA candidate rerank.
- [x] Preserve VSA-before-SQL ordering.
- [x] Preserve predicate synonym behavior.
- [x] Test wrong-route, empty-route, missing-metadata, and cross-dataset boost degradation.
- [x] Add VSA route boost debug counters for boost enablement, boosted items, and route metadata hit/miss.

## Phase 4: SQL/Neo4j Filler Widening

- [ ] Apply route scope to SQL/Neo4j filler.
- [ ] Implement widening from document to collection to domain to allowed dataset.
- [ ] Add source accounting and debug counters.
- [ ] Ensure filler never consumes protected VSA budget first.

## Phase 5: Context Continuity

- [ ] Add section/chunk continuity metadata.
- [ ] Boost same-document and adjacent-chunk candidates.
- [ ] Deduplicate by route + source + predicate + target.
- [ ] Test multi-part document answer retrieval.

## Phase 6: Fast Validation

- [ ] Validate no-context responses.
- [ ] Validate tenant and owner isolation after retrieval.
- [ ] Validate expired fact exclusion.
- [ ] Validate citation/context coverage.
- [ ] Measure validation latency separately.

## Phase 7: Rollout

- [x] Add `LEVARA_DCD_ROUTER=off|observe|boost` and downgrade `filter` to non-filtering boost semantics.
- [x] Ship observe-only mode first.
- [x] Enable boost-only mode behind explicit feature flag after quality report.
- [ ] Enable high-confidence filtering after load report.
- [ ] Keep rollback possible without schema rollback.
