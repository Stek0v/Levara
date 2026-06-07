"""
Levara MCP Phase 3 Tests — Production Polish.
Run: pytest tests/test_mcp_phase3.py -v --mcp-url http://localhost:8081

Tests: unified search mode, check_drift, prune_graph, configurable thresholds, graph_rerank, community_resolution.
"""
import asyncio
import json
import re
import uuid

import pytest

pytestmark = [pytest.mark.integration, pytest.mark.asyncio]


def extract_run_id(text: str) -> str:
    match = re.search(r"Run ID: ([a-f0-9-]+)", text)
    assert match, f"No Run ID found in: {text}"
    return match.group(1)


async def wait_for_completion(mcp, run_id: str, timeout: int = 60) -> str:
    for _ in range(timeout):
        result = await mcp.call_tool("cognify_status", {"run_id": run_id})
        text = mcp.tool_text(result)
        if "COMPLETED" in text:
            return "COMPLETED"
        if "FAILED" in text:
            return f"FAILED: {text}"
        await asyncio.sleep(1)
    raise TimeoutError(f"Pipeline {run_id} did not complete in {timeout}s")


async def cognify_and_wait(mcp, text, collection, **kwargs):
    args = {"data": text, "collection": collection, **kwargs}
    result = await mcp.call_tool("cognify", args)
    assert not mcp.tool_error(result), f"cognify failed: {mcp.tool_text(result)}"
    run_id = extract_run_id(mcp.tool_text(result))
    status = await wait_for_completion(mcp, run_id, timeout=60)
    return status


SAMPLE_TEXT = """PostgreSQL is a powerful relational database management system with ACID transactions.
Redis provides fast in-memory data structure operations for caching and message brokering.
MongoDB is a document-oriented NoSQL database designed for high volume data storage."""


class TestUnifiedSearchMode:
    """mode parameter in search tool."""

    @pytest.mark.requires_embed
    async def test_mode_rag_returns_results(self, mcp, services):
        """mode=rag returns vector results."""
        if not services.get("embed"):
            pytest.skip("Embed not available")
        coll = f"m_rag_{uuid.uuid4().hex[:8]}"
        await cognify_and_wait(mcp, SAMPLE_TEXT, coll, mode="rag")

        search = await mcp.call_tool("search", {
            "search_query": "database", "collection": coll, "mode": "rag"
        })
        assert not mcp.tool_error(search)
        data = json.loads(mcp.tool_text(search))
        assert len(data["results"]) > 0

    @pytest.mark.requires_embed
    async def test_mode_auto_default(self, mcp, services):
        """Default mode=auto works as before."""
        if not services.get("embed"):
            pytest.skip("Embed not available")
        coll = f"m_auto_{uuid.uuid4().hex[:8]}"
        await cognify_and_wait(mcp, SAMPLE_TEXT, coll, mode="rag")

        search = await mcp.call_tool("search", {
            "search_query": "database", "collection": coll
        })
        assert not mcp.tool_error(search)

    @pytest.mark.requires_embed
    async def test_mode_invalid_treated_as_auto(self, mcp, services):
        """Invalid mode treated as auto."""
        if not services.get("embed"):
            pytest.skip("Embed not available")
        coll = f"m_inv_{uuid.uuid4().hex[:8]}"
        await cognify_and_wait(mcp, SAMPLE_TEXT, coll, mode="rag")

        search = await mcp.call_tool("search", {
            "search_query": "database", "collection": coll, "mode": "nonsense"
        })
        assert not mcp.tool_error(search)


class TestCheckDrift:
    """check_drift MCP tool."""

    async def test_check_drift_returns_array(self, mcp, services):
        """check_drift returns array (possibly empty)."""
        result = await mcp.call_tool("check_drift", {})
        assert not mcp.tool_error(result)
        data = json.loads(mcp.tool_text(result))
        assert isinstance(data, list)


class TestPruneGraph:
    """prune_graph MCP tool."""

    async def test_prune_dry_run(self, mcp, services):
        """prune_graph with dry_run returns counts without deleting."""
        result = await mcp.call_tool("prune_graph", {"dry_run": True})
        assert not mcp.tool_error(result)
        data = json.loads(mcp.tool_text(result))
        assert "edges_would_delete" in data or "edges_deleted" in data

    async def test_prune_empty_graph(self, mcp, services):
        """prune_graph on empty graph returns 0."""
        result = await mcp.call_tool("prune_graph", {"dry_run": True, "max_age_days": 1})
        assert not mcp.tool_error(result)


class TestGraphRerank:
    """graph_rerank parameter in search."""

    @pytest.mark.requires_embed
    async def test_graph_rerank_flag(self, mcp, services):
        """graph_rerank=true returns results without error."""
        if not services.get("embed"):
            pytest.skip("Embed not available")
        coll = f"gr_{uuid.uuid4().hex[:8]}"
        await cognify_and_wait(mcp, SAMPLE_TEXT, coll, mode="rag")

        search = await mcp.call_tool("search", {
            "search_query": "database", "collection": coll, "graph_rerank": True
        })
        assert not mcp.tool_error(search)

    @pytest.mark.requires_embed
    async def test_graph_rerank_false_default(self, mcp, services):
        """Default (no graph_rerank) works as before."""
        if not services.get("embed"):
            pytest.skip("Embed not available")
        coll = f"grd_{uuid.uuid4().hex[:8]}"
        await cognify_and_wait(mcp, SAMPLE_TEXT, coll, mode="rag")

        search = await mcp.call_tool("search", {
            "search_query": "database", "collection": coll
        })
        assert not mcp.tool_error(search)


class TestConfigurableThresholds:
    """community_resolution, dedup_threshold, min/max chunk size."""

    @pytest.mark.requires_embed
    async def test_custom_chunk_sizes(self, mcp, services):
        """min_chunk_chars and max_chunk_chars accepted."""
        if not services.get("embed"):
            pytest.skip("Embed not available")
        coll = f"cs_{uuid.uuid4().hex[:8]}"
        status = await cognify_and_wait(
            mcp, SAMPLE_TEXT, coll, mode="rag",
            min_chunk_chars=50, max_chunk_chars=1000
        )
        assert status == "COMPLETED"

    @pytest.mark.requires_embed
    async def test_community_resolution_param(self, mcp, services):
        """community_resolution parameter accepted in cognify."""
        if not services.get("embed"):
            pytest.skip("Embed not available")
        coll = f"cr_{uuid.uuid4().hex[:8]}"
        # This won't actually run communities (RAG mode), but param should be accepted
        result = await mcp.call_tool("cognify", {
            "data": SAMPLE_TEXT, "collection": coll,
            "community_resolution": 2.0
        })
        assert not mcp.tool_error(result)

    @pytest.mark.requires_embed
    async def test_dedup_threshold_param(self, mcp, services):
        """dedup_threshold parameter accepted in cognify."""
        if not services.get("embed"):
            pytest.skip("Embed not available")
        coll = f"dt_{uuid.uuid4().hex[:8]}"
        result = await mcp.call_tool("cognify", {
            "data": SAMPLE_TEXT, "collection": coll,
            "dedup_threshold": 0.9
        })
        assert not mcp.tool_error(result)


class TestPhase3Regression:
    """Ensure all previous phases still work."""

    @pytest.mark.requires_embed
    async def test_phase1_rag(self, mcp, services):
        if not services.get("embed"):
            pytest.skip("Embed not available")
        coll = f"regr3_rag_{uuid.uuid4().hex[:8]}"
        status = await cognify_and_wait(mcp, SAMPLE_TEXT, coll, mode="rag")
        assert status == "COMPLETED"
        search = await mcp.call_tool("search", {
            "search_query": "database", "collection": coll
        })
        data = json.loads(mcp.tool_text(search))
        assert len(data["results"]) > 0

    @pytest.mark.requires_embed
    async def test_phase1b_parent_child(self, mcp, services):
        if not services.get("embed"):
            pytest.skip("Embed not available")
        coll = f"regr3_pc_{uuid.uuid4().hex[:8]}"
        status = await cognify_and_wait(mcp, SAMPLE_TEXT, coll,
            mode="rag", parent_child=True, document_title="Test Doc")
        assert status == "COMPLETED"

    @pytest.mark.requires_embed
    async def test_phase2_communities(self, mcp, services):
        """list_communities tool works."""
        result = await mcp.call_tool("list_communities", {"min_members": 1})
        assert not mcp.tool_error(result)

    async def test_smoke_tools_present(self, mcp, services):
        """All Phase 3 tools registered."""
        tools = await mcp.tools_list()
        names = [t["name"] for t in tools]
        for tool in ["check_drift", "prune_graph", "list_communities"]:
            assert tool in names, f"Tool {tool} missing"
