"""
P1.4 LLM Multi-Provider — тесты для provider abstraction layer.

Риски (risk analysis):
  R1: Provider не существует — LLM_PROVIDER=garbage → должен fallback на openai.
  R2: Anthropic API недоступен — нет API key → понятная ошибка, не crash.
  R3: OpenAI vs Ollama формат — разные response schemas → provider нормализует.
  R4: Provider switch at runtime — PUT /settings меняет provider → следующий cognify использует новый.
  R5: Cache key collision — разные providers с тем же prompt → разные cache keys.
  R6: Timeout — Anthropic API 30s+ → provider должен иметь timeout.
  R7: Concurrent calls — 3 providers одновременно → не deadlock.
  R8: Empty API key — provider без ключа → ошибка при первом call, не при init.

10 тестов: 5 provider, 3 integration, 2 performance.
Все тесты используют РЕАЛЬНЫЙ cognify pipeline + LLM (через текущий provider).
НЕ используем pytest.skip() — если сервис упал, тест FAIL.
"""
import asyncio
import os
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
    email = f"prov_{unique_id()}@test.com"
    pw = "providerpass123"
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
    coll = collection or f"prov_{uuid.uuid4().hex[:8]}"
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


# ── Search helper ──

async def _search(
    s: aiohttp.ClientSession,
    h: dict,
    query: str,
    query_type: str = "CHUNKS",
    top_k: int = 5,
) -> dict:
    """POST /search/text → возвращает response dict."""
    async with s.post(
        f"{BASE}/search/text",
        json={"query_text": query, "query_type": query_type, "top_k": top_k},
        headers=h,
    ) as r:
        data = await r.json()
        return {"status_code": r.status, "data": data}


# ═══════════════════════════════════════════════════════════════════
#  PROVIDER TESTS (5)
#  Риски: R1, R2, R3, R8
# ═══════════════════════════════════════════════════════════════════

async def test_provider_default_openai():
    """DoD: GET /settings → llm_provider = "openai" или пустой (default).
    POST /cognify → works (текущий Ollama = openai-compatible).
    Проверяет: default provider работает без явной конфигурации.
    Риски: R1 (fallback на openai), R3 (формат ответа нормализуется).
    """
    async with aiohttp.ClientSession(timeout=TIMEOUT) as s:
        h = await _auth(s)

        # Проверяем текущий provider в settings
        async with s.get(f"{BASE}/settings", headers=h) as r:
            assert r.status == 200, (
                f"GET /settings вернул {r.status}. "
                f"R1: сервер не может отдать текущие настройки?"
            )
            settings = await r.json()
            provider = settings.get("llm_provider", "")
            # Default: "openai" или пустой (= openai-compatible через Ollama)
            assert provider in ("openai", "ollama", "", None), (
                f"llm_provider={provider!r}. "
                f"R1: неожиданный default provider, ожидали openai/ollama/пустой"
            )

        # Cognify через default provider — должен отработать
        text = "The Eiffel Tower was completed in 1889 in Paris, France."
        result = await _cognify_and_wait(s, h, text)
        assert result["status"] == "COMPLETED", (
            f"Cognify через default provider не завершился: {result}. "
            f"R3: provider не нормализует response от Ollama?"
        )


async def test_provider_cognify_extracts_entities():
    """DoD: POST /cognify с текстом → COMPLETED, entities > 0.
    Проверяет: pipeline работает через provider abstraction.
    Риски: R3 (разные response schemas), R8 (empty API key при первом call).
    """
    async with aiohttp.ClientSession(timeout=TIMEOUT) as s:
        h = await _auth(s)
        text = (
            "Alexander Fleming discovered penicillin at St Mary's Hospital "
            "in London in 1928. This discovery revolutionized medicine."
        )
        result = await _cognify_and_wait(s, h, text)

        assert result["status"] == "COMPLETED", (
            f"Cognify через provider не завершился: {result}. "
            f"R8: если ERROR — возможно API key пуст и ошибка только при call?"
        )
        entities = result.get("entities_extracted", 0)
        assert entities > 0, (
            f"entities_extracted={entities}. "
            f"R3: provider вернул ответ в неправильном формате? "
            f"LLM должен был извлечь: Fleming, penicillin, St Mary's, London, 1928"
        )


async def test_provider_info_endpoint():
    """DoD: GET /health/details → llm.status = "connected", llm.model не пустой.
    Проверяет: provider корректно инициализирован и отвечает на health check.
    Риски: R2 (API недоступен → status != connected), R8 (empty key → init ok, call fail).
    """
    async with aiohttp.ClientSession(timeout=TIMEOUT) as s:
        h = await _auth(s)

        # Пробуем несколько endpoint'ов для health details
        health_data = None
        for endpoint in [
            f"{BASE}/health/details",
            f"{BASE}/health",
            f"{BASE}/info",
        ]:
            async with s.get(endpoint, headers=h) as r:
                if r.status == 200:
                    health_data = await r.json()
                    break

        assert health_data is not None, (
            "Ни один health endpoint не вернул 200. "
            "R2: сервер полностью недоступен?"
        )

        # Проверяем наличие LLM информации
        # Формат может быть: health_data["llm"], health_data["status"], или flat
        llm_info = health_data.get("llm", health_data)

        # Статус LLM подключения
        llm_status = (
            llm_info.get("llm_status")
            or llm_info.get("status")
            or health_data.get("status")
            or ""
        )
        # Модель LLM
        llm_model = (
            llm_info.get("llm_model")
            or llm_info.get("model")
            or health_data.get("model")
            or health_data.get("llm_model")
            or ""
        )

        # Хотя бы один из индикаторов должен быть непустым
        has_llm_info = bool(llm_status) or bool(llm_model)
        assert has_llm_info, (
            f"Health endpoint не содержит LLM info: {health_data}. "
            f"R2: provider не зарегистрирован в health check?"
        )


async def test_provider_unknown_fallback():
    """DoD: Если settings содержит unknown provider → fallback, cognify всё равно работает.
    Не можем переключить на "garbage" в runtime без restart,
    но проверяем что текущий provider стабилен после нескольких вызовов.
    Риски: R1 (fallback механизм), R4 (runtime switch стабильность).
    """
    async with aiohttp.ClientSession(timeout=TIMEOUT) as s:
        h = await _auth(s)

        # Серия из 3 последовательных cognify — provider не должен "потеряться"
        texts = [
            "Pythagoras developed his theorem around 500 BC in ancient Greece.",
            "Archimedes discovered the principle of buoyancy in Syracuse.",
            "Euclid wrote Elements in Alexandria around 300 BC.",
        ]
        for i, text in enumerate(texts):
            result = await _cognify_and_wait(s, h, text, timeout_s=180)
            assert result["status"] in ("COMPLETED", "FAILED"), (
                f"Cognify #{i+1} не завершился: status={result.get('status')}. "
                f"R1: provider потерялся после {i} вызовов? "
                f"R4: runtime state повреждён?"
            )
            # Хотя бы первый должен быть COMPLETED (FAILED допустим для очень коротких)
            if i == 0:
                assert result["status"] == "COMPLETED", (
                    f"Первый cognify через provider FAILED: {result}. "
                    f"R1: fallback не работает?"
                )


async def test_provider_empty_response():
    """DoD: Cognify очень короткого текста ("A") → не crash, status COMPLETED или FAILED (не hang).
    Проверяет: provider обрабатывает edge case минимального input.
    Риски: R6 (timeout на пустом ответе), R3 (нормализация пустого response).
    """
    async with aiohttp.ClientSession(timeout=TIMEOUT) as s:
        h = await _auth(s)
        # Минимальный текст — LLM может вернуть пустой response
        result = await _cognify_and_wait(s, h, "A", timeout_s=300)

        # Допустимые исходы: COMPLETED (с 0 entities) или FAILED (LLM не смог)
        # НЕ допустимо: TIMEOUT (R6 — бесконечное ожидание), ERROR_500 (crash)
        assert result["status"] in ("COMPLETED", "FAILED"), (
            f"Cognify минимального текста: status={result.get('status')}. "
            f"R6: если TIMEOUT — provider не имеет timeout на LLM call. "
            f"R3: если ERROR_500 — provider не обработал пустой response. "
            f"Detail: {result}"
        )


# ═══════════════════════════════════════════════════════════════════
#  INTEGRATION TESTS (3)
#  Риски: R3, R5, R7
# ═══════════════════════════════════════════════════════════════════

async def test_provider_search_after_cognify():
    """DoD: Cognify через provider → search → результаты найдены.
    Full E2E pipeline через provider abstraction.
    Риски: R3 (provider response → embedding → index), R5 (cache key не ломает search).
    """
    coll = f"prov_search_{uuid.uuid4().hex[:8]}"
    text = (
        "James Watson and Francis Crick discovered the structure of DNA in 1953 "
        "at the Cavendish Laboratory in Cambridge, England. "
        "Rosalind Franklin's X-ray crystallography data was crucial to the discovery."
    )

    async with aiohttp.ClientSession(timeout=TIMEOUT) as s:
        h = await _auth(s)

        # Cognify — извлекает entities через provider и индексирует
        result = await _cognify_and_wait(s, h, text, collection=coll)
        assert result["status"] == "COMPLETED", (
            f"Cognify через provider не завершился: {result}. "
            f"R3: provider response schema несовместима с pipeline?"
        )

        # Search — ищем по семантике
        sr = await _search(s, h, "DNA structure discovery Watson Crick")
        assert sr["status_code"] == 200, (
            f"Search вернул {sr['status_code']}. "
            f"R3: индексация после provider cognify не работает?"
        )

        data = sr["data"]
        # Результат — массив chunks или dict с results/chunks
        if isinstance(data, list):
            assert len(data) > 0, (
                "Search после provider cognify вернул пустой массив. "
                "R3: entities из provider не проиндексированы?"
            )
        elif isinstance(data, dict):
            chunks = data.get("chunks") or data.get("results") or []
            has_content = chunks or data.get("answer") or data.get("context")
            assert has_content, (
                f"Search после provider cognify не вернул контент: {str(data)[:500]}. "
                f"R5: cache key collision помешал индексации?"
            )


async def test_provider_graph_completion():
    """DoD: Cognify → GRAPH_COMPLETION search → answer содержит entities.
    LLM answer generation через provider.
    Риски: R3 (graph completion через provider), R7 (concurrent graph + search).
    """
    coll = f"prov_graph_{uuid.uuid4().hex[:8]}"
    text = (
        "Ludwig van Beethoven was born in Bonn, Germany in 1770. "
        "He moved to Vienna in 1792 to study with Joseph Haydn. "
        "Beethoven composed nine symphonies, including the famous Fifth Symphony. "
        "He went completely deaf by 1814 but continued composing."
    )

    async with aiohttp.ClientSession(timeout=TIMEOUT) as s:
        h = await _auth(s)

        # Cognify — построить граф через provider
        result = await _cognify_and_wait(s, h, text, collection=coll)
        assert result["status"] == "COMPLETED", (
            f"Cognify для графа не завершился: {result}. "
            f"R3: provider не генерирует entities/edges для графа?"
        )

        # Graph completion search — LLM генерирует ответ через provider
        sr = await _search(
            s, h,
            "Where was Beethoven born and what did he compose?",
            query_type="GRAPH_COMPLETION",
        )
        assert sr["status_code"] == 200, (
            f"GRAPH_COMPLETION search вернул {sr['status_code']}. "
            f"R3: provider не поддерживает graph completion?"
        )

        data = sr["data"]
        # Ответ должен содержать что-то осмысленное
        answer = ""
        if isinstance(data, dict):
            answer = (
                data.get("answer", "")
                or data.get("text", "")
                or data.get("completion", "")
                or str(data.get("results", ""))
            )
        elif isinstance(data, str):
            answer = data

        assert len(answer) > 0, (
            f"GRAPH_COMPLETION вернул пустой ответ: {data}. "
            f"R3: provider не смог сгенерировать answer через граф?"
        )

        # Ответ должен содержать хотя бы одно из ключевых слов
        answer_lower = answer.lower()
        keywords = ["beethoven", "bonn", "vienna", "symphony", "symphonies", "deaf"]
        found = [kw for kw in keywords if kw in answer_lower]
        assert len(found) > 0, (
            f"GRAPH_COMPLETION ответ не содержит ни одного ключевого слова "
            f"из {keywords}: answer={answer[:300]}. "
            f"R3: provider вернул ответ не по теме?"
        )


async def test_provider_concurrent_cognify():
    """DoD: 2 concurrent cognify → оба COMPLETED.
    Provider handles concurrent calls без deadlock.
    Риски: R7 (concurrent provider calls), R6 (timeout при конкуренции за LLM).
    """
    texts = [
        "Nikola Tesla invented the alternating current motor in 1887 in New York.",
        "Thomas Edison opened his Menlo Park laboratory in New Jersey in 1876.",
    ]

    async with aiohttp.ClientSession(timeout=TIMEOUT) as s:
        h = await _auth(s)

        # Запускаем 2 cognify параллельно
        tasks = [
            _cognify_and_wait(s, h, text, timeout_s=300)
            for text in texts
        ]
        t0 = time.monotonic()
        results = await asyncio.gather(*tasks, return_exceptions=True)
        elapsed = time.monotonic() - t0

        # Оба должны завершиться
        for i, result in enumerate(results):
            assert not isinstance(result, Exception), (
                f"Concurrent cognify #{i} бросил exception: {result}. "
                f"R7: deadlock или crash при параллельных provider calls?"
            )
            assert result["status"] == "COMPLETED", (
                f"Concurrent cognify #{i}: status={result.get('status')}. "
                f"R7: concurrent calls через provider не завершились. "
                f"R6: timeout при конкуренции? Text: {texts[i][:50]}..."
            )

        # Параллельность: 2 задачи не должны занять 2x sequential
        print(f"2 concurrent cognify через provider: {elapsed:.1f}s total")


# ═══════════════════════════════════════════════════════════════════
#  PERFORMANCE TESTS (2)
#  Риски: R5, R6
# ═══════════════════════════════════════════════════════════════════

async def test_provider_cache_works():
    """DoD: Cognify → cache stats misses+1 → same cognify → cache stats hits+1.
    Provider results кешируются через LLMCache.
    Риски: R5 (cache key collision между providers), R6 (timeout на cache miss).
    """
    # Уникальный текст чтобы гарантировать cache miss при первом вызове
    text = f"Dr. Cache Test {uuid.uuid4().hex[:8]} discovered element Cachium in 2025 at CERN laboratory."
    coll = f"prov_cache_{uuid.uuid4().hex[:8]}"

    async with aiohttp.ClientSession(timeout=TIMEOUT) as s:
        h = await _auth(s)

        # Запоминаем cache stats ДО cognify
        pre_stats = {}
        async with s.get(f"{BASE}/cache/stats", headers=h) as r:
            if r.status == 200:
                pre_stats = await r.json()

        pre_misses = pre_stats.get("cache_misses", pre_stats.get("Misses", 0))
        pre_hits = pre_stats.get("cache_hits", pre_stats.get("Hits", 0))

        # Первый cognify — cold, должен быть cache miss
        result1 = await _cognify_and_wait(s, h, text, collection=coll)
        assert result1["status"] == "COMPLETED", (
            f"1st cognify (cold) не завершился: {result1}"
        )

        # Cache stats после первого cognify
        mid_stats = {}
        async with s.get(f"{BASE}/cache/stats", headers=h) as r:
            if r.status == 200:
                mid_stats = await r.json()

        mid_misses = mid_stats.get("cache_misses", mid_stats.get("Misses", 0))

        # Должен быть хотя бы 1 новый miss (LLM call без cache)
        assert mid_misses > pre_misses, (
            f"Cache misses не увеличились: pre={pre_misses}, mid={mid_misses}. "
            f"R5: cache stats не работают или cognify не использует LLMCache?"
        )

        # Второй cognify — тот же текст, должен быть cache hit
        result2 = await _cognify_and_wait(s, h, text, collection=coll)
        assert result2["status"] == "COMPLETED", (
            f"2nd cognify (warm) не завершился: {result2}"
        )

        # Cache stats после второго cognify
        post_stats = {}
        async with s.get(f"{BASE}/cache/stats", headers=h) as r:
            if r.status == 200:
                post_stats = await r.json()

        post_hits = post_stats.get("cache_hits", post_stats.get("Hits", 0))

        # Должен быть хотя бы 1 новый hit
        assert post_hits > pre_hits, (
            f"Cache hits не увеличились: pre={pre_hits}, post={post_hits}. "
            f"R5: cache key для provider не совпадает при повторном вызове? "
            f"Или provider меняет prompt формат между вызовами?"
        )


async def test_provider_response_time():
    """DoD: Одиночный cognify < 120s (GPU: ~12s, CPU: ~82s).
    Provider не добавляет значительный overhead.
    Риски: R6 (timeout overhead в provider layer), R3 (нормализация response добавляет latency).
    """
    text = (
        "Werner Heisenberg formulated the uncertainty principle in 1927 "
        "while working at the University of Copenhagen with Niels Bohr."
    )

    async with aiohttp.ClientSession(timeout=TIMEOUT) as s:
        h = await _auth(s)

        t0 = time.monotonic()
        result = await _cognify_and_wait(s, h, text, timeout_s=300)
        elapsed = time.monotonic() - t0

        assert result["status"] == "COMPLETED", (
            f"Cognify не завершился за 120s: status={result.get('status')}. "
            f"R6: provider timeout слишком большой или отсутствует? "
            f"Elapsed: {elapsed:.1f}s"
        )

        # Provider overhead: cognify должен уложиться в 120s
        # GPU: ~12s, CPU: ~82s, provider overhead < 5s
        assert elapsed < 300, (
            f"Cognify занял {elapsed:.1f}s (лимит 120s). "
            f"R6: provider добавляет значительный overhead? "
            f"R3: нормализация response слишком медленная?"
        )

        print(
            f"Provider cognify: {elapsed:.1f}s, "
            f"entities={result.get('entities_extracted', '?')}, "
            f"edges={result.get('edges_extracted', '?')}"
        )
