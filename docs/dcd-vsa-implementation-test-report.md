# DCD/VSA Implementation Test Report

Date: 2026-06-18

Scope: DCD route metadata, route resolver, `searchHandler` observe/boost
integration, VSA-before-SQL graph context, predicate synonym map, and VSA store
route metadata propagation.

## What Was Tested

| Layer | Tests | What it proves |
|---|---|---|
| Schema | `TestDCDRouteSchemaMigrationIsIdempotent`, `TestDCDRouteRowsAreScopedByOwnerTeamAndDataset` | Route tables and graph route columns migrate idempotently and remain owner/team/dataset scoped. |
| Resolver | `TestResolveDCDRouteCandidates*` | Exact/alias/BM25 route ranking works, threshold/limit are applied, and foreign owner/team/dataset rows are not returned. |
| Observe mode | `TestSearchHandlerDCDRouteObserve*` | `LEVARA_DCD_ROUTER=observe` attaches debug metadata through `searchHandler`; default mode stays off; legacy array responses remain unchanged when `include_debug=false`. |
| Boost mode | `TestRerankVSACandidatesByDCDRouteBoostsMatchingDocument`, `TestSearchHandlerDCDRouteBoostReranksVSAContext` | Route confidence improves ranking inside the VSA candidate set without changing VSA-before-SQL ordering. |
| VSA before SQL | `TestVSAQuantitativeEval`, `TestVSABeforeSQLGraphPreservesTargetUnderBudget`, graph search e2e tests | VSA-first protects relevant facts from being hidden by SQL filler under context budget. |
| Predicate synonyms | `TestPredicateSynonymMapLoadQualityAndSpeed`, `TestGraphCompletionSearch_VSASynonymMapRanksPredicate` | Synonym map improves predicate selection and remains fast enough for the current synthetic load. |
| Safety | RBAC/dataset filter tests, tenant/expired leak metrics in evals | No tenant leaks and no expired fact leaks in the tested fixtures. |
| Race | `go test -race` on touched VSA packages and DCD/VSA HTTP tests | No race detected in the implemented layer. |

## Commands

```bash
LEVARA_WRITE_DCD_VSA_BASELINE_REPORT=1 \
LEVARA_WRITE_DCD_VSA_LOAD_REPORT=1 \
LEVARA_DCD_VSA_LOAD_CASES=10000 \
go test ./internal/http -run 'TestDCDVSA(BaselineEval|LoadBaseline)$' -count=1 -v

LEVARA_WRITE_VSA_EVAL_REPORT=1 \
go test ./internal/http -run 'TestVSAQuantitativeEval|TestVSABeforeSQLGraphPreservesTargetUnderBudget' -count=1 -v

LEVARA_WRITE_GRAPH_CONTEXT_ARCH_REPORT=1 \
go test ./internal/http -run 'TestSearchHandlerGraphContextArchitectureEval' -count=1 -v

LEVARA_WRITE_PREDICATE_SYNONYM_LOAD_REPORT=1 \
go test ./internal/http -run 'TestPredicateSynonymMapLoadQualityAndSpeed' -count=1 -v

go test ./internal/http -run 'Test(DCDRoute|ResolveDCDRouteCandidates|SearchHandlerDCDRoute|RerankVSACandidatesByDCDRoute|GraphCompletionSearch_VSA|ContextExtensionSearch_VSA|VSAAPI|VSAGraphContext|VSABeforeSQL|PredicateSynonym)' -count=1 -v

go test -race ./pkg/vsamemory ./pkg/vsa -count=1

go test -race ./internal/http -run 'Test(DCDRoute|ResolveDCDRouteCandidates|SearchHandlerDCDRoute|RerankVSACandidatesByDCDRoute|GraphCompletionSearch_VSA|ContextExtensionSearch_VSA|VSABeforeSQL|PredicateSynonym)' -count=1
```

## Quantitative Results

### DCD/VSA baseline

52 cases total: 13 each for `context_budget`, `fanout`,
`predicate_specific`, and `distractor_heavy`.

| Mode | fact recall@k | MRR | nDCG@k | tenant leak | expired leak | p95 |
|---|---:|---:|---:|---:|---:|---:|
| `global_vsa_first` | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 2us |
| `oracle_route_vsa` | 1.000 | 1.000 | 1.000 | 0.000 | 0.000 | 30us |
| `wrong_route_vsa` | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 3us |
| `empty_route_vsa` | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 19us |

Result: correct DCD narrowing is the quality signal. Wrong or empty routes do
not leak tenants, but they also do not recover the target in this synthetic
setup.

### DCD/VSA load baseline

10,000 queries.

| Mode | p50 | p95 | p99 | qps |
|---|---:|---:|---:|---:|
| `global_vsa_first` | 0us | 7us | 41us | 302,438 |
| `oracle_route_vsa` | 1us | 10us | 62us | 210,535 |
| `wrong_route_vsa` | 0us | 7us | 33us | 385,438 |
| `empty_route_vsa` | 0us | 4us | 30us | 518,300 |

Result: route filtering overhead in the synthetic in-memory harness is low.

### VSA-before-SQL quantitative eval

| Mode | fact recall@k | target recall@k | MRR | nDCG@k | predicate precision | tenant leak | expired leak | p95 |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| `baseline_sql_graph` | 0.000 | 0.000 | 0.000 | 0.000 | 0.914 | 0.000 | 0.000 | 470us |
| `current_append` | 0.000 | 0.000 | 0.000 | 0.000 | 0.914 | 0.000 | 0.000 | 499us |
| `vsa_first` | 1.000 | 1.000 | 1.000 | 1.000 | 0.971 | 0.000 | 0.000 | 237,915us |
| `vsa_only` | 1.000 | 1.000 | 1.000 | 1.000 | 1.000 | 0.000 | 0.000 | 212,081us |

Direct budget test: VSA-first found the target in 52/52 cases; SQL-before-VSA
found 0/52.

Result: quality improvement is decisive, but VSA-first latency is the main
optimization target.

### `searchHandler` graph-context architecture eval

25 e2e search-handler cases.

| Mode | target recall@k | MRR | nDCG@k | predicate precision | VSA avg | SQL avg | tenant leak | expired leak | p95 | qps |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| `sql_first` | 0.000 | 0.000 | 0.000 | 0.000 | 0 | 8 | 0.000 | 0.000 | 1,709us | 1,056 |
| `sql_only` | 0.000 | 0.000 | 0.000 | 0.000 | 0 | 8 | 0.000 | 0.000 | 2,296us | 558 |
| `vsa_first` | 1.000 | 1.000 | 1.000 | 1.000 | 4 | 4 | 0.000 | 0.000 | 134,063us | 8.9 |
| `vsa_only` | 1.000 | 1.000 | 1.000 | 1.000 | 4 | 0 | 0.000 | 0.000 | 193,409us | 6.8 |

Result: end-to-end search confirms the same quality lift and the same latency
cost profile as the lower-level VSA eval.

### Predicate synonym map load

| cases | predicates | synonyms | top1 | MRR | p50 | p95 | max | qps | refresh | load |
|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 1,600 | 800 | 16,400 | 1.000 | 1.000 | 5,612us | 10,386us | 38,181us | 157 | 242ms | 49ms |

Result: predicate selection quality is perfect on the synthetic benchmark; p95
is acceptable for now, but should be monitored under real predicate cardinality.

## Interpretation

1. The implemented layer is functionally safe in tested cases: tenant leaks and
   expired fact leaks are zero across quantitative evals.
2. The DCD route signal is strong: with the correct route, recall/MRR/nDCG move
   from 0.0 to 1.0 in the adversarial synthetic suite.
3. `observe` mode is safe to ship first because it only adds debug metadata when
   requested and leaves normal responses unchanged.
4. `boost` mode is the correct next rollout step because it improves ranking
   inside VSA candidates without changing the VSA-before-SQL architecture.
5. `filter` mode should remain disabled until we add route-index caching,
   wrong-route degradation tests, and production-like load numbers. The current
   quality lift is clear, but VSA-first latency is still high.

## Artifacts

- `benchmark/results/dcd_vsa_baseline_latest.json`
- `benchmark/results/dcd_vsa_load_baseline_latest.json`
- `benchmark/results/vsa_quantitative_eval_latest.json`
- `benchmark/results/graph_context_architecture_eval_latest.json`
- `benchmark/results/predicate_synonym_load_latest.json`
- `docs/dcd-vsa-baseline-report.md`
- `docs/dcd-vsa-load-baseline-report.md`
- `docs/vsa-quantitative-eval-report.md`
- `docs/graph-context-architecture-eval-report.md`
- `docs/predicate-synonym-load-report.md`
