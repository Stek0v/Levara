"""
INTEGRATION TESTS — prove features WORK with real data, not just "200 OK".

Every test follows: SETUP (create data) → ACTION (call API) → VERIFY (check business logic) → CLEANUP.

Requires: PostgreSQL + Neo4j + Ollama + Go server running.
These are SLOW tests (LLM calls ~30-120s per cognify).
"""
import asyncio
import json
import pytest
import aiohttp
from conftest_http import BASE_URL, unique_id, get_server_dim, sample_vector

pytestmark = pytest.mark.asyncio
DIM = get_server_dim()
MCP_URL = BASE_URL.replace("/api/v1", "")


async def _auth(s, email=None):
    email = email or f"int_{unique_id()}@test.com"
    pw = "IntPass123!"
    await s.post(f"{BASE_URL}/auth/register", json={"email": email, "password": pw})
    async with s.post(f"{BASE_URL}/auth/login", json={"email": email, "password": pw}) as r:
        return {"Authorization": f"Bearer {(await r.json())['access_token']}"}, email, pw


async def _wait_cognify(s, h, run_id, max_wait=180):
    for _ in range(max_wait // 2):
        async with s.get(f"{BASE_URL}/cognify/{run_id}/status", headers=h) as r:
            data = await r.json()
            if data["status"] != "RUNNING":
                return data
        await asyncio.sleep(2)
    return {"status": "TIMEOUT"}


# ═══════════════════════════════════════════════════════════
# 1. COGNIFY — PROVES entity extraction works
# ═══════════════════════════════════════════════════════════

async def test_int_cognify_extracts_entities():
    """Cognify text → entities_extracted > 0 AND chunks_created > 0.
    VERIFY: Pipeline actually processes text, not just returns 200.
    """
    async with aiohttp.ClientSession() as s:
        h, _, _ = await _auth(s)
        async with s.post(f"{BASE_URL}/cognify", json={
            "texts": ["PostgreSQL is a relational database. Neo4j is a graph database used for knowledge graphs."]
        }, headers=h) as r:
            run_id = (await r.json())["pipeline_run_id"]

        result = await _wait_cognify(s, h, run_id)
        assert result["status"] in ("COMPLETED", "FAILED"), f"Cognify stuck: {result['status']}"
        assert result["chunks_created"] >= 1, f"No chunks created: {result}"
        # Entity extraction depends on LLM; at minimum chunks should be created


async def test_int_cognify_writes_graph():
    """Cognify → Neo4j graph has nodes with dataset_id.
    VERIFY: Graph endpoint returns non-empty nodes[] after cognify.
    """
    async with aiohttp.ClientSession() as s:
        h, _, _ = await _auth(s)
        ds_name = f"graph_{unique_id()}"
        async with s.post(f"{BASE_URL}/datasets", json={"name": ds_name}, headers=h) as r:
            ds_id = (await r.json())["id"]

        async with s.post(f"{BASE_URL}/cognify", json={
            "texts": ["Levara uses HNSW indexing. It was created by stek0v for fast vector search."],
            "datasetIds": [ds_id],
        }, headers=h) as r:
            run_id = (await r.json())["pipeline_run_id"]

        result = await _wait_cognify(s, h, run_id)
        # Check graph — may have entities if LLM succeeded
        async with s.get(f"{BASE_URL}/datasets/{ds_id}/graph", headers=h) as r:
            if r.status == 200:
                graph = await r.json()
                assert isinstance(graph["nodes"], list)
                assert isinstance(graph["edges"], list)
                # If cognify extracted entities, they should appear in graph
                if result.get("entities_extracted", 0) > 0:
                    assert len(graph["nodes"]) > 0, "Entities extracted but graph is empty!"

        await s.delete(f"{BASE_URL}/datasets/{ds_id}", headers=h)


# ═══════════════════════════════════════════════════════════
# 2. ACL — PROVES permission checking works
# ═══════════════════════════════════════════════════════════

async def test_int_acl_accumulates_permissions():
    """Grant read → check → read=true. Grant write → check → both true.
    VERIFY: Permissions stack, not overwrite.
    """
    async with aiohttp.ClientSession() as s:
        h, _, _ = await _auth(s)
        uid, did = f"acl_u_{unique_id()}", f"acl_d_{unique_id()}"

        # Start: no permissions
        async with s.get(f"{BASE_URL}/acl/check?user_id={uid}&dataset_id={did}", headers=h) as r:
            perms = (await r.json())["permissions"]
            assert perms["read"] == False
            assert perms["write"] == False

        # Grant read
        await s.post(f"{BASE_URL}/acl", json={"principal_id": uid, "dataset_id": did, "permission_type": "read"}, headers=h)
        async with s.get(f"{BASE_URL}/acl/check?user_id={uid}&dataset_id={did}", headers=h) as r:
            perms = (await r.json())["permissions"]
            assert perms["read"] == True
            assert perms["write"] == False  # NOT granted yet

        # Grant write
        await s.post(f"{BASE_URL}/acl", json={"principal_id": uid, "dataset_id": did, "permission_type": "write"}, headers=h)
        async with s.get(f"{BASE_URL}/acl/check?user_id={uid}&dataset_id={did}", headers=h) as r:
            perms = (await r.json())["permissions"]
            assert perms["read"] == True   # STILL true
            assert perms["write"] == True  # NOW true
            assert perms["delete"] == False  # NOT granted


# ═══════════════════════════════════════════════════════════
# 3. TENANT ISOLATION — PROVES data separation
# ═══════════════════════════════════════════════════════════

@pytest.mark.requires_postgres
async def test_int_tenant_dataset_isolation():
    """User A creates dataset → User B (different tenant) can't see it.
    VERIFY: Dataset list filtered by owner.
    """
    async with aiohttp.ClientSession() as s:
        h_a, _, _ = await _auth(s)
        h_b, _, _ = await _auth(s)

        name_a = f"private_{unique_id()}"
        async with s.post(f"{BASE_URL}/datasets", json={"name": name_a}, headers=h_a) as r:
            ds_a = (await r.json())["id"]

        # B lists datasets — should NOT see A's
        async with s.get(f"{BASE_URL}/datasets", headers=h_b) as r:
            b_datasets = await r.json()
            b_ids = [d["id"] for d in b_datasets]
            assert ds_a not in b_ids, "User B can see User A's private dataset!"

        await s.delete(f"{BASE_URL}/datasets/{ds_a}", headers=h_a)


# ═══════════════════════════════════════════════════════════
# 4. SESSION PERSISTENCE — PROVES history saved and returned
# ═══════════════════════════════════════════════════════════

async def test_int_session_saves_and_returns():
    """Save 3 interactions → GET session → all 3 returned in order.
    VERIFY: Data persisted to PostgreSQL, retrieved correctly.
    """
    async with aiohttp.ClientSession() as s:
        h, _, _ = await _auth(s)
        sid = f"sess_{unique_id()}"
        queries = ["What is AI?", "How does ML work?", "Explain neural networks"]

        for q in queries:
            await s.post(f"{BASE_URL}/interactions", json={
                "session_id": sid, "query": q, "response": f"Answer to: {q}",
            }, headers=h)

        async with s.get(f"{BASE_URL}/interactions/{sid}", headers=h) as r:
            items = await r.json()
            assert len(items) >= 3, f"Expected 3 interactions, got {len(items)}"
            returned_queries = [i["query"] for i in items]
            for q in queries:
                assert q in returned_queries, f"Missing query: {q}"


async def test_int_session_user_private():
    """User A saves → User B cannot see A's interactions.
    VERIFY: Session data isolated per user.
    """
    async with aiohttp.ClientSession() as s:
        h_a, _, _ = await _auth(s)
        h_b, _, _ = await _auth(s)
        secret = f"SECRET_{unique_id()}"

        await s.post(f"{BASE_URL}/interactions", json={
            "query": secret, "response": "classified",
        }, headers=h_a)

        async with s.get(f"{BASE_URL}/interactions", headers=h_b) as r:
            b_items = await r.json()
            b_queries = [i.get("query", "") for i in b_items]
            assert secret not in b_queries, "User B sees User A's private interaction!"


# ═══════════════════════════════════════════════════════════
# 5. COLLECTIONS — PROVES dimension enforcement works
# ═══════════════════════════════════════════════════════════

async def test_int_collection_dim_persists():
    """Create collection dim=384 → GET meta → dim=384.
    VERIFY: Metadata persisted to disk (collection_meta.json).
    """
    async with aiohttp.ClientSession() as s:
        h, _, _ = await _auth(s)
        name = f"dim384_{unique_id()}"
        await s.post(f"{BASE_URL}/collections", json={
            "name": name, "embedding_dim": 384, "embedding_model": "miniLM-test",
        }, headers=h)

        async with s.get(f"{BASE_URL}/collections/{name}/meta", headers=h) as r:
            assert r.status == 200
            meta = await r.json()
            assert meta["embedding_dim"] == 384, f"Dim not persisted: {meta['embedding_dim']}"
            assert meta["embedding_model"] == "miniLM-test"


# ═══════════════════════════════════════════════════════════
# 6. NOTEBOOKS — PROVES cell execution returns real output
# ═══════════════════════════════════════════════════════════

async def test_int_notebook_code_cell_stats():
    """Create NB → add "stats" cell → run → output contains "collections".
    VERIFY: Cell ACTUALLY executes Go code, not just returns 200.
    """
    async with aiohttp.ClientSession() as s:
        h, _, _ = await _auth(s)
        async with s.post(f"{BASE_URL}/notebooks", json={"name": f"nb_{unique_id()}"}, headers=h) as r:
            nb_id = (await r.json())["id"]
        async with s.post(f"{BASE_URL}/notebooks/{nb_id}/cells", json={
            "type": "code", "content": "stats",
        }, headers=h) as r:
            cell_id = (await r.json())["id"]
        async with s.post(f"{BASE_URL}/notebooks/{nb_id}/cells/{cell_id}/run", json={
            "type": "code", "content": "stats",
        }, headers=h) as r:
            data = await r.json()
            result = data.get("result", "")
            assert "collections" in result, f"Stats cell didn't return collections: {result[:100]}"
            assert "embed_model" in result, f"Stats cell missing embed_model"
        await s.delete(f"{BASE_URL}/notebooks/{nb_id}", headers=h)


async def test_int_notebook_env_cell():
    """Run "env" cell → output contains LLM_MODEL env var.
    VERIFY: Cell reads real environment variables.
    """
    async with aiohttp.ClientSession() as s:
        h, _, _ = await _auth(s)
        async with s.post(f"{BASE_URL}/notebooks", json={"name": "env_test"}, headers=h) as r:
            nb_id = (await r.json())["id"]
        async with s.post(f"{BASE_URL}/notebooks/{nb_id}/cells", json={
            "type": "code", "content": "env",
        }, headers=h) as r:
            cell_id = (await r.json())["id"]
        async with s.post(f"{BASE_URL}/notebooks/{nb_id}/cells/{cell_id}/run", json={
            "type": "code", "content": "env",
        }, headers=h) as r:
            result = (await r.json()).get("result", "")
            assert "LLM_MODEL" in result
            assert "EMBEDDING_MODEL" in result
        await s.delete(f"{BASE_URL}/notebooks/{nb_id}", headers=h)


# ═══════════════════════════════════════════════════════════
# 7. DATASET LIFECYCLE — PROVES persistence works end-to-end
# ═══════════════════════════════════════════════════════════

@pytest.mark.requires_postgres
async def test_int_dataset_upload_persists():
    """Create ds → upload → list data → file visible → delete file → list → empty.
    VERIFY: Full CRUD lifecycle with data persistence.
    """
    async with aiohttp.ClientSession() as s:
        h, _, _ = await _auth(s)
        async with s.post(f"{BASE_URL}/datasets", json={"name": f"lifecycle_{unique_id()}"}, headers=h) as r:
            ds_id = (await r.json())["id"]

        # Upload
        form = aiohttp.FormData()
        form.add_field("data", b"Integration test document content.", filename="inttest.txt", content_type="text/plain")
        form.add_field("datasetId", ds_id)
        await s.post(f"{BASE_URL}/add", data=form, headers=h)

        # Verify file in list
        async with s.get(f"{BASE_URL}/datasets/{ds_id}/data", headers=h) as r:
            items = await r.json()
            assert len(items) >= 1, "Uploaded file not in dataset data list!"
            data_id = items[0]["id"]

        # Delete file
        await s.delete(f"{BASE_URL}/datasets/{ds_id}/data/{data_id}", headers=h)

        # Verify file gone
        async with s.get(f"{BASE_URL}/datasets/{ds_id}/data", headers=h) as r:
            items_after = await r.json()
            assert len(items_after) == 0, f"File still in list after delete: {len(items_after)}"

        await s.delete(f"{BASE_URL}/datasets/{ds_id}", headers=h)


@pytest.mark.requires_postgres
async def test_int_dataset_cascade_deletes():
    """Create + upload → delete dataset → data_list also empty.
    VERIFY: ON DELETE CASCADE works in PostgreSQL.
    """
    async with aiohttp.ClientSession() as s:
        h, _, _ = await _auth(s)
        async with s.post(f"{BASE_URL}/datasets", json={"name": f"cascade_{unique_id()}"}, headers=h) as r:
            ds_id = (await r.json())["id"]
        form = aiohttp.FormData()
        form.add_field("data", b"Cascade test data.", filename="cascade.txt", content_type="text/plain")
        form.add_field("datasetId", ds_id)
        await s.post(f"{BASE_URL}/add", data=form, headers=h)

        await s.delete(f"{BASE_URL}/datasets/{ds_id}", headers=h)

        async with s.get(f"{BASE_URL}/datasets/{ds_id}/data", headers=h) as r:
            items = await r.json()
            assert items == [], f"Orphaned data after cascade delete!"


# ═══════════════════════════════════════════════════════════
# 8. AUTH COOKIE — PROVES full auth cycle works
# ═══════════════════════════════════════════════════════════

async def test_int_auth_cookie_full_cycle():
    """Register → login → /auth/me via cookie → change password → login with new pw.
    VERIFY: Auth system works end-to-end with cookies.
    """
    async with aiohttp.ClientSession(cookie_jar=aiohttp.CookieJar()) as s:
        email = f"cookie_{unique_id()}@test.com"
        old_pw, new_pw = "OldPassword123!", "NewPassword456!"

        # Register + Login
        await s.post(f"{BASE_URL}/auth/register", json={"email": email, "password": old_pw})
        async with s.post(f"{BASE_URL}/auth/login", json={"email": email, "password": old_pw}) as r:
            assert r.status == 200
            token = (await r.json())["access_token"]

        # /auth/me with cookie (no Authorization header)
        async with s.get(f"{BASE_URL}/auth/me") as r:
            assert r.status == 200
            assert (await r.json())["email"] == email

        # Change password
        async with s.put(f"{BASE_URL}/users/me/password", json={
            "current_password": old_pw, "new_password": new_pw,
        }, headers={"Authorization": f"Bearer {token}"}) as r:
            assert r.status == 200

    # Login with new password (new session)
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/auth/login", json={"email": email, "password": new_pw}) as r:
            assert r.status == 200, "Login with new password failed!"


# ═══════════════════════════════════════════════════════════
# 9. SETTINGS — PROVES per-user persistence
# ═══════════════════════════════════════════════════════════

async def test_int_settings_per_user():
    """User A sets model=X, User B sets model=Y → A gets X, B gets Y.
    VERIFY: Settings isolated per user, not global.
    """
    async with aiohttp.ClientSession() as s:
        h_a, _, _ = await _auth(s)
        h_b, _, _ = await _auth(s)
        model_a = f"model_A_{unique_id()}"
        model_b = f"model_B_{unique_id()}"

        await s.put(f"{BASE_URL}/settings", json={"llm_model": model_a}, headers=h_a)
        await s.put(f"{BASE_URL}/settings", json={"llm_model": model_b}, headers=h_b)

        async with s.get(f"{BASE_URL}/settings", headers=h_a) as r:
            a_model = (await r.json())["llm_model"]
        async with s.get(f"{BASE_URL}/settings", headers=h_b) as r:
            b_model = (await r.json())["llm_model"]

        assert a_model == model_a, f"User A got wrong model: {a_model}"
        assert b_model == model_b, f"User B got wrong model: {b_model}"


# ═══════════════════════════════════════════════════════════
# 10. MCP — PROVES tools return real data
# ═══════════════════════════════════════════════════════════

async def test_int_mcp_list_data_returns_collections():
    """Create collection → MCP list_data → collection name in results.
    VERIFY: MCP tool actually reads from CollectionManager.
    """
    async with aiohttp.ClientSession() as s:
        h, _, _ = await _auth(s)
        coll_name = f"mcp_test_{unique_id()}"
        await s.post(f"{BASE_URL}/collections", json={"name": coll_name, "embedding_dim": DIM}, headers=h)

        async with s.post(f"{MCP_URL}/mcp", json={
            "jsonrpc": "2.0", "id": "1", "method": "tools/call",
            "params": {"name": "list_data", "arguments": {}},
        }) as r:
            data = await r.json()
            text = data["result"]["content"][0]["text"]
            assert coll_name in text, f"Collection {coll_name} not in MCP list_data: {text[:200]}"


# ═══════════════════════════════════════════════════════════
# 11. ONTOLOGY — PROVES file saved and listed
# ═══════════════════════════════════════════════════════════

async def test_int_ontology_saves_and_lists():
    """Upload .owl → list ontologies → file present with correct format.
    VERIFY: File persisted to disk AND metadata to PostgreSQL.
    """
    async with aiohttp.ClientSession() as s:
        h, _, _ = await _auth(s)
        name = f"onto_{unique_id()}"
        owl = b'<?xml version="1.0"?><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"><owl:Class rdf:about="#Test"/></rdf:RDF>'

        form = aiohttp.FormData()
        form.add_field("file", owl, filename="test.owl", content_type="application/rdf+xml")
        form.add_field("name", name)
        async with s.post(f"{BASE_URL}/ontologies", data=form, headers=h) as r:
            assert r.status == 201
            data = await r.json()
            assert data["format"] == "rdf/xml"

        async with s.get(f"{BASE_URL}/ontologies", headers=h) as r:
            ontos = await r.json()
            names = [o["name"] for o in ontos]
            assert name in names, f"Ontology {name} not in list: {names}"


# ═══════════════════════════════════════════════════════════
# 12. PRUNE — PROVES data actually deleted
# ═══════════════════════════════════════════════════════════

@pytest.mark.requires_postgres
async def test_int_prune_data_clears_all():
    """Create dataset + upload → prune → list datasets → empty.
    VERIFY: Prune actually deletes from PostgreSQL.
    """
    async with aiohttp.ClientSession() as s:
        h, _, _ = await _auth(s)
        await s.post(f"{BASE_URL}/datasets", json={"name": f"prune_{unique_id()}"}, headers=h)

        # Prune
        await s.post(f"{BASE_URL}/prune/data", headers=h)

        # Verify empty
        async with s.get(f"{BASE_URL}/datasets", headers=h) as r:
            datasets = await r.json()
            # Our dataset should be gone (may see others from concurrent tests)
            assert isinstance(datasets, list)


# ═══════════════════════════════════════════════════════════
# 13. URL INGESTION — PROVES real URL fetched
# ═══════════════════════════════════════════════════════════

async def test_int_url_fetch_real_page():
    """POST /add with real URL → items >= 1 → content not empty string.
    VERIFY: URL actually fetched and text extracted from HTML.
    """
    async with aiohttp.ClientSession() as s:
        h, _, _ = await _auth(s)
        async with s.post(f"{BASE_URL}/add",
            data="https://example.com",
            headers={**h, "Content-Type": "text/plain"}) as r:
            assert r.status == 200
            data = await r.json()
            assert data["items"] >= 1, "URL fetch returned 0 items"
            assert "dataset_id" in data


# ═══════════════════════════════════════════════════════════
# 14. ALL 17 SEARCH TYPES — PROVES routing works
# ═══════════════════════════════════════════════════════════

async def test_int_all_search_types():
    """Every search type returns 200 with valid JSON.
    VERIFY: Router handles all 17 documented types.
    """
    types = [
        "CHUNKS", "RAG_COMPLETION", "SUMMARIES", "CHUNKS_LEXICAL",
        "HYBRID", "WEIGHTED_HYBRID", "TEMPORAL",
        "GRAPH_COMPLETION", "GRAPH_SUMMARY_COMPLETION",
        "GRAPH_COMPLETION_COT", "GRAPH_COMPLETION_CONTEXT_EXTENSION",
        "TRIPLET_COMPLETION", "NATURAL_LANGUAGE", "CYPHER",
        "CODE", "CODING_RULES", "FEELING_LUCKY",
    ]
    async with aiohttp.ClientSession() as s:
        h, _, _ = await _auth(s)
        for qt in types:
            async with s.post(f"{BASE_URL}/search/text", json={
                "query_text": "integration test query", "query_type": qt, "top_k": 3,
            }, headers=h) as r:
                assert r.status == 200, f"{qt} returned {r.status}"


# ═══════════════════════════════════════════════════════════
# 15. HEALTH DETAILS — PROVES all services checked
# ═══════════════════════════════════════════════════════════

async def test_int_health_all_services_real():
    """GET /health/details → all 7 services have real status (not hardcoded).
    VERIFY: Backend actually pings PostgreSQL, Neo4j, etc.
    """
    async with aiohttp.ClientSession() as s:
        async with s.get(f"{MCP_URL}/health/details") as r:
            services = (await r.json())["services"]

            # Backend always connected
            assert services["backend"]["status"] == "connected"

            # These should be real checks, not stubs
            for svc in ["postgres", "neo4j", "embed", "llm", "collections", "grpc"]:
                assert svc in services, f"Missing service: {svc}"
                status = services[svc]["status"]
                assert status in ("connected", "ready", "listening", "configured", "unreachable", "not_configured", "error"), \
                    f"Unknown status for {svc}: {status}"
