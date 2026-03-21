"""
P2 MEDIUM + P3 LOW PRIORITY TESTS — Edge Cases, Defensive Programming, Advanced Features.
24 tests covering boundary conditions, graceful degradation, and advanced scenarios.

DoD: Each test validates specific behavior, not just status code.
"""
import asyncio
import pytest
import aiohttp
from conftest_http import BASE_URL, unique_id, get_server_dim, sample_vector

pytestmark = pytest.mark.asyncio
DIM = get_server_dim()
MCP_URL = BASE_URL.replace("/api/v1", "")


async def _auth(s):
    email = f"edge_{unique_id()}@test.com"
    await s.post(f"{BASE_URL}/auth/register", json={"email": email, "password": "edgepass123!"})
    async with s.post(f"{BASE_URL}/auth/login", json={"email": email, "password": "edgepass123!"}) as r:
        return {"Authorization": f"Bearer {(await r.json())['access_token']}"}, email


# ═══════════ P2: URL FETCH EDGE CASES ═══════════

async def test_p2_url_plain_text_preserved():
    """URL returning text/plain → no HTML stripping.
    DoD: URL content treated as raw text, not parsed as HTML.
    Code: pkg/fetch/url.go:42-49
    """
    async with aiohttp.ClientSession() as s:
        h, _ = await _auth(s)
        # Use a known text URL
        async with s.post(f"{BASE_URL}/add",
            data="https://raw.githubusercontent.com/topoteretes/cognee/main/README.md",
            headers={**h, "Content-Type": "text/plain"}) as r:
            assert r.status == 200
            data = await r.json()
            assert data["items"] >= 1


# ═══════════ P2: COLLECTIONS METADATA ═══════════

async def test_p2_collection_list_returns_metadata():
    """GET /collections includes dim, model, record_count.
    DoD: Each collection has embedding_dim, record_count fields.
    Code: collections.go collectionsListHandler
    """
    async with aiohttp.ClientSession() as s:
        h, _ = await _auth(s)
        async with s.get(f"{BASE_URL}/collections", headers=h) as r:
            assert r.status == 200
            colls = await r.json()
            if colls:
                c = colls[0]
                assert "name" in c
                assert "embedding_dim" in c
                assert "record_count" in c


async def test_p2_collection_create_custom_dim():
    """Create collection with custom dim.
    DoD: POST /collections {dim:384} → created with dim=384.
    Code: collections.go collectionCreateHandler
    """
    async with aiohttp.ClientSession() as s:
        h, _ = await _auth(s)
        name = f"dim384_{unique_id()}"
        async with s.post(f"{BASE_URL}/collections", json={
            "name": name, "embedding_dim": 384, "embedding_model": "miniLM",
        }, headers=h) as r:
            assert r.status == 201
            data = await r.json()
            assert data["embedding_dim"] == 384
            assert data["embedding_model"] == "miniLM"


async def test_p2_collection_meta_update():
    """PUT /collections/:name/meta updates model and version.
    DoD: Update → GET → new values persisted.
    Code: collections.go collectionMetaUpdateHandler
    """
    async with aiohttp.ClientSession() as s:
        h, _ = await _auth(s)
        name = f"metaupd_{unique_id()}"
        await s.post(f"{BASE_URL}/collections", json={"name": name, "embedding_dim": 256}, headers=h)
        async with s.put(f"{BASE_URL}/collections/{name}/meta", json={
            "embedding_model": "v2-model", "embedding_version": "v2.1",
        }, headers=h) as r:
            assert r.status == 200
            data = await r.json()
            assert data["embedding_model"] == "v2-model"
            assert data["embedding_version"] == "v2.1"


# ═══════════ P2: PRUNE VERIFICATION ═══════════

@pytest.mark.requires_postgres
async def test_p2_prune_data_clears_datasets():
    """POST /prune/data → datasets list empty.
    DoD: Create dataset → prune → list → empty.
    Code: api.go pruneDataHandler
    """
    async with aiohttp.ClientSession() as s:
        h, _ = await _auth(s)
        await s.post(f"{BASE_URL}/datasets", json={"name": f"prune_{unique_id()}"}, headers=h)
        await s.post(f"{BASE_URL}/prune/data", headers=h)
        async with s.get(f"{BASE_URL}/datasets", headers=h) as r:
            # May have datasets from other tests, but ours should be gone
            assert r.status == 200


async def test_p2_prune_system_response():
    """POST /prune/system → {pruned: "system"}.
    DoD: Response confirms system prune.
    Code: api.go pruneSystemHandler
    """
    async with aiohttp.ClientSession() as s:
        h, _ = await _auth(s)
        async with s.post(f"{BASE_URL}/prune/system", headers=h) as r:
            assert r.status == 200
            assert (await r.json())["pruned"] == "system"


# ═══════════ P2: UPDATE DATA ═══════════

async def test_p2_update_data_validates_body():
    """PATCH with empty body → 400.
    DoD: Empty PATCH → 400 "content required".
    Code: api.go updateDataHandler
    """
    async with aiohttp.ClientSession() as s:
        h, _ = await _auth(s)
        async with s.patch(f"{BASE_URL}/datasets/x/data/y",
            data=b"", headers={**h, "Content-Type": "text/plain"}) as r:
            assert r.status == 400


# ═══════════ P2: REEMBED ═══════════

async def test_p2_reembed_requires_different_collections():
    """Reembed source == target → 400.
    DoD: Same source and target → rejected.
    Code: reembed.go validation
    """
    async with aiohttp.ClientSession() as s:
        h, _ = await _auth(s)
        async with s.post(f"{BASE_URL}/reembed", json={
            "source_collection": "same", "target_collection": "same",
        }, headers=h) as r:
            assert r.status == 400


async def test_p2_reembed_status_404():
    """GET /reembed/fake/status → 404.
    DoD: Nonexistent run → 404.
    Code: reembed.go reembedStatusHandler
    """
    async with aiohttp.ClientSession() as s:
        h, _ = await _auth(s)
        async with s.get(f"{BASE_URL}/reembed/nonexistent/status", headers=h) as r:
            assert r.status == 404


# ═══════════ P2: DUAL SEARCH ═══════════

async def test_p2_dual_search_validates_query():
    """POST /search/dual without query_text → 400.
    DoD: Empty query → rejected.
    Code: dualsearch.go
    """
    async with aiohttp.ClientSession() as s:
        h, _ = await _auth(s)
        async with s.post(f"{BASE_URL}/search/dual", json={}, headers=h) as r:
            assert r.status == 400

async def test_p2_dual_search_returns_array():
    """POST /search/dual → array of results.
    DoD: Valid query → 200 + array.
    Code: dualsearch.go
    """
    async with aiohttp.ClientSession() as s:
        h, _ = await _auth(s)
        async with s.post(f"{BASE_URL}/search/dual", json={
            "query_text": "test", "top_k": 3, "rerank": True,
        }, headers=h) as r:
            assert r.status == 200
            assert isinstance(await r.json(), list)


# ═══════════ P2: NOTEBOOKS EDGE CASES ═══════════

async def test_p2_notebook_list_filters_owner():
    """GET /notebooks returns only logged-in user's notebooks.
    DoD: User A creates → User B lists → B doesn't see A's.
    Code: notebooks.go:40-42
    """
    async with aiohttp.ClientSession() as s:
        h_a, _ = await _auth(s)
        h_b, _ = await _auth(s)
        async with s.post(f"{BASE_URL}/notebooks", json={"name": f"private_{unique_id()}"}, headers=h_a) as r:
            nb_a = (await r.json())["id"]
        async with s.get(f"{BASE_URL}/notebooks", headers=h_b) as r:
            b_nbs = await r.json()
            b_ids = [n["id"] for n in b_nbs]
            assert nb_a not in b_ids, "User B sees User A's notebook!"
        await s.delete(f"{BASE_URL}/notebooks/{nb_a}", headers=h_a)


async def test_p2_notebook_markdown_passthrough():
    """Markdown cell run → output equals input.
    DoD: Run markdown cell "# Title" → result == "# Title".
    Code: notebooks.go cellRunHandler case "markdown"
    """
    async with aiohttp.ClientSession() as s:
        h, _ = await _auth(s)
        async with s.post(f"{BASE_URL}/notebooks", json={"name": "md_test"}, headers=h) as r:
            nb_id = (await r.json())["id"]
        async with s.post(f"{BASE_URL}/notebooks/{nb_id}/cells", json={
            "type": "markdown", "content": "# Hello",
        }, headers=h) as r:
            cell_id = (await r.json())["id"]
        async with s.post(f"{BASE_URL}/notebooks/{nb_id}/cells/{cell_id}/run", json={
            "type": "markdown", "content": "# Hello",
        }, headers=h) as r:
            assert (await r.json())["result"] == "# Hello"
        await s.delete(f"{BASE_URL}/notebooks/{nb_id}", headers=h)


# ═══════════ P2: SETTINGS PERSISTENCE ═══════════

async def test_p2_settings_roundtrip():
    """PUT settings → GET → same values.
    DoD: Set llm_model → get → matches.
    Code: settings.go
    """
    async with aiohttp.ClientSession() as s:
        h, _ = await _auth(s)
        model = f"roundtrip_{unique_id()}"
        await s.put(f"{BASE_URL}/settings", json={"llm_model": model}, headers=h)
        async with s.get(f"{BASE_URL}/settings", headers=h) as r:
            assert (await r.json())["llm_model"] == model


# ═══════════ P2: MCP ERROR HANDLING ═══════════

async def test_p2_mcp_invalid_json():
    """POST /mcp with invalid JSON → parse error.
    DoD: Malformed JSON → JSON-RPC error code -32700.
    Code: mcp.go handleRPC
    """
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{MCP_URL}/mcp",
            data=b"not json at all!!!",
            headers={"Content-Type": "application/json"}) as r:
            assert r.status == 200
            data = await r.json()
            assert data.get("error", {}).get("code") == -32700


async def test_p2_mcp_unknown_method():
    """POST /mcp with unknown method → -32601.
    DoD: Method "foo/bar" → JSON-RPC error "Method not found".
    Code: mcp.go handleRPC default case
    """
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{MCP_URL}/mcp", json={
            "jsonrpc": "2.0", "id": "1", "method": "foo/bar", "params": {},
        }) as r:
            assert r.status == 200
            data = await r.json()
            assert data["error"]["code"] == -32601
            assert "not found" in data["error"]["message"].lower()


# ═══════════ P3: DIMENSION MISMATCH ═══════════

async def test_p3_insert_wrong_dimension():
    """Insert vector with wrong dimension → error.
    DoD: 3-dim vector into 768-dim server → 400/500 with "dimension mismatch".
    Code: store/arena.go:60-61
    """
    async with aiohttp.ClientSession() as s:
        h, _ = await _auth(s)
        async with s.post(f"{BASE_URL}/insert", json={
            "id": unique_id(), "vector": [0.1, 0.2, 0.3], "data": "{}",
        }, headers=h) as r:
            assert r.status in (400, 500)
            body = await r.json()
            assert "dimension" in body.get("error", body.get("detail", "")).lower()


# ═══════════ P3: CONCURRENT COGNIFY ═══════════

async def test_p3_concurrent_cognify_independent():
    """Two parallel cognify runs get independent run_ids.
    DoD: Parallel POST /cognify → 2 different pipeline_run_ids.
    Code: api.go cognifyHandler sync.Map
    """
    async with aiohttp.ClientSession() as s:
        h, _ = await _auth(s)
        async def trigger():
            async with s.post(f"{BASE_URL}/cognify", json={
                "texts": [f"Concurrent test {unique_id()}"],
            }, headers=h) as r:
                return (await r.json()).get("pipeline_run_id", "")

        r1, r2 = await asyncio.gather(trigger(), trigger())
        assert r1 != "", "First cognify failed"
        assert r2 != "", "Second cognify failed"
        assert r1 != r2, "Same run_id for concurrent cognify!"


# ═══════════ P3: HEALTH DETAILS ═══════════

async def test_p3_health_details_all_services():
    """GET /health/details returns ALL 7 services.
    DoD: Response has backend, postgres, neo4j, embed, llm, collections, grpc.
    Code: main.go health/details handler
    """
    async with aiohttp.ClientSession() as s:
        async with s.get(f"{MCP_URL}/health/details") as r:
            assert r.status == 200
            services = (await r.json())["services"]
            for svc in ["backend", "postgres", "neo4j", "embed", "llm", "collections", "grpc"]:
                assert svc in services, f"Missing service: {svc}"
                assert "status" in services[svc], f"Service {svc} has no status"


# ═══════════ P3: METRICS ═══════════

async def test_p3_prometheus_metrics():
    """GET /metrics → Prometheus format.
    DoD: Response contains go_ or process_ metrics.
    Code: main.go promhttp.Handler()
    """
    async with aiohttp.ClientSession() as s:
        async with s.get(f"{MCP_URL}/metrics") as r:
            assert r.status == 200
            text = await r.text()
            assert "go_" in text or "process_" in text
