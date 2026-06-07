# rag-with-temporal-kg

Demonstrates Levara's **temporal knowledge graph**: cognify two texts that
update the same exclusive relationship for one entity, then read both the
*current* state and a *historical* snapshot via the `query_entity` MCP tool.

## What it shows

1. `cognify` runs the LLM extraction pipeline (chunk → extract → embed → upsert).
2. The second cognify on the same `works_at` source auto-supersedes the
   prior edge (sets `valid_until`, `superseded_by`).
3. `query_entity(name)` returns only currently-active edges.
4. `query_entity(name, as_of=<earlier ISO timestamp>)` returns the snapshot
   as it was at that moment — the superseded edge is visible again.

`works_at` is one of the **exclusive** relationships hard-wired in
`pkg/orchestrator/pgupsert.go`. Other exclusive relations: `assigned_to`,
`role_is`, `status_is`, `located_in`, `lives_in`, `owns`, `reports_to`,
`current_state`, `is_a`. Adding domain-specific exclusivity is a deliberate
code change there.

## Run

```bash
docker compose up -d --build                                        # in repo root
cd examples/rag-with-temporal-kg
pip install -r requirements.txt
python main.py
```

Expected output (real run, qwen2.5:1.5b on CPU, ~7 min total):

```
→ Step 1: ingest first fact (Alice-1778291171 works at Acme)
  → run 934089ef done: 2 entities, 1 edges (126430ms)

→ Step 2: ingest the update (Alice-1778291171 now works at Globex)
  → run e8111aea done: 3 entities, 2 edges (297356ms)

--- query_entity(Alice-1778291171) — active edges now ---
  [ACTIVE ] works_at  src=2c8b7ec8 tgt=cb7f94c2  valid_from=...  valid_until=∞
  [ACTIVE ] left      src=2c8b7ec8 tgt=690c7235  valid_from=...  valid_until=∞

--- query_entity(Alice-1778291171, as_of=2026-05-09T01:48:20Z) — historical snapshot ---
  [HISTORY] works_at  src=2c8b7ec8 tgt=690c7235  valid_from=...  valid_until=...  superseded_by=...
```

The script suffixes the entity name with `int(time.time())` so each run
creates a fresh person and never collides with rows from earlier runs or
Levara's persistent LLM cache.

## Tuning

- **LLM**: docker compose defaults to `qwen2.5:1.5b` for footprint. Extraction
  quality at that size is rough and edge counts vary across runs. For
  production-style demos, set `LLM_MODEL=qwen2.5:7b` (or `gpt-4o-mini` with
  an API key) in `.env` before bringing the stack up.
- **Latency**: each `cognify` waits for one full LLM call. With a 1.5B model
  on CPU expect 30–120s per run. The script polls every 2s and times out
  after 600s.
- **Determinism**: small models occasionally emit synthetic IDs (`A1`,
  `B1`) for entities. The pipeline maps known names to UUIDs but cannot
  recover unknown synthetic IDs — they end up in `source_id`/`target_id`
  unchanged. Re-run if a step looks degenerate.

## Related

- [`examples/agent-memory-app/`](../agent-memory-app/) — minimal vector
  insert + search starter, no LLM.
