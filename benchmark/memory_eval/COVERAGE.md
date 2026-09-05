# Levara Memory Eval — Test Coverage Map

This map describes checks present in the harness and related suites. It does
not report current statement coverage or benchmark results. See
[testing evidence](../../docs/testing.md) for observed runs and their limits.

## Layers

| Layer | Question | Harness |
|-------|----------|---------|
| L0 | MCP tools exist | categories 5, 8 |
| L1 | CRUD persistence on selected backend | category 1 |
| L2 | Retrieval on the golden corpus | category 2 |
| L3 | Latency smoke | category 3 |
| L4 | Consolidation dry-run and wake_up | category 4 |
| L5 | Collection and owner scope | categories 6, 10; owner requires auth |
| L6 | Session continuity | category 9 |
| L7 | Context budget | category 11 |
| L8 | Scale smoke | category 12 |
| L9 | Agent dialogue and answer correctness | separate `tests/test_mcp_e2e.py`; not established by this score |
| L10 | Sustained load or one million memories | not established by the default smoke run |

## Categories (12 × 0–3 = 36 points maximum)

| Category | Checks |
|----------|--------|
| 1. CRUD + types | semantic/episodic/procedural fixtures, upsert, delete |
| 2. Retrieval | R@3, P@3, NDCG@3, MRR, Hit; golden v2 has 15 cases and 19 queries |
| 3. Latency | save/recall percentiles, 10-way burst |
| 4. Consolidation | dry-run, wake_up and pin |
| 5. Integration | MCP tools, diary and instructions |
| 6. Collection scope | cross-collection recall |
| 7. Edge cases | hall, Unicode, contradiction and long text |
| 8. Observability | doctor, runtime_stats and heartbeat |
| 9. Cross-session | reconnect MCP and recall |
| 10. Owner scope | JWT user A versus B with `--auth` |
| 11. Context efficiency | wake_up budget trimming |
| 12. Scale smoke | N saves and recall latency |

A score of 36/36 represents category points. It is not 100% retrieval quality,
100% code coverage, or complete tenant isolation. Collection separation and
JWT owner checks cover different boundaries. Inspect per-query results and
skipped embedding/auth cases before comparing runs.

## Related suites

| Area | Location |
|------|----------|
| Go memory tools | `pkg/mcp/tool_save_recall_memory_test.go`, `pkg/mcp/tool_memory_test.go` and related tests |
| HTTP memory and sync | `internal/http/sync_memory_test.go`, `memory_events_*`, `mcp_reconcile_memory_test.go` |
| Palace MCP integration | `tests/test_mcp_palace.py` |
| MCP integration / stress | `tests/test_mcp_integration.py`, `tests/test_mcp_stress.py` |
| Extended harness scenarios | `tests/test_memory_eval_extended.py` |
| Consolidation engine | `pkg/consolidate/*_test.go` |
| Document, identity and sharing acceptance | [document scenario matrix](../../docs/document-workflow-scenarios.md) |
| Search relevance | evaluation tests in `internal/http` |

Statement coverage must be measured for a specific revision and command with
its coverage artifact. This guide does not retain undated package percentages
or infer coverage from the existence of a test file.

## Not established by this harness

- LoCoMo / LongMemEval results or cross-product comparisons;
- multi-turn answer correctness judged against ground truth;
- TTL/decay enforcement or consolidation apply behavior;
- BM25 versus vector A/B in memory recall;
- costs at one million memories;
- crash recovery, backup restore and independent-node synchronization;
- all document, group, tenant or organization authorization paths;
- whether an agent consistently chooses what to save and recall.

Run against a disposable, explicitly chosen target using the
[README procedure](README.md). Do not point benchmark host scripts at production.
