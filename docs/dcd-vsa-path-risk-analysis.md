# DCD/VSA Path Risk Analysis

Date: 2026-06-18

Scope: deeper review of the implemented DCD/VSA paths against the design plan
and saved benchmark artifacts before pushing.

## Path Inventory

### Request path

```text
searchHandler
  -> parse request
  -> searchRequestContext
  -> GetAllowedDatasetIDs
  -> adaptive query_type routing
  -> maybeAttachDCDRouteObserve for graph/VSA-capable strategies
  -> strategy.Execute
```

Current state:

- DCD routing is computed after query type routing.
- With `LEVARA_DCD_ROUTER=observe|boost|filter`, only graph/VSA-capable
  strategies currently pay route resolver SQL/BM25 cost:
  `GRAPH_COMPLETION`, `GRAPH_SUMMARY_COMPLETION`, and
  `GRAPH_COMPLETION_CONTEXT_EXTENSION`.
- `observe` only stores debug metadata in `fiber.Ctx`.
- `boost` passes route candidates into graph context assembly through
  `dcdRouteCandidatesFromCtx`.
- `filter` is explicitly downgraded to effective `boost`; debug metadata reports
  `requested_mode=filter`, `mode=boost`, and `filter_active=false`.

Risk:

- Hard route filtering and widening are still not implemented; `filter` remains
  a non-filtering compatibility/downgrade path.
- DCD cost is now gated by graph-capable strategies, but graph/VSA route
  resolver latency still needs medium/large-corpus measurement before broader
  rollout.

### Route resolver path

```text
resolveDCDRouteCandidates
  -> loadDCDRouteRows
  -> score exact aliases/descriptions/titles
  -> build in-memory BM25 index per request
  -> threshold + limit
```

Current state:

- Owner, team, and allowed dataset filters happen before scoring.
- Route scoring is deterministic and cheap on small fixtures.
- BM25 index is rebuilt on every request.
- `AllowGlobalFallback` exists in policy but is not used by resolver behavior.
- Confidence is `score / 10`, capped at 1.0; it is not calibrated against a
  production corpus.

Risk:

- Per-request BM25 rebuild will not hold under 10k-100k document route rows.
- Empty `ownerID` with `AllowedDatasetIDs == nil` means all route rows are
  loadable. This follows current dev/superuser behavior, but must be explicit in
  rollout docs.
- Confidence thresholds cannot safely drive hard filtering yet.

### VSA graph context path

```text
assembleGraphContext
  -> queryVSA first or SQL first based on policy
  -> vsaGraphContextItems
     -> resolve source nodes
     -> load predicate synonyms
     -> list predicates per dataset
     -> QueryObjectWithOptions per source/dataset/predicate
     -> route-aware rerank inside returned candidates
     -> SQL/Neo4j filler after VSA
```

Current state:

- VSA-before-SQL order is preserved.
- Predicate synonym ranking is preserved.
- Route metadata is carried through `vsamemory.Candidate`.
- Boost mode increases per-predicate `TopK` from 3 to 12, then re-sorts by
  route boost and trims back to 3.
- Route boost score:

```text
base = RerankScore or Similarity
+ 0.10 * confidence when domain matches
+ 0.20 * confidence when collection matches
+ 0.35 * confidence when document matches
```

Risk:

- `TopK=12` in boost mode expands work significantly across up to 5 sources,
  all allowed datasets, and up to 8 predicates.
- Route boost can only help facts that are already present in the VSA candidate
  result returned by `QueryObjectWithOptions`; it is not true scoped retrieval.
- Missing route metadata means boost becomes a no-op, but there is no explicit
  debug counter for that.
- Wrong route positive boost is not yet tested in production path. The baseline
  wrong-route test is synthetic and does not exercise `searchHandler` boost.

### VSA store path

```text
RebuildFromGraph
  -> reads graph_edges
  -> writes vsa_fact_members
  -> writes vsa_fact_shards

QueryObjectWithOptions
  -> reads shards
  -> binds query key
  -> reads shard members
  -> joins target name, edge confidence, route ids
  -> query-text rerank
```

Current state:

- Route metadata is read at query time from `graph_edges`/`graph_nodes`.
- Backward-compatible fallback preserves old schemas without route columns.
- Rebuild does not copy route ids into `vsa_fact_members`; it relies on joins.

Risk:

- Query-time route metadata joins increase cost.
- If graph route metadata changes without VSA rebuild, boost can see new route
  ids from graph joins while shard membership remains stale. That is acceptable
  for metadata-only updates, but not for fact membership changes.
- There is no metric for route-metadata hit/miss rate.

## Archive And Report Findings

### What the archive proves

Existing reports prove a strong quality signal:

- `oracle_route_vsa` moves fact recall/MRR/nDCG from `0.000` to `1.000`.
- VSA-before-SQL finds the target in `52/52`; SQL-before-VSA finds `0/52`.
- `searchHandler` architecture eval confirms `vsa_first` recall/MRR/nDCG
  `1.000`.
- Tenant leak and expired leak rates are `0.000` in quantitative evals.
- Predicate synonym map has top1/MRR `1.000`.

### What the archive does not prove yet

- It does not prove production `boost` degrades safely on wrong routes.
- It does not prove `filter` mode because filter mode is not implemented.
- It does not prove route resolver performance at medium/large corpus sizes.
- It does not prove multi-route fallback.
- It does not prove missing route metadata fallback in `searchHandler`.
- It does not prove route confidence calibration.
- It does not prove SQL/Neo4j filler widening under route scope.

## Push Readiness Classification

### Safe to push as disabled/observe-capable

These pieces are reasonable to push:

- Route schema and idempotent migrations.
- Route resolver with deterministic/BM25 scoring.
- `LEVARA_DCD_ROUTER=off` default.
- `LEVARA_DCD_ROUTER=observe` debug metadata.
- Positive-path `boost` implementation behind flag.
- Test and report artifacts documenting the current quality/perf state.

Reason: default behavior stays off, observe does not affect ranking, and boost
requires explicit opt-in.

### Not safe to market as complete DCD/VSA router

These claims are not supported yet:

- `filter` mode is implemented.
- Hard route filtering is safe.
- Route confidence threshold is production calibrated.
- Wrong route degradation is safe in production path.
- Medium/large route load is acceptable.

Reason: the code and archive do not yet cover those gates.

## Pre-Push Fixes Recommended

### P0: Avoid misleading `filter` semantics

Status: complete.

Current code downgrades `filter` to effective `boost` and exposes
`filter_active=false` in debug metadata. Hard route filtering remains disabled
until route filtering and widening are implemented.

### P0: Add negative-path boost tests

Status: complete.

Added tests:

- wrong route candidate must not remove global/VSA candidates
- empty route candidates keep current VSA behavior
- missing route metadata keeps current VSA behavior
- cross-dataset route candidate does not boost candidate from another dataset

The wrong-route, empty-route, and missing-metadata tests exercise
`searchHandler`; cross-dataset boost is covered at helper level.

### P1: Add route debug accounting

Status: partly complete for VSA route boost path.

Add debug counters:

- `route_candidate_count`
- `route_boost_enabled`
- `route_boosted_candidate_count`
- `route_metadata_hit_count`
- `route_metadata_miss_count`
- `route_filter_active`

This makes observe/boost rollout measurable.

Implemented response fields:

- `debug.dcd_route.candidate_count`
- `debug.dcd_route.boost_active`
- `debug.dcd_route.filter_active`
- `graph_context_route_boost_enabled`
- `graph_context_route_boosted_count`
- `graph_context_route_metadata_hit_count`
- `graph_context_route_metadata_miss_count`

Still pending:

- route cache hit/miss counters
- SQL/Neo4j widening counters
- candidate-level total boost attempts before context truncation

### P1: Cache or prebuild route index

Current per-request BM25 index build is acceptable for small tests but not for
medium/large.

Recommended architecture:

- route index keyed by `(owner_id, team_id, dataset_ids hash)`
- rebuild on route table mutation or background refresh
- TTL fallback for stale entries
- cache hit/miss metrics

### P1: Gate DCD work by strategy or measure separately

Status: complete for current graph/VSA rollout.

Implemented behavior:

- Route resolution runs after query type routing.
- Explicit non-graph strategies such as `CHUNKS` do not run DCD route
  resolution.
- `AUTO` first chooses the final strategy, then DCD runs only if that strategy
  can consume route candidates.

Still pending: route latency metrics for graph/VSA strategies and cached route
index implementation.

## Additional Test Matrix

Before enabling `boost` beyond local/manual use:

| Scenario | Required test | Gate |
|---|---|---|
| wrong route | `searchHandler` boost with wrong route candidate | recall must be no worse than no-route baseline by more than configured tolerance |
| empty route | boost mode with no candidates | output equals no-route VSA path |
| missing metadata | graph facts without route ids | output equals no-route VSA path |
| tenant collision | same route/document names across owners | zero cross-owner candidates and zero cross-dataset context |
| broad query | weak route confidence | route boost disabled or multi-route fallback |
| high fan-out | route boost under many candidates | target recall lift and p95 measured |
| stale VSA | graph edge deleted after VSA rebuild | validation excludes stale/expired facts |

Before enabling `filter`:

| Scenario | Required test | Gate |
|---|---|---|
| route filter high confidence | exact document route | recall lift over global |
| wrong route filter | wrong route selected | fallback to global/widened scope |
| no scoped facts | correct route but no VSA facts | widen to collection/domain/dataset |
| SQL filler | route-scoped VSA + SQL filler | SQL does not consume VSA reserve |
| Neo4j filler | same semantics as SQL | identical access constraints |

## Decision

Push is acceptable only if framed as:

```text
DCD/VSA route metadata + observe mode + route-aware VSA boost behind feature flag
```

Push is not acceptable if framed as:

```text
full DCD/VSA router with safe route filtering
```

The next engineering step should be negative-path boost coverage and explicit
filter-mode semantics before any CI/PR claim that the architecture is ready for
broader rollout.
