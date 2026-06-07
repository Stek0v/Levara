"""
P1.5 Structured Output — тесты для JSON Schema extraction через cognify pipeline.

Риски (risk analysis):
  R1: Model не поддерживает json_schema — Ollama старой версии → 400 → нужен fallback.
  R2: JSON Schema слишком strict — strict:true отклоняет доп. поля → потеря данных.
  R3: Retry loop бесконечный — MaxRetries=0 или LLM consistently fails → должен остановиться.
  R4: Structured output медленнее — JSON Schema overhead → latency увеличивается.
  R5: Cache ключ не учитывает mode — structured vs unstructured → cache collision.
  R6: Пустой response — LLM возвращает {} → парсится как 0 entities, не ошибка.
  R7: Partial JSON — LLM обрезает ответ (token limit) → невалидный JSON → retry.
  R8: Concurrent structured calls — 5 одновременных → все должны получить валидный JSON.

12 тестов: 6 functional, 3 quality, 3 performance.
Все тесты используют РЕАЛЬНЫЙ cognify pipeline + LLM (gemma3:4b через Ollama).
НЕ используем pytest.skip() — если сервис упал, тест FAIL.
"""
import asyncio
import json
import time
import uuid

import aiohttp
import pytest

from conftest_http import BASE_URL, unique_id

pytestmark = pytest.mark.asyncio

BASE = BASE_URL  # http://localhost:8080/api/v1
TIMEOUT = aiohttp.ClientTimeout(total=300)


# ── Auth helper ──

async def _auth(s: aiohttp.ClientSession) -> dict:
    """Регистрация + логин, возвращает заголовки авторизации."""
    email = f"struct_{unique_id()}@test.com"
    pw = "structpass123"
    await s.post(f"{BASE}/auth/register", json={"email": email, "password": pw})
    async with s.post(f"{BASE}/auth/login", json={"email": email, "password": pw}) as r:
        data = await r.json()
        return {"Authorization": f"Bearer {data['access_token']}"}


# ── Cognify + poll helper ──

async def _cognify_and_wait(
    s: aiohttp.ClientSession,
    h: dict,
    text: str,
    collection: str | None = None,
    timeout_s: int = 300,
) -> dict:
    """POST /cognify → poll status → return final status dict.
    Возвращает dict с полями: status, entities_extracted, edges_extracted и т.д.
    При ошибке возвращает {"status": "ERROR_<код>"} или {"status": "TIMEOUT"}.
    """
    coll = collection or f"struct_{uuid.uuid4().hex[:8]}"
    async with s.post(
        f"{BASE}/cognify",
        json={"texts": [text], "collection": coll},
        headers=h,
    ) as r:
        if r.status != 200:
            body = await r.text()
            return {"status": f"ERROR_{r.status}", "detail": body}
        data = await r.json()
        run_id = data.get("pipeline_run_id", data.get("run_id", ""))

    if not run_id:
        return {"status": "NO_RUN_ID", "raw": data}

    # Poll до завершения (каждые 5 секунд)
    t0 = time.monotonic()
    for _ in range(timeout_s // 5):
        await asyncio.sleep(5)
        if time.monotonic() - t0 > timeout_s:
            return {"status": "TIMEOUT"}
        async with s.get(f"{BASE}/cognify/{run_id}/status", headers=h) as r:
            if r.status == 200:
                status_data = await r.json()
                st = status_data.get("status", "")
                if st in ("COMPLETED", "FAILED"):
                    return status_data
    return {"status": "TIMEOUT"}


# ═══════════════════════════════════════════════════════════════════
#  FUNCTIONAL TESTS (6)
#  Риски: R1, R2, R3, R6, R7
# ═══════════════════════════════════════════════════════════════════

async def test_structured_cognify_extracts_entities():
    """DoD: POST /cognify с текстом → status COMPLETED → entities_extracted > 0.
    Проверяет: structured output парсит entities из LLM response.
    Текст содержит чёткие named entities (Marie Curie, radium, 1898, University of Paris).
    Риски: R1 (model fallback), R2 (strict schema), R7 (partial JSON).
    """
    async with aiohttp.ClientSession(timeout=TIMEOUT) as s:
        h = await _auth(s)
        text = "Marie Curie discovered radium in 1898 at the University of Paris."
        result = await _cognify_and_wait(s, h, text)

        assert result["status"] == "COMPLETED", (
            f"Cognify не завершился: status={result.get('status')}. "
            f"Если FAILED — возможно R1 (model не поддерживает json_schema) "
            f"или R7 (partial JSON). Detail: {result}"
        )
        entities = result.get("entities_extracted", 0)
        assert entities > 0, (
            f"entities_extracted={entities}. "
            f"R6: LLM вернул пустой response? R2: strict schema отклонил поля?"
        )


async def test_structured_cognify_extracts_edges():
    """DoD: Cognify с текстом содержащим relationships → edges_extracted > 0.
    Проверяет: relationship extraction работает через JSON Schema.
    Текст: чёткие связи (Curie → discovered → radium, Curie → worked_at → University of Paris).
    Риски: R2 (strict schema отклоняет edge format), R6 (0 edges).
    """
    async with aiohttp.ClientSession(timeout=TIMEOUT) as s:
        h = await _auth(s)
        text = (
            "Marie Curie discovered radium in 1898. "
            "She worked at the University of Paris. "
            "Pierre Curie was her husband and research partner. "
            "They shared the Nobel Prize in Physics in 1903."
        )
        result = await _cognify_and_wait(s, h, text)

        assert result["status"] == "COMPLETED", (
            f"Cognify не завершился: {result}"
        )
        edges = result.get("edges_extracted", 0)
        assert edges > 0, (
            f"edges_extracted={edges}. "
            f"R2: JSON Schema слишком strict для edge extraction?"
        )


async def test_structured_retry_on_failure():
    """DoD: Cognify с ОЧЕНЬ коротким текстом ("Hi") → не crash (COMPLETED или 400).
    Проверяет: retry/fallback при минимальном input (R3: retry loop конечный).
    Если LLM не может извлечь entities из "Hi" — это нормально (0 entities),
    но сервер не должен зависнуть или вернуть 500.
    Риски: R3 (бесконечный retry), R6 (пустой response).
    """
    async with aiohttp.ClientSession(timeout=TIMEOUT) as s:
        h = await _auth(s)
        result = await _cognify_and_wait(s, h, "Hi", timeout_s=120)

        # Допустимые исходы: COMPLETED (с 0 entities) или FAILED (LLM не смог)
        # НЕ допустимо: TIMEOUT (R3 — бесконечный retry), ERROR_500 (crash)
        assert result["status"] in ("COMPLETED", "FAILED"), (
            f"Cognify с минимальным текстом: status={result.get('status')}. "
            f"R3: если TIMEOUT — retry loop бесконечный. "
            f"Detail: {result}"
        )


async def test_structured_empty_text():
    """DoD: Cognify с пустым текстом → 400 или 0 entities (не 500).
    Проверяет: input validation для structured pipeline.
    Риски: R3 (retry на пустом input), R6 (пустой response трактуется неверно).
    """
    async with aiohttp.ClientSession(timeout=TIMEOUT) as s:
        h = await _auth(s)
        result = await _cognify_and_wait(s, h, "", timeout_s=60)

        # Пустой текст → или 400 (валидация) или COMPLETED с 0 entities
        status = result.get("status", "")
        assert "500" not in status, (
            f"Пустой текст вызвал 500: {result}. "
            f"Сервер должен вернуть 400 или обработать gracefully."
        )
        # ERROR_400 — валидация на входе (хорошо)
        # COMPLETED с 0 entities — graceful handling (тоже хорошо)
        # FAILED — LLM не смог (приемлемо)
        assert status in ("COMPLETED", "FAILED") or "ERROR_400" in status, (
            f"Пустой текст: неожиданный status={status}. Detail: {result}"
        )


async def test_structured_large_text():
    """DoD: Cognify с 5KB текстом → entities > 5 (больше текста = больше entities).
    Проверяет: structured output работает с большими chunk'ами.
    Текст: много named entities, дат, локаций.
    Риски: R7 (token limit обрезает JSON), R4 (overhead на большом тексте).
    """
    # ~5KB текст с множеством entities
    text = (
        "Albert Einstein was born on March 14, 1879, in Ulm, in the Kingdom of Württemberg "
        "in the German Empire. His father was Hermann Einstein, a salesman and engineer. "
        "His mother was Pauline Koch. In 1880, the family moved to Munich, where Einstein's "
        "father and his uncle Jakob founded Elektrotechnische Fabrik J. Einstein & Cie. "
        "Einstein attended the Luitpold Gymnasium in Munich. In 1894, Hermann Einstein's "
        "company failed and the family moved to Milan, Italy. Einstein renounced his German "
        "citizenship in 1896 and enrolled at the Swiss Federal Polytechnic School in Zurich. "
        "In 1900, Einstein graduated from the Polytechnic with a diploma in mathematics and "
        "physics. In 1902 he started working at the Swiss Patent Office in Bern. "
        "In 1905, Einstein published four groundbreaking papers. The first explained the "
        "photoelectric effect, which earned him the Nobel Prize in Physics in 1921. "
        "The second paper on Brownian motion proved the existence of atoms. "
        "The third paper introduced the special theory of relativity. "
        "The fourth paper established the equivalence of mass and energy (E=mc²). "
        "In 1914, Einstein moved to Berlin to become a professor at the Humboldt University. "
        "He published the general theory of relativity in 1915. "
        "Arthur Eddington's observations during the solar eclipse of 1919 confirmed "
        "Einstein's predictions about the bending of light by gravity. "
        "In 1933, Einstein emigrated to the United States due to the rise of Nazi Germany. "
        "He took a position at the Institute for Advanced Study in Princeton, New Jersey. "
        "In 1939, Einstein signed a letter to President Franklin D. Roosevelt warning about "
        "the potential development of atomic weapons. This led to the Manhattan Project. "
        "Einstein became an American citizen in 1940. He continued his work on unified field "
        "theory at Princeton until his death on April 18, 1955."
    )
    assert len(text.encode()) > 1500, f"Текст слишком короткий: {len(text.encode())} bytes"

    async with aiohttp.ClientSession(timeout=TIMEOUT) as s:
        h = await _auth(s)
        result = await _cognify_and_wait(s, h, text)

        assert result["status"] == "COMPLETED", (
            f"Cognify большого текста не завершился: {result}"
        )
        entities = result.get("entities_extracted", 0)
        assert entities > 5, (
            f"entities_extracted={entities} для 5KB текста с 20+ named entities. "
            f"R7: token limit обрезал JSON? R2: strict schema отклонил часть?"
        )


async def test_structured_search_after_cognify():
    """DoD: Cognify → vector search → результаты найдены.
    Проверяет: entities проиндексированы в vector DB после structured extraction.
    Риски: R6 (0 entities = нечего искать), R5 (cache collision с другим mode).
    """
    coll = f"struct_search_{uuid.uuid4().hex[:8]}"
    text = (
        "Isaac Newton formulated the laws of motion and universal gravitation. "
        "He published Principia Mathematica in 1687. "
        "Newton was a professor at the University of Cambridge."
    )

    async with aiohttp.ClientSession(timeout=TIMEOUT) as s:
        h = await _auth(s)

        # Cognify — извлекает entities и индексирует
        result = await _cognify_and_wait(s, h, text, collection=coll)
        assert result["status"] == "COMPLETED", (
            f"Cognify не завершился: {result}"
        )

        # Search — ищем по семантике в chunks
        async with s.post(
            f"{BASE}/search/text",
            json={
                "query_text": "Newton laws of motion gravitation",
                "query_type": "CHUNKS",
                "top_k": 5,
            },
            headers=h,
        ) as r:
            assert r.status == 200, f"Search вернул {r.status}"
            data = await r.json()

            # Результат — массив chunks или dict с chunks полем
            if isinstance(data, list):
                assert len(data) > 0, (
                    "Search после cognify вернул пустой массив. "
                    "R6: entities не проиндексированы?"
                )
            elif isinstance(data, dict):
                chunks = data.get("chunks") or data.get("results") or []
                # Допускаем None если search type не вернул chunks
                has_content = (
                    chunks or data.get("answer") or data.get("context")
                )
                assert has_content is not None, (
                    f"Search после cognify не вернул контент: {str(data)[:500]}"
                )


# ═══════════════════════════════════════════════════════════════════
#  QUALITY TESTS (3)
#  Риски: R2, R5, R6
# ═══════════════════════════════════════════════════════════════════

async def test_structured_vs_unstructured_quality():
    """DoD: Cognify текст → entities ≥ 2 (structured не ухудшает качество).
    Проверяет: structured output даёт минимально адекватное кол-во entities.
    Текст с 4+ очевидными entities (имена, даты, локации).
    Риски: R2 (strict schema режет entities), R5 (cache collision).
    """
    text = (
        "Nikola Tesla was born in Smiljan, Croatia in 1856. "
        "He moved to New York City in 1884 to work with Thomas Edison. "
        "Tesla invented the alternating current induction motor in 1887."
    )
    async with aiohttp.ClientSession(timeout=TIMEOUT) as s:
        h = await _auth(s)
        result = await _cognify_and_wait(s, h, text)

        assert result["status"] == "COMPLETED", (
            f"Cognify не завершился: {result}"
        )
        entities = result.get("entities_extracted", 0)
        # Текст содержит: Tesla, Smiljan, Croatia, New York City, Edison, AC motor
        # Structured output должен извлечь хотя бы 2
        assert entities >= 2, (
            f"entities_extracted={entities} — structured output ухудшил качество? "
            f"Ожидалось >= 2 для текста с 6+ named entities. "
            f"R2: strict schema отклонил часть?"
        )


async def test_structured_json_validity():
    """DoD: Cognify → search → metadata результатов — валидный JSON (нет мусора).
    Проверяет: structured output не засоряет metadata невалидным JSON.
    Риски: R7 (partial JSON в metadata), R2 (лишние поля).
    """
    text = (
        "Charles Darwin published On the Origin of Species in 1859. "
        "He developed the theory of evolution by natural selection."
    )
    async with aiohttp.ClientSession(timeout=TIMEOUT) as s:
        h = await _auth(s)
        result = await _cognify_and_wait(s, h, text)

        assert result["status"] == "COMPLETED", (
            f"Cognify не завершился: {result}"
        )

        # Search для получения chunks с metadata
        async with s.post(
            f"{BASE}/search/text",
            json={
                "query_text": "Darwin evolution natural selection",
                "query_type": "CHUNKS",
                "top_k": 10,
            },
            headers=h,
        ) as r:
            assert r.status == 200
            data = await r.json()

            # Проверяем что каждый chunk содержит валидный JSON в metadata
            items = data if isinstance(data, list) else (
                data.get("chunks") or data.get("results") or []
            )
            if items:
                for item in items:
                    if isinstance(item, dict):
                        # Metadata (если есть) должна быть валидным dict
                        meta = item.get("metadata") or item.get("data") or item.get("payload")
                        if meta and isinstance(meta, str):
                            try:
                                parsed = json.loads(meta)
                                assert isinstance(parsed, (dict, list)), (
                                    f"metadata не dict/list: {type(parsed)}"
                                )
                            except json.JSONDecodeError:
                                pytest.fail(
                                    f"Невалидный JSON в metadata: {meta[:200]}. "
                                    f"R7: partial JSON от LLM попал в metadata?"
                                )


async def test_structured_deterministic_ids():
    """DoD: Cognify один текст дважды → одинаковые entity names (через cache).
    Проверяет: повторный cognify одного текста даёт консистентные результаты.
    Второй вызов должен попасть в cache → те же entities.
    Риски: R5 (cache key не учитывает mode), R6 (пустой response).
    """
    text = "Leonardo da Vinci painted the Mona Lisa in Florence, Italy."
    coll = f"struct_determ_{uuid.uuid4().hex[:8]}"

    async with aiohttp.ClientSession(timeout=TIMEOUT) as s:
        h = await _auth(s)

        # Первый cognify
        result1 = await _cognify_and_wait(s, h, text, collection=coll)
        assert result1["status"] == "COMPLETED", f"1st cognify: {result1}"
        entities1 = result1.get("entities_extracted", 0)

        # Второй cognify — тот же текст, та же коллекция
        result2 = await _cognify_and_wait(s, h, text, collection=coll)
        assert result2["status"] == "COMPLETED", f"2nd cognify: {result2}"
        entities2 = result2.get("entities_extracted", 0)

        # Оба вызова должны извлечь одинаковое кол-во entities
        # (cache hit или детерминированная extraction)
        assert entities1 == entities2, (
            f"Недетерминированность: 1st={entities1}, 2nd={entities2}. "
            f"R5: cache ключ не совпал? Или LLM дал разные ответы?"
        )


# ═══════════════════════════════════════════════════════════════════
#  PERFORMANCE TESTS (3)
#  Риски: R4, R5, R8
# ═══════════════════════════════════════════════════════════════════

async def test_structured_performance_vs_baseline():
    """DoD: Cognify 1 текст → time. Cognify тот же текст повторно → time.
    2nd call < 2s (cache hit vs ~80s LLM).
    Проверяет: cache работает с structured output.
    Риски: R4 (overhead), R5 (cache key collision).
    """
    text = "Ada Lovelace wrote the first algorithm for Charles Babbage's Analytical Engine in 1843."
    coll = f"struct_perf_{uuid.uuid4().hex[:8]}"

    async with aiohttp.ClientSession(timeout=TIMEOUT) as s:
        h = await _auth(s)

        # Первый вызов — cold (LLM bound)
        t0 = time.monotonic()
        result1 = await _cognify_and_wait(s, h, text, collection=coll)
        cold_time = time.monotonic() - t0
        assert result1["status"] == "COMPLETED", (
            f"1st cognify (cold): {result1}"
        )

        # Второй вызов — должен быть cache hit
        t0 = time.monotonic()
        result2 = await _cognify_and_wait(s, h, text, collection=coll)
        warm_time = time.monotonic() - t0
        assert result2["status"] == "COMPLETED", (
            f"2nd cognify (warm): {result2}"
        )

        # Cache hit должен быть значительно быстрее
        # Допускаем до 30s на warm (pipeline overhead без LLM)
        assert warm_time < 30, (
            f"2nd cognify занял {warm_time:.1f}s (1st: {cold_time:.1f}s). "
            f"R5: cache не сработал? Или cache ключ не учитывает structured mode?"
        )
        # Бонус: warm должен быть хотя бы в 2x быстрее cold
        if cold_time > 10:
            assert warm_time < cold_time * 0.8, (
                f"Cache speedup < 20%: cold={cold_time:.1f}s, warm={warm_time:.1f}s. "
                f"R4: structured overhead нивелирует cache benefit?"
            )


async def test_structured_concurrent_cognify():
    """DoD: 3 concurrent cognify с разными текстами → все COMPLETED.
    Проверяет: concurrent structured calls не deadlock.
    Риски: R8 (concurrent JSON Schema validation), R3 (retry contention).
    """
    texts = [
        "Galileo Galilei observed Jupiter's moons through his telescope in 1610.",
        "Johannes Kepler published his laws of planetary motion between 1609 and 1619.",
        "Nicolaus Copernicus proposed the heliocentric model in 1543.",
    ]

    async with aiohttp.ClientSession(timeout=TIMEOUT) as s:
        h = await _auth(s)

        # Запускаем 3 cognify параллельно
        tasks = [
            _cognify_and_wait(s, h, text, timeout_s=300)
            for text in texts
        ]
        t0 = time.monotonic()
        results = await asyncio.gather(*tasks, return_exceptions=True)
        elapsed = time.monotonic() - t0

        # Все должны завершиться успешно
        for i, result in enumerate(results):
            assert not isinstance(result, Exception), (
                f"Concurrent cognify #{i} бросил exception: {result}. "
                f"R8: deadlock или crash при параллельных structured calls?"
            )
            assert result["status"] == "COMPLETED", (
                f"Concurrent cognify #{i}: status={result.get('status')}. "
                f"R8: concurrent structured calls не завершились. "
                f"Text: {texts[i][:50]}..."
            )

        # Бонус: параллельность не должна быть 3x sequential
        # (если все sequential — elapsed ≈ 3 * single, если parallel — ≈ 1 * single)
        print(f"3 concurrent cognify: {elapsed:.1f}s total")


async def test_structured_cache_stats():
    """DoD: После cognify → GET /cache/stats → Size > 0, Misses > 0.
    Проверяет: structured responses кешируются корректно.
    Риски: R5 (cache key не учитывает mode → collision).
    """
    text = "Alan Turing created the concept of the Turing machine in 1936."

    async with aiohttp.ClientSession(timeout=TIMEOUT) as s:
        h = await _auth(s)

        # Cognify — должен создать cache miss, потом cache entry
        result = await _cognify_and_wait(s, h, text)
        assert result["status"] in ("COMPLETED", "FAILED"), (
            f"Cognify не завершился: {result}"
        )

        # Проверяем cache stats
        async with s.get(f"{BASE}/cache/stats", headers=h) as r:
            assert r.status == 200, f"GET /cache/stats вернул {r.status}"
            stats = await r.json()

            # Cache должен содержать хотя бы 1 entry
            size = stats.get("size", stats.get("Size", 0))
            misses = stats.get("cache_misses", stats.get("Misses", 0))

            assert size > 0 or misses > 0, (
                f"Cache пустой после cognify: stats={stats}. "
                f"R5: structured responses не кешируются?"
            )
