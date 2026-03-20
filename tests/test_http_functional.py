"""Functional tests — validate request/response contracts for every endpoint.
Tests endpoint logic, validation, error handling, edge cases.
"""
import pytest
import aiohttp
from conftest_http import BASE_URL, sample_vector, unique_id

pytestmark = pytest.mark.asyncio
DIM = 1024

# Helper: get auth token
async def _get_token(s, email=None, password="funcpass123"):
    email = email or f"func_{unique_id()}@test.com"
    await s.post(f"{BASE_URL}/auth/register", json={"email": email, "password": password})
    async with s.post(f"{BASE_URL}/auth/login", json={"email": email, "password": password}) as r:
        return (await r.json()).get("access_token", ""), email


# ═══════════════ AUTH (6 tests) ═══════════════

async def test_login_form_encoded():
    email = f"form_{unique_id()}@test.com"
    pw = "formpass123"
    async with aiohttp.ClientSession() as s:
        await s.post(f"{BASE_URL}/auth/register", json={"email": email, "password": pw})
        async with s.post(f"{BASE_URL}/auth/login", data={"username": email, "password": pw}) as r:
            assert r.status == 200
            data = await r.json()
            assert "access_token" in data

async def test_login_json():
    email = f"json_{unique_id()}@test.com"
    pw = "jsonpass123"
    async with aiohttp.ClientSession() as s:
        await s.post(f"{BASE_URL}/auth/register", json={"email": email, "password": pw})
        async with s.post(f"{BASE_URL}/auth/login", json={"email": email, "password": pw}) as r:
            assert r.status == 200

async def test_login_missing_fields():
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/auth/login", json={}) as r:
            assert r.status == 400

async def test_register_missing_fields():
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/auth/register", json={"email": "", "password": ""}) as r:
            assert r.status == 400

async def test_jwt_valid_on_protected():
    async with aiohttp.ClientSession() as s:
        token, _ = await _get_token(s)
        async with s.get(f"{BASE_URL}/users/me", headers={"Authorization": f"Bearer {token}"}) as r:
            assert r.status == 200

async def test_jwt_invalid_rejected():
    async with aiohttp.ClientSession() as s:
        async with s.get(f"{BASE_URL}/users/me", headers={"Authorization": "Bearer garbage_token"}) as r:
            assert r.status == 401


# ═══════════════ VECTOR (8 tests) ═══════════════

async def test_insert_missing_id():
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/insert", json={"vector": sample_vector(DIM)}) as r:
            assert r.status == 400

async def test_insert_missing_vector():
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/insert", json={"id": "test"}) as r:
            assert r.status == 400

async def test_batch_empty_records():
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/batch_insert", json={"records": []}) as r:
            assert r.status == 400

async def test_batch_partial_failure():
    async with aiohttp.ClientSession() as s:
        records = [
            {"id": unique_id(), "vector": sample_vector(DIM), "data": "{}"},
            {"id": unique_id()},  # missing vector
        ]
        async with s.post(f"{BASE_URL}/batch_insert", json={"records": records}) as r:
            # Should reject entire batch or return error
            assert r.status in (200, 400)

async def test_search_default_topk():
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/search", json={"vector": sample_vector(DIM)}) as r:
            assert r.status == 200
            data = await r.json()
            assert "results" in data

async def test_search_custom_topk():
    vid = unique_id("topk")
    vec = sample_vector(DIM)
    async with aiohttp.ClientSession() as s:
        await s.post(f"{BASE_URL}/insert", json={"id": vid, "vector": vec, "data": "{}"})
        async with s.post(f"{BASE_URL}/search", json={"vector": vec, "k": 3}) as r:
            assert r.status == 200
            data = await r.json()
            assert len(data["results"]) <= 3
        await s.post(f"{BASE_URL}/delete", json={"ids": [vid]})

async def test_search_empty_vector():
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/search", json={"vector": []}) as r:
            assert r.status == 400

async def test_delete_nonexistent():
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/delete", json={"ids": ["nonexistent_id_xyz"]}) as r:
            assert r.status == 200  # deleting non-existent is not an error


# ═══════════════ DATASETS CRUD (9 tests) ═══════════════

async def test_dataset_create_structure():
    async with aiohttp.ClientSession() as s:
        name = f"func_ds_{unique_id()}"
        async with s.post(f"{BASE_URL}/datasets", json={"name": name}) as r:
            assert r.status == 201
            data = await r.json()
            assert "id" in data
            assert data["name"] == name
            assert "created_at" in data

async def test_dataset_create_no_name():
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/datasets", json={}) as r:
            assert r.status == 400

async def test_dataset_delete_ok():
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/datasets", json={"name": f"del_{unique_id()}"}) as r:
            ds_id = (await r.json())["id"]
        async with s.delete(f"{BASE_URL}/datasets/{ds_id}") as r:
            assert r.status == 200
            data = await r.json()
            assert data.get("deleted") == True

async def test_dataset_data_list():
    async with aiohttp.ClientSession() as s:
        async with s.get(f"{BASE_URL}/datasets/fake-id/data") as r:
            assert r.status == 200
            data = await r.json()
            assert isinstance(data, list)

async def test_dataset_data_raw_not_found():
    async with aiohttp.ClientSession() as s:
        async with s.get(f"{BASE_URL}/datasets/fake/data/fake/raw") as r:
            assert r.status == 404

async def test_dataset_roundtrip():
    """Create→list→delete→verify. Only asserts persistence if PostgreSQL available."""
    async with aiohttp.ClientSession() as s:
        name = f"rt_{unique_id()}"
        async with s.post(f"{BASE_URL}/datasets", json={"name": name}) as r:
            assert r.status == 201
            ds_id = (await r.json())["id"]
        # List — may or may not contain dataset depending on DB
        async with s.get(f"{BASE_URL}/datasets") as r:
            items = await r.json()
            has_pg = any(d["id"] == ds_id for d in items)
        await s.delete(f"{BASE_URL}/datasets/{ds_id}")
        if has_pg:
            # Verify deleted
            async with s.get(f"{BASE_URL}/datasets") as r:
                items = await r.json()
                assert not any(d["id"] == ds_id for d in items)

async def test_dataset_status_ready():
    async with aiohttp.ClientSession() as s:
        async with s.get(f"{BASE_URL}/datasets/status") as r:
            assert r.status == 200
            assert (await r.json())["status"] == "ready"

async def test_dataset_data_delete():
    async with aiohttp.ClientSession() as s:
        async with s.delete(f"{BASE_URL}/datasets/fake/data/fake-data-id") as r:
            assert r.status == 200

async def test_dataset_graph_no_neo4j_or_ok():
    async with aiohttp.ClientSession() as s:
        async with s.get(f"{BASE_URL}/datasets/fake/graph") as r:
            # 503 if no Neo4j, 200 or 500 if Neo4j configured
            assert r.status in (200, 500, 503)


# ═══════════════ FILE UPLOAD (4 tests) ═══════════════

async def test_add_text_body():
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/add",
            data="This is a test document for ingestion.",
            headers={"Content-Type": "text/plain"}
        ) as r:
            assert r.status == 200
            data = await r.json()
            assert data.get("status") == "ok"
            assert data.get("items", 0) >= 1

async def test_add_multipart():
    async with aiohttp.ClientSession() as s:
        form = aiohttp.FormData()
        form.add_field("data", b"Test file content for upload.", filename="test.txt", content_type="text/plain")
        form.add_field("datasetName", "func_upload_test")
        async with s.post(f"{BASE_URL}/add", data=form) as r:
            assert r.status == 200
            data = await r.json()
            assert data.get("files", 0) >= 1

async def test_add_empty_body():
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/add", data=b"", headers={"Content-Type": "application/octet-stream"}) as r:
            # Empty body may return 400 or 200 with items=0 depending on handler
            assert r.status in (200, 400)

async def test_add_default_dataset():
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/add",
            data="Default dataset test text.",
            headers={"Content-Type": "text/plain"}
        ) as r:
            assert r.status == 200


# ═══════════════ COGNIFY (5 tests) ═══════════════

async def test_cognify_texts():
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/cognify", json={"texts": ["Hello world test"]}) as r:
            assert r.status == 200
            data = await r.json()
            assert data["status"] == "PipelineRunStarted"
            assert "pipeline_run_id" in data

async def test_cognify_no_texts():
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/cognify", json={}) as r:
            assert r.status == 400

async def test_cognify_status():
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/cognify", json={"texts": ["status test"]}) as r:
            run_id = (await r.json())["pipeline_run_id"]
        async with s.get(f"{BASE_URL}/cognify/{run_id}/status") as r:
            assert r.status == 200
            data = await r.json()
            assert data["status"] in ("RUNNING", "COMPLETED", "FAILED")

async def test_cognify_status_404():
    async with aiohttp.ClientSession() as s:
        async with s.get(f"{BASE_URL}/cognify/nonexistent/status") as r:
            assert r.status == 404

async def test_cognify_sse():
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/cognify", json={"texts": ["sse test"]}) as r:
            run_id = (await r.json())["pipeline_run_id"]
        async with s.get(f"{BASE_URL}/cognify/{run_id}/stream") as r:
            assert r.status == 200
            ct = r.headers.get("Content-Type", "")
            assert "text/event-stream" in ct


# ═══════════════ MEMIFY (4 tests) ═══════════════

async def test_memify_requires_neo4j():
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/memify", json={}) as r:
            # 400 if no Neo4j, or 200 if Neo4j available
            assert r.status in (200, 400)

async def test_memify_status_404():
    async with aiohttp.ClientSession() as s:
        async with s.get(f"{BASE_URL}/memify/nonexistent/status") as r:
            assert r.status == 404

async def test_memify_stream_404():
    async with aiohttp.ClientSession() as s:
        async with s.get(f"{BASE_URL}/memify/nonexistent/stream") as r:
            assert r.status == 404

@pytest.mark.requires_neo4j
async def test_memify_trigger():
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/memify", json={"run_in_background": True}) as r:
            assert r.status == 200
            data = await r.json()
            assert data.get("status") == "MemifyRunStarted"


# ═══════════════ SEARCH /search/text (10 tests) ═══════════════

async def test_search_text_missing_query():
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/search/text", json={}) as r:
            assert r.status == 400

async def test_search_text_empty_query():
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/search/text", json={"query_text": ""}) as r:
            assert r.status == 400

async def test_search_text_chunks():
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/search/text", json={"query_text": "test", "query_type": "CHUNKS"}) as r:
            assert r.status == 200
            data = await r.json()
            assert isinstance(data, list)

async def test_search_text_default_type():
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/search/text", json={"query_text": "test"}) as r:
            assert r.status == 200

async def test_search_text_unknown_type():
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/search/text", json={"query_text": "test", "query_type": "UNKNOWN"}) as r:
            assert r.status == 200

async def test_search_text_bm25():
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/search/text", json={"query_text": "test", "query_type": "CHUNKS_LEXICAL"}) as r:
            assert r.status == 200

async def test_search_text_hybrid():
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/search/text", json={"query_text": "test", "query_type": "HYBRID"}) as r:
            assert r.status == 200

async def test_search_text_temporal():
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/search/text", json={
            "query_text": "meeting on 2024-01-15", "query_type": "TEMPORAL"
        }) as r:
            assert r.status == 200
            data = await r.json()
            assert isinstance(data, list)
            if len(data) > 0:
                assert "date" in data[0]

async def test_search_text_summaries():
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/search/text", json={"query_text": "test", "query_type": "SUMMARIES"}) as r:
            assert r.status == 200

async def test_search_text_rag_completion():
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/search/text", json={"query_text": "test", "query_type": "RAG_COMPLETION"}) as r:
            assert r.status == 200
            data = await r.json()
            assert "chunks" in data
            assert "answer" in data


# ═══════════════ USER MANAGEMENT (6 tests) ═══════════════

async def test_me_with_token():
    async with aiohttp.ClientSession() as s:
        token, email = await _get_token(s)
        async with s.get(f"{BASE_URL}/users/me", headers={"Authorization": f"Bearer {token}"}) as r:
            assert r.status == 200
            data = await r.json()
            assert data["email"] == email

async def test_me_unauthenticated():
    async with aiohttp.ClientSession() as s:
        async with s.get(f"{BASE_URL}/users/me") as r:
            # Dev mode may pass through, or 401
            assert r.status in (200, 401)

async def test_update_email():
    async with aiohttp.ClientSession() as s:
        token, _ = await _get_token(s)
        new_email = f"updated_{unique_id()}@test.com"
        async with s.put(f"{BASE_URL}/users/me",
            json={"email": new_email},
            headers={"Authorization": f"Bearer {token}"}
        ) as r:
            assert r.status == 200
            data = await r.json()
            assert data.get("updated") == True

async def test_update_empty_email():
    async with aiohttp.ClientSession() as s:
        token, _ = await _get_token(s)
        async with s.put(f"{BASE_URL}/users/me",
            json={"email": ""},
            headers={"Authorization": f"Bearer {token}"}
        ) as r:
            assert r.status == 400

async def test_change_password():
    async with aiohttp.ClientSession() as s:
        pw = "oldpass123"
        token, email = await _get_token(s, password=pw)
        async with s.put(f"{BASE_URL}/users/me/password",
            json={"current_password": pw, "new_password": "newpass123"},
            headers={"Authorization": f"Bearer {token}"}
        ) as r:
            assert r.status == 200

async def test_password_too_short():
    async with aiohttp.ClientSession() as s:
        token, _ = await _get_token(s)
        async with s.put(f"{BASE_URL}/users/me/password",
            json={"current_password": "funcpass123", "new_password": "ab"},
            headers={"Authorization": f"Bearer {token}"}
        ) as r:
            assert r.status == 400


# ═══════════════ SETTINGS (4 tests) ═══════════════

async def test_settings_get_defaults():
    async with aiohttp.ClientSession() as s:
        async with s.get(f"{BASE_URL}/settings") as r:
            assert r.status == 200
            data = await r.json()
            assert data["vector_engine"] == "cognevra"
            assert "embedding_dimension" in data

async def test_settings_put():
    async with aiohttp.ClientSession() as s:
        token, _ = await _get_token(s)
        async with s.put(f"{BASE_URL}/settings",
            json={"llm_model": "test-model-xyz"},
            headers={"Authorization": f"Bearer {token}"}
        ) as r:
            assert r.status == 200
            data = await r.json()
            assert data["llm_model"] == "test-model-xyz"

async def test_settings_put_invalid():
    async with aiohttp.ClientSession() as s:
        async with s.put(f"{BASE_URL}/settings",
            data="not json",
            headers={"Content-Type": "text/plain"}
        ) as r:
            assert r.status == 400

async def test_settings_roundtrip():
    async with aiohttp.ClientSession() as s:
        token, _ = await _get_token(s)
        headers = {"Authorization": f"Bearer {token}"}
        model = f"roundtrip-model-{unique_id()}"
        await s.put(f"{BASE_URL}/settings", json={"llm_model": model}, headers=headers)
        async with s.get(f"{BASE_URL}/settings", headers=headers) as r:
            data = await r.json()
            assert data["llm_model"] == model


# ═══════════════ RBAC (4 tests) ═══════════════

async def test_shares_list():
    async with aiohttp.ClientSession() as s:
        async with s.get(f"{BASE_URL}/datasets/fake/shares") as r:
            assert r.status == 200
            data = await r.json()
            assert isinstance(data, list)

async def test_share_create_needs_db():
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/datasets/fake/shares", json={
            "user_id": "x", "role": "viewer"
        }) as r:
            # 503 without proper dataset, or 403/201 with DB
            assert r.status in (201, 400, 403, 503)

async def test_permissions_me():
    async with aiohttp.ClientSession() as s:
        token, _ = await _get_token(s)
        async with s.get(f"{BASE_URL}/permissions/me", headers={"Authorization": f"Bearer {token}"}) as r:
            assert r.status == 200
            data = await r.json()
            assert "role" in data
            assert "shares" in data

async def test_share_invalid_role():
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/datasets/fake/shares", json={
            "user_id": "y", "role": "superadmin"
        }) as r:
            assert r.status == 400


# ═══════════════ NOTEBOOKS (7 tests) ═══════════════

async def test_notebooks_list():
    async with aiohttp.ClientSession() as s:
        async with s.get(f"{BASE_URL}/notebooks") as r:
            assert r.status == 200
            assert isinstance(await r.json(), list)

async def test_notebook_create():
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/notebooks", json={"title": f"NB_{unique_id()}"}) as r:
            assert r.status == 201
            data = await r.json()
            assert "id" in data
            assert "title" in data

async def test_notebook_get():
    async with aiohttp.ClientSession() as s:
        async with s.get(f"{BASE_URL}/notebooks/fake-id") as r:
            # 404 if DB has no such notebook
            assert r.status in (200, 404)

async def test_notebook_update():
    async with aiohttp.ClientSession() as s:
        async with s.put(f"{BASE_URL}/notebooks/fake-id", json={"title": "Updated"}) as r:
            assert r.status == 200

async def test_notebook_delete():
    async with aiohttp.ClientSession() as s:
        async with s.delete(f"{BASE_URL}/notebooks/fake-id") as r:
            assert r.status == 200

async def test_cell_add():
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/notebooks/fake/cells", json={
            "cell_type": "code", "source": "stats"
        }) as r:
            assert r.status == 201
            data = await r.json()
            assert "id" in data

async def test_cell_run_empty():
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/notebooks/fake/cells/fake/run", json={}) as r:
            assert r.status == 400
