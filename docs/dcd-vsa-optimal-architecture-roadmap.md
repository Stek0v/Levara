# DCD/VSA Optimal Architecture Roadmap

Date: 2026-06-18

Status: design decision and execution roadmap.

This document expands every current DCD/VSA path and chooses the optimal
architecture for quality, speed, rollout control, and data-search semantics.

## Executive Decision

The optimal path is not immediate route filtering. The optimal path is staged:

```text
off
  -> observe
  -> measured route-aware boost
  -> cached route resolver
  -> multi-route scoped VSA retrieval
  -> SQL/Neo4j route-aware widening
  -> validation
  -> high-confidence filter
```

Why:

- The archive proves routing can unlock quality: oracle route moves recall/MRR
  from `0.000` to `1.000`.
- The archive also proves speed risk: VSA-first e2e p95 is currently around
  `134ms`, while SQL-first p95 is around `1.7ms`.
- Therefore hard filtering before route confidence calibration would be
  premature.
- `observe` and `boost` are safe rollout modes because they preserve fallback
  and do not remove candidates.
- `filter` should only exist after multi-route fallback, widening, and
  validation are implemented.

## Architectural Invariants

These are non-negotiable:

1. Access policy dominates routing.
   Route candidates are always computed inside owner/team/dataset scope.

2. VSA-before-SQL ordering is preserved.
   SQL/Neo4j remains filler, not the first budget consumer.

3. Routing starts as ranking, not filtering.
   Hard route filters require calibrated confidence and fallback.

4. Wrong routes must degrade gracefully.
   A wrong route can reduce boost, but must not erase global allowed retrieval.

5. Missing metadata must not break recall.
   Partially migrated graph facts fall back to global allowed VSA.

6. Every rollout mode must be measurable.
   Debug counters and report artifacts are part of the feature, not extras.

## End-to-End Target Flow

```text
HTTP /search
  -> parse request
  -> request deadline
  -> auth and AllowedDatasetIDs
  -> query-type routing
  -> if strategy can use DCD:
       route resolver
       predicate synonym resolver
       route policy decision
  -> VSA retrieval
       route-aware boost first
       scoped retrieval only when safe
  -> SQL/Neo4j filler
       route-aware widening
  -> context budget allocator
  -> validation
  -> response + debug metadata
```

Key difference from the current implementation:

- DCD now runs after query-type routing.
- The resolver only runs when the final strategy can use route metadata.

## Branch-by-Branch Analysis

### Branch 1: `off`

Behavior:

- No route resolver.
- No route debug.
- Existing VSA-before-SQL and SQL/Neo4j behavior only.

Quality:

- Baseline quality remains as measured.
- In adversarial fixtures, SQL-first and current append miss targets under
  budget pressure.

Speed:

- Best latency.
- No DCD overhead.

Use:

- Default production mode.
- Rollback mode.

Corner cases:

- No route metadata.
- Route tables missing after partial migration.
- Existing clients expecting legacy array response.

Gate:

- Must remain default until observe/boost metrics are stable.

### Branch 2: `observe`

Behavior:

- Resolve route candidates.
- Attach debug metadata only when response shape supports it.
- Do not change ranking, retrieval, or filtering.

Quality:

- No quality change expected.
- Generates route correctness observations.

Speed:

- Adds route SQL and route scoring cost.
- Current resolver rebuilds BM25 per request, so observe must be measured.

Control:

- Safe first rollout.
- Can be enabled by environment flag.
- Should be exposed with per-request debug:

```json
{
  "dcd_route": {
    "mode": "observe",
    "candidate_count": 3,
    "latency_us": 1200,
    "top_confidence": 0.82,
    "filter_active": false,
    "boost_active": false
  }
}
```

Corner cases:

- Query type is `CHUNKS` or `BM25`: route resolver is skipped until those
  paths have route-aware ranking/filtering semantics.
- User has no `user_id`: dev/superuser semantics can load all route rows.
- Route table exists but is empty.
- Route candidates are ambiguous.

Decision:

- Keep observe.
- Observe runs after query-type routing for graph/VSA-capable strategies.

Gate:

- p95 route latency must be reported for at least small and medium profiles.
- Zero route resolver errors under normal test fixtures.

### Branch 3: `boost`

Behavior:

- Route candidates become ranking features inside VSA candidate set.
- No candidate is removed solely because of route mismatch.
- VSA-before-SQL order stays intact.

Quality:

- Positive path already proven: matching route can lift the correct VSA context
  to the top.
- Still needs wrong-route and missing-metadata degradation tests.

Speed:

- Current boost increases VSA per-predicate TopK from 3 to 12.
- Worst-case work:

```text
sources <= 5
datasets = allowed datasets
predicates <= 8
per predicate candidates = 12
```

This is acceptable only with measurement.

Control:

- Safe as opt-in after negative-path tests.
- Must report boost counters:

```text
route_boost_enabled
route_boosted_candidate_count
route_metadata_hit_count
route_metadata_miss_count
route_candidate_count
```

Corner cases:

- Wrong route strongly matches a distractor.
- Correct route has low confidence.
- Route candidate from another dataset.
- Candidate has route metadata on edge but not node.
- Candidate has route metadata on node but not edge.
- Candidate has no route metadata.
- Multiple route candidates match different facts.
- Route confidence is high due generic domain word.

Decision:

- Keep boost as the next meaningful rollout mode.
- Do not promote until negative-path boost tests pass.

Gate:

- Wrong-route boost recall >= no-route recall minus tolerance.
- Empty-route boost output equals no-route VSA behavior.
- Missing-metadata boost output equals no-route VSA behavior.
- Cross-dataset route does not boost foreign dataset candidate.
- p95 boost overhead is measured and documented.

### Branch 4: `filter`

Behavior intended:

- Hard or soft route scope narrows VSA candidate retrieval before SQL filler.

Current behavior:

- Not implemented as hard filtering.
- `filter` is explicitly downgraded to effective `boost`.
- Debug metadata reports `requested_mode=filter`, `mode=boost`, and
  `filter_active=false`.

Quality:

- Potentially strongest distractor suppression.
- Highest recall risk if route is wrong or metadata missing.

Speed:

- Can improve speed if it reduces candidate probes.
- Can hurt speed if it causes fallback/widening loops.

Control:

- Must not be accepted as production mode until implemented.
- Preferred immediate fix: downgrade `filter` to `boost` and expose
  `filter_active=false`, or reject it as unsupported.

Required route policy:

```text
if confidence >= high_threshold and route has scoped facts:
    document scope
elif confidence >= medium_threshold and route has scoped facts:
    collection or multi-route scope
else:
    global allowed VSA
```

Required widening:

```text
document
  -> collection
  -> domain
  -> allowed dataset
```

Corner cases:

- Correct route but no VSA facts in document.
- Route metadata exists on document chunks but not graph edges.
- Query asks broad exploratory question.
- Multiple documents are needed for answer.
- Route selected from stale alias.
- Route conflicts with explicit dataset or collection filter.

Decision:

- Do not ship filter yet.
- Implement after route cache, debug accounting, widening, and validation.

Gate:

- Wrong-route filter fallback recall >= 70% of global VSA recall.
- Tenant leak exactly 0.
- Expired leak exactly 0.
- Missing metadata does not reduce recall vs global VSA.
- p95 overhead measured on small and medium profiles.

## Resolver Architecture

### Current resolver

```text
SQL load route rows
  -> deterministic score
  -> per-request BM25 index
  -> threshold
  -> top-k
```

Good:

- Simple.
- Testable.
- Owner/team/dataset scoped before scoring.

Weak:

- Per-request BM25 build.
- No confidence calibration.
- No multi-route ambiguity policy.
- `AllowGlobalFallback` is not semantic yet.

### Optimal resolver

```text
routeScopeKey = owner_id + team_id + sorted(dataset_ids)

RouteIndexProvider.Get(routeScopeKey)
  -> cached deterministic alias map
  -> cached BM25 route index
  -> optional route embedding/VSA index
  -> version and freshness metadata

Resolve(query, policy)
  -> exact/alias hits
  -> BM25 hits
  -> optional embedding hits
  -> score fusion
  -> confidence calibration
  -> route policy decision
```

Recommended score:

```text
route_score =
  exact_document * 5.0
  + alias_document * 4.0
  + exact_collection * 3.0
  + exact_domain * 2.0
  + bm25_norm * 2.0
  + embedding_norm * 2.0
  + predicate_prior * 1.0
  - ambiguity_penalty
```

Confidence should be calibrated from:

- top score absolute value
- margin between top-1 and top-2
- number of matched route levels
- whether match is exact alias vs description
- route metadata coverage for candidate facts

Policy output should be more than candidates:

```go
type routeDecision struct {
    Candidates []routeCandidate
    Mode string // none, boost, multi_route, filter
    Confidence float64
    Ambiguous bool
    AllowGlobalFallback bool
    Reason string
}
```

## Route Index Caching

### Cache key

```text
owner_id
team_id
dataset_ids_hash
route_schema_version
route_data_version
```

### Cache invalidation

Invalidate on:

- route table insert/update/delete
- dataset delete
- dataset share/access change when key includes visible datasets
- ingestion job updates route metadata
- explicit route rebuild command

### Cache modes

| Mode | Use |
|---|---|
| cold build | first request or explicit rebuild |
| background refresh | route changed but stale index still usable |
| stale allowed | observe mode only |
| stale denied | filter mode |

### Metrics

- `dcd_route_cache_hit_total`
- `dcd_route_cache_miss_total`
- `dcd_route_cache_stale_total`
- `dcd_route_build_seconds`
- `dcd_route_rows_indexed`
- `dcd_route_resolve_seconds`

## Data Search Semantics

### Route metadata semantics

Route metadata can live on:

- route taxonomy tables
- graph nodes
- graph edges
- document chunks
- source document metadata

Precedence:

```text
edge route ids
  -> target node route ids
  -> source document route ids
  -> chunk metadata
  -> no route metadata
```

No route metadata must mean:

```text
eligible for allowed global retrieval,
not eligible for route-specific boost/filter.
```

### Search semantics by query type

| Query type | DCD route use |
|---|---|
| `GRAPH_COMPLETION` | observe, boost, future filter/widen |
| `GRAPH_COMPLETION_CONTEXT_EXTENSION` | observe, boost, future filter/widen |
| `GRAPH_SUMMARY_COMPLETION` | observe, boost, future filter/widen |
| `TRIPLET_COMPLETION` | later, after graph context path is stable |
| `COMMUNITY_LOCAL/GLOBAL` | later, route can select community summaries |
| `CHUNKS` | skipped until chunk route filtering/reranking is implemented |
| `HYBRID` | skipped until route-aware rerank is implemented |
| `BM25` | skipped until metadata prefilter is implemented |
| `RAG_COMPLETION` | later, after chunk route metadata is reliable |
| `TEMPORAL` | no hard route filter; route only as optional debug |

Optimal immediate control:

```text
AUTO route query type first
  -> run DCD only if chosen strategy can consume it
explicit query type
  -> run DCD only for graph-capable strategies
```

## SQL/Neo4j Widening

SQL and Neo4j are filler after VSA. Route scope must not let filler consume the
protected VSA budget.

Widening stages:

1. `document`: exact document facts.
2. `collection`: sibling facts in same collection.
3. `domain`: broader domain facts.
4. `dataset`: allowed dataset global graph facts.

Decision rules:

```text
if vsa_context_count >= reserve:
    SQL filler starts at document/collection depending query breadth
else:
    SQL filler can widen earlier, but only after VSA attempt is recorded
```

Debug:

```json
{
  "sql_widening": [
    {"level":"document","count":2},
    {"level":"collection","count":4},
    {"level":"dataset","count":4}
  ]
}
```

Corner cases:

- Neo4j has newer facts than SQL.
- SQL has facts not yet in VSA.
- VSA has stale facts deleted from SQL.
- SQL route columns are missing after migration.
- Neo4j has no route properties.

## Validation Layer

Validation should run after context assembly and before final answer.

Checks:

- no tenant leak
- no expired facts
- route filter did not eliminate all evidence
- citations/evidence ids exist when strict grounded mode is enabled
- stale VSA candidate still exists in SQL graph if validation is enabled

Validation result:

```go
type retrievalValidation struct {
    OK bool
    TenantLeaks int
    ExpiredFacts int
    StaleVSAFacts int
    EvidenceCount int
    Action string // continue, widen, abstain
}
```

Decision:

- In observe/boost: validation only records debug unless strict mode is enabled.
- In filter: validation can trigger widening or abstain.

## Full Corner Case Catalog

### Access and ownership

- Same domain name across owners.
- Same document title across teams.
- User loses dataset share after route cache was built.
- Superuser/dev request has no `AllowedDatasetIDs`.
- Dataset deleted but route rows remain.
- Route rows point to archived dataset.
- Team id missing from request context.
- Explicit collection/domain conflicts with allowed datasets.

### Routing

- Query matches two domains equally.
- Query mentions collection alias but no domain.
- Query is broad: "what do we know about billing?"
- Query is narrow: "who owns Checkout webhook retries?"
- Query includes old service name.
- Query has typo/transliteration.
- Query mixes Russian and English.
- Query has no route signal.
- Query route signal conflicts with predicate signal.
- Generic words like "runbook" dominate route score.

### Retrieval

- VSA index empty.
- VSA shard stale after graph mutation.
- Predicate synonym map missing.
- Predicate synonym over-expands to generic predicates.
- Huge source fan-out.
- Target is ranked below current per-predicate top-k.
- Route metadata exists only on edge.
- Route metadata exists only on node.
- Route metadata missing entirely.
- Answer spans adjacent chunks.
- SQL filler duplicates VSA fact.

### Performance

- 10k route candidates.
- 100k documents.
- 1M graph facts.
- Large alias map.
- Cache cold start.
- Concurrent searches during route rebuild.
- Route rebuild while ingestion updates rows.
- LLM fallback timeout.
- Many allowed datasets for one user.

### Response and compatibility

- Legacy list response without `include_debug`.
- Debug envelope response with new DCD fields.
- Strategy returns custom response shape.
- RAG strict grounded mode with no evidence.
- Streaming answer later needs pre-token validation.

## Test Architecture

### Unit tests

Resolver:

- exact domain/collection/document scoring
- alias scoring
- BM25 scoring
- score margin ambiguity
- confidence threshold
- owner/team/dataset filtering
- empty route tables
- malformed aliases JSON
- multilingual aliases

VSA boost:

- matching document wins
- wrong route does not remove candidate
- cross-dataset route does not boost
- missing route metadata no-op
- edge route metadata precedence over node route metadata
- confidence scales boost

Cache:

- cache key includes owner/team/datasets
- access change invalidates or misses
- route mutation invalidates
- stale allowed in observe
- stale denied in filter

### Integration tests

Search handler:

- observe debug for graph and non-graph strategies
- boost positive path
- boost wrong route path
- boost empty route path
- boost missing metadata path
- boost cross-tenant collision
- explicit dataset conflict
- AUTO query type then DCD

SQL/Neo4j:

- document widening
- collection widening
- domain widening
- dataset fallback
- SQL filler does not consume VSA reserve
- Neo4j and SQL semantics match

Validation:

- stale VSA edge excluded
- expired edge excluded
- tenant leak blocked
- no context -> abstain in strict mode

### Quantitative tests

Scenarios, at least 13 each:

- context budget
- fan-out
- predicate-specific
- distractor-heavy
- ambiguous route
- missing metadata
- tenant collision
- stale VSA
- multilingual alias
- broad query

Modes:

- global VSA
- observe
- boost
- scoped filter
- filter + widening
- oracle route
- wrong route
- empty route

Required metrics:

- route accuracy@1
- route recall@3
- fact recall@k
- target recall@k
- MRR
- nDCG@k
- predicate precision
- distractor rate
- tenant leak
- expired leak
- route latency p50/p95/p99
- VSA latency p50/p95/p99
- SQL filler latency p50/p95/p99
- total handler latency
- VSA probes/request
- SQL queries/request
- cache hit rate

## Rollout Gates

### Gate 1: Observe

Required:

- route debug present when requested
- legacy response unchanged
- route resolver p95 measured
- route resolver error rate measured
- default off

### Gate 2: Boost

Required:

- positive boost improves or preserves target rank: done in unit and
  `searchHandler` tests
- wrong route does not reduce recall below tolerance: covered for candidate
  preservation through `searchHandler`
- empty route equals no-route behavior: covered for candidate preservation
  through `searchHandler`
- missing metadata equals no-route behavior: covered for candidate preservation
  through `searchHandler`
- cross-dataset route does not boost: covered at helper level
- p95 overhead reported

### Gate 3: Cached resolver

Required:

- cache hit rate reported
- cold build time reported
- invalidation tests pass
- medium profile route p95 acceptable

### Gate 4: Scoped filter

Required:

- high-confidence filter improves distractor-heavy recall
- wrong route falls back/widens
- missing metadata falls back
- tenant and expired leak exactly 0
- SQL/Neo4j widening debug present

### Gate 5: Default enablement

Required:

- small and medium load reports
- quality lift stable
- latency budget accepted
- rollback tested with `LEVARA_DCD_ROUTER=off`
- CI covers non-race and selected race paths

## Recommended Next Implementation Order

1. Fix feature flag semantics:
   - `filter` should not pretend to be implemented.
   - Expose `filter_active=false` or reject/downgrade to boost.
   - Status: done; `filter` downgrades to effective `boost`.

2. Add negative-path boost tests:
   - wrong route
   - empty route
   - missing metadata
   - cross-dataset route
   - Status: done for wrong/empty/missing metadata through `searchHandler`;
     cross-dataset is covered at helper level.

3. Add debug accounting:
   - candidate count
   - boost enabled
   - boosted count
   - metadata hits/misses
   - filter active
   - Status: done for resolver debug and selected VSA context items; route
     cache and SQL/Neo4j widening counters remain pending.

4. Move DCD resolution after query-type routing:
   - only graph-capable strategies consume it.
   - AUTO should choose strategy first, then compute DCD.
   - Status: done for `GRAPH_COMPLETION`, `GRAPH_SUMMARY_COMPLETION`, and
     `GRAPH_COMPLETION_CONTEXT_EXTENSION`.

5. Implement cached route index:
   - cache key by owner/team/datasets.
   - route mutation invalidation.
   - stale policy by mode.

6. Implement multi-route decision:
   - top-1 only with high confidence and high margin.
   - top-2/top-3 when ambiguous.
   - global fallback on low confidence.

7. Implement filter and widening:
   - document -> collection -> domain -> dataset.
   - only after gates above pass.

8. Add validation:
   - stale VSA check.
   - tenant/expired check.
   - no evidence handling.

## Optimal Final State

The final production architecture should behave like this:

```text
default:
  LEVARA_DCD_ROUTER=off

safe rollout:
  LEVARA_DCD_ROUTER=observe

quality rollout:
  LEVARA_DCD_ROUTER=boost
  cached route index enabled
  debug counters enabled

filtered rollout:
  LEVARA_DCD_ROUTER=filter
  only high-confidence route decisions
  multi-route fallback
  SQL/Neo4j widening
  validation enabled
```

This gives Levara the DCD quality lift without sacrificing the existing access
model, VSA-before-SQL ordering, or rollback safety.
