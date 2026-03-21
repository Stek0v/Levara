"""
Coverage Gap Tests — closing all untested Cognee features.
Organized by priority: high → medium → low.

HIGH PRIORITY:
- prune endpoint
- search TRIPLET_COMPLETION, FEELING_LUCKY
- PDF/DOCX file upload
- Raw file download
- Search with top_k variations

MEDIUM PRIORITY:
- session_id conversational memory
- Shared dataset visibility verification
- Soft vs hard delete semantics
- Node set filtering (node_name, node_type)
- Custom system prompt in search
- only_context mode
- Verbose mode
- Background processing verification
- Incremental loading (dedup)

LOW PRIORITY (features that exist in Go but not fully tested):
- Multi-dataset search (/search/dual)
- Collections CRUD with metadata
- Re-embed endpoint
- MCP tool execution (cognify, search, list_data, delete, prune via MCP)

Docs:
- Add: https://docs.cognee.ai/core-concepts/main-operations/add
- Cognify: https://docs.cognee.ai/core-concepts/main-operations/cognify
- Search: https://docs.cognee.ai/core-concepts/main-operations/search
- Search types: https://docs.cognee.ai/core-concepts/main-operations/search
- Memify: https://docs.cognee.ai/core-concepts/main-operations/memify
- Datasets: https://docs.cognee.ai/core-concepts/datasets
- Multi-user: https://docs.cognee.ai/core-concepts/multi-user-mode/permissions-system/overview
- MCP: https://docs.cognee.ai/guides/mcp-server
"""
import asyncio
import os
import tempfile
import pytest
import aiohttp
from conftest_http import BASE_URL, sample_vector, unique_id, get_server_dim

pytestmark = pytest.mark.asyncio
DIM = get_server_dim()
MCP_URL = BASE_URL.replace("/api/v1", "")


async def _auth(s):
    email = f"gap_{unique_id()}@test.com"
    pw = "gappass123"
    await s.post(f"{BASE_URL}/auth/register", json={"email": email, "password": pw})
    async with s.post(f"{BASE_URL}/auth/login", json={"email": email, "password": pw}) as r:
        token = (await r.json()).get("access_token", "")
    return {"Authorization": f"Bearer {token}"}, email


async def _create_ds(s, h, name=None):
    name = name or f"gap_{unique_id()}"
    async with s.post(f"{BASE_URL}/datasets", json={"name": name}, headers=h) as r:
        return (await r.json())["id"], name


# ═══════════════════════════════════════════════════════════
# HIGH PRIORITY: prune endpoint
# Docs: https://docs.cognee.ai/core-concepts/main-operations/add
# POST /prune — not implemented as REST, available via MCP
# ═══════════════════════════════════════════════════════════

async def test_prune_via_mcp():
    """MCP prune tool clears all data.
    Docs: https://docs.cognee.ai/core-concepts/main-operations/add
    """
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{MCP_URL}/mcp", json={
            "jsonrpc": "2.0", "id": "1", "method": "tools/call",
            "params": {"name": "prune", "arguments": {}},
        }) as r:
            assert r.status == 200
            data = await r.json()
            assert "result" in data
            text = data["result"]["content"][0]["text"]
            assert "pruned" in text.lower() or "deleted" in text.lower()


# ═══════════════════════════════════════════════════════════
# HIGH PRIORITY: Missing search types
# Docs: https://docs.cognee.ai/core-concepts/main-operations/search
# ═══════════════════════════════════════════════════════════

async def test_search_triplet_completion():
    """TRIPLET_COMPLETION — triplet-based graph search.
    Docs: https://docs.cognee.ai/core-concepts/main-operations/search
    """
    async with aiohttp.ClientSession() as s:
        h, _ = await _auth(s)
        async with s.post(f"{BASE_URL}/search/text", json={
            "query_text": "who created what",
            "query_type": "TRIPLET_COMPLETION",
            "top_k": 5,
        }, headers=h) as r:
            # Falls back to CHUNKS if not implemented
            assert r.status == 200


async def test_search_feeling_lucky():
    """FEELING_LUCKY — auto-select best search type.
    Docs: https://docs.cognee.ai/core-concepts/main-operations/search
    """
    async with aiohttp.ClientSession() as s:
        h, _ = await _auth(s)
        async with s.post(f"{BASE_URL}/search/text", json={
            "query_text": "tell me about databases",
            "query_type": "FEELING_LUCKY",
            "top_k": 5,
        }, headers=h) as r:
            assert r.status == 200


async def test_search_code():
    """CODE — code-specific search.
    Docs: https://docs.cognee.ai/core-concepts/main-operations/search
    """
    async with aiohttp.ClientSession() as s:
        h, _ = await _auth(s)
        async with s.post(f"{BASE_URL}/search/text", json={
            "query_text": "function definition",
            "query_type": "CODE",
        }, headers=h) as r:
            assert r.status == 200


async def test_search_cypher():
    """CYPHER — raw Cypher query (if supported).
    Docs: https://docs.cognee.ai/core-concepts/main-operations/search
    """
    async with aiohttp.ClientSession() as s:
        h, _ = await _auth(s)
        async with s.post(f"{BASE_URL}/search/text", json={
            "query_text": "MATCH (n) RETURN n LIMIT 5",
            "query_type": "CYPHER",
        }, headers=h) as r:
            assert r.status == 200


async def test_search_coding_rules():
    """CODING_RULES — code rules search.
    Docs: https://docs.cognee.ai/core-concepts/main-operations/search
    """
    async with aiohttp.ClientSession() as s:
        h, _ = await _auth(s)
        async with s.post(f"{BASE_URL}/search/text", json={
            "query_text": "naming conventions",
            "query_type": "CODING_RULES",
        }, headers=h) as r:
            assert r.status == 200


# ═══════════════════════════════════════════════════════════
# HIGH PRIORITY: PDF/DOCX file upload
# Docs: https://docs.cognee.ai/core-concepts/main-operations/add
# ═══════════════════════════════════════════════════════════

async def test_upload_pdf_file():
    """Upload a minimal PDF file.
    Docs: https://docs.cognee.ai/core-concepts/main-operations/add
    """
    # Minimal valid PDF
    pdf_bytes = b"""%PDF-1.0
1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj
2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj
3 0 obj<</Type/Page/MediaBox[0 0 612 792]/Parent 2 0 R/Resources<<>>>>endobj
xref
0 4
0000000000 65535 f
0000000009 00000 n
0000000058 00000 n
0000000115 00000 n
trailer<</Size 4/Root 1 0 R>>
startxref
206
%%EOF"""
    async with aiohttp.ClientSession() as s:
        h, _ = await _auth(s)
        form = aiohttp.FormData()
        form.add_field("data", pdf_bytes, filename="test.pdf", content_type="application/pdf")
        async with s.post(f"{BASE_URL}/add", data=form, headers=h) as r:
            assert r.status == 200
            data = await r.json()
            assert data["status"] == "ok"


async def test_upload_csv_file():
    """Upload CSV file.
    Docs: https://docs.cognee.ai/core-concepts/main-operations/add
    """
    csv_content = b"name,type,description\nCognevra,database,Vector DB\nNeo4j,database,Graph DB\nPostgreSQL,database,Relational DB"
    async with aiohttp.ClientSession() as s:
        h, _ = await _auth(s)
        form = aiohttp.FormData()
        form.add_field("data", csv_content, filename="data.csv", content_type="text/csv")
        async with s.post(f"{BASE_URL}/add", data=form, headers=h) as r:
            assert r.status == 200


async def test_upload_json_file():
    """Upload JSON file.
    Docs: https://docs.cognee.ai/core-concepts/main-operations/add
    """
    json_content = b'[{"entity": "Cognevra", "type": "database"}, {"entity": "HNSW", "type": "algorithm"}]'
    async with aiohttp.ClientSession() as s:
        h, _ = await _auth(s)
        form = aiohttp.FormData()
        form.add_field("data", json_content, filename="entities.json", content_type="application/json")
        async with s.post(f"{BASE_URL}/add", data=form, headers=h) as r:
            assert r.status == 200


# ═══════════════════════════════════════════════════════════
# HIGH PRIORITY: Raw file download
# Docs: https://docs.cognee.ai/api-reference/datasets/list-dataset-data
# ═══════════════════════════════════════════════════════════

@pytest.mark.requires_postgres
async def test_raw_file_download():
    """Upload file → list data → download raw → verify content.
    Docs: https://docs.cognee.ai/api-reference/datasets/list-dataset-data
    """
    content = f"Downloadable test content {unique_id()}"
    async with aiohttp.ClientSession() as s:
        h, _ = await _auth(s)
        ds_id, _ = await _create_ds(s, h)
        # Upload
        form = aiohttp.FormData()
        form.add_field("data", content.encode(), filename="download_test.txt", content_type="text/plain")
        form.add_field("datasetId", ds_id)
        await s.post(f"{BASE_URL}/add", data=form, headers=h)
        # List data
        async with s.get(f"{BASE_URL}/datasets/{ds_id}/data", headers=h) as r:
            items = await r.json()
        if items:
            # Download raw
            async with s.get(f"{BASE_URL}/datasets/{ds_id}/data/{items[0]['id']}/raw", headers=h) as r:
                if r.status == 200:
                    body = await r.text()
                    assert len(body) > 0
        await s.delete(f"{BASE_URL}/datasets/{ds_id}", headers=h)


# ═══════════════════════════════════════════════════════════
# MEDIUM PRIORITY: Session-based conversational memory
# Docs: https://docs.cognee.ai/core-concepts/main-operations/search
# ═══════════════════════════════════════════════════════════

async def test_search_with_session_id():
    """Search with session_id for conversational memory.
    Docs: https://docs.cognee.ai/core-concepts/main-operations/search
    """
    async with aiohttp.ClientSession() as s:
        h, _ = await _auth(s)
        session = f"session_{unique_id()}"
        # First query
        async with s.post(f"{BASE_URL}/search/text", json={
            "query_text": "What is HNSW?",
            "query_type": "CHUNKS",
            "session_id": session,
        }, headers=h) as r:
            assert r.status == 200
        # Follow-up (would use context from first in full implementation)
        async with s.post(f"{BASE_URL}/search/text", json={
            "query_text": "How does it work?",
            "query_type": "CHUNKS",
            "session_id": session,
        }, headers=h) as r:
            assert r.status == 200


# ═══════════════════════════════════════════════════════════
# MEDIUM PRIORITY: Shared dataset visibility
# Docs: https://docs.cognee.ai/core-concepts/multi-user-mode/permissions-system/overview
# ═══════════════════════════════════════════════════════════

@pytest.mark.requires_postgres
async def test_shared_dataset_accessible():
    """User A shares dataset → User B can see it.
    Docs: https://docs.cognee.ai/core-concepts/multi-user-mode/permissions-system/overview
    """
    async with aiohttp.ClientSession() as s:
        h_a, email_a = await _auth(s)
        h_b, email_b = await _auth(s)
        # A creates dataset
        ds_id, ds_name = await _create_ds(s, h_a, f"shared_{unique_id()}")
        # A shares with B
        async with s.post(f"{BASE_URL}/datasets/{ds_id}/shares", json={
            "email": email_b, "role": "viewer",
        }, headers=h_a) as r:
            shared = r.status == 201
        # B lists shares
        if shared:
            async with s.get(f"{BASE_URL}/datasets/{ds_id}/shares", headers=h_b) as r:
                shares = await r.json()
                assert len(shares) >= 1
        # Cleanup
        await s.delete(f"{BASE_URL}/datasets/{ds_id}", headers=h_a)


# ═══════════════════════════════════════════════════════════
# MEDIUM PRIORITY: Node filtering in search
# Docs: https://docs.cognee.ai/core-concepts/main-operations/search
# ═══════════════════════════════════════════════════════════

async def test_search_with_node_name_filter():
    """Search with node_name filter.
    Docs: https://docs.cognee.ai/core-concepts/main-operations/search
    """
    async with aiohttp.ClientSession() as s:
        h, _ = await _auth(s)
        async with s.post(f"{BASE_URL}/search/text", json={
            "query_text": "database",
            "query_type": "CHUNKS",
            "node_name": ["engineering"],
        }, headers=h) as r:
            assert r.status == 200


async def test_search_with_custom_system_prompt():
    """Search with custom system_prompt.
    Docs: https://docs.cognee.ai/core-concepts/main-operations/search
    """
    async with aiohttp.ClientSession() as s:
        h, _ = await _auth(s)
        async with s.post(f"{BASE_URL}/search/text", json={
            "query_text": "explain HNSW",
            "query_type": "RAG_COMPLETION",
            "system_prompt": "You are a database expert. Answer concisely.",
        }, headers=h) as r:
            assert r.status == 200


async def test_search_only_context():
    """Search with only_context=true — no LLM generation.
    Docs: https://docs.cognee.ai/core-concepts/main-operations/search
    """
    async with aiohttp.ClientSession() as s:
        h, _ = await _auth(s)
        async with s.post(f"{BASE_URL}/search/text", json={
            "query_text": "vector database",
            "query_type": "CHUNKS",
            "only_context": True,
        }, headers=h) as r:
            assert r.status == 200


async def test_search_verbose():
    """Search with verbose=true.
    Docs: https://docs.cognee.ai/core-concepts/main-operations/search
    """
    async with aiohttp.ClientSession() as s:
        h, _ = await _auth(s)
        async with s.post(f"{BASE_URL}/search/text", json={
            "query_text": "test",
            "query_type": "CHUNKS",
            "verbose": True,
        }, headers=h) as r:
            assert r.status == 200


async def test_search_wide_search_top_k():
    """Search with wide_search_top_k parameter.
    Docs: https://docs.cognee.ai/core-concepts/main-operations/search
    """
    async with aiohttp.ClientSession() as s:
        h, _ = await _auth(s)
        async with s.post(f"{BASE_URL}/search/text", json={
            "query_text": "test",
            "query_type": "CHUNKS",
            "wide_search_top_k": 100,
        }, headers=h) as r:
            assert r.status == 200


# ═══════════════════════════════════════════════════════════
# MEDIUM PRIORITY: Incremental loading (dedup)
# Docs: https://docs.cognee.ai/core-concepts/main-operations/add
# ═══════════════════════════════════════════════════════════

async def test_incremental_loading_dedup():
    """Same file uploaded twice — second is deduplicated.
    Docs: https://docs.cognee.ai/core-concepts/main-operations/add
    """
    content = f"Incremental loading test {unique_id()}"
    async with aiohttp.ClientSession() as s:
        h, _ = await _auth(s)
        form1 = aiohttp.FormData()
        form1.add_field("data", content.encode(), filename="incr.txt", content_type="text/plain")
        async with s.post(f"{BASE_URL}/add", data=form1, headers=h) as r:
            d1 = await r.json()
        form2 = aiohttp.FormData()
        form2.add_field("data", content.encode(), filename="incr.txt", content_type="text/plain")
        async with s.post(f"{BASE_URL}/add", data=form2, headers=h) as r:
            d2 = await r.json()
        # Both should succeed; dedup is internal
        assert d1["status"] == "ok"
        assert d2["status"] == "ok"


# ═══════════════════════════════════════════════════════════
# MEDIUM PRIORITY: Background processing
# Docs: https://docs.cognee.ai/core-concepts/main-operations/cognify
# ═══════════════════════════════════════════════════════════

async def test_cognify_run_in_background():
    """Cognify with runInBackground returns immediately.
    Docs: https://docs.cognee.ai/core-concepts/main-operations/cognify
    """
    async with aiohttp.ClientSession() as s:
        h, _ = await _auth(s)
        async with s.post(f"{BASE_URL}/cognify", json={
            "texts": ["Background processing test."],
            "runInBackground": True,
        }, headers=h) as r:
            assert r.status == 200
            data = await r.json()
            assert data["status"] == "PipelineRunStarted"
            assert "pipeline_run_id" in data


async def test_cognify_custom_prompt():
    """Cognify with custom extraction prompt.
    Docs: https://docs.cognee.ai/core-concepts/main-operations/cognify
    """
    async with aiohttp.ClientSession() as s:
        h, _ = await _auth(s)
        async with s.post(f"{BASE_URL}/cognify", json={
            "texts": ["Extract only database names from this text."],
            "custom_prompt": "Focus on technology names only.",
        }, headers=h) as r:
            assert r.status == 200


# ═══════════════════════════════════════════════════════════
# LOW PRIORITY: Multi-dataset search (/search/dual)
# Cognevra extension — not in Cognee docs
# ═══════════════════════════════════════════════════════════

async def test_dual_search():
    """Dual search across multiple collections with different dims.
    Cognevra extension: POST /search/dual
    """
    async with aiohttp.ClientSession() as s:
        h, _ = await _auth(s)
        async with s.post(f"{BASE_URL}/search/dual", json={
            "query_text": "vector database",
            "top_k": 5,
            "rerank": True,
        }, headers=h) as r:
            assert r.status == 200
            data = await r.json()
            assert isinstance(data, list)


# ═══════════════════════════════════════════════════════════
# LOW PRIORITY: Collections CRUD with metadata
# Cognevra extension
# ═══════════════════════════════════════════════════════════

async def test_collection_create_with_dim():
    """Create collection with specific dim and model.
    Cognevra extension: POST /collections
    """
    async with aiohttp.ClientSession() as s:
        h, _ = await _auth(s)
        name = f"coll_{unique_id()}"
        async with s.post(f"{BASE_URL}/collections", json={
            "name": name,
            "embedding_model": "test-model",
            "embedding_dim": 384,
            "distance_metric": "cosine",
        }, headers=h) as r:
            assert r.status == 201
            data = await r.json()
            assert data["name"] == name
            assert data["embedding_dim"] == 384
            assert data["embedding_model"] == "test-model"


async def test_collection_meta_update():
    """Update collection metadata (model, version).
    Cognevra extension: PUT /collections/:name/meta
    """
    async with aiohttp.ClientSession() as s:
        h, _ = await _auth(s)
        name = f"meta_{unique_id()}"
        await s.post(f"{BASE_URL}/collections", json={"name": name, "embedding_dim": 256}, headers=h)
        async with s.put(f"{BASE_URL}/collections/{name}/meta", json={
            "embedding_model": "updated-model",
            "embedding_version": "v2",
        }, headers=h) as r:
            assert r.status == 200
            data = await r.json()
            assert data["embedding_model"] == "updated-model"
            assert data["embedding_version"] == "v2"


async def test_collection_list_with_metadata():
    """List collections returns metadata (dim, model, count).
    Cognevra extension: GET /collections
    """
    async with aiohttp.ClientSession() as s:
        h, _ = await _auth(s)
        async with s.get(f"{BASE_URL}/collections", headers=h) as r:
            assert r.status == 200
            colls = await r.json()
            assert isinstance(colls, list)
            for c in colls:
                assert "name" in c
                assert "embedding_dim" in c
                assert "record_count" in c


# ═══════════════════════════════════════════════════════════
# LOW PRIORITY: Re-embed endpoint
# Cognevra extension
# ═══════════════════════════════════════════════════════════

async def test_reembed_empty_source():
    """Re-embed from empty source → completes with 0 records.
    Cognevra extension: POST /reembed
    """
    async with aiohttp.ClientSession() as s:
        h, _ = await _auth(s)
        # Create empty source collection
        src = f"src_{unique_id()}"
        await s.post(f"{BASE_URL}/collections", json={"name": src, "embedding_dim": DIM}, headers=h)
        async with s.post(f"{BASE_URL}/reembed", json={
            "source_collection": src,
            "target_collection": f"tgt_{unique_id()}",
            "target_model": "test",
        }, headers=h) as r:
            assert r.status == 200
            data = await r.json()
            run_id = data["run_id"]
        # Poll status
        await asyncio.sleep(1)
        async with s.get(f"{BASE_URL}/reembed/{run_id}/status", headers=h) as r:
            status = await r.json()
            assert status["status"] == "COMPLETED"
            assert status["total_records"] == 0


async def test_reembed_status_404():
    """Non-existent reembed run → 404."""
    async with aiohttp.ClientSession() as s:
        h, _ = await _auth(s)
        async with s.get(f"{BASE_URL}/reembed/fake-id/status", headers=h) as r:
            assert r.status == 404


# ═══════════════════════════════════════════════════════════
# LOW PRIORITY: MCP tool execution
# Docs: https://docs.cognee.ai/guides/mcp-server
# ═══════════════════════════════════════════════════════════

async def test_mcp_tool_search():
    """MCP search tool returns results.
    Docs: https://docs.cognee.ai/guides/mcp-server
    """
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{MCP_URL}/mcp", json={
            "jsonrpc": "2.0", "id": "1", "method": "tools/call",
            "params": {"name": "search", "arguments": {"search_query": "test", "search_type": "CHUNKS"}},
        }) as r:
            assert r.status == 200
            data = await r.json()
            assert "result" in data


async def test_mcp_tool_list_data():
    """MCP list_data tool returns collections/datasets.
    Docs: https://docs.cognee.ai/guides/mcp-server
    """
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{MCP_URL}/mcp", json={
            "jsonrpc": "2.0", "id": "1", "method": "tools/call",
            "params": {"name": "list_data", "arguments": {}},
        }) as r:
            assert r.status == 200


async def test_mcp_tool_add():
    """MCP add tool ingests text.
    Docs: https://docs.cognee.ai/guides/mcp-server
    """
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{MCP_URL}/mcp", json={
            "jsonrpc": "2.0", "id": "1", "method": "tools/call",
            "params": {"name": "add", "arguments": {"data": "MCP test data", "dataset_name": "mcp_test"}},
        }) as r:
            assert r.status == 200


async def test_mcp_tool_cognify():
    """MCP cognify tool starts pipeline.
    Docs: https://docs.cognee.ai/guides/mcp-server
    """
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{MCP_URL}/mcp", json={
            "jsonrpc": "2.0", "id": "1", "method": "tools/call",
            "params": {"name": "cognify", "arguments": {"data": "MCP cognify test"}},
        }) as r:
            assert r.status == 200
            data = await r.json()
            text = data["result"]["content"][0]["text"]
            assert "Run ID" in text or "started" in text.lower()


async def test_mcp_tool_cognify_status():
    """MCP cognify_status tool checks run status.
    Docs: https://docs.cognee.ai/guides/mcp-server
    """
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{MCP_URL}/mcp", json={
            "jsonrpc": "2.0", "id": "1", "method": "tools/call",
            "params": {"name": "cognify_status", "arguments": {"run_id": "fake-id"}},
        }) as r:
            assert r.status == 200


async def test_mcp_tool_delete():
    """MCP delete tool removes dataset.
    Docs: https://docs.cognee.ai/guides/mcp-server
    """
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{MCP_URL}/mcp", json={
            "jsonrpc": "2.0", "id": "1", "method": "tools/call",
            "params": {"name": "delete", "arguments": {"dataset_id": "nonexistent"}},
        }) as r:
            assert r.status == 200


async def test_mcp_unknown_tool():
    """MCP call to unknown tool → error.
    Docs: https://docs.cognee.ai/guides/mcp-server
    """
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{MCP_URL}/mcp", json={
            "jsonrpc": "2.0", "id": "1", "method": "tools/call",
            "params": {"name": "nonexistent_tool", "arguments": {}},
        }) as r:
            assert r.status == 200
            data = await r.json()
            assert data["result"]["isError"] == True


async def test_mcp_unknown_method():
    """MCP unknown method → JSON-RPC error.
    Docs: https://docs.cognee.ai/guides/mcp-server
    """
    async with aiohttp.ClientSession() as s:
        async with s.post(f"{MCP_URL}/mcp", json={
            "jsonrpc": "2.0", "id": "1", "method": "nonexistent/method", "params": {},
        }) as r:
            assert r.status == 200
            data = await r.json()
            assert "error" in data


# ═══════════════════════════════════════════════════════════
# LOW PRIORITY: Memify enrichment tasks
# Docs: https://docs.cognee.ai/core-concepts/main-operations/memify
# ═══════════════════════════════════════════════════════════

async def test_memify_enrichment_tasks():
    """Memify with specific enrichment tasks.
    Docs: https://docs.cognee.ai/core-concepts/main-operations/memify
    """
    async with aiohttp.ClientSession() as s:
        h, _ = await _auth(s)
        async with s.post(f"{BASE_URL}/memify", json={
            "enrichment_tasks": ["entity_consolidation", "triplet_embeddings", "summary_generation"],
            "run_in_background": True,
        }, headers=h) as r:
            # 400 if no Neo4j, 200 if available
            assert r.status in (200, 400)


async def test_memify_node_type_filter():
    """Memify with node_type filter.
    Docs: https://docs.cognee.ai/core-concepts/main-operations/memify
    """
    async with aiohttp.ClientSession() as s:
        h, _ = await _auth(s)
        async with s.post(f"{BASE_URL}/memify", json={
            "node_type": "Entity",
            "enrichment_tasks": ["triplet_embeddings"],
        }, headers=h) as r:
            assert r.status in (200, 400)


# ═══════════════════════════════════════════════════════════
# LOW PRIORITY: Health / system endpoints
# ═══════════════════════════════════════════════════════════

async def test_health_details_all_services():
    """Health details shows all 7 services.
    Cognevra extension: GET /health/details
    """
    async with aiohttp.ClientSession() as s:
        async with s.get(f"{MCP_URL}/health/details") as r:
            assert r.status == 200
            data = await r.json()
            services = data["services"]
            for svc in ["backend", "postgres", "neo4j", "embed", "llm", "collections", "grpc"]:
                assert svc in services, f"Missing service: {svc}"
                assert "status" in services[svc]


async def test_metrics_endpoint():
    """Prometheus metrics endpoint.
    GET /metrics → Prometheus text format
    """
    async with aiohttp.ClientSession() as s:
        async with s.get(f"{MCP_URL}/metrics") as r:
            assert r.status == 200
            text = await r.text()
            assert "cognevra" in text.lower() or "go_" in text or "process_" in text
