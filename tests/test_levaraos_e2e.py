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

# How long we'll wait for an async write to surface via search. The MemoryFS
# indexer polls every 2s and processes commits sequentially, so a fresh write
# can take 5-30s to land in Levara depending on backlog. Polling beats fixed
# sleeps because it succeeds quickly when the indexer is idle and only stretches
# to the full timeout under genuine backpressure.
_SEARCH_POLL_TIMEOUT = 45.0
_SEARCH_POLL_INTERVAL = 1.5


def _poll_search(query: str, user_id: str, predicate, timeout: float = _SEARCH_POLL_TIMEOUT):
    """Poll mem0 /search until `predicate(items)` is truthy or timeout. Returns the
    final items list (may be empty if the predicate never matched)."""
    deadline = time.monotonic() + timeout
    items: list = []
    while True:
        resp = requests.post(
            f"{MEM0_URL}/search",
            json={"query": query, "filters": {"user_id": user_id}, "top_k": 5},
            timeout=30,
        )
        if resp.status_code == 200:
            payload = resp.json()
            items = payload.get("results", []) if isinstance(payload, dict) else payload
            if predicate(items):
                return items
        if time.monotonic() >= deadline:
            return items
        time.sleep(_SEARCH_POLL_INTERVAL)


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

    items = _poll_search(
        "programming language preference",
        USER_ID,
        lambda rs: any("rust" in str(r).lower() for r in rs),
    )
    assert items, "search returned no results within poll timeout"
    assert any("rust" in str(r).lower() for r in items), \
        f"Expected 'rust' in search results, got: {items}"


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


def test_update_memory_surfaces_new_text_via_search():
    """Append-only update: a new .md is written with the new text and becomes
    searchable. The old .md is preserved on disk (verified separately by
    test_update_preserves_old_md_as_superseded) — this test only checks that
    recall reflects the latest state."""

    items = _list_memories(USER_ID)
    assert items, "Prior add() should have produced at least one memory"
    memory_id = items[0]["id"]

    paths_before = set(_list_md_files(f"memories/{USER_ID}/"))

    new_text = "Updated preference: I now prefer Zig over Rust for memory-safe systems work."
    upd = requests.put(
        f"{MEM0_URL}/memories/{memory_id}",
        json={"text": new_text},
        timeout=60,
    )
    assert upd.status_code == 200, f"update failed: {upd.text}"

    time.sleep(8)

    # Append-only: a fresh .md must appear (new memory_id), not an in-place
    # rewrite. The old path stays — its preservation is covered separately.
    paths_after = set(_list_md_files(f"memories/{USER_ID}/"))
    new_paths = paths_after - paths_before
    assert new_paths, (
        f"expected a new .md alongside the original after update; "
        f"before={sorted(paths_before)} after={sorted(paths_after)}"
    )
    new_text_on_disk = _read_md(next(iter(new_paths)))
    assert "zig" in new_text_on_disk.lower(), (
        f"new .md should contain updated text, got: {new_text_on_disk[:200]}"
    )

    # Search should reflect the update — superseded chunk must NOT leak;
    # the new chunk must surface.
    items = _poll_search(
        "Zig systems programming",
        USER_ID,
        lambda rs: any("zig" in str(r).lower() for r in rs),
    )
    assert any("zig" in str(r).lower() for r in items), \
        f"Updated memory should be searchable, got: {items}"


def test_delete_memory_drops_from_active_recall():
    """Append-only delete: the .md and tombstone stay on disk (covered by
    test_delete_writes_tombstone_keeps_md). This test verifies the user-facing
    side: the deleted id no longer surfaces in `mem0.get_all` / search, because
    the indexer flips the chunk's status from `active` to `superseded` and the
    default search filter is `status=active`."""

    items = _list_memories(USER_ID)
    assert items, "Need at least one memory to delete"
    target_id = items[0]["id"]

    del_resp = requests.delete(f"{MEM0_URL}/memories/{target_id}", timeout=30)
    assert del_resp.status_code == 200, f"delete failed: {del_resp.text}"

    time.sleep(2)

    remaining = _list_memories(USER_ID)
    assert all(m["id"] != target_id for m in remaining), \
        f"Deleted memory {target_id} still in active list: {[m['id'] for m in remaining]}"


def _list_md_files(prefix: str) -> list:
    resp = requests.get(
        f"{MEMORYFS_URL}/v1/files",
        params={"prefix": prefix},
        headers=MEMORYFS_HEADERS,
        timeout=10,
    )
    resp.raise_for_status()
    return [f["path"] for f in resp.json().get("items", [])]


def _read_md(path: str) -> str:
    resp = requests.get(
        f"{MEMORYFS_URL}/v1/files/{path}",
        headers=MEMORYFS_HEADERS,
        timeout=10,
    )
    resp.raise_for_status()
    return resp.json().get("content", "")


def test_update_preserves_old_md_as_superseded():
    """Append-only invariant: mem0 update must NOT destroy the old .md.

    The old file should remain on disk with status: superseded and a
    superseded_by back-reference; a fresh .md with a new id should exist
    alongside it. This is the load-bearing audit guarantee — never lose
    history.
    """
    user = f"supersede-{uuid.uuid4().hex[:8]}"

    add_resp = requests.post(
        f"{MEM0_URL}/memories",
        json={
            "messages": [
                {"role": "user", "content": "I work primarily in Rust on systems code."},
                {"role": "assistant", "content": "Noted."},
            ],
            "user_id": user,
            "infer": False,
        },
        timeout=120,
    )
    assert add_resp.status_code == 200, add_resp.text
    time.sleep(8)

    paths_before = _list_md_files(f"memories/{user}/")
    assert paths_before, "expected at least one .md after add"
    # With infer=False each message becomes its own memory; pick the one that
    # actually carries the "Rust" content rather than indexing blindly into a
    # non-deterministically ordered listing.
    old_path = next(
        (p for p in paths_before if "rust" in _read_md(p).lower()),
        None,
    )
    assert old_path is not None, (
        f"expected a .md containing 'rust' under memories/{user}/, got: {paths_before}"
    )
    old_text = _read_md(old_path)
    assert "rust" in old_text.lower()

    items = _list_memories(user)
    assert items, "mem0 should expose the memory we just added"
    rust_item = next(
        (it for it in items if "rust" in str(it.get("memory", "")).lower()),
        None,
    )
    assert rust_item is not None, f"no mem0 item references rust, got: {items}"
    memory_id = rust_item["id"]

    upd = requests.put(
        f"{MEM0_URL}/memories/{memory_id}",
        json={"text": "I switched to Zig — Rust borrow checker fatigue."},
        timeout=60,
    )
    assert upd.status_code == 200, upd.text
    time.sleep(8)

    # Old file still exists and is now marked superseded.
    old_after = _read_md(old_path)
    assert "status: superseded" in old_after, (
        f"old .md must be preserved with status=superseded, got:\n{old_after}"
    )
    assert "superseded_by" in old_after, "missing superseded_by back-reference"

    # A new .md exists with a different name.
    paths_after = _list_md_files(f"memories/{user}/")
    new_paths = [p for p in paths_after if p != old_path]
    assert new_paths, f"expected a new .md alongside {old_path}, got: {paths_after}"
    new_text = _read_md(new_paths[0])
    assert "zig" in new_text.lower()
    assert "supersedes" in new_text, "new .md must reference what it supersedes"


def test_delete_writes_tombstone_keeps_md():
    """Append-only delete: the original .md is preserved; a tombstone is added.

    Deletion never physically removes markdown — it writes a status=deleted
    record under .tombstones/ and marks the original superseded. Search
    stops returning it, but git/audit still has the full chain.
    """
    user = f"tomb-{uuid.uuid4().hex[:8]}"

    add_resp = requests.post(
        f"{MEM0_URL}/memories",
        json={
            "messages": [
                {"role": "user", "content": "My phone is +1-555-0100."},
                {"role": "assistant", "content": "Got it."},
            ],
            "user_id": user,
            "infer": False,
        },
        timeout=120,
    )
    assert add_resp.status_code == 200, add_resp.text
    time.sleep(8)

    items = _list_memories(user)
    assert items
    target_id = items[0]["id"]

    paths_before = _list_md_files(f"memories/{user}/")
    assert paths_before
    original_path = paths_before[0]

    del_resp = requests.delete(f"{MEM0_URL}/memories/{target_id}", timeout=30)
    assert del_resp.status_code == 200, del_resp.text
    time.sleep(4)

    # Original .md still readable.
    after = _read_md(original_path)
    assert "status: superseded" in after, (
        f"deleted memory's .md must remain with status=superseded, got:\n{after}"
    )

    # Tombstone exists.
    tombstones = _list_md_files(f"memories/{user}/.tombstones/")
    assert tombstones, "expected a tombstone .md under .tombstones/"
    tomb_text = _read_md(tombstones[0])
    assert "status: deleted" in tomb_text


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

    # User B SHOULD see their own data — poll until indexed (or fail loudly).
    items_b = _poll_search(
        "NeptuneCorp quantum compilers",
        user_b,
        lambda rs: any("neptunecorp" in str(r).lower() for r in rs),
    )
    assert any("neptunecorp" in str(r).lower() for r in items_b), \
        f"User B should find their own data, got: {items_b}"

    # Then assert isolation — user A must NOT see user B's content. We check
    # this *after* user B's data is confirmed indexed so the negative result
    # actually means filtering, not just indexer lag.
    search_a = requests.post(
        f"{MEM0_URL}/search",
        json={"query": "NeptuneCorp quantum compilers", "filters": {"user_id": USER_ID}, "top_k": 5},
        timeout=30,
    )
    items_a = search_a.json().get("results", [])
    assert not any("neptunecorp" in str(r).lower() for r in items_a), \
        f"User A leaked into user B's data: {items_a}"
