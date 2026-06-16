"""
Extended memory eval scenarios (categories 9–12) as pytest integration tests.

Run against a live Levara instance:
  pytest tests/test_memory_eval_extended.py -m integration -v --mcp-url http://localhost:8081
"""
from __future__ import annotations

import json
import os
import uuid

import pytest

from conftest_mcp import MCPTestClient


pytestmark = [pytest.mark.integration]


async def _save(client: MCPTestClient, coll: str, key: str, value: str, **extra) -> None:
    args = {
        "key": key,
        "value": value,
        "collection": coll,
        "room": extra.get("room", "test"),
        "hall": extra.get("hall", "fact"),
    }
    args.update({k: v for k, v in extra.items() if k not in ("room", "hall")})
    r = await client.call_tool("save_memory", args)
    assert not r.get("isError"), r


def _rows(result: dict) -> list[dict]:
    sc = result.get("structuredContent") or {}
    if sc.get("results"):
        return sc["results"]
    text = (result.get("content") or [{}])[0].get("text", "")
    try:
        data = json.loads(text)
        return data.get("results", data if isinstance(data, list) else [])
    except json.JSONDecodeError:
        return []


@pytest.mark.asyncio
async def test_cross_session_recall_after_reconnect(fresh_mcp: MCPTestClient):
    """Cat 9: same JWT user survives MCP session teardown."""
    coll = f"pytest_xs_{uuid.uuid4().hex[:8]}"
    email = os.environ.get("LEVARA_TEST_EMAIL", "memeval_xs@bench.local")
    password = os.environ.get("LEVARA_TEST_PASSWORD", "MemEval_Bench_Local_2026")
    await fresh_mcp.login_or_register(email, password)

    await _save(
        fresh_mcp,
        coll,
        "cross_session_fact",
        "Session boundary test: warehouse location is Building C aisle 12",
    )
    await fresh_mcp.close()
    await fresh_mcp.login_or_register(email, password)
    await fresh_mcp.connect()

    r = await fresh_mcp.call_tool(
        "recall_memory", {"query": "Building C aisle", "collection": coll}
    )
    rows = _rows(r)
    assert any(row.get("key") == "cross_session_fact" for row in rows), rows


@pytest.mark.asyncio
async def test_owner_isolation_between_jwt_users(fresh_mcp: MCPTestClient):
    """Cat 10: user B must not see user A's owner-scoped memory."""
    coll = f"pytest_owner_{uuid.uuid4().hex[:8]}"
    email_a = os.environ.get("LEVARA_TEST_EMAIL", "memeval@bench.local")
    pwd_a = os.environ.get("LEVARA_TEST_PASSWORD", "MemEval_Bench_Local_2026")
    await fresh_mcp.login_or_register(email_a, pwd_a)
    await _save(
        fresh_mcp,
        coll,
        "owner_classified",
        "Project Phoenix clearance level 5 — user-specific secret",
        room="classified",
    )

    other = MCPTestClient(fresh_mcp.base_url)
    email_b = os.environ.get("LEVARA_TEST_EMAIL_B", "memeval_b@bench.local")
    pwd_b = os.environ.get("LEVARA_TEST_PASSWORD_B", "MemEval_Bench_Local_B_2026")
    await other.login_or_register(email_b, pwd_b)
    await other.connect()
    try:
        r = await other.call_tool(
            "recall_memory", {"query": "Phoenix clearance", "collection": coll}
        )
        text = json.dumps(_rows(r))
        assert "Phoenix" not in text and "clearance level 5" not in text
    finally:
        await other.close()


@pytest.mark.asyncio
async def test_wake_up_respects_token_budget(fresh_mcp: MCPTestClient):
    """Cat 11: pinned memories trim to max_tokens."""
    coll = f"pytest_ctx_{uuid.uuid4().hex[:8]}"
    for n in range(6):
        await _save(
            fresh_mcp,
            coll,
            f"pin_{n}",
            f"Pinned infra fact #{n}: production shard {n} runs on node-{n}.",
            pin=True,
            pin_priority=5 + n,
        )
    r = await fresh_mcp.call_tool("wake_up", {"max_tokens": 120, "collection": coll})
    sc = r.get("structuredContent") or {}
    if not sc:
        sc = json.loads((r.get("content") or [{}])[0].get("text", "{}"))
    assert sc.get("tokens_used", 9999) <= 125
    assert len(sc.get("pinned") or []) > 0


@pytest.mark.asyncio
async def test_scale_recall_smoke(fresh_mcp: MCPTestClient):
    """Cat 12: recall still hits after N sequential saves."""
    coll = f"pytest_scale_{uuid.uuid4().hex[:8]}"
    prefix = f"scale_{uuid.uuid4().hex[:6]}"
    n = 20
    for i in range(n):
        await _save(
            fresh_mcp,
            coll,
            f"{prefix}_{i}",
            f"Scale bench memory item {i} unique token {prefix}",
        )
    mid = n // 2
    r = await fresh_mcp.call_tool(
        "recall_memory", {"query": f"{prefix}_{mid}", "collection": coll}
    )
    rows = _rows(r)
    assert any(row.get("key") == f"{prefix}_{mid}" for row in rows), rows
