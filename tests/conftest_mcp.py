"""
Levara MCP Test Suite — Shared fixtures and configuration.

Usage:
    pytest tests/test_mcp_smoke.py -m smoke --mcp-url http://localhost:8080
    pytest tests/ -m "smoke or integration" -v
    pytest tests/ -m e2e --mcp-url http://pi:8080
"""
import asyncio
import json
import os
import time
import uuid

import aiohttp
import pytest
import pytest_asyncio

# ── CLI Options ──────────────────────────────────────────────────────────────

def pytest_addoption(parser):
    parser.addoption("--mcp-url", default=os.environ.get("LEVARA_URL", "http://localhost:8080"),
                     help="Levara server URL")
    parser.addoption("--results-dir", default="test-results", help="JSON results output dir")


def pytest_configure(config):
    for m in ["smoke", "integration", "e2e", "stress"]:
        config.addinivalue_line("markers", f"{m}: {m}-level test")
    config.addinivalue_line("markers", "requires_llm: needs Ollama/LLM endpoint")
    config.addinivalue_line("markers", "requires_embed: needs embedding endpoint")


# ── MCP Client ───────────────────────────────────────────────────────────────

class MCPTestClient:
    """Lightweight MCP JSON-RPC 2.0 client for testing."""

    def __init__(self, base_url: str):
        self.base_url = base_url.rstrip("/")
        self.mcp_url = f"{self.base_url}/mcp"
        self.session_id = None
        self._http = None
        self._rpc_id = 0

    async def connect(self):
        self._http = aiohttp.ClientSession()
        # Initialize MCP session
        resp = await self._rpc("initialize", {
            "protocolVersion": "2025-03-26",
            "capabilities": {},
            "clientInfo": {"name": "levara-test", "version": "1.0"}
        })
        self.session_id = resp.get("_session_id")
        # Send initialized notification
        await self._notify("notifications/initialized")
        return resp

    async def close(self):
        if self._http:
            if self.session_id:
                try:
                    await self._http.delete(self.mcp_url,
                                            headers={"Mcp-Session-Id": self.session_id})
                except Exception:
                    pass
            await self._http.close()

    async def _rpc(self, method: str, params: dict = None) -> dict:
        """Send JSON-RPC request, return result."""
        self._rpc_id += 1
        body = {"jsonrpc": "2.0", "id": self._rpc_id, "method": method}
        if params:
            body["params"] = params

        headers = {"Content-Type": "application/json"}
        if self.session_id:
            headers["Mcp-Session-Id"] = self.session_id

        async with self._http.post(self.mcp_url, json=body, headers=headers) as r:
            # Capture session ID from initialize
            sid = r.headers.get("Mcp-Session-Id")
            data = await r.json()
            if sid:
                data["_session_id"] = sid
                self.session_id = sid
            return data

    async def _notify(self, method: str, params: dict = None) -> int:
        """Send JSON-RPC notification (no id), return HTTP status."""
        body = {"jsonrpc": "2.0", "method": method}
        if params:
            body["params"] = params
        headers = {"Content-Type": "application/json"}
        if self.session_id:
            headers["Mcp-Session-Id"] = self.session_id
        async with self._http.post(self.mcp_url, json=body, headers=headers) as r:
            return r.status

    async def call_tool(self, name: str, arguments: dict = None) -> dict:
        """Call MCP tool, return result content."""
        resp = await self._rpc("tools/call", {"name": name, "arguments": arguments or {}})
        return resp.get("result", resp)

    async def call_tool_timed(self, name: str, arguments: dict = None) -> tuple:
        """Call MCP tool, return (result, latency_ms)."""
        t0 = time.perf_counter()
        result = await self.call_tool(name, arguments)
        latency = (time.perf_counter() - t0) * 1000
        return result, latency

    async def tools_list(self) -> list:
        resp = await self._rpc("tools/list")
        return resp.get("result", {}).get("tools", [])

    async def resources_list(self) -> list:
        resp = await self._rpc("resources/list")
        return resp.get("result", {}).get("resources", [])

    async def resources_read(self, uri: str) -> dict:
        resp = await self._rpc("resources/read", {"uri": uri})
        return resp.get("result", {})

    async def ping(self) -> dict:
        return await self._rpc("ping")

    async def health(self) -> dict:
        async with self._http.get(f"{self.base_url}/health") as r:
            return await r.json()

    async def health_details(self) -> dict:
        async with self._http.get(f"{self.base_url}/health/details") as r:
            return await r.json()

    def tool_text(self, result: dict) -> str:
        """Extract text from MCP tool result."""
        content = result.get("content", [])
        if content and isinstance(content, list):
            return content[0].get("text", "")
        return str(result)

    def tool_error(self, result: dict) -> bool:
        """Check if tool result is an error."""
        return result.get("isError", False)


# ── Fixtures ─────────────────────────────────────────────────────────────────

@pytest.fixture(scope="session")
def event_loop():
    loop = asyncio.new_event_loop()
    yield loop
    loop.close()


@pytest.fixture(scope="session")
def mcp_url(request):
    return request.config.getoption("--mcp-url")


@pytest_asyncio.fixture(scope="session")
async def mcp(mcp_url):
    """Session-scoped MCP client. Shared across all tests."""
    client = MCPTestClient(mcp_url)
    await client.connect()
    yield client
    await client.close()


@pytest_asyncio.fixture(scope="function")
async def fresh_mcp(mcp_url):
    """Function-scoped: fresh MCP session per test."""
    client = MCPTestClient(mcp_url)
    await client.connect()
    yield client
    await client.close()


@pytest.fixture(scope="function")
def test_collection():
    """Unique collection name per test."""
    return f"test_{uuid.uuid4().hex[:8]}"


@pytest.fixture(scope="function")
def test_session_id():
    """Unique session ID per test."""
    return f"sess_{uuid.uuid4().hex[:8]}"


# ── Service Detection ────────────────────────────────────────────────────────

@pytest_asyncio.fixture(scope="session")
async def services(mcp):
    """Detect which services are available."""
    try:
        details = await mcp.health_details()
        svcs = details.get("services", {})
        return {
            "server": True,
            "embed": svcs.get("embed", {}).get("status") == "connected",
            "llm": svcs.get("llm", {}).get("status") == "connected",
            "neo4j": svcs.get("neo4j", {}).get("status") == "connected",
        }
    except Exception:
        return {"server": False, "embed": False, "llm": False, "neo4j": False}


# ── Result Collector ─────────────────────────────────────────────────────────

class ResultCollector:
    def __init__(self):
        self.results = []

    def record(self, name, category, latency_ms=0, passed=True, meta=None):
        self.results.append({
            "test": name, "category": category,
            "latency_ms": round(latency_ms, 2), "passed": passed,
            "meta": meta or {}, "ts": time.time()
        })

    def dump(self, path):
        os.makedirs(os.path.dirname(path) if os.path.dirname(path) else ".", exist_ok=True)
        with open(path, "w") as f:
            json.dump(self.results, f, indent=2)


@pytest.fixture(scope="session")
def results(request):
    rc = ResultCollector()
    yield rc
    out_dir = request.config.getoption("--results-dir")
    rc.dump(os.path.join(out_dir, "mcp_test_results.json"))


# ── Helpers ──────────────────────────────────────────────────────────────────

def percentile(data, p):
    """Compute p-th percentile."""
    if not data:
        return 0
    s = sorted(data)
    k = (len(s) - 1) * p / 100
    f = int(k)
    c = min(f + 1, len(s) - 1)
    return s[f] + (k - f) * (s[c] - s[f])
