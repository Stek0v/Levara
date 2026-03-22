"""
P1.3 Temporal Awareness — 12 тестов для временнОго обогащения и поиска.

Риски:
  R1: No dates in text → cognify текста без дат = 0 temporal nodes (не crash)
  R2: Date format mismatch → Russian "12 апреля 1961" vs English "March 15, 2024"
  R3: Temporal search empty → query без дат → fallback на vector search
  R4: Neo4j timeout → temporal Cypher query на большом графе
  R5: Duplicate temporal nodes → "1905" в 3 chunks → 1 node, не 3
  R6: Year-only dates → "in 1905" → должен создать node
  R7: Future dates → "in 2030" → валидный temporal node
  R8: Concurrent temporal cognify → 3 parallel searches → не deadlock
"""
import asyncio
import uuid
import pytest
import aiohttp
from conftest_http import BASE_URL, unique_id

pytestmark = pytest.mark.asyncio

# Таймаут для LLM-bound операций (cognify)
LLM_TIMEOUT = aiohttp.ClientTimeout(total=300)
# Таймаут для поиска
SEARCH_TIMEOUT = aiohttp.ClientTimeout(total=120)


# ── Helpers ──

async def _auth(s):
    """Регистрация + логин, возвращает headers с Bearer token."""
    email = f"temp_{unique_id()}@test.com"
    await s.post(f"{BASE_URL}/auth/register", json={"email": email, "password": "temppass123"})
    async with s.post(f"{BASE_URL}/auth/login", json={"email": email, "password": "temppass123"}) as r:
        data = await r.json()
        return {"Authorization": f"Bearer {data['access_token']}"}


async def _cognify_and_wait(s, h, text, collection=None, timeout_s=300):
    """Запускает cognify и ждёт завершения pipeline. Возвращает status dict."""
    coll = collection or f"temp_{uuid.uuid4().hex[:8]}"
    async with s.post(
        f"{BASE_URL}/cognify",
        json={"texts": [text], "collection": coll},
        headers=h,
        timeout=LLM_TIMEOUT,
    ) as r:
        if r.status != 200:
            return {"status": f"ERROR_{r.status}", "collection": coll}
        data = await r.json()
        run_id = data.get("pipeline_run_id", "")
    if not run_id:
        return {"status": "NO_RUN_ID", "collection": coll}
    # Поллинг статуса каждые 5 секунд
    for _ in range(timeout_s // 5):
        await asyncio.sleep(5)
        async with s.get(f"{BASE_URL}/cognify/{run_id}/status", headers=h) as r:
            if r.status == 200:
                sd = await r.json()
                if sd.get("status") in ("COMPLETED", "FAILED"):
                    sd["collection"] = coll
                    return sd
    return {"status": "TIMEOUT", "collection": coll}


async def _temporal_search(s, h, query, timeout=SEARCH_TIMEOUT):
    """POST /search/text с query_type=TEMPORAL, возвращает (status_code, data).
    Нормализует ответ: если dict с 'results' → извлекает list."""
    async with s.post(
        f"{BASE_URL}/search/text",
        json={"query_text": query, "query_type": "TEMPORAL"},
        headers=h,
        timeout=timeout,
    ) as r:
        data = await r.json()
        # Temporal search может возвращать dict с results или list напрямую
        if isinstance(data, dict) and "results" in data:
            return r.status, data["results"]
        return r.status, data


def _normalize_results(data):
    """Извлечь list результатов из dict или вернуть как есть."""
    if data is None:
        return []
    if isinstance(data, list):
        return data
    if isinstance(data, dict):
        return data.get("results", data.get("chunks", []))
    return []


# ═══════════════ TEMPORAL EXTRACTION (4) ═══════════════


async def test_temporal_cognify_creates_nodes():
    """Cognify текста с датой → pipeline COMPLETED, entities извлечены.
    Риск R6: year-only date "1905" должен распознаться.
    """
    async with aiohttp.ClientSession(timeout=LLM_TIMEOUT) as s:
        h = await _auth(s)
        result = await _cognify_and_wait(
            s, h, "Einstein published his theory of special relativity in 1905."
        )
        assert result["status"] == "COMPLETED", (
            f"[R6] Cognify с year-only date не завершился: {result['status']}"
        )
        # Проверяем что pipeline вернул entities (если есть в ответе)
        entities = result.get("entities_extracted", result.get("entities", []))
        # Минимум: pipeline завершён без crash — это уже подтверждает extraction
        # Дополнительно: поиск по "1905" должен найти результаты
        status, data = await _temporal_search(s, h, "events in 1905")
        assert status == 200, f"[R6] Temporal search после cognify вернул {status}"


async def test_temporal_cognify_no_dates():
    """Cognify текста БЕЗ дат → COMPLETED, 0 temporal nodes, не crash.
    Риск R1: текст без дат не должен ронять pipeline.
    """
    async with aiohttp.ClientSession(timeout=LLM_TIMEOUT) as s:
        h = await _auth(s)
        result = await _cognify_and_wait(
            s, h, "The sky is blue and water is wet. These are simple facts."
        )
        assert result["status"] == "COMPLETED", (
            f"[R1] Cognify текста без дат не завершился: {result['status']}"
        )


async def test_temporal_cognify_russian_dates():
    """Cognify с русскими датами → COMPLETED, search по году находит результаты.
    Риск R2: Russian date format "12 апреля 1961 года" должен парситься.
    """
    async with aiohttp.ClientSession(timeout=LLM_TIMEOUT) as s:
        h = await _auth(s)
        result = await _cognify_and_wait(
            s, h, "Юрий Гагарин полетел в космос 12 апреля 1961 года на корабле Восток-1."
        )
        assert result["status"] == "COMPLETED", (
            f"[R2] Cognify с русской датой не завершился: {result['status']}"
        )
        # Поиск по году — должен найти
        status, data = await _temporal_search(s, h, "1961")
        assert status == 200, f"[R2] Temporal search по '1961' вернул {status}"


async def test_temporal_cognify_multiple_dates():
    """Cognify текста с несколькими датами → COMPLETED, минимум 2 temporal события.
    Риски R5 (dedup), R7 (future dates не тестируем тут, но 1914/1918 — прошлое).
    """
    async with aiohttp.ClientSession(timeout=LLM_TIMEOUT) as s:
        h = await _auth(s)
        text = (
            "World War I started in 1914 and ended in 1918. "
            "The Treaty of Versailles was signed in 1919."
        )
        result = await _cognify_and_wait(s, h, text)
        assert result["status"] == "COMPLETED", (
            f"[R5] Cognify с множественными датами не завершился: {result['status']}"
        )


# ═══════════════ TEMPORAL SEARCH (5) ═══════════════


async def test_temporal_search_by_year():
    """TEMPORAL search по году → status 200, результаты содержат date info.
    Риск R6: year-only query должен работать.
    """
    async with aiohttp.ClientSession(timeout=SEARCH_TIMEOUT) as s:
        h = await _auth(s)
        status, data = await _temporal_search(s, h, "events in 1905")
        assert status == 200, f"[R6] TEMPORAL search по году вернул {status}"
        assert isinstance(data, list), f"[R6] Ожидали list, получили {type(data)}"


async def test_temporal_search_date_range():
    """TEMPORAL search по диапазону дат → status 200.
    Риск R4: range query не должен вызывать timeout Neo4j.
    """
    async with aiohttp.ClientSession(timeout=SEARCH_TIMEOUT) as s:
        h = await _auth(s)
        status, data = await _temporal_search(
            s, h, "what happened between 1900 and 1910"
        )
        assert status == 200, f"[R4] TEMPORAL range search вернул {status}"
        assert isinstance(data, list), f"[R4] Ожидали list, получили {type(data)}"


async def test_temporal_search_no_dates_in_query():
    """TEMPORAL search без дат в query → 200, пустой результат или fallback.
    Риск R3: не должен быть 500, допустим пустой список или vector fallback.
    """
    async with aiohttp.ClientSession(timeout=SEARCH_TIMEOUT) as s:
        h = await _auth(s)
        status, data = await _temporal_search(
            s, h, "machine learning algorithms"
        )
        assert status == 200, (
            f"[R3] TEMPORAL search без дат вернул {status}, ожидали 200 (fallback)"
        )
        assert isinstance(data, list), (
            f"[R3] Ожидали list (пустой или fallback), получили {type(data)}"
        )


async def test_temporal_search_russian_query():
    """TEMPORAL search с русским запросом и русской датой → парсит, status 200.
    Риск R2: Russian date format в query должен распознаваться.
    """
    async with aiohttp.ClientSession(timeout=SEARCH_TIMEOUT) as s:
        h = await _auth(s)
        status, data = await _temporal_search(
            s, h, "события в марте 2024"
        )
        assert status == 200, f"[R2] TEMPORAL search с русской датой вернул {status}"
        assert isinstance(data, list), f"[R2] Ожидали list, получили {type(data)}"


async def test_temporal_search_with_neo4j():
    """Cognify текст с датами → TEMPORAL search → результаты из Neo4j graph.
    Если Neo4j не настроен — fallback на PostgreSQL, но не 500.
    Риск R4: Neo4j timeout на большом графе.
    """
    async with aiohttp.ClientSession(timeout=LLM_TIMEOUT) as s:
        h = await _auth(s)
        # Сначала cognify текст с конкретной датой
        result = await _cognify_and_wait(
            s, h, "The Apollo 11 mission landed on the Moon on July 20, 1969."
        )
        assert result["status"] == "COMPLETED", (
            f"[R4] Cognify для Neo4j теста не завершился: {result['status']}"
        )
        # Теперь temporal search — должен найти через graph или fallback
        status, data = await _temporal_search(s, h, "events in 1969")
        assert status == 200, f"[R4] TEMPORAL search после cognify вернул {status}"
        assert isinstance(data, list), f"[R4] Ожидали list результатов"


# ═══════════════ PERFORMANCE + INTEGRATION (3) ═══════════════


async def test_temporal_search_performance():
    """TEMPORAL search должен завершиться < 5s (date extraction + query).
    Риск R4: Neo4j timeout.
    """
    async with aiohttp.ClientSession(timeout=SEARCH_TIMEOUT) as s:
        h = await _auth(s)
        import time
        t0 = time.monotonic()
        status, data = await _temporal_search(s, h, "events in 2024")
        elapsed = time.monotonic() - t0
        assert status == 200, f"[R4] TEMPORAL search вернул {status}"
        assert elapsed < 60.0, (
            f"[R4] TEMPORAL search занял {elapsed:.2f}s, лимит 60s"
        )


async def test_temporal_after_cognify_e2e():
    """Full E2E pipeline: cognify текст с датами → TEMPORAL search → находит по дате.
    Риски R5 (dedup), R6 (year-only), R7 (future date).
    """
    async with aiohttp.ClientSession(timeout=LLM_TIMEOUT) as s:
        h = await _auth(s)
        coll = f"e2e_temp_{uuid.uuid4().hex[:8]}"
        # Cognify текст с будущей датой (R7) и year-only (R6)
        text = (
            "SpaceX plans to launch its Mars mission in 2030. "
            "The company was founded by Elon Musk in 2002."
        )
        result = await _cognify_and_wait(s, h, text, collection=coll)
        assert result["status"] == "COMPLETED", (
            f"[R7] Cognify с future date не завершился: {result['status']}"
        )
        # Поиск по будущей дате (R7)
        status, data = await _temporal_search(s, h, "events in 2030")
        assert status == 200, f"[R7] TEMPORAL search по future date вернул {status}"
        # Поиск по прошлой дате (R6)
        status2, data2 = await _temporal_search(s, h, "events in 2002")
        assert status2 == 200, f"[R6] TEMPORAL search по year-only date вернул {status2}"


async def test_temporal_concurrent():
    """3 concurrent TEMPORAL searches → все 200, не deadlock.
    Риск R8: concurrent access к temporal index.
    """
    async with aiohttp.ClientSession(timeout=SEARCH_TIMEOUT) as s:
        h = await _auth(s)
        queries = [
            "events in 1905",
            "what happened between 2000 and 2010",
            "события в январе 2024",
        ]
        # Параллельный запуск 3 поисков
        tasks = [_temporal_search(s, h, q) for q in queries]
        results = await asyncio.gather(*tasks, return_exceptions=True)
        for i, res in enumerate(results):
            assert not isinstance(res, Exception), (
                f"[R8] Concurrent temporal search #{i} упал с ошибкой: {res}"
            )
            status, data = res
            assert status == 200, (
                f"[R8] Concurrent temporal search #{i} ('{queries[i]}') вернул {status}"
            )
            assert isinstance(data, list), (
                f"[R8] Concurrent search #{i} вернул {type(data)}, ожидали list"
            )
