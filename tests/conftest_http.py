"""Shared fixtures for Cognevra HTTP API tests."""
import os
import uuid
import random
import math
import pytest
import aiohttp

BASE_URL = os.getenv("COGNEVRA_HTTP_URL", "http://localhost:8080/api/v1")

# ── Markers ──

def pytest_configure(config):
    config.addinivalue_line("markers", "smoke: minimal tests, only Go server needed")
    config.addinivalue_line("markers", "requires_postgres: skip without PostgreSQL")
    config.addinivalue_line("markers", "requires_neo4j: skip without Neo4j")
    config.addinivalue_line("markers", "requires_embed: skip without embed-server")
    config.addinivalue_line("markers", "requires_llm: skip without Ollama/LLM")


# ── Service availability detection ──

_capabilities = {}

async def _detect_capabilities():
    global _capabilities
    if _capabilities:
        return _capabilities
    try:
        async with aiohttp.ClientSession() as s:
            # Health check
            async with s.get(f"{BASE_URL}/health") as r:
                _capabilities["server"] = r.status == 200

            # Settings reveal backend availability
            async with s.get(f"{BASE_URL}/settings") as r:
                if r.status == 200:
                    data = await r.json()
                    _capabilities["neo4j"] = data.get("graph_engine", "none") != "none"
                    _capabilities["embed"] = bool(data.get("embedding_endpoint", ""))
                    _capabilities["llm"] = data.get("llm_provider", "none") != "none"
                    _capabilities["postgres"] = False  # detect via roundtrip below

            # Postgres detection: create+delete dataset roundtrip
            async with s.post(f"{BASE_URL}/datasets", json={"name": f"_probe_{uuid.uuid4().hex[:8]}"}) as r:
                if r.status == 201:
                    data = await r.json()
                    ds_id = data.get("id", "")
                    # Try to list and see if it persists
                    async with s.get(f"{BASE_URL}/datasets") as lr:
                        if lr.status == 200:
                            items = await lr.json()
                            _capabilities["postgres"] = any(d.get("id") == ds_id for d in items)
                    # Cleanup
                    await s.delete(f"{BASE_URL}/datasets/{ds_id}")
    except Exception:
        _capabilities["server"] = False
    return _capabilities


def pytest_collection_modifyitems(config, items):
    """Skip tests based on marker requirements and detected capabilities."""
    import asyncio
    loop = asyncio.new_event_loop()
    caps = loop.run_until_complete(_detect_capabilities())
    loop.close()

    skip_map = {
        "requires_postgres": ("postgres", "PostgreSQL not available"),
        "requires_neo4j": ("neo4j", "Neo4j not available"),
        "requires_embed": ("embed", "Embed-server not available"),
        "requires_llm": ("llm", "LLM not available"),
    }

    for item in items:
        for marker, (cap_key, reason) in skip_map.items():
            if marker in [m.name for m in item.iter_markers()]:
                if not caps.get(cap_key, False):
                    item.add_marker(pytest.mark.skip(reason=reason))


# ── Fixtures ──

@pytest.fixture(scope="session")
def base_url():
    return BASE_URL


@pytest.fixture(scope="session")
def event_loop():
    """Session-scoped event loop for async fixtures."""
    import asyncio
    loop = asyncio.new_event_loop()
    yield loop
    loop.close()


@pytest.fixture(scope="session")
async def http_session():
    session = aiohttp.ClientSession()
    yield session
    await session.close()


@pytest.fixture(scope="session")
async def auth_token(http_session, base_url):
    """Register + login to get JWT token."""
    email = f"testuser_{uuid.uuid4().hex[:8]}@cognevra.dev"
    password = "testpass123456"
    # Register (ignore conflict if already exists)
    await http_session.post(
        f"{base_url}/auth/register",
        json={"email": email, "password": password}
    )
    # Login
    async with http_session.post(
        f"{base_url}/auth/login",
        json={"email": email, "password": password}
    ) as r:
        data = await r.json()
        return data.get("access_token", "")


@pytest.fixture(scope="session")
async def auth_headers(auth_token):
    return {"Authorization": f"Bearer {auth_token}"}


def unique_id(prefix="http_test"):
    return f"{prefix}:{uuid.uuid4().hex[:12]}"


def sample_vector(dim=128):
    """Generate a normalized random vector."""
    v = [random.gauss(0, 1) for _ in range(dim)]
    norm = math.sqrt(sum(x * x for x in v))
    return [x / norm for x in v]


@pytest.fixture
def vid():
    """Unique vector ID for test isolation."""
    return unique_id()


@pytest.fixture
def vec():
    """Random 128-dim vector."""
    return sample_vector(128)
