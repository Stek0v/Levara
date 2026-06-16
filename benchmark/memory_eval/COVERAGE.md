# Levara Memory Eval — Coverage Matrix

Maps the Mem0/Zep/LangMem evaluation framework to what this harness tests today,
what lives elsewhere in the repo, and planned gaps.

## Layer model

| Layer | Question | Harness |
|-------|----------|---------|
| **L0** | MCP contract / tools exist | cat 5, 8 |
| **L1** | CRUD + PG persist | cat 1 |
| **L2** | Retrieval quality (golden) | cat 2 |
| **L3** | Latency smoke | cat 3 |
| **L4** | Consolidation / wake_up | cat 4 |
| **L5** | Tenant / owner isolation | cat 6, 10 |
| **L6** | Session continuity | cat 9 |
| **L7** | Context budget | cat 11 |
| **L8** | Scale smoke | cat 12 |
| **L9** | Agent e2e + LLM judge | `tests/test_mcp_e2e.py` (partial) |
| **L10** | Load / 1M memories | not automated |

## Category map (12 × 0–3 = 36 pts max)

| Cat | Name | Mem0/Zep block | In harness |
|-----|------|----------------|------------|
| 1 | CRUD + types | §1 Functional | semantic/episodic/procedural, upsert, delete |
| 2 | Retrieval | §2 Quality | R@3, P@3, NDCG@3, MRR, Hit; ~25 golden queries |
| 3 | Latency | §3 Performance | p50/p95 save+recall, 10-way burst |
| 4 | Consolidation | §4 Forgetting | dry_run, wake_up+pin |
| 5 | Integration | §5 LLM/agent | MCP tools, diary, instructions |
| 6 | Collection isolation | §6 Multi-tenant | cross-collection recall |
| 7 | Edge cases | §7 Resilience | hall, unicode, contradiction, long text |
| 8 | Observability | §8 Checklist | doctor, runtime_stats, heartbeat |
| 9 | Cross-session | §2 cross-session | reconnect MCP → recall |
| 10 | Owner isolation | §6 Multi-tenant | JWT user A vs B (`--auth`) |
| 11 | Context efficiency | §3 tokens | wake_up budget trim |
| 12 | Scale smoke | §3 scale | N saves → recall latency |

## Go unit / integration coverage (memory layer)

| Package | Stmt coverage | Memory-related tests |
|---------|---------------|----------------------|
| `pkg/mcp` | **88.7%** | `tool_save_recall_memory_test.go` (25 tests), `tool_memory_test.go` (29) |
| `internal/http` | **54.5%** | `sync_memory_test.go`, `memory_events_*`, `mcp_reconcile_memory_test.go` |

**Well covered in Go:** `ToolSaveMemory`, `ToolRecallMemory` (vector + SQL paths), consolidate dry_run, pin/wake_up at pkg layer, collection_name upsert, owner scope.

**Thin in Go (HTTP wiring only):** `internal/http/mcp_palace.go` tool handlers (0% — exercised via MCP pytest instead), REST `memories.go` handlers (~4–11%).

**New pytest layer (cats 9–12):** `tests/test_memory_eval_extended.py` — cross-session, owner isolation, wake_up budget, scale smoke.

## Elsewhere in repo

| Area | Location |
|------|----------|
| Go unit tests (memory tools) | `pkg/mcp/tool_*_memory*_test.go` |
| Palace MCP pytest | `tests/test_mcp_palace.py` |
| MCP integration / stress | `tests/test_mcp_integration.py`, `test_mcp_stress.py` |
| Consolidate engine | `pkg/consolidate/*_test.go` |
| Full product scenarios | `docs/full-testing-scenarios.md` |
| Search relevance (non-memory) | `internal/http` eval tests |

## Known gaps (not in harness)

- LoCoMo / LongMemEval import
- LLM-as-judge e2e dialog (10-turn → answer correctness)
- TTL / decay automatic expiry
- consolidate **apply** (non-dry_run) on prod collections
- BM25 vs vector A/B in memory recall
- Cost per 1M memories
- Crash recovery / backup restore of memories
- Write-time noise filter (agent policy, not server)

## Running full matrix

```bash
bash benchmark/memory_eval/run_all_hosts.sh
# or single host with auth for cat 10:
python3 benchmark/memory_eval/run_memory_eval.py \
  --url http://localhost:8081 --label local --auth \
  --scale-memories 50 -v
```
