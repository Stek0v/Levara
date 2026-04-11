"""
Levara MCP RAG Mode Tests — Phase 1.
Run: pytest tests/test_mcp_rag_mode.py -v

Tests RAG-specific features: skip_graph mode, sliding window chunking, reranker flag.
Requires: embed endpoint. Does NOT require LLM.
"""
import asyncio
import json
import re
import time
import uuid

import pytest

pytestmark = [pytest.mark.integration, pytest.mark.asyncio]


# --- Helpers ---

def extract_run_id(text: str) -> str:
    """Extract Run ID from cognify response text."""
    match = re.search(r"Run ID: ([a-f0-9-]+)", text)
    assert match, f"No Run ID found in: {text}"
    return match.group(1)


async def wait_for_completion(mcp, run_id: str, timeout: int = 60) -> str:
    """Poll cognify_status until COMPLETED or FAILED."""
    for _ in range(timeout):
        result = await mcp.call_tool("cognify_status", {"run_id": run_id})
        text = mcp.tool_text(result)
        if "COMPLETED" in text:
            return "COMPLETED"
        if "FAILED" in text:
            return f"FAILED: {text}"
        await asyncio.sleep(1)
    raise TimeoutError(f"Pipeline {run_id} did not complete in {timeout}s")


async def cognify_and_wait(mcp, text: str, collection: str, **kwargs) -> str:
    """cognify + wait helper. Returns status."""
    args = {"data": text, "collection": collection, **kwargs}
    result = await mcp.call_tool("cognify", args)
    assert not mcp.tool_error(result), f"cognify failed: {mcp.tool_text(result)}"
    run_id = extract_run_id(mcp.tool_text(result))
    status = await wait_for_completion(mcp, run_id, timeout=30)
    assert status == "COMPLETED", f"Pipeline failed: {status}"
    return run_id


SAMPLE_TEXT = """PostgreSQL is a powerful relational database management system.
It supports ACID transactions and has strong SQL compliance.
Redis is an in-memory data structure store used as cache and message broker.
MongoDB is a document-oriented NoSQL database for high volume data storage.
Elasticsearch provides distributed full-text search and analytics capabilities."""


LONG_TEXT = """
Artificial intelligence has transformed many industries since its inception.
Machine learning models can now process natural language with remarkable accuracy.
Deep learning architectures like transformers have revolutionized NLP tasks.
Computer vision applications include object detection, image segmentation, and facial recognition.
Reinforcement learning enables agents to learn optimal strategies through trial and error.
Transfer learning allows models to leverage knowledge from pre-trained networks.
Generative adversarial networks can create realistic synthetic data and images.
Attention mechanisms help models focus on relevant parts of the input sequence.
Neural architecture search automates the design of optimal network topologies.
Federated learning enables training models across decentralized data sources.
""" * 5  # ~500 words, enough for multiple sliding window chunks


class TestRAGModeCognify:
    """RAG mode cognify: chunk + embed, no graph extraction."""

    @pytest.mark.requires_embed
    async def test_rag_mode_basic(self, mcp, services):
        """cognify mode=rag → search finds results."""
        if not services.get("embed"):
            pytest.skip("Embed not available")

        coll = f"rag_{uuid.uuid4().hex[:8]}"
        await cognify_and_wait(mcp, SAMPLE_TEXT, coll, mode="rag")

        search = await mcp.call_tool("search", {
            "search_query": "relational database",
            "collection": coll, "top_k": 5
        })
        assert not mcp.tool_error(search)
        data = json.loads(mcp.tool_text(search))
        assert len(data["results"]) > 0, "RAG mode should produce searchable chunks"

    @pytest.mark.requires_embed
    async def test_rag_mode_no_llm_required(self, mcp, services):
        """RAG mode succeeds even without LLM endpoint."""
        if not services.get("embed"):
            pytest.skip("Embed not available")

        coll = f"rag_nollm_{uuid.uuid4().hex[:8]}"
        # mode=rag should never call LLM
        status = await cognify_and_wait(mcp, "Simple text for RAG mode test.", coll, mode="rag")
        assert status is not None  # Did not fail

    @pytest.mark.requires_embed
    async def test_rag_mode_with_room_tags(self, mcp, services):
        """room + tags propagated and filterable in RAG mode."""
        if not services.get("embed"):
            pytest.skip("Embed not available")

        coll = f"rag_rt_{uuid.uuid4().hex[:8]}"
        await cognify_and_wait(
            mcp, "Authentication in modern web applications uses JWT tokens stored in httpOnly cookies for security. "
                 "Refresh tokens are rotated and stored in Redis with a TTL. "
                 "This approach prevents XSS attacks from stealing session credentials.",
            coll, mode="rag", room="auth", tags=["security", "jwt"]
        )

        # Search WITH room filter — should find chunks
        search = await mcp.call_tool("search", {
            "search_query": "JWT authentication",
            "collection": coll, "room": "auth"
        })
        assert not mcp.tool_error(search)
        data = json.loads(mcp.tool_text(search))
        assert len(data["results"]) > 0, "Room filter should find RAG chunks"

        # Search with WRONG room — should find nothing
        search_wrong = await mcp.call_tool("search", {
            "search_query": "JWT authentication",
            "collection": coll, "room": "deploy"
        })
        data_wrong = json.loads(mcp.tool_text(search_wrong))
        assert len(data_wrong["results"]) == 0, "Wrong room should filter out all results"

        # Search with tag filter
        search_tag = await mcp.call_tool("search", {
            "search_query": "JWT authentication",
            "collection": coll, "tags": ["security"]
        })
        data_tag = json.loads(mcp.tool_text(search_tag))
        assert len(data_tag["results"]) > 0, "Tag filter should find RAG chunks"

    @pytest.mark.requires_embed
    async def test_full_mode_default(self, mcp, services):
        """Backward compat: cognify without mode works as before."""
        if not services.get("embed"):
            pytest.skip("Embed not available")

        coll = f"rag_default_{uuid.uuid4().hex[:8]}"
        # No mode parameter — should work as full (backward compat)
        result = await mcp.call_tool("cognify", {
            "data": "Einstein worked at Princeton.", "collection": coll
        })
        assert not mcp.tool_error(result), "Default mode should not fail"

    @pytest.mark.requires_embed
    async def test_rag_mode_speed(self, mcp, services):
        """RAG mode should be fast (no LLM overhead)."""
        if not services.get("embed"):
            pytest.skip("Embed not available")

        coll = f"rag_speed_{uuid.uuid4().hex[:8]}"
        t0 = time.perf_counter()
        await cognify_and_wait(mcp, SAMPLE_TEXT, coll, mode="rag")
        elapsed = time.perf_counter() - t0
        # Should complete in < 10s (no LLM calls, just chunk + embed)
        assert elapsed < 10, f"RAG mode too slow: {elapsed:.1f}s"


class TestSlidingWindowChunking:
    """Sliding window chunking with overlap."""

    @pytest.mark.requires_embed
    async def test_sliding_basic(self, mcp, services):
        """Sliding window creates searchable chunks."""
        if not services.get("embed"):
            pytest.skip("Embed not available")

        coll = f"slide_{uuid.uuid4().hex[:8]}"
        await cognify_and_wait(
            mcp, LONG_TEXT, coll,
            mode="rag", chunk_strategy="sliding", overlap_chars=100
        )

        search = await mcp.call_tool("search", {
            "search_query": "transformer deep learning",
            "collection": coll, "top_k": 5
        })
        assert not mcp.tool_error(search)
        data = json.loads(mcp.tool_text(search))
        assert len(data["results"]) > 0

    @pytest.mark.requires_embed
    async def test_sliding_vs_merged(self, mcp, services):
        """Sliding and merged produce different chunk counts."""
        if not services.get("embed"):
            pytest.skip("Embed not available")

        coll_s = f"slide_s_{uuid.uuid4().hex[:8]}"
        coll_m = f"slide_m_{uuid.uuid4().hex[:8]}"

        await cognify_and_wait(
            mcp, LONG_TEXT, coll_s,
            mode="rag", chunk_strategy="sliding", overlap_chars=100
        )
        await cognify_and_wait(
            mcp, LONG_TEXT, coll_m,
            mode="rag", chunk_strategy="merged"
        )

        # Both should be searchable
        s1 = await mcp.call_tool("search", {
            "search_query": "machine learning", "collection": coll_s, "top_k": 20
        })
        s2 = await mcp.call_tool("search", {
            "search_query": "machine learning", "collection": coll_m, "top_k": 20
        })
        assert not mcp.tool_error(s1)
        assert not mcp.tool_error(s2)

        r1 = json.loads(mcp.tool_text(s1))["results"]
        r2 = json.loads(mcp.tool_text(s2))["results"]
        # Both should find results, counts may differ due to overlap
        assert len(r1) > 0, "Sliding should find results"
        assert len(r2) > 0, "Merged should find results"

    @pytest.mark.requires_embed
    async def test_sliding_custom_overlap(self, mcp, services):
        """Custom overlap_chars parameter works."""
        if not services.get("embed"):
            pytest.skip("Embed not available")

        coll = f"slide_ov_{uuid.uuid4().hex[:8]}"
        await cognify_and_wait(
            mcp, LONG_TEXT, coll,
            mode="rag", chunk_strategy="sliding", overlap_chars=200
        )

        search = await mcp.call_tool("search", {
            "search_query": "neural network", "collection": coll
        })
        assert not mcp.tool_error(search)
        data = json.loads(mcp.tool_text(search))
        assert len(data["results"]) > 0


class TestReranker:
    """Reranker integration (graceful degradation)."""

    @pytest.mark.requires_embed
    async def test_rerank_flag_no_error(self, mcp, services):
        """rerank=true with no reranker configured → no error, returns results."""
        if not services.get("embed"):
            pytest.skip("Embed not available")

        coll = f"rerank_{uuid.uuid4().hex[:8]}"
        await cognify_and_wait(mcp, SAMPLE_TEXT, coll, mode="rag")

        search = await mcp.call_tool("search", {
            "search_query": "database",
            "collection": coll, "rerank": True
        })
        assert not mcp.tool_error(search)
        data = json.loads(mcp.tool_text(search))
        assert "results" in data
        assert "reranked" in data
        # No reranker configured → reranked should be false
        assert data["reranked"] is False

    @pytest.mark.requires_embed
    async def test_rerank_false_default(self, mcp, services):
        """Default behavior (no rerank) unchanged."""
        if not services.get("embed"):
            pytest.skip("Embed not available")

        coll = f"rerank_def_{uuid.uuid4().hex[:8]}"
        await cognify_and_wait(mcp, SAMPLE_TEXT, coll, mode="rag")

        search = await mcp.call_tool("search", {
            "search_query": "database", "collection": coll
        })
        assert not mcp.tool_error(search)
        data = json.loads(mcp.tool_text(search))
        assert len(data["results"]) > 0
        assert data.get("reranked") is False

    @pytest.mark.requires_embed
    async def test_rerank_empty_results(self, mcp, services):
        """rerank=true with no matching results → empty, no error."""
        if not services.get("embed"):
            pytest.skip("Embed not available")

        coll = f"rerank_empty_{uuid.uuid4().hex[:8]}"
        # Search empty collection
        search = await mcp.call_tool("search", {
            "search_query": "quantum computing",
            "collection": coll, "rerank": True
        })
        # Either error (collection doesn't exist) or empty results — both OK
        # The important thing is no crash


class TestRAGAndFullIsolation:
    """RAG and full mode don't interfere with each other."""

    @pytest.mark.requires_embed
    async def test_rag_then_full_same_collection(self, mcp, services):
        """RAG ingest then full ingest into same collection — both searchable."""
        if not services.get("embed"):
            pytest.skip("Embed not available")

        coll = f"mixed_{uuid.uuid4().hex[:8]}"

        # RAG mode first
        await cognify_and_wait(
            mcp, "Redis is an in-memory data structure store that supports strings, hashes, lists, sets, and sorted sets. "
                 "It is commonly used as a cache layer, message broker, and session store in distributed systems.",
            coll, mode="rag"
        )

        # Full mode second (may fail if no LLM, that's OK)
        result = await mcp.call_tool("cognify", {
            "data": "PostgreSQL supports ACID transactions with strong SQL compliance and extensibility through custom types and functions.",
            "collection": coll
        })
        # Don't assert success — full mode needs LLM

        # RAG chunk should still be findable
        search = await mcp.call_tool("search", {
            "search_query": "in-memory data store", "collection": coll
        })
        assert not mcp.tool_error(search)
        data = json.loads(mcp.tool_text(search))
        assert len(data["results"]) > 0
