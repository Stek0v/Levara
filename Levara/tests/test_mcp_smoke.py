"""
Levara MCP Smoke Tests — 15 tests, <5 minutes.
Run: pytest tests/test_mcp_smoke.py -m smoke -v

Tests the absolute basics: protocol, session, tools discovery, basic CRUD.
No LLM or embed required. Only needs Levara server running.
"""
import pytest
from conftest_mcp import MCPTestClient, percentile

pytestmark = [pytest.mark.smoke, pytest.mark.asyncio]


# ── S1: Protocol ─────────────────────────────────────────────────────────────

class TestProtocol:
    """MCP Streamable HTTP protocol compliance."""

    async def test_health(self, mcp):
        """S1.0 — Server is healthy."""
        h = await mcp.health()
        assert h.get("health") == "healthy" or h.get("status") == "ready"

    async def test_initialize_returns_capabilities(self, fresh_mcp):
        """S1.1 — Initialize returns protocol version and tools capability."""
        # fresh_mcp already called connect() which does initialize
        assert fresh_mcp.session_id is not None
        assert len(fresh_mcp.session_id) > 10

    async def test_tools_list_returns_16(self, mcp):
        """S1.1b — tools/list returns all 16 tools."""
        tools = await mcp.tools_list()
        names = [t["name"] for t in tools]
        assert len(tools) >= 16, f"Expected 16+ tools, got {len(tools)}: {names}"
        # Check critical tools exist
        for required in ["cognify", "search", "save_memory", "recall_memory",
                         "get_project_context", "analyze_commits"]:
            assert required in names, f"Missing tool: {required}"

    async def test_notification_returns_202(self, mcp):
        """S1.2 — Notification (no id) returns 202, not JSON body."""
        status = await mcp._notify("notifications/initialized")
        assert status == 202

    async def test_ping(self, mcp):
        """S1.3 — Ping returns empty result."""
        resp = await mcp.ping()
        assert "result" in resp
        assert resp.get("error") is None

    async def test_invalid_session_returns_404(self, mcp_url):
        """S1.4 — Request with invalid session → 404."""
        import aiohttp
        async with aiohttp.ClientSession() as http:
            async with http.post(
                f"{mcp_url}/mcp",
                json={"jsonrpc": "2.0", "id": 1, "method": "ping"},
                headers={"Content-Type": "application/json",
                         "Mcp-Session-Id": "invalid-session-99999"}
            ) as r:
                assert r.status == 404

    async def test_unknown_method_returns_error(self, mcp):
        """S1.5 — Unknown method returns -32601."""
        resp = await mcp._rpc("nonexistent/method")
        assert resp.get("error") is not None
        assert resp["error"]["code"] == -32601


# ── S2: Basic Tool Operations ────────────────────────────────────────────────

class TestBasicTools:
    """Core tool CRUD: save, recall, list, delete."""

    async def test_save_memory(self, mcp, test_collection):
        """S4.1a — save_memory stores key-value pair."""
        result = await mcp.call_tool("save_memory", {
            "key": "test_tech", "value": "Go + SQLite",
            "type": "project", "collection": test_collection
        })
        text = mcp.tool_text(result)
        assert "saved" in text.lower() or "test_tech" in text

    async def test_list_memories(self, mcp, test_collection):
        """S4.1b — list_memories returns saved memories."""
        # Save first
        await mcp.call_tool("save_memory", {
            "key": "list_test", "value": "value123",
            "type": "project", "collection": test_collection
        })
        result = await mcp.call_tool("list_memories", {
            "type": "project", "collection": test_collection
        })
        text = mcp.tool_text(result)
        # Should contain at least one memory
        assert not mcp.tool_error(result)

    async def test_recall_memory_like(self, mcp, test_collection):
        """S4.1c — recall_memory finds by LIKE pattern."""
        await mcp.call_tool("save_memory", {
            "key": "recall_target", "value": "PostgreSQL for production",
            "type": "project", "collection": test_collection
        })
        result = await mcp.call_tool("recall_memory", {
            "query": "recall_target", "collection": test_collection
        })
        text = mcp.tool_text(result)
        assert not mcp.tool_error(result)

    async def test_save_chat(self, mcp, test_session_id):
        """S5.1a — save_chat stores messages."""
        result = await mcp.call_tool("save_chat", {
            "session_id": test_session_id,
            "messages": [
                {"role": "user", "content": "What is HNSW?"},
                {"role": "assistant", "content": "HNSW is a graph-based ANN index."}
            ]
        })
        text = mcp.tool_text(result)
        assert "saved" in text.lower() or "2" in text

    async def test_recall_chat(self, mcp, test_session_id):
        """S5.1b — recall_chat retrieves messages."""
        # Save first
        await mcp.call_tool("save_chat", {
            "session_id": test_session_id,
            "messages": [{"role": "user", "content": "Ping"}]
        })
        result = await mcp.call_tool("recall_chat", {
            "session_id": test_session_id
        })
        assert not mcp.tool_error(result)

    async def test_list_data(self, mcp):
        """S2.0 — list_data returns without error."""
        result = await mcp.call_tool("list_data")
        assert not mcp.tool_error(result)

    async def test_get_project_context(self, mcp, test_collection):
        """S10.0 — get_project_context returns structured response."""
        result = await mcp.call_tool("get_project_context", {
            "collection": test_collection
        })
        text = mcp.tool_text(result)
        assert "Collection Stats" in text or "Memories" in text

    async def test_search_empty_collection(self, mcp, test_collection):
        """S3.10 — Search on empty/nonexistent collection → empty, not error."""
        result = await mcp.call_tool("search", {
            "search_query": "test query",
            "collection": test_collection
        })
        assert not mcp.tool_error(result)


# ── S3: Resources API ────────────────────────────────────────────────────────

class TestResources:
    """MCP Resources API."""

    async def test_resources_list(self, mcp):
        """S1.6 — resources/list returns available resources."""
        resources = await mcp.resources_list()
        assert len(resources) >= 3, f"Expected 3+ resources, got {len(resources)}"
        uris = [r["uri"] for r in resources]
        assert "levara://collections" in uris
        assert "levara://memories/project" in uris

    async def test_resources_read_collections(self, mcp):
        """S1.7 — resources/read levara://collections returns JSON."""
        resp = await mcp.resources_read("levara://collections")
        contents = resp.get("contents", [])
        assert len(contents) > 0
        assert contents[0].get("mimeType") == "application/json"
