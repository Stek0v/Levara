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


def _list_memories(user_id: str) -> list:
    resp = requests.get(
        f"{MEM0_URL}/memories",
        params={"user_id": user_id},
        timeout=30,
    )
    resp.raise_for_status()
    payload = resp.json()
    return payload.get("results", []) if isinstance(payload, dict) else payload


def test_update_memory_rewrites_md_and_reindexes():
    """PUT /memories/{id} should rewrite the .md file and the new text should be searchable."""

    items = _list_memories(USER_ID)
    assert items, "Prior add() should have produced at least one memory"
    memory_id = items[0]["id"]

    new_text = "Updated preference: I now prefer Zig over Rust for memory-safe systems work."
    upd = requests.put(
        f"{MEM0_URL}/memories/{memory_id}",
        json={"text": new_text},
        timeout=60,
    )
    assert upd.status_code == 200, f"update failed: {upd.text}"

    time.sleep(8)

    # The .md file should now contain the new text.
    file_path = None
    files_resp = requests.get(
        f"{MEMORYFS_URL}/v1/files",
        params={"prefix": f"memories/{USER_ID}/"},
        headers=MEMORYFS_HEADERS,
        timeout=10,
    )
    for f in files_resp.json().get("items", []):
        if memory_id in f["path"]:
            file_path = f["path"]
            break
    assert file_path, f"No .md found for memory {memory_id}"

    read_resp = requests.get(
        f"{MEMORYFS_URL}/v1/files/{file_path}",
        headers=MEMORYFS_HEADERS,
        timeout=10,
    )
    content = read_resp.json().get("content", "")
    assert "zig" in content.lower(), f"Updated .md should contain new text, got: {content[:200]}"

    # Search should reflect the update.
    search_resp = requests.post(
        f"{MEM0_URL}/search",
        json={"query": "Zig systems programming", "filters": {"user_id": USER_ID}, "top_k": 5},
        timeout=30,
    )
    payload = search_resp.json()
    items = payload.get("results", []) if isinstance(payload, dict) else payload
    assert any("zig" in str(r).lower() for r in items), \
        f"Updated memory should be searchable, got: {items}"


def test_delete_memory_removes_from_index():
    """DELETE /memories/{id} should remove it from Levara so search no longer returns it."""

    items = _list_memories(USER_ID)
    assert items, "Need at least one memory to delete"
    target_id = items[0]["id"]

    del_resp = requests.delete(f"{MEM0_URL}/memories/{target_id}", timeout=30)
    assert del_resp.status_code == 200, f"delete failed: {del_resp.text}"

    time.sleep(2)

    remaining = _list_memories(USER_ID)
    assert all(m["id"] != target_id for m in remaining), \
        f"Deleted memory {target_id} still in list: {[m['id'] for m in remaining]}"


def test_multi_user_isolation():
    """Memories added by user A must not show up in user B's search."""

    user_b = f"smoke-other-{uuid.uuid4().hex[:8]}"

    add_resp = requests.post(
        f"{MEM0_URL}/memories",
        json={
            "messages": [
                {"role": "user", "content": "I work at NeptuneCorp on quantum compilers."},
                {"role": "assistant", "content": "Noted."},
            ],
            "user_id": user_b,
            "infer": False,
        },
        timeout=120,
    )
    assert add_resp.status_code == 200, f"add for user B failed: {add_resp.text}"

    time.sleep(8)

    # User A should NOT see NeptuneCorp.
    search_a = requests.post(
        f"{MEM0_URL}/search",
        json={"query": "NeptuneCorp quantum compilers", "filters": {"user_id": USER_ID}, "top_k": 5},
        timeout=30,
    )
    items_a = search_a.json().get("results", [])
    assert not any("neptunecorp" in str(r).lower() for r in items_a), \
        f"User A leaked into user B's data: {items_a}"

    # User B SHOULD see it.
    search_b = requests.post(
        f"{MEM0_URL}/search",
        json={"query": "NeptuneCorp quantum compilers", "filters": {"user_id": user_b}, "top_k": 5},
        timeout=30,
    )
    items_b = search_b.json().get("results", [])
    assert any("neptunecorp" in str(r).lower() for r in items_b), \
        f"User B should find their own data, got: {items_b}"
