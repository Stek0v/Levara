"""
Levara Memory-Palace MCP Tests — covers the mempalace-inspired memory features:
  • room/hall hierarchy on memories (incl. controlled hall vocabulary)
  • pin_memory / unpin_memory + wake_up bundle
  • query_entity with temporal validity (current vs as_of)
  • diary_write / diary_read (per-agent namespace)
  • tag filtering on add / list_data
  • search post-filter by room/tags

Run:
    pytest tests/test_mcp_palace.py -m smoke -v --mcp-url http://localhost:8080

These tests only need Levara server + DB. The wake_up + query_entity + search
filter tests are tolerant of empty graphs (they assert protocol/response shape,
not specific recall numbers, so they pass on a fresh deployment).
"""
import json
import uuid

import pytest

from conftest_mcp import MCPTestClient

pytestmark = [pytest.mark.smoke, pytest.mark.asyncio]


# ── helpers ─────────────────────────────────────────────────────────────────


def _text(mcp: MCPTestClient, result: dict) -> str:
    return mcp.tool_text(result)


def _is_error(mcp: MCPTestClient, result: dict) -> bool:
    return mcp.tool_error(result)


def _parse_json_text(mcp: MCPTestClient, result: dict):
    """Tools return either a JSON blob or a status string. Try to parse JSON."""
    txt = _text(mcp, result)
    try:
        return json.loads(txt)
    except (json.JSONDecodeError, ValueError):
        return None


# ── tools/list discovery ────────────────────────────────────────────────────


class TestPalaceToolsRegistered:
    """All new memory-palace tools are exposed via tools/list."""

    async def test_palace_tools_present(self, mcp):
        tools = await mcp.tools_list()
        names = {t["name"] for t in tools}
        for required in (
            "wake_up",
            "pin_memory",
            "unpin_memory",
            "query_entity",
            "diary_write",
            "diary_read",
        ):
            assert required in names, f"missing tool {required}: have {sorted(names)}"

    async def test_save_memory_schema_has_room_hall(self, mcp):
        tools = await mcp.tools_list()
        save = next(t for t in tools if t["name"] == "save_memory")
        props = save["inputSchema"]["properties"]
        assert "room" in props
        assert "hall" in props
        assert "pin" in props
        assert "pin_priority" in props


# ── room / hall on memories ─────────────────────────────────────────────────


class TestRoomHallMemories:

    async def test_save_with_room_hall(self, mcp, test_collection):
        result = await mcp.call_tool(
            "save_memory",
            {
                "key": "room_hall_basic",
                "value": "We use SQLite for tests, Postgres for prod",
                "collection": test_collection,
                "room": "infra",
                "hall": "decision",
            },
        )
        assert not _is_error(mcp, result), _text(mcp, result)
        assert "saved" in _text(mcp, result).lower()

    async def test_invalid_hall_rejected(self, mcp, test_collection):
        result = await mcp.call_tool(
            "save_memory",
            {
                "key": "bad_hall_key",
                "value": "should fail",
                "collection": test_collection,
                "hall": "rumor",  # not in vocab
            },
        )
        assert _is_error(mcp, result)
        msg = _text(mcp, result).lower()
        assert "invalid hall" in msg

    async def test_recall_filters_by_hall(self, mcp, test_collection):
        # Two memories: one decision, one preference. Search must filter.
        await mcp.call_tool(
            "save_memory",
            {
                "key": "auth_decision_jwt",
                "value": "Tokens are signed RS256",
                "collection": test_collection,
                "room": "auth",
                "hall": "decision",
            },
        )
        await mcp.call_tool(
            "save_memory",
            {
                "key": "auth_preference_lib",
                "value": "Prefer go-jwt over jwx",
                "collection": test_collection,
                "room": "auth",
                "hall": "preference",
            },
        )

        decisions = await mcp.call_tool(
            "recall_memory",
            {"query": "auth", "collection": test_collection, "hall": "decision"},
        )
        decisions_text = _text(mcp, decisions)
        assert "auth_decision_jwt" in decisions_text
        assert "auth_preference_lib" not in decisions_text

        prefs = await mcp.call_tool(
            "recall_memory",
            {"query": "auth", "collection": test_collection, "hall": "preference"},
        )
        prefs_text = _text(mcp, prefs)
        assert "auth_preference_lib" in prefs_text
        assert "auth_decision_jwt" not in prefs_text

    async def test_list_filters_by_room(self, mcp, test_collection):
        await mcp.call_tool(
            "save_memory",
            {
                "key": "deploy_note",
                "value": "Pi 5 systemd unit",
                "collection": test_collection,
                "room": "deploy",
                "hall": "fact",
            },
        )
        result = await mcp.call_tool(
            "list_memories",
            {"collection": test_collection, "room": "deploy"},
        )
        text = _text(mcp, result)
        assert "deploy_note" in text


# ── pin / unpin / wake_up ───────────────────────────────────────────────────


class TestPinAndWakeUp:

    async def test_pin_then_wake_up_returns_pinned(self, mcp, test_collection):
        key = f"pin_{uuid.uuid4().hex[:6]}"
        await mcp.call_tool(
            "save_memory",
            {
                "key": key,
                "value": "Levara HNSW dim=1024",
                "collection": test_collection,
                "hall": "fact",
            },
        )
        pin_res = await mcp.call_tool(
            "pin_memory", {"key": key, "priority": 10}
        )
        assert not _is_error(mcp, pin_res), _text(mcp, pin_res)

        wake = await mcp.call_tool(
            "wake_up", {"collection": test_collection, "max_tokens": 400}
        )
        bundle = _parse_json_text(mcp, wake)
        assert bundle is not None, _text(mcp, wake)
        assert "pinned" in bundle
        keys = [m.get("key") for m in (bundle.get("pinned") or [])]
        assert key in keys

    async def test_unpin_removes_from_wake_up(self, mcp, test_collection):
        key = f"unpin_{uuid.uuid4().hex[:6]}"
        await mcp.call_tool(
            "save_memory",
            {
                "key": key,
                "value": "ephemeral",
                "collection": test_collection,
            },
        )
        await mcp.call_tool("pin_memory", {"key": key, "priority": 5})
        await mcp.call_tool("unpin_memory", {"key": key})
        wake = await mcp.call_tool(
            "wake_up", {"collection": test_collection, "max_tokens": 400}
        )
        bundle = _parse_json_text(mcp, wake) or {}
        keys = [m.get("key") for m in (bundle.get("pinned") or [])]
        assert key not in keys

    async def test_wake_up_respects_token_budget(self, mcp, test_collection):
        # Pin several large memories and ensure response stays within budget.
        for i in range(5):
            k = f"big_{i}_{uuid.uuid4().hex[:4]}"
            await mcp.call_tool(
                "save_memory",
                {
                    "key": k,
                    "value": "x" * 200,
                    "collection": test_collection,
                },
            )
            await mcp.call_tool("pin_memory", {"key": k, "priority": 1})
        wake = await mcp.call_tool(
            "wake_up", {"collection": test_collection, "max_tokens": 60}
        )
        text = _text(mcp, wake)
        # Budget is approximate (chars/4); allow generous slack but not unbounded.
        assert len(text) <= 60 * 4 * 4, f"wake_up exceeded slack budget: {len(text)} chars"

    async def test_pin_unknown_key_errors(self, mcp):
        result = await mcp.call_tool(
            "pin_memory", {"key": f"missing_{uuid.uuid4().hex}"}
        )
        assert _is_error(mcp, result)


# ── query_entity ────────────────────────────────────────────────────────────


class TestQueryEntity:
    """query_entity is tolerant of empty graphs — we only assert shape."""

    async def test_query_unknown_entity(self, mcp):
        result = await mcp.call_tool(
            "query_entity", {"name": f"NoSuchEntity_{uuid.uuid4().hex}"}
        )
        # Either friendly "not found" message or empty edges list.
        text = _text(mcp, result).lower()
        assert "no entity found" in text or "edges" in text

    async def test_query_with_as_of_accepted(self, mcp):
        # Even on empty graph, the call should not error on syntax.
        result = await mcp.call_tool(
            "query_entity",
            {"name": "Maya", "as_of": "2025-01-01T00:00:00Z", "limit": 5},
        )
        assert not _is_error(mcp, result), _text(mcp, result)


# ── diary tools ─────────────────────────────────────────────────────────────


class TestDiary:

    async def test_diary_round_trip(self, mcp, test_collection):
        agent = f"reviewer_{uuid.uuid4().hex[:4]}"
        write = await mcp.call_tool(
            "diary_write",
            {
                "agent": agent,
                "key": "first_note",
                "value": "found duplicate test fixture in conftest_mcp",
                "collection": test_collection,
            },
        )
        assert not _is_error(mcp, write), _text(mcp, write)

        read = await mcp.call_tool(
            "diary_read", {"agent": agent, "collection": test_collection}
        )
        assert "duplicate test fixture" in _text(mcp, read)

    async def test_diary_namespace_isolated(self, mcp, test_collection):
        a, b = (
            f"architect_{uuid.uuid4().hex[:4]}",
            f"oncall_{uuid.uuid4().hex[:4]}",
        )
        await mcp.call_tool(
            "diary_write",
            {"agent": a, "key": "decision", "value": "pick HNSW", "collection": test_collection},
        )
        await mcp.call_tool(
            "diary_write",
            {"agent": b, "key": "incident", "value": "wal fsync stall", "collection": test_collection},
        )
        a_read = _text(mcp, await mcp.call_tool("diary_read", {"agent": a, "collection": test_collection}))
        b_read = _text(mcp, await mcp.call_tool("diary_read", {"agent": b, "collection": test_collection}))
        assert "pick HNSW" in a_read and "wal fsync stall" not in a_read
        assert "wal fsync stall" in b_read and "pick HNSW" not in b_read


# ── tag filter on add / list_data ───────────────────────────────────────────


class TestTagsAndRoomData:
    """Tag/room metadata travel through ingest into the data table."""

    async def test_add_with_tags_and_room(self, mcp):
        result = await mcp.call_tool(
            "add",
            {
                "data": "OCR benchmark notes about gemma4:e2b on Mac",
                "dataset_name": f"ds_{uuid.uuid4().hex[:6]}",
                "tags": ["ocr", "bench"],
                "room": "ocr-bench",
            },
        )
        assert not _is_error(mcp, result), _text(mcp, result)
        assert "ingested" in _text(mcp, result).lower()

    async def test_list_data_filters_by_tag(self, mcp):
        tag = f"unique_{uuid.uuid4().hex[:6]}"
        await mcp.call_tool(
            "add",
            {
                "data": f"piece tagged with {tag}",
                "dataset_name": f"ds_{uuid.uuid4().hex[:6]}",
                "tags": [tag],
            },
        )
        result = await mcp.call_tool("list_data", {"tags": [tag]})
        text = _text(mcp, result)
        # Tag should appear in the JSON output of at least one row.
        assert tag in text


# ── search filter shape (no embedding required) ─────────────────────────────


class TestSearchFilterShape:
    """We can't reliably test recall without LLM/embed in smoke,
    so we only assert that the search tool accepts room/tags args."""

    async def test_search_accepts_room_tags(self, mcp, test_collection):
        result = await mcp.call_tool(
            "search",
            {
                "search_query": "anything",
                "collection": test_collection,
                "room": "auth",
                "tags": ["mcp"],
                "top_k": 5,
            },
        )
        # Should not be a protocol error. Empty results are fine.
        assert not _is_error(mcp, result), _text(mcp, result)
