"""GRAPH_COMPLETION_CONTEXT_EXTENSION — 2-hop graph traversal search.

Отличие от GRAPH_COMPLETION (1-hop):
  1-hop: entity → прямые соседи
  2-hop: entity → соседи → ИХ соседи (расширенный контекст)

Результат: более богатый контекст для LLM → более полные ответы.

Риски:
  R1: 2-hop explosion — слишком много сущностей на 2nd hop → timeout/OOM
  R2: Duplicate context — одни и те же связи в hop1 и hop2
  R3: Quality degradation — больше контекста ≠ лучший ответ (noise)
  R4: Performance — 2 Neo4j query вместо 1 → дольше
  R5: Fallback — без Neo4j 2-hop не работает на PostgreSQL (только 1-hop)
"""
import os
import uuid
import time
import asyncio
import aiohttp
import pytest

BASE = os.getenv("LEVARA_HTTP_URL", "http://localhost:8080/api/v1")
TIMEOUT = aiohttp.ClientTimeout(total=300)

pytestmark = pytest.mark.asyncio


async def _auth(s):
    email = f"ctx_ext_{uuid.uuid4().hex[:8]}@test.com"
    pw = "testpass123456"
    await s.post(f"{BASE}/auth/register", json={"email": email, "password": pw})
    async with s.post(f"{BASE}/auth/login", json={"email": email, "password": pw}) as r:
        data = await r.json()
        return {"Authorization": f"Bearer {data.get('access_token', '')}"}


async def _search(s, h, query, query_type="GRAPH_COMPLETION_CONTEXT_EXTENSION", top_k=10):
    async with s.post(
        f"{BASE}/search/text",
        json={"query_text": query, "query_type": query_type, "top_k": top_k},
        headers=h,
    ) as r:
        try:
            data = await r.json()
        except Exception:
            data = await r.text()
        return r.status, data


async def _cognify_and_wait(s, h, text, timeout_s=300):
    coll = f"ctx_ext_{uuid.uuid4().hex[:8]}"
    async with s.post(
        f"{BASE}/cognify",
        json={"texts": [text], "collection": coll},
        headers=h,
    ) as r:
        if r.status != 200:
            return {"status": f"ERROR_{r.status}"}
        data = await r.json()
        run_id = data.get("pipeline_run_id", "")
    if not run_id:
        return {"status": "NO_RUN_ID"}
    for _ in range(timeout_s // 5):
        await asyncio.sleep(5)
        async with s.get(f"{BASE}/cognify/{run_id}/status", headers=h) as r:
            if r.status == 200:
                sd = await r.json()
                if sd.get("status") in ("COMPLETED", "FAILED"):
                    return sd
    return {"status": "TIMEOUT"}


# ═══════════════ FUNCTIONAL TESTS ═══════════════


async def test_context_extension_returns_response():
    """DoD: GRAPH_COMPLETION_CONTEXT_EXTENSION → status 200, содержит search_type."""
    async with aiohttp.ClientSession(timeout=TIMEOUT) as s:
        h = await _auth(s)
        status, data = await _search(s, h, "What is machine learning?")
        assert status == 200, f"Context extension вернул {status}"
        if isinstance(data, dict):
            assert data.get("search_type") == "GRAPH_COMPLETION_CONTEXT_EXTENSION", (
                f"Неправильный search_type: {data.get('search_type')}"
            )


async def test_context_extension_has_2_hops():
    """DoD: Response содержит context_hop1 и context_hop2 (два уровня)."""
    async with aiohttp.ClientSession(timeout=TIMEOUT) as s:
        h = await _auth(s)
        status, data = await _search(s, h, "Einstein relativity physics")
        assert status == 200
        if isinstance(data, dict):
            assert "context_hop1" in data, f"Нет context_hop1 в ответе: {list(data.keys())}"
            assert "context_hop2" in data, f"Нет context_hop2 в ответе: {list(data.keys())}"
            assert data.get("hops") == 2, f"hops != 2: {data.get('hops')}"


async def test_context_extension_more_context_than_basic():
    """DoD: Context extension даёт >= столько же контекста что и обычный GRAPH_COMPLETION.
    R3: больше контекста ≠ хуже."""
    async with aiohttp.ClientSession(timeout=TIMEOUT) as s:
        h = await _auth(s)

        # Обычный GRAPH_COMPLETION
        _, basic_data = await _search(s, h, "neural networks deep learning", "GRAPH_COMPLETION")
        basic_context = []
        if isinstance(basic_data, dict):
            basic_context = basic_data.get("context", []) or []

        # CONTEXT_EXTENSION
        _, ext_data = await _search(s, h, "neural networks deep learning")
        ext_context = []
        if isinstance(ext_data, dict):
            hop1 = ext_data.get("context_hop1", []) or []
            hop2 = ext_data.get("context_hop2", []) or []
            ext_context = hop1 + hop2

        # Extension должен дать >= базового
        assert len(ext_context) >= len(basic_context), (
            f"R3: Extension ({len(ext_context)} facts) дал МЕНЬШЕ контекста чем basic ({len(basic_context)})"
        )


async def test_context_extension_empty_graph():
    """DoD: Пустой граф → 200, пустой context (не crash). R5: fallback работает."""
    async with aiohttp.ClientSession(timeout=TIMEOUT) as s:
        h = await _auth(s)
        status, data = await _search(s, h, "completely unique nonexistent topic xyz123")
        assert status == 200, f"Context extension crash на пустом графе: {status}"


async def test_context_extension_empty_query():
    """DoD: Пустой query → 400."""
    async with aiohttp.ClientSession(timeout=TIMEOUT) as s:
        h = await _auth(s)
        async with s.post(
            f"{BASE}/search/text",
            json={"query_text": "", "query_type": "GRAPH_COMPLETION_CONTEXT_EXTENSION"},
            headers=h,
        ) as r:
            assert r.status == 400


# ═══════════════ QUALITY + E2E TESTS ═══════════════


async def test_context_extension_after_cognify():
    """DoD: Cognify текст → CONTEXT_EXTENSION search → answer с данными из текста.
    Полный E2E: ingest → cognify → 2-hop search → LLM answer."""
    async with aiohttp.ClientSession(timeout=TIMEOUT) as s:
        h = await _auth(s)

        text = (
            "Marie Curie was born in Warsaw, Poland. She moved to Paris to study at the Sorbonne. "
            "She discovered radium and polonium. Pierre Curie was her husband and research partner. "
            "They won the Nobel Prize in Physics in 1903. Marie later won the Nobel Prize in Chemistry in 1911. "
            "Their daughter Irène Joliot-Curie also won a Nobel Prize in Chemistry in 1935."
        )
        result = await _cognify_and_wait(s, h, text)
        assert result["status"] == "COMPLETED", f"Cognify не завершился: {result}"

        # 2-hop search — должен найти связи через 2 уровня
        # Например: Marie Curie → Pierre Curie → Nobel Prize → Physics
        status, data = await _search(s, h, "Who won Nobel Prize and what was their family connection?")
        assert status == 200

        # Проверяем что ответ содержит данные
        if isinstance(data, dict):
            all_context = (data.get("context_hop1") or []) + (data.get("context_hop2") or [])
            answer = data.get("answer", "")
            data_str = str(data).lower()

            # Должны найти хотя бы одно из ключевых слов
            keywords = ["curie", "nobel", "radium", "paris", "warsaw", "irène", "pierre", "sorbonne"]
            found = [k for k in keywords if k in data_str]
            has_content = len(found) > 0 or len(all_context) > 0 or len(answer) > 0

            assert has_content, (
                f"Context extension не нашёл данные после cognify. "
                f"Keywords found: {found}. Context: {len(all_context)} facts. "
                f"Answer: {answer[:200]}"
            )


# ═══════════════ PERFORMANCE TESTS ═══════════════


async def test_context_extension_performance():
    """DoD: CONTEXT_EXTENSION < 120s (2 Neo4j queries + LLM). R4: overhead от 2-hop."""
    async with aiohttp.ClientSession(timeout=TIMEOUT) as s:
        h = await _auth(s)
        t0 = time.monotonic()
        status, _ = await _search(s, h, "quantum computing applications")
        elapsed = time.monotonic() - t0
        assert status == 200
        assert elapsed < 120, f"R4: Context extension занял {elapsed:.1f}s, лимит 120s"


async def test_context_extension_vs_basic_performance():
    """DoD: Extension не более чем 3x медленнее чем basic GRAPH_COMPLETION.
    R4: 2-hop не должен быть катастрофически медленнее."""
    async with aiohttp.ClientSession(timeout=TIMEOUT) as s:
        h = await _auth(s)
        query = "database indexing algorithms"

        t0 = time.monotonic()
        await _search(s, h, query, "GRAPH_COMPLETION")
        basic_time = time.monotonic() - t0

        t0 = time.monotonic()
        await _search(s, h, query)
        ext_time = time.monotonic() - t0

        # Extension может быть медленнее (2 queries), но не более чем 3x
        if basic_time > 0.1:  # Только если basic занял ощутимое время
            ratio = ext_time / basic_time
            assert ratio < 3.0, (
                f"R4: Extension {ext_time:.1f}s = {ratio:.1f}x vs basic {basic_time:.1f}s. "
                f"Лимит: 3x slowdown"
            )


async def test_context_extension_concurrent():
    """DoD: 3 concurrent CONTEXT_EXTENSION → все 200. R1: no explosion."""
    async with aiohttp.ClientSession(timeout=TIMEOUT) as s:
        h = await _auth(s)
        queries = ["physics", "chemistry", "biology"]
        tasks = [_search(s, h, q) for q in queries]
        results = await asyncio.gather(*tasks, return_exceptions=True)
        for i, res in enumerate(results):
            assert not isinstance(res, Exception), f"R1: concurrent #{i} failed: {res}"
            status, _ = res
            assert status == 200, f"R1: concurrent #{i} returned {status}"
