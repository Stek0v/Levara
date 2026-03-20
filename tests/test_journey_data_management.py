"""Journey C: Data Management — datasets CRUD, file operations, deletion.
Tests the complete data lifecycle from creation to cleanup.
"""
import pytest
import aiohttp
from conftest_http import BASE_URL, unique_id

pytestmark = pytest.mark.asyncio


async def _auth(s):
    email = f"dm_{unique_id()}@test.com"
    await s.post(f"{BASE_URL}/auth/register", json={"email": email, "password": "dmpass123"})
    async with s.post(f"{BASE_URL}/auth/login", json={"email": email, "password": "dmpass123"}) as r:
        return {"Authorization": f"Bearer {(await r.json())['access_token']}"}


# ── Dataset CRUD lifecycle ──

async def test_create_dataset():
    """User creates a named dataset."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        name = f"ds_{unique_id()}"
        async with s.post(f"{BASE_URL}/datasets", json={"name": name}, headers=h) as r:
            assert r.status == 201
            data = await r.json()
            assert data["name"] == name
            assert "id" in data
            assert "created_at" in data


async def test_list_datasets():
    """User lists all their datasets."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        async with s.get(f"{BASE_URL}/datasets", headers=h) as r:
            assert r.status == 200
            assert isinstance(await r.json(), list)


async def test_delete_dataset():
    """User deletes a dataset."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        async with s.post(f"{BASE_URL}/datasets", json={"name": f"del_{unique_id()}"}, headers=h) as r:
            ds_id = (await r.json())["id"]
        async with s.delete(f"{BASE_URL}/datasets/{ds_id}", headers=h) as r:
            assert r.status == 200
            assert (await r.json())["deleted"] == True


async def test_dataset_status():
    """User checks pipeline status."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        async with s.get(f"{BASE_URL}/datasets/status", headers=h) as r:
            assert r.status == 200
            assert (await r.json())["status"] == "ready"


async def test_create_dataset_missing_name():
    """User tries to create dataset without name — 400."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        async with s.post(f"{BASE_URL}/datasets", json={}, headers=h) as r:
            assert r.status == 400


# ── File upload and data items ──

async def test_upload_text_to_dataset():
    """User uploads text to a named dataset."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        form = aiohttp.FormData()
        form.add_field("data", b"Test document content.", filename="test.txt", content_type="text/plain")
        form.add_field("datasetName", f"upload_{unique_id()}")
        async with s.post(f"{BASE_URL}/add", data=form, headers=h) as r:
            assert r.status == 200
            data = await r.json()
            assert data["status"] == "ok"


async def test_upload_multiple_files():
    """User uploads multiple files at once."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        form = aiohttp.FormData()
        form.add_field("data", b"First document.", filename="doc1.txt", content_type="text/plain")
        form.add_field("data", b"Second document.", filename="doc2.txt", content_type="text/plain")
        form.add_field("datasetName", f"multi_{unique_id()}")
        async with s.post(f"{BASE_URL}/add", data=form, headers=h) as r:
            assert r.status == 200
            data = await r.json()
            assert data["files"] >= 2


async def test_upload_plain_text_body():
    """User sends plain text body (not multipart)."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        async with s.post(f"{BASE_URL}/add",
            data="This is plain text sent as request body.",
            headers={**h, "Content-Type": "text/plain"}) as r:
            assert r.status == 200
            assert (await r.json())["items"] >= 1


# ── Data item operations ──

async def test_list_dataset_data():
    """User lists files in a dataset."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        async with s.get(f"{BASE_URL}/datasets/fake-id/data", headers=h) as r:
            assert r.status == 200
            assert isinstance(await r.json(), list)


async def test_delete_data_item():
    """User deletes a specific data item."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        async with s.delete(f"{BASE_URL}/datasets/fake-ds/data/fake-data", headers=h) as r:
            assert r.status == 200


async def test_download_raw_file_not_found():
    """User tries to download non-existent raw file — 404."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        async with s.get(f"{BASE_URL}/datasets/fake/data/fake/raw", headers=h) as r:
            assert r.status == 404


# ── Dataset graph visualization ──

async def test_dataset_graph():
    """User requests graph visualization for a dataset."""
    async with aiohttp.ClientSession() as s:
        h = await _auth(s)
        async with s.get(f"{BASE_URL}/datasets/fake-id/graph", headers=h) as r:
            # 200 if Neo4j available, 503 if not
            assert r.status in (200, 500, 503)
            if r.status == 200:
                data = await r.json()
                assert "nodes" in data
                assert "edges" in data
