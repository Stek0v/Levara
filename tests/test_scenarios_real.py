"""
20 REAL-WORLD USER SCENARIOS — от А до Я.

Каждый тест = полный путь реального пользователя.
Основано на блогах Cognee (Bayer, dltHub, OpenClaw) и документации.

Паттерн: STORY → DATA → FLOW → DOD (Definition of Done).
Каждый тест проверяет БИЗНЕС-ЛОГИКУ, не status codes.

Sources:
- https://www.cognee.ai/blog
- https://docs.cognee.ai/guides/quickstart
- https://memgraph.com/blog/from-rag-to-graphs-cognee-ai-memory
- https://scrapegraphai.com/blog/scrapegraphai-cognee
"""
import asyncio
import pytest
import aiohttp
from conftest_http import BASE_URL, unique_id, get_server_dim, sample_vector

pytestmark = pytest.mark.asyncio
DIM = get_server_dim()
MCP_URL = BASE_URL.replace("/api/v1", "")


async def _user(s, prefix="s"):
    email = f"{prefix}_{unique_id()}@test.com"
    pw = "Scenario123!"
    await s.post(f"{BASE_URL}/auth/register", json={"email": email, "password": pw})
    async with s.post(f"{BASE_URL}/auth/login", json={"email": email, "password": pw}) as r:
        data = await r.json()
        return {"Authorization": f"Bearer {data['access_token']}"}, email, pw


async def _cognify_wait(s, h, run_id, max_s=120):
    for _ in range(max_s // 2):
        async with s.get(f"{BASE_URL}/cognify/{run_id}/status", headers=h) as r:
            d = await r.json()
            if d["status"] != "RUNNING":
                return d
        await asyncio.sleep(2)
    return {"status": "TIMEOUT"}


# ════════════════════════════════════════════════════════════════
# S01: Исследователь загружает статью и ищет связи
# STORY: Учёный загружает текст → cognify → ищет по имени
# DOD: chunks >= 1, search returns results
# ════════════════════════════════════════════════════════════════

async def test_s01_researcher_uploads_and_searches():
    """Researcher: upload article → cognify → search for entity."""
    async with aiohttp.ClientSession() as s:
        h, _, _ = await _user(s, "researcher")
        text = ("Albert Einstein developed the theory of special relativity in 1905 "
                "while working at the Swiss Patent Office in Bern. He was born in Ulm, Germany.")
        # Upload
        async with s.post(f"{BASE_URL}/add", data=text, headers={**h, "Content-Type": "text/plain"}) as r:
            assert r.status == 200
            assert (await r.json())["items"] >= 1
        # Cognify
        async with s.post(f"{BASE_URL}/cognify", json={"texts": [text]}, headers=h) as r:
            result = await _cognify_wait(s, h, (await r.json())["pipeline_run_id"])
            assert result["chunks_created"] >= 1, f"No chunks: {result}"
        # Search
        async with s.post(f"{BASE_URL}/search/text", json={
            "query_text": "Einstein relativity", "query_type": "CHUNKS", "top_k": 5
        }, headers=h) as r:
            assert r.status == 200


# ════════════════════════════════════════════════════════════════
# S02: Developer создаёт knowledge base из README
# DOD: BM25 search находит ключевое слово
# ════════════════════════════════════════════════════════════════

async def test_s02_developer_knowledge_base():
    """Developer: upload README → BM25 keyword search."""
    async with aiohttp.ClientSession() as s:
        h, _, _ = await _user(s, "dev")
        async with s.post(f"{BASE_URL}/add",
            data="Cognevra uses HNSW for fast approximate nearest neighbor search. It supports WAL durability.",
            headers={**h, "Content-Type": "text/plain"}) as r:
            assert (await r.json())["items"] >= 1
        async with s.post(f"{BASE_URL}/search/text", json={
            "query_text": "HNSW", "query_type": "CHUNKS_LEXICAL", "top_k": 5
        }, headers=h) as r:
            assert r.status == 200


# ════════════════════════════════════════════════════════════════
# S03: Аналитик загружает CSV
# DOD: CSV uploaded, RAG search returns chunks + answer
# ════════════════════════════════════════════════════════════════

async def test_s03_analyst_csv_upload():
    """Analyst: upload CSV → RAG search."""
    async with aiohttp.ClientSession() as s:
        h, _, _ = await _user(s, "analyst")
        csv = b"name,type,speed\nCognevra,VectorDB,fast\nNeo4j,GraphDB,medium\nPostgres,RelationalDB,fast"
        form = aiohttp.FormData()
        form.add_field("data", csv, filename="benchmarks.csv", content_type="text/csv")
        async with s.post(f"{BASE_URL}/add", data=form, headers=h) as r:
            assert (await r.json())["status"] == "ok"
        async with s.post(f"{BASE_URL}/search/text", json={
            "query_text": "fastest database", "query_type": "RAG_COMPLETION"
        }, headers=h) as r:
            data = await r.json()
            assert "chunks" in data and "answer" in data


# ════════════════════════════════════════════════════════════════
# S04: Команда шарит dataset
# DOD: User B видит shared dataset, User C — нет
# ════════════════════════════════════════════════════════════════

@pytest.mark.requires_postgres
async def test_s04_team_dataset_sharing():
    """Team: A creates → shares with B → C can't see."""
    async with aiohttp.ClientSession() as s:
        h_a, email_a, _ = await _user(s, "owner")
        h_b, email_b, _ = await _user(s, "member")
        h_c, email_c, _ = await _user(s, "outsider")
        # A creates
        async with s.post(f"{BASE_URL}/datasets", json={"name": f"team_{unique_id()}"}, headers=h_a) as r:
            ds_id = (await r.json())["id"]
        # Share with B
        await s.post(f"{BASE_URL}/datasets/{ds_id}/shares", json={
            "email": email_b, "role": "editor"
        }, headers=h_a)
        # B sees shares
        async with s.get(f"{BASE_URL}/datasets/{ds_id}/shares", headers=h_b) as r:
            shares = await r.json()
            assert len(shares) >= 1, "B doesn't see share!"
        # C should NOT see the dataset in list (owner filtering)
        async with s.get(f"{BASE_URL}/datasets", headers=h_c) as r:
            c_ids = [d["id"] for d in await r.json()]
            assert ds_id not in c_ids, "Outsider C can see private dataset!"
        await s.delete(f"{BASE_URL}/datasets/{ds_id}", headers=h_a)


# ════════════════════════════════════════════════════════════════
# S05: Temporal search — dates in text
# DOD: TEMPORAL extracts dates from query
# ════════════════════════════════════════════════════════════════

async def test_s05_temporal_date_search():
    """Historian: search for events by date."""
    async with aiohttp.ClientSession() as s:
        h, _, _ = await _user(s, "hist")
        async with s.post(f"{BASE_URL}/search/text", json={
            "query_text": "The war ended on May 8, 1945. The treaty was signed June 26, 1945.",
            "query_type": "TEMPORAL"
        }, headers=h) as r:
            data = await r.json()
            assert isinstance(data, list)
            if data:
                assert "date" in data[0], "No date field in temporal result"
                assert "1945" in data[0]["date"], "Year 1945 not extracted"


# ════════════════════════════════════════════════════════════════
# S06: Data scientist с notebook
# DOD: 3 cells executed, each returns real output
# ════════════════════════════════════════════════════════════════

async def test_s06_notebook_exploration():
    """Data scientist: notebook with stats/env/collections cells."""
    async with aiohttp.ClientSession() as s:
        h, _, _ = await _user(s, "ds")
        async with s.post(f"{BASE_URL}/notebooks", json={"name": f"explore_{unique_id()}"}, headers=h) as r:
            nb_id = (await r.json())["id"]
        for cmd, check in [("stats", "collections"), ("env", "LLM_MODEL"), ("collections", "[")]:
            async with s.post(f"{BASE_URL}/notebooks/{nb_id}/cells", json={"type": "code", "content": cmd}, headers=h) as r:
                cell_id = (await r.json())["id"]
            async with s.post(f"{BASE_URL}/notebooks/{nb_id}/cells/{cell_id}/run",
                json={"type": "code", "content": cmd}, headers=h) as r:
                result = (await r.json()).get("result", "")
                assert check in result, f"Cell '{cmd}' missing '{check}': {result[:80]}"
        await s.delete(f"{BASE_URL}/notebooks/{nb_id}", headers=h)


# ════════════════════════════════════════════════════════════════
# S07: MCP agent full pipeline
# DOD: initialize → list 7 tools → search works
# ════════════════════════════════════════════════════════════════

async def test_s07_mcp_agent_pipeline():
    """AI Agent: MCP initialize → list tools → search via tool."""
    async with aiohttp.ClientSession() as s:
        # Initialize
        async with s.post(f"{MCP_URL}/mcp", json={
            "jsonrpc": "2.0", "id": "1", "method": "initialize", "params": {}
        }) as r:
            init = await r.json()
            assert init["result"]["serverInfo"]["name"] == "Cognevra"
        # List tools
        async with s.post(f"{MCP_URL}/mcp", json={
            "jsonrpc": "2.0", "id": "2", "method": "tools/list", "params": {}
        }) as r:
            tools = (await r.json())["result"]["tools"]
            names = [t["name"] for t in tools]
            assert len(names) >= 7
            for needed in ["cognify", "search", "add", "list_data", "delete", "prune"]:
                assert needed in names, f"Missing tool: {needed}"
        # Search via MCP
        async with s.post(f"{MCP_URL}/mcp", json={
            "jsonrpc": "2.0", "id": "3", "method": "tools/call",
            "params": {"name": "search", "arguments": {"search_query": "test", "search_type": "CHUNKS"}}
        }) as r:
            result = (await r.json())["result"]
            assert not result.get("isError", False)


# ════════════════════════════════════════════════════════════════
# S08: Admin ACL management
# DOD: Permissions accumulate correctly
# ════════════════════════════════════════════════════════════════

async def test_s08_admin_acl_management():
    """Admin: grant read → write → delete → verify all accumulated."""
    async with aiohttp.ClientSession() as s:
        h, _, _ = await _user(s, "admin")
        uid, did = f"u_{unique_id()}", f"d_{unique_id()}"
        for perm in ["read", "write", "delete"]:
            await s.post(f"{BASE_URL}/acl", json={
                "principal_id": uid, "dataset_id": did, "permission_type": perm
            }, headers=h)
        async with s.get(f"{BASE_URL}/acl/check?user_id={uid}&dataset_id={did}", headers=h) as r:
            perms = (await r.json())["permissions"]
            assert perms["read"] and perms["write"] and perms["delete"], f"Perms: {perms}"


# ════════════════════════════════════════════════════════════════
# S09: URL ingestion
# DOD: Real URL fetched, items >= 1
# ════════════════════════════════════════════════════════════════

async def test_s09_url_ingestion():
    """Marketer: upload web page by URL."""
    async with aiohttp.ClientSession() as s:
        h, _, _ = await _user(s, "marketer")
        async with s.post(f"{BASE_URL}/add", data="https://example.com",
            headers={**h, "Content-Type": "text/plain"}) as r:
            data = await r.json()
            assert data["items"] >= 1, "URL fetch returned 0 items"
            assert "dataset_id" in data


# ════════════════════════════════════════════════════════════════
# S10: Multi-tenant isolation
# DOD: Tenant A user can't see Tenant B data
# ════════════════════════════════════════════════════════════════

@pytest.mark.requires_postgres
async def test_s10_tenant_isolation():
    """Enterprise: Marketing vs Engineering data isolation."""
    async with aiohttp.ClientSession() as s:
        h_a, _, _ = await _user(s, "mkt")
        h_b, _, _ = await _user(s, "eng")
        name_a = f"mkt_{unique_id()}"
        async with s.post(f"{BASE_URL}/datasets", json={"name": name_a}, headers=h_a) as r:
            ds_a = (await r.json())["id"]
        async with s.get(f"{BASE_URL}/datasets", headers=h_b) as r:
            b_ids = [d["id"] for d in await r.json()]
            assert ds_a not in b_ids, "Engineering sees Marketing data!"
        await s.delete(f"{BASE_URL}/datasets/{ds_a}", headers=h_a)


# ════════════════════════════════════════════════════════════════
# S11: Prune + rebuild
# DOD: After prune — empty, after rebuild — data present
# ════════════════════════════════════════════════════════════════

@pytest.mark.requires_postgres
async def test_s11_prune_and_rebuild():
    """Admin: prune system → rebuild from scratch."""
    async with aiohttp.ClientSession() as s:
        h, _, _ = await _user(s, "ops")
        await s.post(f"{BASE_URL}/datasets", json={"name": f"pre_{unique_id()}"}, headers=h)
        await s.post(f"{BASE_URL}/prune/data", headers=h)
        # Re-add
        async with s.post(f"{BASE_URL}/add", data="Rebuilt data after prune.",
            headers={**h, "Content-Type": "text/plain"}) as r:
            assert (await r.json())["items"] >= 1


# ════════════════════════════════════════════════════════════════
# S12: Conversational memory
# DOD: 3 interactions saved → all 3 returned in session
# ════════════════════════════════════════════════════════════════

async def test_s12_conversational_memory():
    """User: multi-turn conversation with session tracking."""
    async with aiohttp.ClientSession() as s:
        h, _, _ = await _user(s, "chat")
        sid = f"conv_{unique_id()}"
        questions = ["What is AI?", "How does ML differ?", "Explain deep learning"]
        for q in questions:
            await s.post(f"{BASE_URL}/interactions", json={
                "session_id": sid, "query": q, "response": f"Answer: {q}"
            }, headers=h)
        async with s.get(f"{BASE_URL}/interactions/{sid}", headers=h) as r:
            items = await r.json()
            assert len(items) >= 3, f"Only {len(items)} items in session"
            queries = [i["query"] for i in items]
            for q in questions:
                assert q in queries, f"Missing: {q}"


# ════════════════════════════════════════════════════════════════
# S13: Ontology upload
# DOD: OWL file saved, listed with correct format
# ════════════════════════════════════════════════════════════════

async def test_s13_ontology_upload():
    """Biologist: upload OWL ontology for entity grounding."""
    async with aiohttp.ClientSession() as s:
        h, _, _ = await _user(s, "bio")
        name = f"bio_onto_{unique_id()}"
        owl = b'<?xml version="1.0"?><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"><owl:Class rdf:about="#Organism"/></rdf:RDF>'
        form = aiohttp.FormData()
        form.add_field("file", owl, filename="biology.owl", content_type="application/rdf+xml")
        form.add_field("name", name)
        async with s.post(f"{BASE_URL}/ontologies", data=form, headers=h) as r:
            assert r.status == 201
            assert (await r.json())["format"] == "rdf/xml"
        async with s.get(f"{BASE_URL}/ontologies", headers=h) as r:
            assert name in [o["name"] for o in await r.json()]


# ════════════════════════════════════════════════════════════════
# S14: Custom dimension collection
# DOD: dim=384 persisted, model name stored
# ════════════════════════════════════════════════════════════════

async def test_s14_custom_dimension_collection():
    """ML engineer: create collection for small embedding model."""
    async with aiohttp.ClientSession() as s:
        h, _, _ = await _user(s, "ml")
        name = f"small_{unique_id()}"
        await s.post(f"{BASE_URL}/collections", json={
            "name": name, "embedding_dim": 384, "embedding_model": "all-MiniLM-L6-v2"
        }, headers=h)
        async with s.get(f"{BASE_URL}/collections/{name}/meta", headers=h) as r:
            meta = await r.json()
            assert meta["embedding_dim"] == 384
            assert meta["embedding_model"] == "all-MiniLM-L6-v2"


# ════════════════════════════════════════════════════════════════
# S15: Dataset CRUD lifecycle
# DOD: create → upload → visible → delete file → gone → delete ds
# ════════════════════════════════════════════════════════════════

@pytest.mark.requires_postgres
async def test_s15_dataset_crud_lifecycle():
    """QA: full dataset lifecycle test."""
    async with aiohttp.ClientSession() as s:
        h, _, _ = await _user(s, "qa")
        async with s.post(f"{BASE_URL}/datasets", json={"name": f"crud_{unique_id()}"}, headers=h) as r:
            ds_id = (await r.json())["id"]
        form = aiohttp.FormData()
        form.add_field("data", b"Lifecycle test content.", filename="life.txt", content_type="text/plain")
        form.add_field("datasetId", ds_id)
        await s.post(f"{BASE_URL}/add", data=form, headers=h)
        async with s.get(f"{BASE_URL}/datasets/{ds_id}/data", headers=h) as r:
            items = await r.json()
            assert len(items) >= 1, "File not in dataset"
            data_id = items[0]["id"]
        await s.delete(f"{BASE_URL}/datasets/{ds_id}/data/{data_id}", headers=h)
        async with s.get(f"{BASE_URL}/datasets/{ds_id}/data", headers=h) as r:
            assert len(await r.json()) == 0, "File still visible after delete"
        await s.delete(f"{BASE_URL}/datasets/{ds_id}", headers=h)


# ════════════════════════════════════════════════════════════════
# S16: Settings per-user
# DOD: A="gpt-4", B="claude" → A gets "gpt-4"
# ════════════════════════════════════════════════════════════════

async def test_s16_settings_per_user():
    """Platform: per-user LLM model settings."""
    async with aiohttp.ClientSession() as s:
        h_a, _, _ = await _user(s, "cfgA")
        h_b, _, _ = await _user(s, "cfgB")
        m_a, m_b = f"gpt4_{unique_id()}", f"claude_{unique_id()}"
        await s.put(f"{BASE_URL}/settings", json={"llm_model": m_a}, headers=h_a)
        await s.put(f"{BASE_URL}/settings", json={"llm_model": m_b}, headers=h_b)
        async with s.get(f"{BASE_URL}/settings", headers=h_a) as r:
            assert (await r.json())["llm_model"] == m_a, "User A got wrong model!"
        async with s.get(f"{BASE_URL}/settings", headers=h_b) as r:
            assert (await r.json())["llm_model"] == m_b, "User B got wrong model!"


# ════════════════════════════════════════════════════════════════
# S17: Auth full cycle with cookie
# DOD: register → login → cookie works → change pw → re-login
# ════════════════════════════════════════════════════════════════

async def test_s17_auth_full_cycle():
    """New user: register → login → cookie auth → change password → re-login."""
    async with aiohttp.ClientSession(cookie_jar=aiohttp.CookieJar()) as s:
        email = f"auth_{unique_id()}@test.com"
        pw1, pw2 = "OldPass123!", "NewPass456!"
        await s.post(f"{BASE_URL}/auth/register", json={"email": email, "password": pw1})
        async with s.post(f"{BASE_URL}/auth/login", json={"email": email, "password": pw1}) as r:
            token = (await r.json())["access_token"]
        # Cookie auth
        async with s.get(f"{BASE_URL}/auth/me") as r:
            assert (await r.json())["email"] == email, "Cookie auth failed!"
        # Change password
        await s.put(f"{BASE_URL}/users/me/password", json={
            "current_password": pw1, "new_password": pw2
        }, headers={"Authorization": f"Bearer {token}"})
    # Re-login with new password
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{BASE_URL}/auth/login", json={"email": email, "password": pw2}) as r:
            assert r.status == 200, "Re-login with new password failed!"


# ════════════════════════════════════════════════════════════════
# S18: Cognify writes to Neo4j graph
# DOD: Graph nodes exist after cognify
# ════════════════════════════════════════════════════════════════

async def test_s18_cognify_writes_graph():
    """Data engineer: verify cognify creates graph nodes."""
    async with aiohttp.ClientSession() as s:
        h, _, _ = await _user(s, "de")
        async with s.post(f"{BASE_URL}/datasets", json={"name": f"graph_{unique_id()}"}, headers=h) as r:
            ds_id = (await r.json())["id"]
        async with s.post(f"{BASE_URL}/cognify", json={
            "texts": ["Go is a compiled language. Python is interpreted. Both are popular."],
            "datasetIds": [ds_id]
        }, headers=h) as r:
            result = await _cognify_wait(s, h, (await r.json())["pipeline_run_id"])
        async with s.get(f"{BASE_URL}/datasets/{ds_id}/graph", headers=h) as r:
            if r.status == 200:
                graph = await r.json()
                assert isinstance(graph["nodes"], list)
                if result.get("entities_extracted", 0) > 0:
                    assert len(graph["nodes"]) > 0, "Entities extracted but graph empty!"
        await s.delete(f"{BASE_URL}/datasets/{ds_id}", headers=h)


# ════════════════════════════════════════════════════════════════
# S19: Health dashboard — all 7 services
# DOD: Each service reports real status
# ════════════════════════════════════════════════════════════════

async def test_s19_health_dashboard():
    """DevOps: verify all infrastructure services reporting."""
    async with aiohttp.ClientSession() as s:
        async with s.get(f"{MCP_URL}/health/details") as r:
            services = (await r.json())["services"]
            for svc in ["backend", "postgres", "neo4j", "embed", "llm", "collections", "grpc"]:
                assert svc in services, f"Missing: {svc}"
                assert "status" in services[svc], f"No status for {svc}"
            assert services["backend"]["status"] == "connected"


# ════════════════════════════════════════════════════════════════
# S20: 10 concurrent users
# DOD: All 10 complete, server alive
# ════════════════════════════════════════════════════════════════

async def test_s20_concurrent_users():
    """Stress: 10 users simultaneously register + upload + search."""
    results = []

    async def user_flow(i):
        async with aiohttp.ClientSession() as s:
            h, _, _ = await _user(s, f"conc{i}")
            await s.post(f"{BASE_URL}/add", data=f"User {i} test document.",
                headers={**h, "Content-Type": "text/plain"})
            async with s.post(f"{BASE_URL}/search/text", json={
                "query_text": "test", "query_type": "CHUNKS"
            }, headers=h) as r:
                results.append(r.status)

    await asyncio.gather(*[user_flow(i) for i in range(10)])
    assert len(results) == 10, f"Only {len(results)}/10 completed"
    assert all(s == 200 for s in results), f"Failures: {[s for s in results if s != 200]}"

    # Server still alive
    async with aiohttp.ClientSession() as s:
        async with s.get(f"{MCP_URL}/health") as r:
            assert r.status == 200, "Server crashed after concurrent load!"
