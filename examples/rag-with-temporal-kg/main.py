"""rag-with-temporal-kg — cognify + temporal knowledge graph demo.

Runs against the local stack started by `make stack-dev`. Ingests two
texts that update the same exclusive relationship (`works_at`) for one
entity, then demonstrates Levara's temporal knowledge graph by:

  1. Reading the active state via the `query_entity` MCP tool.
  2. Time-travelling with `as_of` to see the state at an earlier point.

The pipeline (chunk → LLM-extract → embed → upsert) auto-supersedes the
prior `works_at` edge when the second text arrives, so the graph carries
both the current edge (valid_until is NULL) and the historical edge
(valid_until set, superseded_by populated).

Provider tip: extraction quality depends on the LLM. The default stack
ships qwen2.5:1.5b for footprint reasons — it works but may need a few
retries to produce stable JSON. For richer extraction, set LLM_MODEL to
something larger (e.g. qwen2.5:7b) before `make stack-dev`.
"""

from __future__ import annotations

import json
import sys
import time
from datetime import datetime, timezone
from typing import Any

import requests

LEVARA_URL = "http://localhost:8080"
COGNIFY_POLL_SECONDS = 2
COGNIFY_TIMEOUT_SECONDS = 600


def cognify(text: str) -> str:
    """Trigger pipeline; block until COMPLETED. Returns the run id."""
    r = requests.post(
        f"{LEVARA_URL}/api/v1/cognify",
        json={"texts": [text]},
        timeout=10,
    )
    r.raise_for_status()
    run_id = r.json()["pipeline_run_id"]

    deadline = time.time() + COGNIFY_TIMEOUT_SECONDS
    while time.time() < deadline:
        s = requests.get(f"{LEVARA_URL}/api/v1/cognify/{run_id}/status", timeout=10).json()
        if s["status"] == "COMPLETED":
            print(f"  → run {run_id[:8]} done: "
                  f"{s['entities_extracted']} entities, {s['edges_extracted']} edges "
                  f"({s['elapsed_ms']}ms)")
            return run_id
        if s["status"] == "FAILED":
            raise RuntimeError(f"cognify failed: {s.get('message')}")
        time.sleep(COGNIFY_POLL_SECONDS)
    raise TimeoutError(f"cognify {run_id} did not complete within {COGNIFY_TIMEOUT_SECONDS}s")


def mcp_call(tool: str, arguments: dict[str, Any]) -> dict[str, Any]:
    """Invoke an MCP tool over the JSON-RPC endpoint. Returns parsed payload."""
    r = requests.post(
        f"{LEVARA_URL}/mcp",
        json={"jsonrpc": "2.0", "id": 1, "method": "tools/call",
              "params": {"name": tool, "arguments": arguments}},
        timeout=15,
    )
    r.raise_for_status()
    body = r.json()
    if "error" in body:
        raise RuntimeError(f"MCP error: {body['error']}")
    text = body["result"]["content"][0]["text"]
    if body["result"].get("isError"):
        raise RuntimeError(f"tool {tool} failed: {text}")
    return json.loads(text)


def print_edges(label: str, payload: dict[str, Any]) -> None:
    print(f"\n--- {label} ---")
    edges = payload.get("edges") or []
    if not edges:
        print("  (no edges)")
        return
    for e in edges:
        active = "ACTIVE " if not e["valid_until"] else "HISTORY"
        sup = f"  superseded_by={e['superseded_by'][:8]}" if e["superseded_by"] else ""
        print(f"  [{active}] {e['relationship']:<12}"
              f" src={e['source_id'][:8]} tgt={e['target_id'][:8]}"
              f"  valid_from={e['valid_from'][:19]}"
              f"  valid_until={e['valid_until'][:19] if e['valid_until'] else '∞'}"
              f"{sup}")


def main() -> int:
    # Unique person name per run sidesteps Levara's LLM cache and any
    # graph rows persisted from earlier runs — both texts go through the
    # full extraction pipeline every time.
    person = f"Alice-{int(time.time())}"

    print(f"→ Step 1: ingest first fact ({person} works at Acme)")
    cognify(f"{person} works at Acme. {person} is a senior engineer.")

    t_between = datetime.now(timezone.utc).isoformat()
    # The auto-supersession update fires inside the same tx as the new edge,
    # so we need a small gap between the two cognify runs to make `as_of`
    # snapshots strictly distinguishable.
    time.sleep(2)

    print(f"\n→ Step 2: ingest the update ({person} now works at Globex)")
    cognify(f"{person} works at Globex now. {person} left Acme last month.")

    state_now = mcp_call("query_entity", {"name": person})
    print_edges(f"query_entity({person}) — active edges now", state_now)

    state_then = mcp_call("query_entity", {"name": person, "as_of": t_between})
    print_edges(f"query_entity({person}, as_of={t_between[:19]}Z) — historical snapshot", state_then)

    print("\n→ Done. The active view drops the old works_at edge once a new one")
    print("  arrives (works_at is in the exclusive-relations list); the as_of")
    print("  snapshot still sees it because supersession is non-destructive.")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except requests.HTTPError as exc:
        print(f"HTTP error: {exc.response.status_code} {exc.response.text}", file=sys.stderr)
        sys.exit(1)
    except requests.ConnectionError:
        print("Cannot reach Levara — run `make stack-dev` first.", file=sys.stderr)
        sys.exit(1)
