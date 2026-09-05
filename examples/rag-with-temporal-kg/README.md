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

## Prerequisites

Start a dedicated SQL-backed server using [getting started](../../docs/getting-started.md)
and add the LLM settings from [integrations](../../docs/integrations.md). Configure
SQL or Neo4j for durable graph state. The base Compose file starts Levara and
Prometheus only; it does not supply that complete graph/model environment.

The script hardcodes `http://localhost:8080`, so use that port on a dedicated
local instance. Match the vector dimension to the embedding service. A model
service inside a container needs an address reachable from that container.

The script sends no credentials, so use an isolated local test instance or
adapt its HTTP/MCP requests for auth. This example tests temporal extraction;
it is not a document-sharing or corporate identity acceptance test.

## Run

```bash
cd examples/rag-with-temporal-kg
pip install -r requirements.txt
python main.py
```

## Inspect the result

The script prints the two run results, current edges and an `as_of` historical
view. Require successful processing, then compare the actual extracted entities
and relations with the input. A particular LLM is not guaranteed to emit the
same relation spelling or number of edges every time. Supersession only applies
when the same source and an exclusive relation are actually persisted.

The script polls at two-second intervals with a 600-second limit. That is a
client timeout, not a latency target. It suffixes entity names with a timestamp
to reduce collisions across runs; it does not provide a general isolation or
rollback mechanism. Run against disposable data and inspect degenerate results
instead of repeatedly adding them to a shared graph.

## Related

- [`examples/agent-memory-app/`](../agent-memory-app/) — minimal vector
  insert + search starter, no LLM.
