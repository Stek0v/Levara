"""LevaraOS E2E smoke test — Variant B write path.

Flow: mem0 add() → MemoryFS REST (write .md + commit) → Levara index → mem0 search().

Requires all services running:
    docker compose -f docker-compose.levaraos.yml up -d --build

Usage:
    pytest tests/test_levaraos_e2e.py -v -s
"""

import time

import pytest
import requests

MEMORYFS_URL = "http://localhost:7777"
MEM0_URL = "http://localhost:8888"
LEVARA_URL = "http://localhost:8080"

import uuid

USER_ID = f"smoke-{uuid.uuid4().hex[:8]}"
MEMORYFS_TOKEN = "atk_mem0"
MEMORYFS_HEADERS = {"authorization": f"Bearer {MEMORYFS_TOKEN}"}


@pytest.fixture(scope="module", autouse=True)
def check_services():
    for name, url in [
        ("MemoryFS", f"{MEMORYFS_URL}/v1/admin/health"),
        ("mem0", f"{MEM0_URL}/docs"),
        ("Levara", f"{LEVARA_URL}/metrics"),
    ]:
        try:
            r = requests.get(url, timeout=5)
            r.raise_for_status()
        except Exception as e:
            pytest.skip(f"{name} not reachable at {url}: {e}")


def test_write_via_mem0_read_via_search():
    """mem0 add() should write through MemoryFS, then search should find it."""

    add_resp = requests.post(
        f"{MEM0_URL}/memories",
        json={
            "messages": [
                {"role": "user", "content": "My favorite programming language is Rust and I love building vector databases."},
                {"role": "assistant", "content": "Great choices! Rust is excellent for systems programming."},
            ],
            "user_id": USER_ID,
            "infer": False,
        },
        timeout=120,
    )
    assert add_resp.status_code == 200, f"mem0 add failed: {add_resp.text}"
    add_data = add_resp.json()
    assert "results" in add_data

    time.sleep(8)

    search_resp = requests.post(
        f"{MEM0_URL}/search",
        json={
            "query": "programming language preference",
            "filters": {"user_id": USER_ID},
            "top_k": 5,
        },
        timeout=30,
    )
    assert search_resp.status_code == 200, f"mem0 search failed: {search_resp.text}"
    payload = search_resp.json()
    items = payload.get("results", []) if isinstance(payload, dict) else payload
    assert len(items) > 0, f"search returned no results: {payload}"

    found_rust = any("rust" in str(r).lower() for r in items)
    assert found_rust, f"Expected 'rust' in search results, got: {items}"


def test_memoryfs_has_committed_files():
    """After mem0 add(), MemoryFS should have .md files in the memories/ directory."""

    files_resp = requests.get(
        f"{MEMORYFS_URL}/v1/files",
        params={"prefix": f"memories/{USER_ID}/"},
        headers=MEMORYFS_HEADERS,
        timeout=10,
    )
    assert files_resp.status_code == 200, f"MemoryFS list failed: {files_resp.text}"
    data = files_resp.json()
    items = data.get("items", [])
    assert len(items) > 0, "No memory files found in MemoryFS after mem0 add()"

    first_path = items[0]["path"]
    assert first_path.endswith(".md"), f"Expected .md file, got: {first_path}"

    read_resp = requests.get(
        f"{MEMORYFS_URL}/v1/files/{first_path}",
        headers=MEMORYFS_HEADERS,
        timeout=10,
    )
    assert read_resp.status_code == 200
    content = read_resp.json().get("content", "")
    assert "---" in content, "Memory file should have frontmatter"
