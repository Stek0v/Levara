"""Smoke tests — verify every major endpoint responds correctly in dev mode.
Requires only the Go server running, no PostgreSQL/Neo4j/embed-server.
"""
import pytest
import aiohttp
from conftest_http import BASE_URL, sample_vector, unique_id

pytestmark = [pytest.mark.asyncio, pytest.mark.smoke]


# ── Public endpoints ──

async def test_health():
    async with aiohttp.ClientSession() as s:
        async with s.get(f"{BASE_URL}/health") as r:
            assert r.status == 200
            data = await r.json()
            assert data["status"] == "ready"
            assert data["health"] == "healthy"
            assert "version" in data


async def test_info():
    async with aiohttp.ClientSession() as s:
        async with s.get(f"{BASE_URL}/info") as r:
            assert r.status == 200
            data = await r.json()
            assert isinstance(data["dimension"], int)
            assert isinstance(data["shards"], int)
            assert data["status"] == "ready"


async def test_visualize():
    async with aiohttp.ClientSession() as s:
        async with s.get(f"{BASE_URL}/visualize") as r:
            # 200 with HTML if Neo4j configured, 503 if not
            assert r.status in (200, 503)
            if r.status == 200:
                assert "text/html" in r.headers.get("Content-Type", "")


# ── Auth endpoints ──

async def test_register():
    async with aiohttp.ClientSession() as s:
        email = f"smoke_{unique_id()}@test.com"
        async with s.post(f"{BASE_URL}/auth/register", json={
            "email": email, "password": "pass123456"
        }) as r:
            assert r.status == 201
            data = await r.json()
            assert "id" in data
            assert "email" in data
            assert "access_token" in data


async def test_login():
    """Register then login with same credentials."""
    async with aiohttp.ClientSession() as s:
        email = f"smoke_login_{unique_id()}@test.com"
        password = "smokepass123"
        # Register first
        async with s.post(f"{BASE_URL}/auth/register", json={
            "email": email, "password": password
        }) as r:
            assert r.status == 201

        # Login
        async with s.post(f"{BASE_URL}/auth/login", json={
            "email": email, "password": password
        }) as r:
            assert r.status == 200
            data = await r.json()
            assert "access_token" in data
            assert data["token_type"] == "bearer"
            assert len(data["access_token"]) > 20


# ── Legacy vector endpoints ──

async def test_insert_and_search():
    vid = unique_id("smoke")
    vec = sample_vector(DIM)
    async with aiohttp.ClientSession() as s:
        # Insert
        async with s.post(f"{BASE_URL}/insert", json={
            "id": vid, "vector": vec, "data": '{"test": "smoke"}'
        }) as r:
            assert r.status == 200
            data = await r.json()
            assert data["message"] == "data inserted successfully"

        # Search
        async with s.post(f"{BASE_URL}/search", json={
            "vector": vec, "k": 1
        }) as r:
            assert r.status == 200
            data = await r.json()
            assert "results" in data

        # Cleanup
        await s.post(f"{BASE_URL}/delete", json={"ids": [vid]})


async def test_batch_insert():
    ids = [unique_id("smoke_batch") for _ in range(3)]
    records = [{"id": i, "vector": sample_vector(DIM), "data": "{}"} for i in ids]
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/batch_insert", json={"records": records}) as r:
            assert r.status == 200
            data = await r.json()
            assert data["inserted"] == 3
            assert data["failed"] == 0
        # Cleanup
        await s.post(f"{BASE_URL}/delete", json={"ids": ids})


async def test_delete():
    vid = unique_id("smoke_del")
    vec = sample_vector(DIM)
    async with aiohttp.ClientSession() as s:
        await s.post(f"{BASE_URL}/insert", json={"id": vid, "vector": vec, "data": "{}"})
        async with s.post(f"{BASE_URL}/delete", json={"ids": [vid]}) as r:
            assert r.status == 200
            data = await r.json()
            assert "deleted" in data


async def test_insert_validation():
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/insert", json={}) as r:
            assert r.status == 400


# ── Cognee API endpoints (dev mode) ──

async def test_datasets_list_empty():
    async with aiohttp.ClientSession() as s:
        async with s.get(f"{BASE_URL}/datasets") as r:
            assert r.status == 200
            data = await r.json()
            assert isinstance(data, list)


async def test_dataset_create():
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/datasets", json={
            "name": f"smoke_ds_{unique_id()}"
        }) as r:
            assert r.status == 201
            data = await r.json()
            assert "id" in data
            assert "name" in data


async def test_dataset_status():
    async with aiohttp.ClientSession() as s:
        async with s.get(f"{BASE_URL}/datasets/status") as r:
            assert r.status == 200
            data = await r.json()
            assert data["status"] == "ready"


async def test_settings_get():
    async with aiohttp.ClientSession() as s:
        async with s.get(f"{BASE_URL}/settings") as r:
            assert r.status == 200
            data = await r.json()
            assert data["vector_engine"] == "cognevra"
            assert "chunk_strategy" in data
            assert "embedding_dimension" in data


async def test_users_me():
    """Register, login, then hit /users/me with token."""
    async with aiohttp.ClientSession() as s:
        email = f"me_{unique_id()}@dev.com"
        pw = "mepass123"
        await s.post(f"{BASE_URL}/auth/register", json={"email": email, "password": pw})
        async with s.post(f"{BASE_URL}/auth/login", json={"email": email, "password": pw}) as r:
            token = (await r.json())["access_token"]

        async with s.get(f"{BASE_URL}/users/me", headers={
            "Authorization": f"Bearer {token}"
        }) as r:
            assert r.status == 200
            data = await r.json()
            assert "id" in data
            assert "email" in data
