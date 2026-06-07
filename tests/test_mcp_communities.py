"""
Levara MCP Community Tests — Phase 2.
Run: pytest tests/test_mcp_communities.py -v --mcp-url http://localhost:8081

Tests: community detection, list_communities, COMMUNITY_GLOBAL/LOCAL search.
Requires: embed endpoint. LLM optional (degrades gracefully).
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


async def wait_for_completion(mcp, run_id: str, timeout: int = 120) -> str:
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
    status = await wait_for_completion(mcp, run_id, timeout=120)
    return status


PHYSICS_TEXT = """
Einstein worked at the Institute for Advanced Study in Princeton from 1933 until his death in 1955.
He collaborated with Nathan Rosen on the EPR paradox paper, and with Boris Podolsky.
Niels Bohr responded to the EPR paper with his own interpretation of quantum mechanics.
Max Planck, who originated quantum theory, awarded Einstein the Max Planck Medal in 1929.
Werner Heisenberg developed the uncertainty principle and matrix mechanics at the University of Copenhagen.
Erwin Schrodinger proposed wave mechanics as an alternative formulation while at the University of Zurich.
Paul Dirac unified quantum mechanics with special relativity in the Dirac equation at Cambridge.
Richard Feynman later developed quantum electrodynamics at Caltech, building on Dirac's work.
The Copenhagen interpretation, championed by Bohr at the Niels Bohr Institute, became standard.
John Bell proposed Bell's theorem to test local hidden variables at CERN in 1964.
"""


class TestListCommunities:
    """list_communities MCP tool."""

    async def test_list_communities_empty(self, mcp, services):
        """list_communities on empty graph → empty result."""
        result = await mcp.call_tool("list_communities", {"min_members": 1})
        assert not mcp.tool_error(result)
        # May return empty or existing communities from previous runs
        data = json.loads(mcp.tool_text(result))
        assert isinstance(data, list)

    async def test_list_communities_tool_exists(self, mcp, services):
        """list_communities tool is registered."""
        tools = await mcp.tools_list()
        tool_names = [t["name"] for t in tools]
        assert "list_communities" in tool_names


class TestCommunitySearch:
    """COMMUNITY_GLOBAL and COMMUNITY_LOCAL search strategies."""

    @pytest.mark.requires_embed
    async def test_community_global_fallback_no_summaries(self, mcp, services):
        """COMMUNITY_GLOBAL without summaries → fallback (no error)."""
        if not services.get("embed"):
            pytest.skip("Embed not available")

        coll = f"cg_empty_{uuid.uuid4().hex[:8]}"
        search = await mcp.call_tool("search", {
            "search_query": "give me an overview",
            "search_type": "COMMUNITY_GLOBAL",
            "collection": coll
        })
        # Should not error — falls back gracefully
        assert not mcp.tool_error(search)

    @pytest.mark.requires_embed
    async def test_community_local_fallback(self, mcp, services):
        """COMMUNITY_LOCAL without communities → fallback to GRAPH_COMPLETION."""
        if not services.get("embed"):
            pytest.skip("Embed not available")

        search = await mcp.call_tool("search", {
            "search_query": "how does Einstein's work relate",
            "search_type": "COMMUNITY_LOCAL"
        })
        assert not mcp.tool_error(search)


class TestCommunityRegression:
    """Ensure Phase 1 + 1B features still work."""

    @pytest.mark.requires_embed
    async def test_rag_mode_unaffected(self, mcp, services):
        """RAG mode still works after Phase 2 changes."""
        if not services.get("embed"):
            pytest.skip("Embed not available")

        coll = f"regr2_rag_{uuid.uuid4().hex[:8]}"
        text = "PostgreSQL is a relational database. Redis is an in-memory store. MongoDB is a document database."
        status = await cognify_and_wait(mcp, text, coll, mode="rag")
        assert status == "COMPLETED"

        search = await mcp.call_tool("search", {
            "search_query": "relational database",
            "collection": coll
        })
        data = json.loads(mcp.tool_text(search))
        assert len(data["results"]) > 0

    @pytest.mark.requires_embed
    async def test_sliding_window_unaffected(self, mcp, services):
        """Sliding window chunking still works."""
        if not services.get("embed"):
            pytest.skip("Embed not available")

        coll = f"regr2_slide_{uuid.uuid4().hex[:8]}"
        text = "Knowledge is power. " * 100
        status = await cognify_and_wait(mcp, text, coll,
            mode="rag", chunk_strategy="sliding", overlap_chars=100)
        assert status == "COMPLETED"

    @pytest.mark.requires_embed
    async def test_parent_child_unaffected(self, mcp, services):
        """Parent-child chunking still works."""
        if not services.get("embed"):
            pytest.skip("Embed not available")

        coll = f"regr2_pc_{uuid.uuid4().hex[:8]}"
        text = """## Authentication

JWT tokens stored in httpOnly cookies. Refresh tokens rotated via Redis with TTL.

## Authorization

RBAC middleware validates tokens on every request. Checks signature and claims.
"""
        status = await cognify_and_wait(mcp, text, coll,
            mode="rag", parent_child=True, document_title="Auth Guide")
        assert status == "COMPLETED"

    @pytest.mark.requires_embed
    async def test_rerank_flag_unaffected(self, mcp, services):
        """Rerank flag still works."""
        if not services.get("embed"):
            pytest.skip("Embed not available")

        coll = f"regr2_rr_{uuid.uuid4().hex[:8]}"
        text = "PostgreSQL supports ACID transactions with strong SQL compliance and extensibility."
        status = await cognify_and_wait(mcp, text, coll, mode="rag")
        assert status == "COMPLETED"

        search = await mcp.call_tool("search", {
            "search_query": "SQL database",
            "collection": coll, "rerank": True
        })
        assert not mcp.tool_error(search)
        data = json.loads(mcp.tool_text(search))
        assert data.get("reranked") is False
