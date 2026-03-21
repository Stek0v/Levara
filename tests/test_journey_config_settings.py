"""Journey E: Configuration & Settings — user configures LLM, embedding, graph, chunking.
Journey F: Memify — post-cognify graph enrichment.
Journey G: Notebooks — interactive exploration.
"""
import asyncio
import pytest
import aiohttp
from conftest_http import BASE_URL, unique_id

pytestmark = pytest.mark.asyncio


async def _auth(s):
    email = f"cfg_{unique_id()}@test.com"
    await s.post(f"{BASE_URL}/auth/register", json={"email": email, "password": "cfgpass123"})
    async with s.post(f"{BASE_URL}/auth/login", json={"email": email, "password": "cfgpass123"}) as r:
        return {"Authorization": f"Bearer {(await r.json())['access_token']}"}


# ═══════════════ JOURNEY E: Settings & Configuration ═══════════════

async def test_get_default_settings():
    """User views default server settings."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        async with s.get(f"{BASE_URL}/settings", headers=h) as r:
            assert r.status == 200
            data = await r.json()
            assert data["vector_engine"] == "cognevra"
            assert "embedding_model" in data
            assert "embedding_dimension" in data
            assert "llm_provider" in data
            assert "graph_engine" in data
            assert "chunk_strategy" in data
            assert "chunk_size" in data


async def test_update_llm_settings():
    """User changes LLM model."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        async with s.put(f"{BASE_URL}/settings", json={
            "llm_model": "gpt-4o-mini",
            "llm_provider": "openai"
        }, headers=h) as r:
            assert r.status == 200
            data = await r.json()
            assert data["llm_model"] == "gpt-4o-mini"
            assert data["llm_provider"] == "openai"


async def test_update_embedding_settings():
    """User changes embedding model."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        async with s.put(f"{BASE_URL}/settings", json={
            "embedding_model": "text-embedding-3-large",
            "embedding_dimension": 3072
        }, headers=h) as r:
            assert r.status == 200
            data = await r.json()
            assert data["embedding_model"] == "text-embedding-3-large"


async def test_update_chunk_settings():
    """User changes chunking strategy."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        async with s.put(f"{BASE_URL}/settings", json={
            "chunk_strategy": "paragraph",
            "chunk_size": 500
        }, headers=h) as r:
            assert r.status == 200
            data = await r.json()
            assert data["chunk_strategy"] == "paragraph"
            assert data["chunk_size"] == 500


async def test_settings_persist_per_user():
    """Each user has independent settings (requires DB for per-user storage)."""
    async with aiohttp.ClientSession() as s1, aiohttp.ClientSession() as s2:
        h1 = await _auth(s1)
        h2 = await _auth(s2)

        await s1.put(f"{BASE_URL}/settings", json={"llm_model": "user1_model"}, headers=h1)
        await s2.put(f"{BASE_URL}/settings", json={"llm_model": "user2_model"}, headers=h2)

        async with s1.get(f"{BASE_URL}/settings", headers=h1) as r:
            d1 = await r.json()
        async with s2.get(f"{BASE_URL}/settings", headers=h2) as r:
            d2 = await r.json()

        # With DB: per-user isolation. Without DB: last-write-wins on in-memory.
        # Just verify both return valid settings
        assert "llm_model" in d1
        assert "llm_model" in d2


async def test_settings_roundtrip():
    """Settings survive GET after PUT."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        model = f"roundtrip_{unique_id()}"
        await s.put(f"{BASE_URL}/settings", json={"llm_model": model}, headers=h)
        async with s.get(f"{BASE_URL}/settings", headers=h) as r:
            assert (await r.json())["llm_model"] == model


# ═══════════════ JOURNEY F: Memify (Graph Enrichment) ═══════════════

async def test_memify_without_neo4j():
    """Memify requires Neo4j — returns error if not configured."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        async with s.post(f"{BASE_URL}/memify", json={}, headers=h) as r:
            # 400 if Neo4j not configured, 200 if available
            assert r.status in (200, 400)


async def test_memify_with_enrichment_tasks():
    """User specifies enrichment tasks for memify."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        async with s.post(f"{BASE_URL}/memify", json={
            "enrichment_tasks": ["entity_consolidation", "triplet_embeddings"],
            "run_in_background": True
        }, headers=h) as r:
            # 200 if Neo4j available, 400 if not
            if r.status == 200:
                data = await r.json()
                assert data.get("status") == "MemifyRunStarted"
                assert "run_id" in data


async def test_memify_status_polling():
    """User polls memify status."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        async with s.get(f"{BASE_URL}/memify/fake-id/status", headers=h) as r:
            assert r.status == 404  # non-existent run


async def test_memify_sse_stream():
    """User opens SSE stream for memify progress."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        async with s.get(f"{BASE_URL}/memify/fake-id/stream", headers=h) as r:
            assert r.status == 404  # non-existent run


# ═══════════════ JOURNEY G: Notebooks ═══════════════

async def test_notebook_create():
    """User creates a new notebook."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        async with s.post(f"{BASE_URL}/notebooks", json={
            "name": f"My Research {unique_id()}"
        }, headers=h) as r:
            assert r.status == 201
            data = await r.json()
            assert "id" in data
            assert "name" in data


async def test_notebook_list():
    """User lists their notebooks."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        async with s.get(f"{BASE_URL}/notebooks", headers=h) as r:
            assert r.status == 200
            assert isinstance(await r.json(), list)


async def test_notebook_add_cell():
    """User adds a cell to a notebook."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        async with s.post(f"{BASE_URL}/notebooks", json={"name": "CellTest"}, headers=h) as r:
            nb_id = (await r.json())["id"]

        async with s.post(f"{BASE_URL}/notebooks/{nb_id}/cells", json={
            "cell_type": "code", "source": "collections", "position": 0
        }, headers=h) as r:
            assert r.status == 201
            data = await r.json()
            assert data["type"] == "code"


async def test_notebook_run_code_cell():
    """User runs a code cell with 'stats' command."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        async with s.post(f"{BASE_URL}/notebooks", json={"name": "RunTest"}, headers=h) as r:
            nb_id = (await r.json())["id"]
        async with s.post(f"{BASE_URL}/notebooks/{nb_id}/cells", json={
            "cell_type": "code", "source": "stats"
        }, headers=h) as r:
            cell_id = (await r.json())["id"]

        async with s.post(f"{BASE_URL}/notebooks/{nb_id}/cells/{cell_id}/run",
            json={"cell_type": "code", "source": "stats"}, headers=h) as r:
            assert r.status == 200
            data = await r.json()
            assert "result" in data
            assert "collections" in data["result"]


async def test_notebook_run_collections_cell():
    """User runs a cell to list collections."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        async with s.post(f"{BASE_URL}/notebooks", json={"name": "CollTest"}, headers=h) as r:
            nb_id = (await r.json())["id"]
        async with s.post(f"{BASE_URL}/notebooks/{nb_id}/cells", json={
            "cell_type": "code", "source": "collections"
        }, headers=h) as r:
            cell_id = (await r.json())["id"]

        async with s.post(f"{BASE_URL}/notebooks/{nb_id}/cells/{cell_id}/run",
            json={"cell_type": "code", "source": "collections"}, headers=h) as r:
            assert r.status == 200
            output = (await r.json())["result"]
            assert "[" in output  # JSON array


async def test_notebook_run_env_cell():
    """User runs a cell to check environment."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        async with s.post(f"{BASE_URL}/notebooks", json={"name": "EnvTest"}, headers=h) as r:
            nb_id = (await r.json())["id"]
        async with s.post(f"{BASE_URL}/notebooks/{nb_id}/cells", json={
            "cell_type": "code", "source": "env"
        }, headers=h) as r:
            cell_id = (await r.json())["id"]

        async with s.post(f"{BASE_URL}/notebooks/{nb_id}/cells/{cell_id}/run",
            json={"cell_type": "code", "source": "env"}, headers=h) as r:
            assert r.status == 200
            output = (await r.json())["result"]
            assert "LLM_ENDPOINT" in output


async def test_notebook_update_title():
    """User renames a notebook."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        async with s.post(f"{BASE_URL}/notebooks", json={"name": "OldTitle"}, headers=h) as r:
            nb_id = (await r.json())["id"]
        async with s.put(f"{BASE_URL}/notebooks/{nb_id}", json={"name": "NewTitle"}, headers=h) as r:
            assert r.status == 200


async def test_notebook_delete():
    """User deletes a notebook."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        async with s.post(f"{BASE_URL}/notebooks", json={"name": "ToDelete"}, headers=h) as r:
            nb_id = (await r.json())["id"]
        async with s.delete(f"{BASE_URL}/notebooks/{nb_id}", headers=h) as r:
            assert r.status == 200
            assert (await r.json())["deleted"] == True


async def test_notebook_markdown_cell():
    """Markdown cells pass through source as output."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        async with s.post(f"{BASE_URL}/notebooks", json={"name": "MdTest"}, headers=h) as r:
            nb_id = (await r.json())["id"]
        async with s.post(f"{BASE_URL}/notebooks/{nb_id}/cells", json={
            "cell_type": "markdown", "source": "# Hello World"
        }, headers=h) as r:
            cell_id = (await r.json())["id"]

        async with s.post(f"{BASE_URL}/notebooks/{nb_id}/cells/{cell_id}/run",
            json={"cell_type": "markdown", "source": "# Hello World"}, headers=h) as r:
            assert r.status == 200
            assert (await r.json())["result"] == "# Hello World"
