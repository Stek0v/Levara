"""Tests for Router (AUTO), Cross-Project (set_context, cross_search), and Sync features.

Covers 21 tests across 3 feature areas with risk/corner case coverage.
Run: PYTHONPATH=. python3 -m pytest test_new_features.py -v --mcp-url http://10.23.0.53:8080 --tb=short
"""
import json
import pytest
import aiohttp
import uuid


# ── Router MCP Tests ──

class TestRouter:
    """R: Smart search router via MCP AUTO routing."""

    @pytest.mark.smoke
    async def test_search_auto_routing(self, mcp):
        """R1 — search without search_type returns routing metadata."""
        result = await mcp.call_tool("search", {
            "search_query": "test query",
        })
        assert not mcp.tool_error(result), mcp.tool_text(result)
        text = mcp.tool_text(result)
        data = json.loads(text)
        assert "routing" in data, "AUTO search should include routing metadata"
        assert "selected_type" in data["routing"]
        assert "reason" in data["routing"]
        assert "confidence" in data["routing"]

    @pytest.mark.integration
    async def test_search_auto_question(self, mcp):
        """R2 — question query routes intelligently (RAG if LLM, else HYBRID/CHUNKS)."""
        result = await mcp.call_tool("search", {
            "search_query": "how does the search pipeline work?",
        })
        assert not mcp.tool_error(result)
        data = json.loads(mcp.tool_text(result))
        # Without LLM, RAG_COMPLETION is unavailable → falls back to HYBRID or CHUNKS
        assert data["routing"]["selected_type"] in ("RAG_COMPLETION", "HYBRID", "CHUNKS"), \
            f"Question should route to RAG/HYBRID/CHUNKS, got {data['routing']['selected_type']}"
        assert data["routing"]["reason"], "Routing should have a reason"

    @pytest.mark.smoke
    async def test_search_explicit_no_routing(self, mcp):
        """R3 — explicit search_type=CHUNKS should NOT include routing metadata."""
        result = await mcp.call_tool("search", {
            "search_query": "test",
            "search_type": "CHUNKS",
        })
        assert not mcp.tool_error(result)
        text = mcp.tool_text(result)
        data = json.loads(text)
        assert "routing" not in data, "Explicit search_type should not trigger routing"

    @pytest.mark.smoke
    async def test_tools_list_has_19(self, mcp):
        """R4 — tools/list returns 19 tools (16 original + set_context + cross_search + sync)."""
        tools = await mcp.tools_list()
        names = [t["name"] for t in tools]
        assert len(tools) >= 19, f"Expected 19+ tools, got {len(tools)}: {names}"
        for new_tool in ["set_context", "cross_search", "sync"]:
            assert new_tool in names, f"Missing new tool: {new_tool}"


# ── Cross-Project Tests ──

class TestCrossProject:
    """CP: Cross-project access via set_context and cross_search."""

    @pytest.mark.integration
    async def test_set_context_valid(self, fresh_mcp):
        """CP1 — set_context sets session default collection."""
        coll = f"test_{uuid.uuid4().hex[:8]}"
        result = await fresh_mcp.call_tool("set_context", {"collection": coll})
        assert not fresh_mcp.tool_error(result)
        text = fresh_mcp.tool_text(result)
        assert coll in text
        assert "set" in text.lower()

    @pytest.mark.integration
    async def test_set_context_nonexistent(self, fresh_mcp):
        """CP2 — set_context with non-existent collection warns but doesn't error."""
        result = await fresh_mcp.call_tool("set_context", {"collection": "nonexistent_xyz_999"})
        assert not fresh_mcp.tool_error(result), "set_context should not error for future collections"
        text = fresh_mcp.tool_text(result)
        assert "not yet created" in text.lower() or "set" in text.lower()

    @pytest.mark.smoke
    async def test_cross_search_max_5(self, mcp):
        """CP3 — cross_search with >5 collections returns error."""
        result = await mcp.call_tool("cross_search", {
            "search_query": "test",
            "collections": ["a", "b", "c", "d", "e", "f"],
        })
        assert mcp.tool_error(result), "Should reject >5 collections"
        assert "max 5" in mcp.tool_text(result).lower()

    @pytest.mark.smoke
    async def test_cross_search_empty_collections(self, mcp):
        """CP4 — cross_search with empty collections array returns error."""
        result = await mcp.call_tool("cross_search", {
            "search_query": "test",
            "collections": [],
        })
        assert mcp.tool_error(result)

    @pytest.mark.integration
    @pytest.mark.requires_embed
    async def test_cross_search_basic(self, mcp, services):
        """CP5 — cross_search across 2 collections returns tagged results."""
        if not services.get("embed"):
            pytest.skip("Embed not available")
        result = await mcp.call_tool("cross_search", {
            "search_query": "project architecture",
            "collections": ["_memories", "default"],
            "top_k": 3,
        })
        assert not mcp.tool_error(result), mcp.tool_text(result)
        data = json.loads(mcp.tool_text(result))
        assert "results" in data
        # Each result should have a collection tag
        for cr in data["results"]:
            assert "collection" in cr

    @pytest.mark.integration
    async def test_cross_search_sensitive_filter(self, mcp):
        """CP6 — sensitive keys filtered from cross_search memory results."""
        # Save a sensitive memory
        coll = f"test_{uuid.uuid4().hex[:8]}"
        await mcp.call_tool("save_memory", {
            "key": "api_key_prod",
            "value": "sk-secret-12345",
            "type": "project",
            "collection": coll,
        })
        # Cross-search should filter it
        result = await mcp.call_tool("cross_search", {
            "search_query": "sk-secret",
            "collections": [coll],
            "include_memories": True,
        })
        assert not mcp.tool_error(result)
        text = mcp.tool_text(result)
        assert "sk-secret-12345" not in text, "Sensitive key should be filtered from cross_search"

    @pytest.mark.integration
    async def test_get_project_context_related(self, mcp):
        """CP7 — get_project_context with include_related shows related summaries."""
        result = await mcp.call_tool("get_project_context", {
            "collection": "default",
            "include_related": ["_memories"],
        })
        assert not mcp.tool_error(result)
        text = mcp.tool_text(result)
        assert "Related Projects" in text
        assert "_memories" in text


# ── Sync Tests ──

class TestSync:
    """SY: Cross-instance sync endpoints and MCP tool."""

    @pytest.mark.smoke
    async def test_sync_manifest(self, mcp_url):
        """SY1 — GET /sync/manifest returns valid JSON with counts."""
        async with aiohttp.ClientSession() as s:
            async with s.get(f"{mcp_url}/api/v1/sync/manifest") as r:
                assert r.status == 200
                data = await r.json()
                assert "embed_model" in data
                assert "embed_dim" in data
                assert "memories" in data
                assert "count" in data["memories"]
                assert "collections" in data
                assert isinstance(data["collections"], list)

    @pytest.mark.smoke
    async def test_sync_export_memories(self, mcp_url):
        """SY2 — GET /sync/export/memories returns array."""
        async with aiohttp.ClientSession() as s:
            async with s.get(f"{mcp_url}/api/v1/sync/export/memories") as r:
                assert r.status == 200
                data = await r.json()
                assert isinstance(data, list)
                if len(data) > 0:
                    assert "key" in data[0]
                    assert "value" in data[0]
                    assert "updated_at" in data[0]

    @pytest.mark.integration
    async def test_sync_export_memories_since(self, mcp_url):
        """SY3 — ?since= filter returns only newer records."""
        async with aiohttp.ClientSession() as s:
            # Future timestamp should return empty
            async with s.get(f"{mcp_url}/api/v1/sync/export/memories?since=2099-01-01T00:00:00Z") as r:
                assert r.status == 200
                data = await r.json()
                assert len(data) == 0, "Future timestamp should return no records"

    @pytest.mark.integration
    async def test_sync_import_memories_upsert(self, mcp_url):
        """SY4 — POST import memories with upsert semantics."""
        test_key = f"sync_test_{uuid.uuid4().hex[:8]}"
        memories = [{
            "id": str(uuid.uuid4()),
            "key": test_key,
            "value": "test value for sync",
            "type": "project",
            "owner_id": "",
            "collection_name": "",
            "created_at": "2026-03-27T00:00:00Z",
            "updated_at": "2026-03-27T00:00:00Z",
        }]
        async with aiohttp.ClientSession() as s:
            async with s.post(f"{mcp_url}/api/v1/sync/import/memories",
                              json=memories) as r:
                assert r.status == 200
                data = await r.json()
                assert data["imported"] == 1
                assert data["total"] == 1

    @pytest.mark.integration
    async def test_sync_import_memories_conflict(self, mcp_url):
        """SY5 — importing older record is skipped (last-writer-wins)."""
        test_key = f"sync_conflict_{uuid.uuid4().hex[:8]}"
        # First import with recent timestamp
        new_memory = [{
            "id": str(uuid.uuid4()),
            "key": test_key, "value": "new value",
            "type": "project", "owner_id": "", "collection_name": "",
            "created_at": "2026-03-27T12:00:00Z",
            "updated_at": "2026-03-27T12:00:00Z",
        }]
        async with aiohttp.ClientSession() as s:
            await s.post(f"{mcp_url}/api/v1/sync/import/memories", json=new_memory)

            # Second import with OLDER timestamp — should be skipped
            old_memory = [{
                "id": str(uuid.uuid4()),
                "key": test_key, "value": "old value",
                "type": "project", "owner_id": "", "collection_name": "",
                "created_at": "2026-03-26T00:00:00Z",
                "updated_at": "2026-03-26T00:00:00Z",
            }]
            async with s.post(f"{mcp_url}/api/v1/sync/import/memories",
                              json=old_memory) as r:
                data = await r.json()
                assert data["skipped"] == 1, "Older record should be skipped"

    @pytest.mark.integration
    async def test_sync_export_import_graph(self, mcp_url):
        """SY6 — export graph → import → roundtrip."""
        async with aiohttp.ClientSession() as s:
            # Export
            async with s.get(f"{mcp_url}/api/v1/sync/export/graph") as r:
                assert r.status == 200
                graph = await r.json()
                assert "nodes" in graph
                assert "edges" in graph

            # Import same data back (should upsert, not duplicate)
            if len(graph["nodes"]) > 0:
                async with s.post(f"{mcp_url}/api/v1/sync/import/graph",
                                  json=graph) as r:
                    assert r.status == 200
                    data = await r.json()
                    assert "nodes_imported" in data

    @pytest.mark.integration
    async def test_sync_export_collection(self, mcp_url):
        """SY7 — export collection returns text + metadata (no vectors)."""
        async with aiohttp.ClientSession() as s:
            # Find a collection that exists
            async with s.get(f"{mcp_url}/api/v1/collections") as cr:
                colls = await cr.json()
                existing = [c["name"] for c in colls if c.get("record_count", 0) > 0]
                if not existing:
                    existing = [c["name"] for c in colls][:1]
                if not existing:
                    pytest.skip("No collections available")
            async with s.get(f"{mcp_url}/api/v1/sync/export/collection/{existing[0]}") as r:
                assert r.status == 200
                data = await r.json()
                assert "collection" in data
                assert "records" in data
                assert isinstance(data["records"], list)
                if len(data["records"]) > 0:
                    rec = data["records"][0]
                    assert "id" in rec
                    assert "text" in rec
                    assert "metadata" in rec

    @pytest.mark.smoke
    async def test_sync_import_empty_collection(self, mcp_url):
        """SY8 — importing empty collection should NOT crash (R1 fix)."""
        async with aiohttp.ClientSession() as s:
            async with s.post(f"{mcp_url}/api/v1/sync/import/collection",
                              json={
                                  "collection": "test_empty",
                                  "source_model": "test",
                                  "source_dim": 768,
                                  "records": [],
                              }) as r:
                assert r.status == 200
                data = await r.json()
                assert data.get("status") == "empty" or "no records" in str(data).lower(), \
                    f"Empty import should not crash, got: {data}"

    @pytest.mark.smoke
    async def test_sync_collection_not_found(self, mcp_url):
        """SY9 — export non-existent collection returns 404."""
        async with aiohttp.ClientSession() as s:
            async with s.get(f"{mcp_url}/api/v1/sync/export/collection/nonexistent_xyz") as r:
                assert r.status == 404

    @pytest.mark.integration
    async def test_sync_mcp_tool_manifest(self, mcp):
        """SY10 — MCP sync tool returns remote manifest on error for invalid URL."""
        result = await mcp.call_tool("sync", {
            "remote_url": "http://127.0.0.1:19999/api/v1",
            "direction": "pull",
        })
        # Should fail because remote is unreachable
        assert mcp.tool_error(result), "Sync to unreachable host should error"
        text = mcp.tool_text(result)
        assert "failed" in text.lower() or "error" in text.lower()
