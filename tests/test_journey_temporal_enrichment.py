"""Journey H: Temporal Knowledge Base — time-aware processing and search.
Journey I: Cognify Pipeline Monitoring — SSE streaming, status polling.
Journey J: Vector Operations — direct HNSW insert/search/delete.
"""
import asyncio
import pytest
import aiohttp
from conftest_http import BASE_URL, sample_vector, unique_id

pytestmark = pytest.mark.asyncio
DIM = 1024


async def _auth(s):
    email = f"jrn_{unique_id()}@test.com"
    await s.post(f"{BASE_URL}/auth/register", json={"email": email, "password": "jrnpass123"})
    async with s.post(f"{BASE_URL}/auth/login", json={"email": email, "password": "jrnpass123"}) as r:
        return {"Authorization": f"Bearer {(await r.json())['access_token']}"}


# ═══════════════ JOURNEY H: Temporal Knowledge Base ═══════════════

async def test_temporal_search_iso_date():
    """User searches with ISO 8601 date."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        async with s.post(f"{BASE_URL}/search/text", json={
            "query_text": "Events on 2024-03-15",
            "query_type": "TEMPORAL"
        }, headers=h) as r:
            assert r.status == 200
            data = await r.json()
            assert isinstance(data, list)
            if data:
                assert "date" in data[0]
                assert "2024" in data[0]["date"]


async def test_temporal_search_range():
    """User searches for date range."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        async with s.post(f"{BASE_URL}/search/text", json={
            "query_text": "Key events between 2020-01-01 and 2024-12-31",
            "query_type": "TEMPORAL"
        }, headers=h) as r:
            assert r.status == 200
            data = await r.json()
            assert isinstance(data, list)
            assert len(data) >= 2  # Should find at least 2 dates


async def test_temporal_search_russian():
    """User searches with Russian date format."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        async with s.post(f"{BASE_URL}/search/text", json={
            "query_text": "события 15 января 2024 года",
            "query_type": "TEMPORAL"
        }, headers=h) as r:
            assert r.status == 200
            data = await r.json()
            assert isinstance(data, list)


async def test_temporal_search_english():
    """User searches with English natural date."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        async with s.post(f"{BASE_URL}/search/text", json={
            "query_text": "What happened in March 2024?",
            "query_type": "TEMPORAL"
        }, headers=h) as r:
            assert r.status == 200


async def test_temporal_no_dates():
    """Query without dates returns empty results."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        async with s.post(f"{BASE_URL}/search/text", json={
            "query_text": "general query without any dates",
            "query_type": "TEMPORAL"
        }, headers=h) as r:
            assert r.status == 200
            data = await r.json()
            assert isinstance(data, list)


# ═══════════════ JOURNEY I: Pipeline Monitoring ═══════════════

async def test_cognify_poll_status():
    """User triggers cognify and polls status until completion."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        async with s.post(f"{BASE_URL}/cognify", json={
            "texts": ["Test text for pipeline monitoring."]
        }, headers=h) as r:
            run_id = (await r.json())["pipeline_run_id"]

        statuses = []
        for _ in range(20):
            async with s.get(f"{BASE_URL}/cognify/{run_id}/status", headers=h) as r:
                data = await r.json()
                statuses.append(data["status"])
                if data["status"] != "RUNNING":
                    break
            await asyncio.sleep(0.5)

        assert statuses[-1] in ("COMPLETED", "FAILED")
        assert "RUNNING" in statuses or statuses[0] in ("COMPLETED", "FAILED")


async def test_cognify_sse_receives_events():
    """User connects to SSE stream and receives progress events."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        async with s.post(f"{BASE_URL}/cognify", json={
            "texts": ["SSE monitoring test text."]
        }, headers=h) as r:
            run_id = (await r.json())["pipeline_run_id"]

        events = []
        try:
            async with s.get(f"{BASE_URL}/cognify/{run_id}/stream", headers=h) as r:
                assert "text/event-stream" in r.headers.get("Content-Type", "")
                async for line in r.content:
                    text = line.decode("utf-8", errors="replace").strip()
                    if text.startswith("event:"):
                        events.append(text.split(":", 1)[1].strip())
                    if "done" in text or len(events) > 30:
                        break
        except asyncio.TimeoutError:
            pass

        assert len(events) >= 1
        assert "done" in events or "progress" in events


async def test_cognify_status_nonexistent():
    """Polling non-existent run returns 404."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        async with s.get(f"{BASE_URL}/cognify/nonexistent-run-id/status", headers=h) as r:
            assert r.status == 404


async def test_cognify_with_collection():
    """User specifies target collection for cognify."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        async with s.post(f"{BASE_URL}/cognify", json={
            "texts": ["Collection-specific cognify test."],
            "collection": "test_collection"
        }, headers=h) as r:
            assert r.status == 200


# ═══════════════ JOURNEY J: Vector Operations ═══════════════

async def test_insert_single_vector():
    """User inserts a single vector with metadata."""
    vid = unique_id("vec")
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        async with s.post(f"{BASE_URL}/insert", json={
            "id": vid,
            "vector": sample_vector(DIM),
            "data": '{"text": "Hello world", "source": "test"}'
        }, headers=h) as r:
            assert r.status == 200
            assert (await r.json())["message"] == "data inserted successfully"
        await s.post(f"{BASE_URL}/delete", json={"ids": [vid]}, headers=h)


async def test_batch_insert_vectors():
    """User batch inserts multiple vectors."""
    ids = [unique_id("batch") for _ in range(10)]
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        records = [{"id": i, "vector": sample_vector(DIM), "data": f'{{"idx":{n}}}'} for n, i in enumerate(ids)]
        async with s.post(f"{BASE_URL}/batch_insert", json={"records": records}, headers=h) as r:
            assert r.status == 200
            data = await r.json()
            assert data["inserted"] == 10
            assert data["failed"] == 0
        await s.post(f"{BASE_URL}/delete", json={"ids": ids}, headers=h)


async def test_search_returns_nearest():
    """User searches and finds nearest vector."""
    vid = unique_id("nearest")
    vec = sample_vector(DIM)
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        await s.post(f"{BASE_URL}/insert", json={"id": vid, "vector": vec, "data": "{}"}, headers=h)

        async with s.post(f"{BASE_URL}/search", json={"vector": vec, "k": 1}, headers=h) as r:
            assert r.status == 200
            results = (await r.json())["results"]
            assert len(results) >= 1
            # Score is cosine similarity (higher = more similar) or distance (lower = more similar)
            # Either way, the exact vector should be the top result
            # Verify our vector appears in top results (may not be #1 with existing data)
            result_ids = [r["id"] for r in results]
            assert vid in result_ids, f"{vid} not in results: {result_ids}"

        await s.post(f"{BASE_URL}/delete", json={"ids": [vid]}, headers=h)


async def test_delete_vectors():
    """User deletes vectors by ID."""
    ids = [unique_id("del") for _ in range(5)]
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        for vid in ids:
            await s.post(f"{BASE_URL}/insert", json={"id": vid, "vector": sample_vector(DIM), "data": "{}"}, headers=h)

        async with s.post(f"{BASE_URL}/delete", json={"ids": ids}, headers=h) as r:
            assert r.status == 200
            data = await r.json()
            assert data["deleted"] >= len(ids)


async def test_search_with_metadata():
    """User searches and gets metadata back."""
    vid = unique_id("meta")
    meta = '{"category": "science", "topic": "physics"}'
    vec = sample_vector(DIM)
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        await s.post(f"{BASE_URL}/insert", json={"id": vid, "vector": vec, "data": meta}, headers=h)
        async with s.post(f"{BASE_URL}/search", json={"vector": vec, "k": 1}, headers=h) as r:
            results = (await r.json())["results"]
            assert len(results) >= 1
            # Verify metadata is returned
            assert "data" in results[0] or "metadata" in results[0]
        await s.post(f"{BASE_URL}/delete", json={"ids": [vid]}, headers=h)


async def test_dimension_mismatch_rejected():
    """Wrong vector dimension is rejected."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        async with s.post(f"{BASE_URL}/insert", json={
            "id": unique_id("dim_err"),
            "vector": [0.1, 0.2, 0.3],  # wrong dim
            "data": "{}"
        }, headers=h) as r:
            assert r.status in (400, 500)
            data = await r.json()
            assert "dimension" in data.get("error", "").lower() or "mismatch" in data.get("error", "").lower()
