"""Journey B: All Search Types — user explores different search modes.
Tests every documented SearchType with appropriate queries.
"""
import pytest
import aiohttp
from conftest_http import BASE_URL, unique_id

pytestmark = pytest.mark.asyncio


async def _auth(s):
    email = f"search_{unique_id()}@test.com"
    await s.post(f"{BASE_URL}/auth/register", json={"email": email, "password": "searchpass"})
    async with s.post(f"{BASE_URL}/auth/login", json={"email": email, "password": "searchpass"}) as r:
        return {"Authorization": f"Bearer {(await r.json())['access_token']}"}


async def _search(s, h, query, query_type, top_k=5):
    async with s.post(f"{BASE_URL}/search/text", json={
        "query_text": query, "query_type": query_type, "top_k": top_k
    }, headers=h) as r:
        return r.status, await r.json()


# ── CHUNKS: raw vector similarity search ──

async def test_chunks_search():
    """CHUNKS returns array of matching text chunks."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        status, data = await _search(s, h, "machine learning algorithms", "CHUNKS")
        assert status == 200
        assert isinstance(data, list)


async def test_chunks_with_topk():
    """CHUNKS respects top_k parameter."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        status, data = await _search(s, h, "neural networks", "CHUNKS", top_k=3)
        assert status == 200
        assert isinstance(data, list)
        assert len(data) <= 3


# ── RAG_COMPLETION: chunks + LLM answer ──

async def test_rag_completion_structure():
    """RAG_COMPLETION returns {chunks: [], answer: string}."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        status, data = await _search(s, h, "What is deep learning?", "RAG_COMPLETION")
        assert status == 200
        assert "chunks" in data
        assert "answer" in data
        assert isinstance(data["chunks"], list)
        assert isinstance(data["answer"], str)


# ── GRAPH_COMPLETION: full graph context + LLM ──

async def test_graph_completion():
    """GRAPH_COMPLETION returns results with graph context."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        status, data = await _search(s, h, "Who discovered DNA?", "GRAPH_COMPLETION")
        assert status == 200


# ── SUMMARIES: pre-computed summaries ──

async def test_summaries_search():
    """SUMMARIES searches summary collections."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        status, data = await _search(s, h, "overview of the project", "SUMMARIES")
        assert status == 200
        assert isinstance(data, list)


# ── CHUNKS_LEXICAL: BM25 keyword search ──

async def test_chunks_lexical():
    """CHUNKS_LEXICAL uses BM25 inverted index."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        status, data = await _search(s, h, "HNSW WAL durability", "CHUNKS_LEXICAL")
        assert status == 200
        # Returns list or empty (null → []) if no BM25 indexes populated
        assert data is None or isinstance(data, list)


# ── HYBRID: vector + BM25 fusion ──

async def test_hybrid_search():
    """HYBRID combines vector and BM25 via RRF fusion."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        status, data = await _search(s, h, "vector database performance", "HYBRID")
        assert status == 200
        assert isinstance(data, list)


# ── TEMPORAL: date-aware search ──

async def test_temporal_with_dates():
    """TEMPORAL extracts dates from query."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        status, data = await _search(s, h, "Events before 2020-01-01", "TEMPORAL")
        assert status == 200
        assert isinstance(data, list)
        if len(data) > 0:
            assert "date" in data[0]


async def test_temporal_with_natural_date():
    """TEMPORAL handles natural language dates."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        status, data = await _search(s, h, "What happened in January 2024?", "TEMPORAL")
        assert status == 200
        assert isinstance(data, list)


async def test_temporal_with_russian_date():
    """TEMPORAL handles Russian date formats."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        status, data = await _search(s, h, "события 15 марта 2024 года", "TEMPORAL")
        assert status == 200


# ── Edge cases ──

async def test_unknown_type_fallback():
    """Unknown query_type falls back to CHUNKS."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        status, data = await _search(s, h, "test", "NONEXISTENT_TYPE")
        assert status == 200


async def test_empty_query_rejected():
    """Empty query_text is rejected."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        async with s.post(f"{BASE_URL}/search/text", json={
            "query_text": "", "query_type": "CHUNKS"
        }, headers=h) as r:
            assert r.status == 400


async def test_missing_query_rejected():
    """Missing query_text is rejected."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        async with s.post(f"{BASE_URL}/search/text", json={
            "query_type": "CHUNKS"
        }, headers=h) as r:
            assert r.status == 400


async def test_all_search_types_respond():
    """Every documented search type returns 200."""
    types = ["CHUNKS", "RAG_COMPLETION", "SUMMARIES", "GRAPH_COMPLETION",
             "CHUNKS_LEXICAL", "HYBRID", "TEMPORAL"]
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        for qt in types:
            status, _ = await _search(s, h, "test query for all types", qt)
            assert status == 200, f"{qt} returned {status}"
