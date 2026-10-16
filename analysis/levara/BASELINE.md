# BASELINE — Phase 1 reference point

Recorded 2026-10-16 per `MODERNIZATION_BRIEF.md` Phase 1 entry criteria.
All subsequent phases must return to this state (green) before merge.

## Anchor

- Branch: `refactor/levara-core-1` (created from `v3`)
- Commit SHA (full suite green at this tree): `89fd6b2493453375d936b71c5e76dad5ff2ad974`
- Toolchain: go1.27.2 darwin/arm64; `go.mod` directive `go 1.26.6` (bump to
  1.27.2 approved for Phase 4, §7)

## Generated contract hashes (must be byte-identical after Phase 1)

```
db130ff2be8f37c4adb32bfe8fdc00eea7ddaa1bfba07ef208fc19c75524e1c1  docs/contract.json      (66191 B)
dc5b216cee55c8fc3e1425155687e899a92afb7976a1c70465bde459b4c9c856  docs/contract-core.json (59273 B)
```

## Suite profile (measured 2026-10-16, this tree)

- `go test -count=1 ./...` = **373.44s wall / 182.06s user / 101.25s sys**
- 61 packages `ok`, 9 `no test files`, 1 failure = known timing flake
  `pkg/community TestLouvain_1K_Performance` (passes in isolation, 0.7s;
  re-run in isolation before treating any red as real)
- Slowest: `internal/http` 315.7s, `internal/store` 56.2s, `pkg/mcp` 41.5s,
  `cmd/server` 37.8s, `pkg/access` 32.3s, `pkg/backup` 32.0s, `cmd/cli` 30.6s

## P0 rule → equivalence vehicle mapping

| P0 rule | Vehicle (file anchors; exact test IDs enumerated at execution) |
|---|---|
| RULE-001 hall vocabulary | `pkg/memoryhall` tests; hall-validation regressions (ledger I06, both SQL) |
| RULE-015 same-key archive | `pkg/mcp/tool_save_recall_memory_test*` archive/retire cases |
| RULE-016 superseded hidden / includeSuperseded asymmetry | recall tests, both branches (asymmetry tests ADDED in Phase 1 per B2) |
| RULE-017 exclusive-rel supersede | `pkg/orchestrator` upsert tests (pgupsert) |
| RULE-018 deduped single-tx upsert | same |
| RULE-021 recall never fails on embed errors | `pkg/embed` breaker tests + degradation/recall fallback tests |
| RULE-023 filters/tags dual contract | `internal/http` integration suite (ledger: integration506, T10) + contract byte-drift gate |
| RULE-027 owner-scoped LIKE fallback | owner-isolation tests (ledger I02/I06 family) |
| B3 invariant (API-key narrows only) | **LANDED 2026-10-16:** `pkg/access/apikey_invariant_test.go` (semantics table + 15 targeted narrow/widen cases + 112-combination exhaustive conjunction oracle) and `internal/http/workspace_actor_test.go` (REST Fiber-locals adapter, unauthenticated defaults, MCP context adapter). gRPC has NO API-key authn path (zero APIKey refs under internal/grpc) — invariant vacuous there, documented in the test header. Package suites green: `pkg/access` 8.9s full, `internal/http` targeted. **Finding surfaced by the tests:** RULE-007 matrix corrected — `normalizeAction` folds delete/share into write-class (finding M25), editor effectively holds write+delete+share; re-confirm Q10 filed. |

## Dead-code cross-check verdict (Phase 1 entry criterion; REVISES Phase 1b scope)

Method: `check_references` per symbol + `pkg/mcp/deps.go` interface
reading (recorded in session log 2026-10-16).

| Candidate (from ASSESSMENT Debt-3) | Verdict | Action |
|---|---|---|
| `internal/store/heap.go` MaxHeap+MinHeap (incl. Push pair) | **NOT DEAD — build gate caught it 2026-10-16**: the file defines `searchResult` + `srMinHeap`/`srMaxHeap` used by the live `internal/store/hnsw.go:423-477` (HNSW top-k selection). The zero-reference check had queried the wrong symbol name (`MaxHeap` vs the real `srMaxHeap`), and `container/heap` invokes `Push` through `heap.Interface` — a second dispatch blind spot. **Deletion attempted, build broke, restored from HEAD, suite green again.** | KEEP. No deletion. |
| `internal/grpc/service.go` ListCollections:160, HasCollection:164 (v1) | **NOT dead** — gRPC server methods are wire-reachable via the registered service descriptor regardless of in-repo callers | Document only (owner decision §7, 2026-10-16) |
| `internal/http/mcp.go` + `mcp_embed.go` "dead" methods (CollectionExists, CollectionInsert, CollectionInsertDeferredHook, CollectionHasRecord, OntologyPromptSuffix, PipelineFinalizesStatus, RerankEnabled, ListLexicalCollections, LogHeartbeat, EmbedBatch; EmbedAvailable false-positive) | **NOT deletable unilaterally** — required members of the `Deps` interface family (`pkg/mcp/deps.go:31-107`: CollectionDeps, PipelineDeps, SearchDeps, ObservabilityDeps, EmbedDeps); handler implements them; fakes in `deps_test.go` too. They are *unused interface surface* (no current tool calls them via the interface), not dead code. Note: `EmbedAvailable` IS invoked through the interface (`ToolSaveMemory` calls `deps.EmbedAvailable()`) — gortex dead-flag was an interface-dispatch blind spot | Reclassify as "interface-slimming candidates": each removal = coordinated edit of `deps.go` + all impls + fakes, compile+tests+contract gate. Default for Phase 1: record list, DO NOT remove (QUALITY-FIRST minimal steps); slimming = separate reviewed micro-change if desired |
| `benchmark/memory_eval/run_memory_eval.py:143` | out of Go scope | noted only |

This is a pilot-class finding recorded before the pilot extraction: the
ASSESSMENT's dead-code sweep overcounted because interface-satisfaction
and wire-dispatch targets look dead to a static caller graph. **Final
Phase 1(b) outcome: the verified-dead set in the Go core is EMPTY — every
flagged candidate is live (interface-required, wire-reachable, or a
support type for live code). Phase 1(b) executes no deletions.**

## Working discipline (owner rule, hard)

All test runs in the isolated environment on `refactor/levara-core-1`.
No prod instance (Mac :8081 / Pi / embed :9101) is restarted, redeployed
or pointed at this branch.
