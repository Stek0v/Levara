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

USER_ID = "smoke-test-user"


@pytest.fixture(scope="module", autouse=True)
def check_services():
    for name, url in [
        ("MemoryFS", f"{MEMORYFS_URL}/v1/admin/health"),
        ("mem0", f"{MEM0_URL}/health"),
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
        f"{MEM0_URL}/v1/memories/",
        json={
            "messages": [
                {"role": "user", "content": "My favorite programming language is Rust and I love building vector databases."},
                {"role": "assistant", "content": "Great choices! Rust is excellent for systems programming."},
            ],
            "user_id": USER_ID,
        },
        timeout=60,
    )
    assert add_resp.status_code == 200, f"mem0 add failed: {add_resp.text}"
    add_data = add_resp.json()
    assert "results" in add_data

    time.sleep(3)

    search_resp = requests.post(
        f"{MEM0_URL}/v1/memories/search/",
        json={
            "query": "programming language preference",
            "user_id": USER_ID,
            "top_k": 5,
        },
        timeout=30,
    )
    assert search_resp.status_code == 200, f"mem0 search failed: {search_resp.text}"
    results = search_resp.json()
    assert len(results) > 0, "search returned no results"

    found_rust = any("rust" in str(r).lower() for r in results)
    assert found_rust, f"Expected 'rust' in search results, got: {results}"


def test_memoryfs_has_committed_files():
    """After mem0 add(), MemoryFS should have .md files in the memories/ directory."""

    files_resp = requests.get(
        f"{MEMORYFS_URL}/v1/files",
        params={"prefix": f"memories/{USER_ID}/"},
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
        timeout=10,
    )
    assert read_resp.status_code == 200
    content = read_resp.json().get("content", "")
    assert "---" in content, "Memory file should have frontmatter"
