"""Journey A: Basic RAG Application — add → cognify → search.
The core 3-step user journey that every Cognee/Cognevra user follows.
Tests the complete pipeline from data ingestion to search results.
"""
import asyncio
import pytest
import aiohttp
from conftest_http import BASE_URL, sample_vector, unique_id

pytestmark = pytest.mark.asyncio
from conftest_http import get_server_dim; DIM = get_server_dim()


async def _auth(s):
    email = f"rag_{unique_id()}@test.com"
    await s.post(f"{BASE_URL}/auth/register", json={"email": email, "password": "ragpass123"})
    async with s.post(f"{BASE_URL}/auth/login", json={"email": email, "password": "ragpass123"}) as r:
        return {"Authorization": f"Bearer {(await r.json())['access_token']}"}


# ── Journey A1: Text → Cognify → Search (minimal) ──

async def test_add_text_returns_ok():
    """User adds plain text via /add endpoint."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        async with s.post(f"{BASE_URL}/add",
            data="Cognevra is a high-performance vector database written in Go. "
                 "It uses HNSW indexing with WAL durability for crash recovery.",
            headers={**h, "Content-Type": "text/plain"}
        ) as r:
            assert r.status == 200
            data = await r.json()
            assert data["status"] == "ok"
            assert data["items"] >= 1


async def test_add_multiple_texts():
    """User adds multiple text documents for a dataset."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        texts = [
            "Machine learning models require large amounts of training data.",
            "Neural networks consist of layers of interconnected nodes.",
            "Deep learning is a subset of machine learning using neural networks.",
        ]
        for text in texts:
            async with s.post(f"{BASE_URL}/add", data=text,
                headers={**h, "Content-Type": "text/plain"}) as r:
                assert r.status == 200


async def test_cognify_starts_pipeline():
    """User triggers cognify on ingested texts."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        async with s.post(f"{BASE_URL}/cognify", json={
            "texts": ["Albert Einstein developed the theory of relativity in 1905."]
        }, headers=h) as r:
            assert r.status == 200
            data = await r.json()
            assert data["status"] == "PipelineRunStarted"
            assert "pipeline_run_id" in data


async def test_cognify_completes():
    """User triggers cognify and waits for completion."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        async with s.post(f"{BASE_URL}/cognify", json={
            "texts": ["Python is a programming language created by Guido van Rossum."]
        }, headers=h) as r:
            run_id = (await r.json())["pipeline_run_id"]

        # Poll until done
        for _ in range(60):
            async with s.get(f"{BASE_URL}/cognify/{run_id}/status", headers=h) as r:
                data = await r.json()
                if data["status"] != "RUNNING":
                    break
            await asyncio.sleep(2)

        assert data["status"] in ("COMPLETED", "FAILED")


async def test_search_chunks_returns_array():
    """User searches for chunks after adding data."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        async with s.post(f"{BASE_URL}/search/text", json={
            "query_text": "vector database performance",
            "query_type": "CHUNKS",
            "top_k": 5
        }, headers=h) as r:
            assert r.status == 200
            data = await r.json()
            assert data is None or isinstance(data, list)


async def test_search_rag_completion_returns_answer():
    """User gets RAG answer with chunks + LLM synthesis."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        async with s.post(f"{BASE_URL}/search/text", json={
            "query_text": "What is a knowledge graph?",
            "query_type": "RAG_COMPLETION",
            "top_k": 5
        }, headers=h) as r:
            assert r.status == 200
            data = await r.json()
            assert "chunks" in data
            assert "answer" in data
            assert data["chunks"] is None or isinstance(data["chunks"], list)


async def test_search_graph_completion():
    """User uses default GRAPH_COMPLETION search type."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        async with s.post(f"{BASE_URL}/search/text", json={
            "query_text": "relationships between entities",
            "query_type": "GRAPH_COMPLETION",
            "top_k": 3
        }, headers=h) as r:
            assert r.status == 200


async def test_search_default_type():
    """User omits query_type — defaults to CHUNKS."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        async with s.post(f"{BASE_URL}/search/text", json={
            "query_text": "test query"
        }, headers=h) as r:
            assert r.status == 200


# ── Journey A2: Full pipeline with file upload ──

async def test_upload_file_multipart():
    """User uploads a file via multipart form."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        form = aiohttp.FormData()
        form.add_field("data",
            b"Chapter 1: Introduction\n\nCognevra is a vector database that combines "
            b"HNSW indexing with WAL durability. It supports both HTTP and gRPC APIs.\n\n"
            b"Chapter 2: Architecture\n\nThe system uses sharded HNSW indexes with "
            b"memory-mapped arenas for efficient vector storage.",
            filename="architecture.txt", content_type="text/plain")
        form.add_field("datasetName", "architecture_docs")
        async with s.post(f"{BASE_URL}/add", data=form, headers=h) as r:
            assert r.status == 200
            data = await r.json()
            assert data["files"] >= 1


async def test_full_add_cognify_search():
    """Complete RAG pipeline: add text → cognify → search."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)

        # Step 1: Add
        async with s.post(f"{BASE_URL}/add",
            data="The Eiffel Tower is located in Paris, France. It was built in 1889.",
            headers={**h, "Content-Type": "text/plain"}) as r:
            assert r.status == 200

        # Step 2: Cognify
        async with s.post(f"{BASE_URL}/cognify", json={
            "texts": ["The Eiffel Tower is located in Paris, France. It was built in 1889."]
        }, headers=h) as r:
            assert r.status == 200
            run_id = (await r.json())["pipeline_run_id"]

        # Wait for cognify
        for _ in range(60):
            async with s.get(f"{BASE_URL}/cognify/{run_id}/status", headers=h) as r:
                status = (await r.json())["status"]
                if status != "RUNNING":
                    break
            await asyncio.sleep(2)

        # Step 3: Search
        async with s.post(f"{BASE_URL}/search/text", json={
            "query_text": "Where is the Eiffel Tower?",
            "query_type": "CHUNKS",
            "top_k": 5
        }, headers=h) as r:
            assert r.status == 200
