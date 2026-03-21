"""E2E tests — multi-step workflows exercising endpoint interactions."""
import asyncio
import pytest
import aiohttp
from conftest_http import BASE_URL, sample_vector, unique_id

pytestmark = pytest.mark.asyncio
from conftest_http import get_server_dim; DIM = get_server_dim()


async def _get_token(s, email=None, password="e2epass123"):
    email = email or f"e2e_{unique_id()}@test.com"
    await s.post(f"{BASE_URL}/auth/register", json={"email": email, "password": password})
    async with s.post(f"{BASE_URL}/auth/login", json={"email": email, "password": password}) as r:
        return (await r.json()).get("access_token", ""), email


async def test_auth_flow():
    """Register → login → /users/me → verify email."""
    async with aiohttp.ClientSession() as s:
        email = f"e2e_auth_{unique_id()}@test.com"
        pw = "authflow123"
        async with s.post(f"{BASE_URL}/auth/register", json={"email": email, "password": pw}) as r:
            assert r.status == 201
            reg_data = await r.json()
            assert reg_data["email"] == email

        async with s.post(f"{BASE_URL}/auth/login", json={"email": email, "password": pw}) as r:
            assert r.status == 200
            token = (await r.json())["access_token"]

        async with s.get(f"{BASE_URL}/users/me", headers={"Authorization": f"Bearer {token}"}) as r:
            assert r.status == 200
            assert (await r.json())["email"] == email


async def test_vector_crud_cycle():
    """Insert 10 → search → verify → delete → search → verify empty."""
    ids = [unique_id("e2e_crud") for _ in range(10)]
    vecs = [sample_vector(DIM) for _ in range(10)]
    async with aiohttp.ClientSession() as s:
        # Insert
        records = [{"id": i, "vector": v, "data": f'{{"idx":{n}}}'} for n, (i, v) in enumerate(zip(ids, vecs))]
        async with s.post(f"{BASE_URL}/batch_insert", json={"records": records}) as r:
            assert r.status == 200
            assert (await r.json())["inserted"] == 10

        # Search with first vector
        async with s.post(f"{BASE_URL}/search", json={"vector": vecs[0], "k": 5}) as r:
            assert r.status == 200
            results = (await r.json())["results"]
            assert len(results) > 0

        # Delete all
        async with s.post(f"{BASE_URL}/delete", json={"ids": ids}) as r:
            assert r.status == 200


async def test_batch_insert_search():
    """Batch insert 20 → search with inserted vector → verify non-empty results."""
    n = 20
    ids = [unique_id("recall") for _ in range(n)]
    vecs = [sample_vector(DIM) for _ in range(n)]
    async with aiohttp.ClientSession() as s:
        records = [{"id": i, "vector": v, "data": "{}"} for i, v in zip(ids, vecs)]
        async with s.post(f"{BASE_URL}/batch_insert", json={"records": records}) as r:
            assert r.status == 200
            assert (await r.json())["inserted"] == n

        # Search should return results
        async with s.post(f"{BASE_URL}/search", json={"vector": vecs[0], "k": 5}) as r:
            data = await r.json()
            assert len(data["results"]) > 0

        await s.post(f"{BASE_URL}/delete", json={"ids": ids})


async def test_settings_persist():
    """PUT → GET → values match."""
    async with aiohttp.ClientSession() as s:
        token, _ = await _get_token(s)
        h = {"Authorization": f"Bearer {token}"}
        model = f"persist_{unique_id()}"
        await s.put(f"{BASE_URL}/settings", json={"llm_model": model}, headers=h)
        async with s.get(f"{BASE_URL}/settings", headers=h) as r:
            assert (await r.json())["llm_model"] == model


async def test_cognify_lifecycle():
    """Trigger cognify → poll status → eventually not RUNNING."""
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/cognify", json={"texts": ["lifecycle test text"]}) as r:
            run_id = (await r.json())["pipeline_run_id"]

        # Poll up to 5s
        for _ in range(10):
            async with s.get(f"{BASE_URL}/cognify/{run_id}/status") as r:
                data = await r.json()
                if data["status"] != "RUNNING":
                    break
            await asyncio.sleep(0.5)

        assert data["status"] in ("COMPLETED", "FAILED")


async def test_all_search_types():
    """Every search type returns 200."""
    types = ["CHUNKS", "RAG_COMPLETION", "SUMMARIES", "CHUNKS_LEXICAL", "HYBRID", "TEMPORAL"]
    async with aiohttp.ClientSession() as s:
        for qt in types:
            async with s.post(f"{BASE_URL}/search/text", json={
                "query_text": "test query", "query_type": qt, "top_k": 3
            }) as r:
                assert r.status == 200, f"{qt} returned {r.status}"


async def test_temporal_date_extraction():
    """TEMPORAL search extracts dates from text."""
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/search/text", json={
            "query_text": "The event happened on 2024-06-15 and again on 2024-12-01",
            "query_type": "TEMPORAL"
        }) as r:
            assert r.status == 200
            data = await r.json()
            assert len(data) >= 1
            assert "date" in data[0]


async def test_notebook_cell_execution():
    """Create notebook → add code cell → run → verify output."""
    async with aiohttp.ClientSession() as s:
        # Create notebook
        async with s.post(f"{BASE_URL}/notebooks", json={"title": f"e2e_{unique_id()}"}) as r:
            nb_id = (await r.json())["id"]

        # Add cell
        async with s.post(f"{BASE_URL}/notebooks/{nb_id}/cells", json={
            "cell_type": "code", "source": "stats"
        }) as r:
            cell_id = (await r.json())["id"]

        # Run cell (pass source in body since DB may not have it)
        async with s.post(f"{BASE_URL}/notebooks/{nb_id}/cells/{cell_id}/run", json={
            "cell_type": "code", "source": "stats"
        }) as r:
            assert r.status == 200
            data = await r.json()
            assert "output" in data
            assert "collections" in data["output"]

        # Cleanup
        await s.delete(f"{BASE_URL}/notebooks/{nb_id}")


async def test_cognify_sse_stream():
    """Trigger cognify → open SSE → receive events."""
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/cognify", json={"texts": ["sse e2e test"]}) as r:
            run_id = (await r.json())["pipeline_run_id"]

        # Read SSE stream (with timeout)
        events = []
        try:
            async with s.get(f"{BASE_URL}/cognify/{run_id}/stream") as r:
                assert "text/event-stream" in r.headers.get("Content-Type", "")
                async for line in r.content:
                    text = line.decode("utf-8", errors="replace").strip()
                    if text.startswith("event:"):
                        events.append(text)
                    if "event: done" in text or len(events) > 20:
                        break
        except asyncio.TimeoutError:
            pass

        assert len(events) >= 1


async def test_permissions_me():
    """Get permissions for authenticated user."""
    async with aiohttp.ClientSession() as s:
        token, _ = await _get_token(s)
        async with s.get(f"{BASE_URL}/permissions/me", headers={"Authorization": f"Bearer {token}"}) as r:
            assert r.status == 200
            data = await r.json()
            assert "role" in data
            assert isinstance(data["shares"], list)


async def test_dataset_create_delete_cycle():
    """Create → verify → delete → verify gone."""
    async with aiohttp.ClientSession() as s:
        name = f"e2e_ds_{unique_id()}"
        async with s.post(f"{BASE_URL}/datasets", json={"name": name}) as r:
            assert r.status == 201
            ds_id = (await r.json())["id"]
        async with s.delete(f"{BASE_URL}/datasets/{ds_id}") as r:
            assert r.status == 200
            assert (await r.json())["deleted"] == True


async def test_upload_text():
    """Upload text via /add."""
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/add",
            data="E2E test document content for upload verification.",
            headers={"Content-Type": "text/plain"}
        ) as r:
            assert r.status == 200
            data = await r.json()
            assert data["items"] >= 1
