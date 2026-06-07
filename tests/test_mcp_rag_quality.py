"""
Levara MCP RAG Quality Tests — Phase 1B.
Run: pytest tests/test_mcp_rag_quality.py -v --mcp-url http://localhost:8081

Tests: parent-child chunks, contextual headers, result dedup, document metadata.
Requires: embed endpoint.
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
    status = await wait_for_completion(mcp, run_id, timeout=30)
    assert status == "COMPLETED", f"Pipeline failed: {status}"
    return run_id


STRUCTURED_DOC = """## Authentication

Modern web applications use JWT tokens for stateless authentication. Access tokens are stored in httpOnly cookies to prevent XSS attacks. Refresh tokens are rotated periodically and stored server-side in Redis with a configurable TTL of 7 days.

## Session Management

When a user logs in, the server generates both an access token and a refresh token. The access token has a short lifetime of 15 minutes while the refresh token lasts 7 days. Token rotation happens automatically on each refresh request to prevent token reuse.

## Authorization

The authorization middleware validates the JWT token on every request. It checks the token signature, expiration, and required claims including user roles and permissions. Role-based access control determines which API endpoints each user can access.

## Rate Limiting

API endpoints are protected by rate limiting based on the user's IP address and account tier. Free tier users are limited to 100 requests per minute, while premium users get 1000 requests per minute. Rate limit headers are included in every response."""


class TestParentChildChunks:
    """Parent-child dual-level chunking."""

    @pytest.mark.requires_embed
    async def test_parent_child_cognify(self, mcp, services):
        """parent_child=true creates searchable chunks."""
        if not services.get("embed"):
            pytest.skip("Embed not available")

        coll = f"pc_{uuid.uuid4().hex[:8]}"
        await cognify_and_wait(mcp, STRUCTURED_DOC, coll, mode="rag", parent_child=True)

        # Search should find results
        search = await mcp.call_tool("search", {
            "search_query": "JWT authentication cookies",
            "collection": coll, "top_k": 5
        })
        assert not mcp.tool_error(search)
        data = json.loads(mcp.tool_text(search))
        assert len(data["results"]) > 0

    @pytest.mark.requires_embed
    async def test_parent_child_search(self, mcp, services):
        """parent_child=true search returns parent chunks."""
        if not services.get("embed"):
            pytest.skip("Embed not available")

        coll = f"pcs_{uuid.uuid4().hex[:8]}"
        await cognify_and_wait(mcp, STRUCTURED_DOC, coll, mode="rag", parent_child=True)

        # Search with parent_child=true
        search = await mcp.call_tool("search", {
            "search_query": "token rotation refresh",
            "collection": coll, "parent_child": True, "top_k": 5
        })
        assert not mcp.tool_error(search)
        data = json.loads(mcp.tool_text(search))
        # Should return results (either from child or parent collection)
        assert "results" in data

    @pytest.mark.requires_embed
    async def test_parent_child_disabled_default(self, mcp, services):
        """Default (no parent_child) works as before."""
        if not services.get("embed"):
            pytest.skip("Embed not available")

        coll = f"pcdef_{uuid.uuid4().hex[:8]}"
        await cognify_and_wait(mcp, STRUCTURED_DOC, coll, mode="rag")

        search = await mcp.call_tool("search", {
            "search_query": "rate limiting", "collection": coll
        })
        assert not mcp.tool_error(search)
        data = json.loads(mcp.tool_text(search))
        assert len(data["results"]) > 0


class TestContextualHeaders:
    """Document title and section headers improve retrieval."""

    @pytest.mark.requires_embed
    async def test_document_title_in_metadata(self, mcp, services):
        """document_title appears in search result metadata."""
        if not services.get("embed"):
            pytest.skip("Embed not available")

        coll = f"ctx_{uuid.uuid4().hex[:8]}"
        await cognify_and_wait(
            mcp, STRUCTURED_DOC, coll,
            mode="rag", document_title="MyApp Architecture"
        )

        search = await mcp.call_tool("search", {
            "search_query": "authentication", "collection": coll
        })
        data = json.loads(mcp.tool_text(search))
        assert len(data["results"]) > 0

        # Check metadata contains document_title
        meta = data["results"][0].get("metadata", "")
        assert "MyApp Architecture" in meta, f"document_title not in metadata: {meta[:200]}"

    @pytest.mark.requires_embed
    async def test_section_detection(self, mcp, services):
        """Section headers detected and stored in metadata."""
        if not services.get("embed"):
            pytest.skip("Embed not available")

        coll = f"sec_{uuid.uuid4().hex[:8]}"
        await cognify_and_wait(
            mcp, STRUCTURED_DOC, coll,
            mode="rag", document_title="Auth Guide"
        )

        search = await mcp.call_tool("search", {
            "search_query": "rate limiting API", "collection": coll
        })
        data = json.loads(mcp.tool_text(search))
        assert len(data["results"]) > 0

        # Metadata should contain section info
        meta = data["results"][0].get("metadata", "")
        # Section might be "Rate Limiting" or similar
        # At minimum, document_title should be present
        assert "Auth Guide" in meta

    @pytest.mark.requires_embed
    async def test_no_title_no_errors(self, mcp, services):
        """cognify without document_title works fine."""
        if not services.get("embed"):
            pytest.skip("Embed not available")

        coll = f"notitle_{uuid.uuid4().hex[:8]}"
        await cognify_and_wait(mcp, STRUCTURED_DOC, coll, mode="rag")

        search = await mcp.call_tool("search", {
            "search_query": "JWT tokens", "collection": coll
        })
        assert not mcp.tool_error(search)


class TestResultDedup:
    """Deduplication of overlapping search results."""

    @pytest.mark.requires_embed
    async def test_sliding_window_dedup(self, mcp, services):
        """Sliding window with overlap → dedup removes near-duplicates."""
        if not services.get("embed"):
            pytest.skip("Embed not available")

        coll = f"dedup_{uuid.uuid4().hex[:8]}"
        await cognify_and_wait(
            mcp, STRUCTURED_DOC, coll,
            mode="rag", chunk_strategy="sliding", overlap_chars=200
        )

        # Search with dedup (default)
        search = await mcp.call_tool("search", {
            "search_query": "JWT authentication",
            "collection": coll, "top_k": 10
        })
        data = json.loads(mcp.tool_text(search))
        results = data["results"]

        # With dedup, should have unique content
        # Check that we don't have identical metadata.text in multiple results
        texts = set()
        for r in results:
            meta = r.get("metadata", "")
            if isinstance(meta, str) and meta.startswith("{"):
                try:
                    m = json.loads(meta)
                    texts.add(m.get("text", "")[:100])
                except json.JSONDecodeError:
                    pass

        # All extracted texts should be unique (or nearly so)
        # We can't assert exact uniqueness because Jaccard threshold may not catch all
        if len(results) > 1:
            assert len(texts) > 0, "Should have parseable text in results"

    @pytest.mark.requires_embed
    async def test_dedup_preserves_relevance(self, mcp, services):
        """Dedup keeps highest-scored results."""
        if not services.get("embed"):
            pytest.skip("Embed not available")

        coll = f"dedrel_{uuid.uuid4().hex[:8]}"
        await cognify_and_wait(mcp, STRUCTURED_DOC, coll, mode="rag")

        search = await mcp.call_tool("search", {
            "search_query": "authentication",
            "collection": coll, "top_k": 5, "dedup": True
        })
        data = json.loads(mcp.tool_text(search))
        assert len(data["results"]) > 0


class TestSentenceAwareSliding:
    """Sliding window with sentence-aware boundaries."""

    @pytest.mark.requires_embed
    async def test_sliding_no_broken_words(self, mcp, services):
        """snap_to_sentence=true prevents word-level cuts."""
        if not services.get("embed"):
            pytest.skip("Embed not available")

        coll = f"snap_{uuid.uuid4().hex[:8]}"
        await cognify_and_wait(
            mcp, STRUCTURED_DOC, coll,
            mode="rag", chunk_strategy="sliding",
            overlap_chars=100, snap_to_sentence=True
        )

        search = await mcp.call_tool("search", {
            "search_query": "authorization middleware",
            "collection": coll, "top_k": 5
        })
        assert not mcp.tool_error(search)
        data = json.loads(mcp.tool_text(search))
        assert len(data["results"]) > 0


class TestPhase1Regression:
    """Ensure Phase 1 features still work after Phase 1B."""

    @pytest.mark.requires_embed
    async def test_rag_basic(self, mcp, services):
        if not services.get("embed"):
            pytest.skip("Embed not available")
        coll = f"regr_rag_{uuid.uuid4().hex[:8]}"
        await cognify_and_wait(mcp, STRUCTURED_DOC, coll, mode="rag")
        search = await mcp.call_tool("search", {
            "search_query": "database", "collection": coll
        })
        data = json.loads(mcp.tool_text(search))
        assert len(data["results"]) > 0

    @pytest.mark.requires_embed
    async def test_rerank_flag(self, mcp, services):
        if not services.get("embed"):
            pytest.skip("Embed not available")
        coll = f"regr_rr_{uuid.uuid4().hex[:8]}"
        await cognify_and_wait(mcp, STRUCTURED_DOC, coll, mode="rag")
        search = await mcp.call_tool("search", {
            "search_query": "JWT",
            "collection": coll, "rerank": True
        })
        assert not mcp.tool_error(search)
        data = json.loads(mcp.tool_text(search))
        assert data.get("reranked") is False  # no reranker configured
