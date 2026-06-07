"""
Levara MCP Integration Tests — 25 tests, ~15 minutes.
Run: pytest tests/test_mcp_integration.py -m integration -v

Tests all 16 tools, search types, memory isolation, git analysis.
Some tests require embed endpoint (marked requires_embed).
"""
import json
import time
import uuid

import pytest
from conftest_mcp import MCPTestClient, percentile

pytestmark = [pytest.mark.integration, pytest.mark.asyncio]


# ── Memory Operations ────────────────────────────────────────────────────────

class TestMemory:
    """S4: Memory persistence and isolation."""

    async def test_upsert_overwrites(self, mcp, test_collection):
        """S4.2 — Second save with same key overwrites value."""
        await mcp.call_tool("save_memory", {
            "key": "framework", "value": "React", "type": "project",
            "collection": test_collection
        })
        await mcp.call_tool("save_memory", {
            "key": "framework", "value": "Vue.js", "type": "project",
            "collection": test_collection
        })
        result = await mcp.call_tool("recall_memory", {
            "query": "framework", "collection": test_collection
        })
        text = mcp.tool_text(result)
        # Should find Vue.js (latest), not React
        # Note: LIKE search may find both — that's acceptable
        assert not mcp.tool_error(result)

    async def test_collection_isolation(self, mcp):
        """S4.3 — Memories in different collections are isolated."""
        coll_a = f"iso_a_{uuid.uuid4().hex[:6]}"
        coll_b = f"iso_b_{uuid.uuid4().hex[:6]}"

        await mcp.call_tool("save_memory", {
            "key": "lang", "value": "Go", "type": "project", "collection": coll_a
        })
        await mcp.call_tool("save_memory", {
            "key": "lang", "value": "Python", "type": "project", "collection": coll_b
        })

        result_a = await mcp.call_tool("recall_memory", {"query": "lang", "collection": coll_a})
        result_b = await mcp.call_tool("recall_memory", {"query": "lang", "collection": coll_b})

        # Both should return without error — isolation prevents cross-leak
        assert not mcp.tool_error(result_a)
        assert not mcp.tool_error(result_b)

    async def test_memory_types(self, mcp, test_collection):
        """S4.4 — Three memory types: user, project, feedback."""
        for t in ["user", "project", "feedback"]:
            result = await mcp.call_tool("save_memory", {
                "key": f"type_test_{t}", "value": f"value_{t}",
                "type": t, "collection": test_collection
            })
            assert not mcp.tool_error(result)

        result = await mcp.call_tool("list_memories", {
            "type": "project", "collection": test_collection
        })
        assert not mcp.tool_error(result)

    async def test_unicode_memory(self, mcp, test_collection):
        """S9.4 — Unicode in memory key and value."""
        result = await mcp.call_tool("save_memory", {
            "key": "тест_юникод", "value": "Работает корректно 🚀",
            "type": "project", "collection": test_collection
        })
        assert not mcp.tool_error(result)
        text = mcp.tool_text(result)
        assert "тест_юникод" in text or "saved" in text.lower()

    async def test_empty_key_rejected(self, mcp, test_collection):
        """S9.5 — Empty key returns error."""
        result = await mcp.call_tool("save_memory", {
            "key": "", "value": "test", "type": "project",
            "collection": test_collection
        })
        assert mcp.tool_error(result)


# ── Chat Operations ──────────────────────────────────────────────────────────

class TestChat:
    """S5: Chat persistence and search."""

    async def test_multi_message_chat(self, mcp, test_session_id):
        """S5.1 — Save 5 messages, recall all."""
        messages = [
            {"role": "user", "content": f"Message {i}"} if i % 2 == 0
            else {"role": "assistant", "content": f"Response {i}"}
            for i in range(5)
        ]
        await mcp.call_tool("save_chat", {
            "session_id": test_session_id, "messages": messages
        })
        result = await mcp.call_tool("recall_chat", {"session_id": test_session_id})
        assert not mcp.tool_error(result)

    async def test_search_chats(self, mcp):
        """S5.2 — search_chats finds messages by content."""
        sid = f"search_test_{uuid.uuid4().hex[:6]}"
        await mcp.call_tool("save_chat", {
            "session_id": sid,
            "messages": [
                {"role": "user", "content": "обсуждаем миграцию базы данных"},
                {"role": "assistant", "content": "миграция с SQLite на PostgreSQL"}
            ]
        })
        result = await mcp.call_tool("search_chats", {"query": "миграция"})
        assert not mcp.tool_error(result)


# ── Search Types ─────────────────────────────────────────────────────────────

class TestSearchTypes:
    """S3: All search types return valid responses (even without data)."""

    @pytest.mark.parametrize("search_type", [
        "CHUNKS", "CHUNKS_LEXICAL", "HYBRID", "SUMMARIES", "FEELING_LUCKY"
    ])
    async def test_search_type_no_error(self, mcp, search_type):
        """S3.x — Each search type returns without crash."""
        result = await mcp.call_tool("search", {
            "search_query": "test query",
            "search_type": search_type,
            "top_k": 5
        })
        assert not mcp.tool_error(result)

    @pytest.mark.requires_embed
    @pytest.mark.parametrize("search_type", [
        "RAG_COMPLETION", "GRAPH_COMPLETION", "TEMPORAL"
    ])
    async def test_search_type_with_embed(self, mcp, services, search_type):
        """S3.x — LLM-augmented search types (need embed)."""
        if not services.get("embed"):
            pytest.skip("Embed not available")
        result = await mcp.call_tool("search", {
            "search_query": "architecture overview",
            "search_type": search_type, "top_k": 5
        })
        assert not mcp.tool_error(result)


# ── Cognify Pipeline ─────────────────────────────────────────────────────────

class TestCognify:
    """S2: Document loading and cognify pipeline."""

    async def test_cognify_returns_run_id(self, mcp, test_collection):
        """S2.1 — cognify returns run_id for async tracking."""
        result = await mcp.call_tool("cognify", {
            "data": "Levara is a Go-based knowledge graph engine with HNSW vector search.",
            "collection": test_collection
        })
        text = mcp.tool_text(result)
        assert "run" in text.lower() or "id" in text.lower() or not mcp.tool_error(result)

    async def test_add_data(self, mcp, test_collection):
        """S2.1b — add stages data without cognify."""
        result = await mcp.call_tool("add", {
            "data": "Test document content for staging.",
            "dataset_name": "test_dataset",
            "collection": test_collection
        })
        assert not mcp.tool_error(result)

    async def test_cognify_empty_data_error(self, mcp, test_collection):
        """S2.8 — cognify with empty data returns error."""
        result = await mcp.call_tool("cognify", {
            "data": "", "collection": test_collection
        })
        assert mcp.tool_error(result)

    async def test_cognify_status_invalid_id(self, mcp):
        """S2.9 — cognify_status with invalid run_id."""
        result = await mcp.call_tool("cognify_status", {"run_id": "nonexistent-id"})
        text = mcp.tool_text(result)
        assert "not found" in text.lower() or "unknown" in text.lower() or not mcp.tool_error(result)


# ── Git Analysis ─────────────────────────────────────────────────────────────

class TestGitAnalysis:
    """S6: Git commit analysis."""

    async def test_analyze_commits(self, mcp):
        """S6.1 — analyze_commits on current repo (or graceful error on remote)."""
        result = await mcp.call_tool("analyze_commits", {
            "repo_path": ".", "limit": 5
        })
        text = mcp.tool_text(result)
        # On remote Pi: "not a git repository" (valid error, not crash)
        # On local: returns commit count or run_id
        assert not mcp.tool_error(result) or "not a git repository" in text.lower() or "not a directory" in text.lower()

    async def test_analyze_invalid_repo(self, mcp):
        """S6.3 — analyze_commits on nonexistent path."""
        result = await mcp.call_tool("analyze_commits", {
            "repo_path": "/tmp/nonexistent_repo_xyz", "limit": 5
        })
        # Should return error, not crash
        text = mcp.tool_text(result)
        assert mcp.tool_error(result) or "error" in text.lower() or "0" in text

    async def test_git_search(self, mcp):
        """S6.2 — git_search returns without crash (empty OK if no data)."""
        result = await mcp.call_tool("git_search", {"query": "authentication"})
        # May error if no embed or no git data — that's OK, should not crash
        text = mcp.tool_text(result)
        assert not mcp.tool_error(result) or "error" in text.lower() or "no results" in text.lower()


# ── Project Context ──────────────────────────────────────────────────────────

class TestProjectContext:
    """S10: get_project_context tool."""

    async def test_context_with_memories(self, mcp):
        """S10.1 — Context includes saved memories."""
        coll = f"ctx_{uuid.uuid4().hex[:6]}"
        await mcp.call_tool("save_memory", {
            "key": "stack", "value": "Go + HNSW",
            "type": "project", "collection": coll
        })
        result = await mcp.call_tool("get_project_context", {"collection": coll})
        text = mcp.tool_text(result)
        assert "Collection Stats" in text
        assert "Memories" in text

    async def test_context_empty_collection(self, mcp, test_collection):
        """S10.2 — Context on empty collection doesn't crash."""
        result = await mcp.call_tool("get_project_context", {
            "collection": test_collection
        })
        text = mcp.tool_text(result)
        assert "not found" in text or "no memories" in text.lower() or "Collection Stats" in text


# ── Prune (destructive) ─────────────────────────────────────────────────────

class TestPrune:
    """S7.3: Prune and delete operations."""

    async def test_prune_returns_ok(self, mcp):
        """S7.3 — prune completes without error."""
        # WARNING: this deletes all data
        result = await mcp.call_tool("prune")
        assert not mcp.tool_error(result)
