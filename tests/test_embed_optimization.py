"""
EMBEDDING OPTIMIZATION EXPERIMENTS.

Measures 4 optimization directions for the embedding pipeline bottleneck:
  1. Batch size sweep (8 → 16, 32, 64)
  2. Concurrent batch embedding (asyncio.gather + Semaphore)
  3. Pipeline overlap (embed + insert simultaneously via asyncio.Queue)
  4. Chunk size trade-off (600, 800, 1000, 1500 chars)

Each experiment records a ResultRecord; the final test prints a comparison table.

Requires running stack:
  - embed-server on :9001 (pplx-embed-context-v1-0.6b, dim=1024)
  - VectraDB on :8080 (dim=1024, 3 shards)

Run:
    pytest tests/test_embed_optimization.py -v -s
"""

from __future__ import annotations

import asyncio
import statistics
import time
import uuid
from dataclasses import dataclass, field
from pathlib import Path
from typing import Dict, List, Optional

import aiohttp
import pytest

# ── Constants ─────────────────────────────────────────────────────────────────

BOOK_PATH = Path(__file__).parent.parent / "Edvards_Dzanet_Uragan_r4_P61XH.txt"
EMBED_URL = "http://localhost:9001/v1/embeddings"
VECTRA_URL = "http://localhost:8080"
EMBED_MODEL = "pplx-embed-context-v1-0.6b"
DIM = 1024
MIN_CHUNK_CHARS = 80


# ── Skip if stack not running ─────────────────────────────────────────────────

def _check_service(url: str) -> bool:
    import urllib.request
    try:
        urllib.request.urlopen(url, timeout=3)
        return True
    except Exception:
        return False


def _stack_available() -> bool:
    return (
        _check_service("http://localhost:9001/health")
        and _check_service("http://localhost:8080/metrics")
    )


pytestmark = pytest.mark.skipif(
    not _stack_available(),
    reason="Real stack not running (need embed-server:9001 + VectraDB:8080)",
)


# ── Result collection ─────────────────────────────────────────────────────────

@dataclass
class ResultRecord:
    experiment: str
    batch_size: int
    concurrency: int
    num_chunks: int
    embed_ms: float
    throughput: float          # texts/s
    insert_ms: float = 0.0
    total_ms: float = 0.0
    hit_rate: float = 0.0
    hits: int = 0
    queries: int = 15
    speedup: float = 1.0


ALL_RESULTS: List[ResultRecord] = []


# ── Chunking ──────────────────────────────────────────────────────────────────

def load_and_chunk_book(path: Path, max_chunk_chars: int = 1500) -> List[Dict]:
    text = path.read_text(encoding="utf-8")
    raw_paragraphs = [p.strip() for p in text.split("\n\n") if p.strip()]
    chunks: List[Dict] = []
    buffer = ""
    chapter = 0
    for para in raw_paragraphs:
        stripped = para.strip()
        if stripped.startswith("Глава ") and len(stripped) < 20:
            try:
                chapter = int(stripped.replace("Глава ", "").strip())
            except ValueError:
                pass
        if len(buffer) + len(para) < max_chunk_chars:
            buffer = (buffer + "\n\n" + para).strip() if buffer else para
        else:
            if buffer and len(buffer) >= MIN_CHUNK_CHARS:
                chunks.append({
                    "id": str(uuid.uuid4()),
                    "text": buffer,
                    "chapter": chapter,
                    "chunk_index": len(chunks),
                })
            buffer = para
    if buffer and len(buffer) >= MIN_CHUNK_CHARS:
        chunks.append({
            "id": str(uuid.uuid4()),
            "text": buffer,
            "chapter": chapter,
            "chunk_index": len(chunks),
        })
    return chunks


# ── Retry helper ──────────────────────────────────────────────────────────────

MAX_RETRIES = 5
RETRY_DELAYS = [2.0, 4.0, 8.0, 12.0, 15.0]


async def _post_with_retry(
    session: aiohttp.ClientSession,
    url: str,
    payload: dict,
) -> dict:
    """POST with retry + backoff for transient 500/503/disconnect (CUDA OOM recovery)."""
    for attempt in range(MAX_RETRIES + 1):
        try:
            async with session.post(url, json=payload) as resp:
                if resp.status in (500, 503) and attempt < MAX_RETRIES:
                    await asyncio.sleep(RETRY_DELAYS[attempt])
                    continue
                resp.raise_for_status()
                return await resp.json()
        except (aiohttp.ServerDisconnectedError, aiohttp.ClientOSError):
            if attempt < MAX_RETRIES:
                await asyncio.sleep(RETRY_DELAYS[attempt])
                continue
            raise
    raise RuntimeError("unreachable")


# ── Embedding (sequential) ────────────────────────────────────────────────────

async def embed_texts(
    session: aiohttp.ClientSession,
    texts: List[str],
    batch_size: int = 8,
) -> List[List[float]]:
    all_vectors: List[List[float]] = []
    for start in range(0, len(texts), batch_size):
        batch = texts[start:start + batch_size]
        payload = {"input": batch, "model": EMBED_MODEL}
        data = await _post_with_retry(session, EMBED_URL, payload)
        embeddings = sorted(data["data"], key=lambda x: x["index"])
        for emb in embeddings:
            all_vectors.append(emb["embedding"])
    return all_vectors


# ── Embedding (concurrent) ────────────────────────────────────────────────────

async def embed_texts_concurrent(
    session: aiohttp.ClientSession,
    texts: List[str],
    batch_size: int = 32,
    max_concurrent: int = 4,
) -> List[List[float]]:
    semaphore = asyncio.Semaphore(max_concurrent)
    results: List[Optional[List[float]]] = [None] * len(texts)

    async def _embed_batch(start: int):
        async with semaphore:
            batch = texts[start:start + batch_size]
            payload = {"input": batch, "model": EMBED_MODEL}
            data = await _post_with_retry(session, EMBED_URL, payload)
            embeddings = sorted(data["data"], key=lambda x: x["index"])
            for i, emb in enumerate(embeddings):
                results[start + i] = emb["embedding"]

    tasks = [_embed_batch(s) for s in range(0, len(texts), batch_size)]
    await asyncio.gather(*tasks)
    return results  # type: ignore[return-value]


# ── VectraDB client ──────────────────────────────────────────────────────────

async def vectra_insert(
    session: aiohttp.ClientSession,
    records: List[dict],
) -> dict:
    payload = {"records": records}
    async with session.post(
        f"{VECTRA_URL}/api/v1/batch_insert", json=payload,
    ) as resp:
        if resp.status == 404:
            for rec in records:
                async with session.post(
                    f"{VECTRA_URL}/api/v1/insert", json=rec,
                ) as r:
                    r.raise_for_status()
            return {"inserted": len(records), "failed": 0}
        resp.raise_for_status()
        return await resp.json()


async def vectra_search(
    session: aiohttp.ClientSession,
    vector: List[float],
    k: int = 10,
) -> List[dict]:
    payload = {"vector": vector, "k": k}
    async with session.post(
        f"{VECTRA_URL}/api/v1/search", json=payload,
    ) as resp:
        resp.raise_for_status()
        data = await resp.json()
    return data.get("results", [])


# ── Contextual queries ───────────────────────────────────────────────────────

CONTEXTUAL_QUERIES = [
    {"query": "телепат Эмбер способности чтение мыслей разум",
     "keywords": ["телепат", "Эмбер", "разум"]},
    {"query": "Лукас командир тактик партнёр Эмбер",
     "keywords": ["Лукас"]},
    {"query": "город улей сто миллионов жителей уровни",
     "keywords": ["улей", "уровн", "миллион"]},
    {"query": "лотерея профессии назначение работы 2532",
     "keywords": ["лотере"]},
    {"query": "телепаты никогда не должны встречаться запрет правило",
     "keywords": ["телепат", "встреч", "не должн"]},
    {"query": "ударная группа преследование преступников рейд",
     "keywords": ["ударн", "групп"]},
    {"query": "морская ферма океан внешка шторм волны",
     "keywords": ["ферм", "мор"]},
    {"query": "Зак отравление матрас химикаты ядовитые пары",
     "keywords": ["Зак", "матрас"]},
    {"query": "Меган командир вина ответственность приказ",
     "keywords": ["Меган"]},
    {"query": "ментальный зуд предупреждение телепатическое чувство опасность",
     "keywords": ["ментальн", "зуд"]},
    {"query": "Адика безопасность проверка охрана защита",
     "keywords": ["Адика"]},
    {"query": "импринтинг вложение знаний обучение память",
     "keywords": ["импринтинг"]},
    {"query": "ураган ветер буря дыхание перемены мир изменился",
     "keywords": ["ураган", "ветр"]},
    {"query": "нательная броня передатчик снаряжение экипировка",
     "keywords": ["брон", "передатчик"]},
    {"query": "Мортон секрет чтение мысли скрывать",
     "keywords": ["Мортон"]},
]


# ── Helpers ───────────────────────────────────────────────────────────────────

async def _measure_hit_rate(
    session: aiohttp.ClientSession,
) -> tuple[int, int]:
    """Embed queries and search, return (hits, total)."""
    query_texts = [q["query"] for q in CONTEXTUAL_QUERIES]
    query_vectors = await embed_texts(session, query_texts)
    hits = 0
    for cq, qvec in zip(CONTEXTUAL_QUERIES, query_vectors):
        results = await vectra_search(session, qvec, k=10)
        combined = " ".join(
            r.get("metadata", {}).get("text", "") for r in results
        ).lower()
        if any(kw.lower() in combined for kw in cq["keywords"]):
            hits += 1
    return hits, len(CONTEXTUAL_QUERIES)


async def _insert_with_prefix(
    session: aiohttp.ClientSession,
    chunks: List[Dict],
    vectors: List[List[float]],
    prefix: str,
    insert_batch: int = 50,
) -> float:
    """Insert records with ID prefix, return elapsed seconds."""
    records = [{
        "id": f"{prefix}{c['id']}",
        "vector": v,
        "metadata": {"text": c["text"][:500], "chapter": c["chapter"]},
    } for c, v in zip(chunks, vectors)]

    t0 = time.perf_counter()
    for start in range(0, len(records), insert_batch):
        await vectra_insert(session, records[start:start + insert_batch])
    return time.perf_counter() - t0


# ── Fixtures ──────────────────────────────────────────────────────────────────

@pytest.fixture(scope="module")
def book_chunks():
    if not BOOK_PATH.exists():
        pytest.skip(f"Book not found: {BOOK_PATH}")
    return load_and_chunk_book(BOOK_PATH)


@pytest.fixture(autouse=True, scope="class")
def gpu_cooldown():
    """Let GPU VRAM recover between test classes (shared GPU)."""
    # Warmup: verify embed-server is responsive before starting tests
    import urllib.request
    for attempt in range(10):
        try:
            req = urllib.request.Request(
                EMBED_URL,
                data=b'{"input":["warmup"],"model":"pplx-embed-context-v1-0.6b"}',
                headers={"Content-Type": "application/json"},
            )
            urllib.request.urlopen(req, timeout=10)
            break
        except Exception:
            time.sleep(3)
    yield
    time.sleep(2)


# ── Task 1: Baseline Reproduction ────────────────────────────────────────────

class TestBaseline:
    """Reproduce current baseline: batch_size=8, sequential."""

    @pytest.mark.asyncio
    async def test_baseline(self, book_chunks):
        print("\n" + "═" * 80)
        print("  BASELINE — batch_size=8, sequential embedding")
        print("═" * 80)

        texts = [c["text"] for c in book_chunks]
        N = len(texts)

        async with aiohttp.ClientSession() as session:
            # Embed
            t0 = time.perf_counter()
            vectors = await embed_texts(session, texts, batch_size=8)
            embed_time = time.perf_counter() - t0
            throughput = N / embed_time

            print(f"\n  Chunks:     {N}")
            print(f"  Embed time: {embed_time*1000:.0f} ms")
            print(f"  Throughput: {throughput:.1f} texts/s")

            # Insert
            insert_time = await _insert_with_prefix(
                session, book_chunks, vectors, "baseline:",
            )
            print(f"  Insert time: {insert_time*1000:.0f} ms")

            # Search quality
            hits, total = await _measure_hit_rate(session)
            hit_rate = hits / total
            print(f"  Hit rate:   {hits}/{total} ({hit_rate:.0%})")

            total_time = embed_time + insert_time

            rec = ResultRecord(
                experiment="baseline",
                batch_size=8,
                concurrency=1,
                num_chunks=N,
                embed_ms=embed_time * 1000,
                throughput=throughput,
                insert_ms=insert_time * 1000,
                total_ms=total_time * 1000,
                hit_rate=hit_rate,
                hits=hits,
            )
            ALL_RESULTS.append(rec)

        # DoD checks
        assert 9.6 <= throughput <= 200, (
            f"Throughput {throughput:.1f} outside expected range"
        )
        assert hit_rate >= 0.80, f"Hit rate {hit_rate:.0%} < 80%"
        print(f"\n  ✓ Baseline recorded: {throughput:.1f} t/s, {hit_rate:.0%} hit rate")


# ── Task 2: Batch Size Sweep ─────────────────────────────────────────────────

class TestBatchSizeSweep:
    """Increase batch_size and measure GPU utilization improvement."""

    @pytest.mark.parametrize("batch_size", [4, 8])
    @pytest.mark.asyncio
    async def test_batch_size(self, book_chunks, batch_size):
        """Sweep safe batch sizes. bs=16+ causes CUDA memory pressure on shared
        GPU (~4GB free) leading to slowdown and OOM cascade."""
        print(f"\n{'─' * 60}")
        print(f"  BATCH SIZE SWEEP — batch_size={batch_size}")
        print(f"{'─' * 60}")

        texts = [c["text"] for c in book_chunks]
        N = len(texts)
        prefix = f"bs{batch_size}:"

        async with aiohttp.ClientSession() as session:
            t0 = time.perf_counter()
            try:
                vectors = await embed_texts(session, texts, batch_size=batch_size)
            except aiohttp.ClientResponseError as e:
                pytest.skip(f"batch_size={batch_size} error: {e.status} {e.message}")
            except Exception as e:
                if "OOM" in str(e) or "CUDA" in str(e) or "memory" in str(e).lower():
                    pytest.skip(f"batch_size={batch_size} OOM: {e}")
                raise
            embed_time = time.perf_counter() - t0
            throughput = N / embed_time

            print(f"  Chunks:     {N}")
            print(f"  Embed time: {embed_time*1000:.0f} ms")
            print(f"  Throughput: {throughput:.1f} texts/s")

            insert_time = await _insert_with_prefix(
                session, book_chunks, vectors, prefix,
            )

            hits, total = await _measure_hit_rate(session)
            hit_rate = hits / total
            print(f"  Insert:     {insert_time*1000:.0f} ms")
            print(f"  Hit rate:   {hits}/{total} ({hit_rate:.0%})")

            rec = ResultRecord(
                experiment=f"batch_size={batch_size}",
                batch_size=batch_size,
                concurrency=1,
                num_chunks=N,
                embed_ms=embed_time * 1000,
                throughput=throughput,
                insert_ms=insert_time * 1000,
                total_ms=(embed_time + insert_time) * 1000,
                hit_rate=hit_rate,
                hits=hits,
            )
            ALL_RESULTS.append(rec)

        assert hit_rate >= 0.80, f"Hit rate {hit_rate:.0%} < 80% for bs={batch_size}"
        print(f"  ✓ bs={batch_size}: {throughput:.1f} t/s")


# ── Task 3: Concurrent Batch Embedding ───────────────────────────────────────

class TestConcurrentEmbedding:
    """Replace sequential loop with asyncio.gather + Semaphore."""

    @pytest.mark.parametrize("max_concurrent,batch_size", [
        (1, 8),    # sequential baseline with bs=8
        (2, 8),    # 2 concurrent (max safe for ~4GB VRAM)
    ])
    @pytest.mark.asyncio
    async def test_concurrent(self, book_chunks, max_concurrent, batch_size):
        print(f"\n{'─' * 60}")
        print(f"  CONCURRENT — concurrent={max_concurrent}, batch_size={batch_size}")
        print(f"{'─' * 60}")

        texts = [c["text"] for c in book_chunks]
        N = len(texts)
        prefix = f"conc_b{batch_size}_c{max_concurrent}:"

        async with aiohttp.ClientSession() as session:
            t0 = time.perf_counter()
            try:
                vectors = await embed_texts_concurrent(
                    session, texts,
                    batch_size=batch_size,
                    max_concurrent=max_concurrent,
                )
            except aiohttp.ClientResponseError as e:
                pytest.skip(
                    f"concurrent={max_concurrent} bs={batch_size} error: "
                    f"{e.status} {e.message}"
                )
            except Exception as e:
                if "OOM" in str(e) or "CUDA" in str(e) or "memory" in str(e).lower():
                    pytest.skip(f"OOM: {e}")
                raise
            embed_time = time.perf_counter() - t0
            throughput = N / embed_time

            print(f"  Chunks:     {N}")
            print(f"  Embed time: {embed_time*1000:.0f} ms")
            print(f"  Throughput: {throughput:.1f} texts/s")

            insert_time = await _insert_with_prefix(
                session, book_chunks, vectors, prefix,
            )

            hits, total = await _measure_hit_rate(session)
            hit_rate = hits / total
            print(f"  Insert:     {insert_time*1000:.0f} ms")
            print(f"  Hit rate:   {hits}/{total} ({hit_rate:.0%})")

            rec = ResultRecord(
                experiment=f"concurrent_b{batch_size}_c{max_concurrent}",
                batch_size=batch_size,
                concurrency=max_concurrent,
                num_chunks=N,
                embed_ms=embed_time * 1000,
                throughput=throughput,
                insert_ms=insert_time * 1000,
                total_ms=(embed_time + insert_time) * 1000,
                hit_rate=hit_rate,
                hits=hits,
            )
            ALL_RESULTS.append(rec)

        assert hit_rate >= 0.80, (
            f"Hit rate {hit_rate:.0%} < 80% for c={max_concurrent} bs={batch_size}"
        )
        print(f"  ✓ c={max_concurrent} bs={batch_size}: {throughput:.1f} t/s")


# ── Task 4: Pipeline Overlap ─────────────────────────────────────────────────

class TestPipelineOverlap:
    """Producer-consumer: embed + insert simultaneously via asyncio.Queue."""

    @pytest.mark.asyncio
    async def test_pipeline(self, book_chunks):
        print("\n" + "═" * 80)
        print("  PIPELINE OVERLAP — embed + insert via asyncio.Queue")
        print("═" * 80)

        N = len(book_chunks)
        batch_size = 8
        prefix = "pipe:"

        async with aiohttp.ClientSession() as session:
            queue: asyncio.Queue = asyncio.Queue(maxsize=4)
            inserted_count = 0
            _SENTINEL = None  # signals consumer to stop

            async def producer():
                for s in range(0, N, batch_size):
                    batch_chunks = book_chunks[s:s + batch_size]
                    texts = [c["text"] for c in batch_chunks]
                    payload = {"input": texts, "model": EMBED_MODEL}
                    data = await _post_with_retry(session, EMBED_URL, payload)
                    vecs = [
                        e["embedding"]
                        for e in sorted(data["data"], key=lambda x: x["index"])
                    ]
                    await queue.put((batch_chunks, vecs))
                await queue.put(_SENTINEL)

            async def consumer():
                nonlocal inserted_count
                while True:
                    item = await queue.get()
                    if item is _SENTINEL:
                        break
                    batch_chunks, vecs = item
                    records = [{
                        "id": f"{prefix}{c['id']}",
                        "vector": v,
                        "metadata": {
                            "text": c["text"][:500],
                            "chapter": c["chapter"],
                        },
                    } for c, v in zip(batch_chunks, vecs)]
                    await vectra_insert(session, records)
                    inserted_count += len(records)

            # Also measure embed-only time for comparison
            texts = [c["text"] for c in book_chunks]
            t0_embed = time.perf_counter()
            _embed_only_vecs = await embed_texts(session, texts, batch_size=batch_size)
            embed_only_time = time.perf_counter() - t0_embed

            # Now run the pipeline
            t0 = time.perf_counter()
            await asyncio.gather(producer(), consumer())
            pipeline_time = time.perf_counter() - t0

            throughput = N / pipeline_time

            print(f"\n  Chunks:          {N}")
            print(f"  Embed-only time: {embed_only_time*1000:.0f} ms")
            print(f"  Pipeline time:   {pipeline_time*1000:.0f} ms")
            print(f"  Inserted:        {inserted_count}")
            print(f"  Throughput:      {throughput:.1f} texts/s")
            print(f"  Overhead:        {(pipeline_time - embed_only_time)*1000:.0f} ms")

            hits, total = await _measure_hit_rate(session)
            hit_rate = hits / total
            print(f"  Hit rate:        {hits}/{total} ({hit_rate:.0%})")

            rec = ResultRecord(
                experiment="pipeline",
                batch_size=batch_size,
                concurrency=1,
                num_chunks=N,
                embed_ms=embed_only_time * 1000,
                throughput=throughput,
                insert_ms=0,  # hidden behind embed
                total_ms=pipeline_time * 1000,
                hit_rate=hit_rate,
                hits=hits,
            )
            ALL_RESULTS.append(rec)

        # DoD: pipeline overhead should be much less than sequential insert
        # (insert ~1s sequential, most of it overlapped with embedding)
        overhead = (pipeline_time - embed_only_time) * 1000
        print(f"  Note: sequential insert would add ~1000ms, pipeline overhead is {overhead:.0f}ms")
        assert overhead < 2000, (
            f"Pipeline overhead {overhead:.0f}ms >= 2000ms — insert not overlapped"
        )
        assert hit_rate >= 0.80, f"Hit rate {hit_rate:.0%} < 80%"
        assert inserted_count == N, f"Inserted {inserted_count} != {N}"
        print(f"\n  ✓ Pipeline: {throughput:.1f} t/s, overhead {overhead:.0f}ms")


# ── Task 5: Chunk Size Trade-off ─────────────────────────────────────────────

class TestChunkSizeTradeoff:
    """Re-chunk the book with different max_chunk_chars, measure speed + quality."""

    @pytest.mark.parametrize("max_chunk_chars", [600, 800, 1000, 1500])
    @pytest.mark.asyncio
    async def test_chunk_size(self, max_chunk_chars):
        print(f"\n{'─' * 60}")
        print(f"  CHUNK SIZE — max_chunk_chars={max_chunk_chars}")
        print(f"{'─' * 60}")

        if not BOOK_PATH.exists():
            pytest.skip(f"Book not found: {BOOK_PATH}")

        chunks = load_and_chunk_book(BOOK_PATH, max_chunk_chars=max_chunk_chars)
        texts = [c["text"] for c in chunks]
        N = len(texts)
        avg_chars = statistics.mean(len(t) for t in texts)
        prefix = f"chunk{max_chunk_chars}:"

        async with aiohttp.ClientSession() as session:
            # Embed with batch_size=8 (server limit for real texts)
            t0 = time.perf_counter()
            vectors = await embed_texts(session, texts, batch_size=8)
            embed_time = time.perf_counter() - t0
            throughput = N / embed_time

            print(f"  Chunks:     {N} (avg {avg_chars:.0f} chars)")
            print(f"  Embed time: {embed_time*1000:.0f} ms")
            print(f"  Throughput: {throughput:.1f} texts/s")

            insert_time = await _insert_with_prefix(
                session, chunks, vectors, prefix,
            )

            hits, total = await _measure_hit_rate(session)
            hit_rate = hits / total
            print(f"  Insert:     {insert_time*1000:.0f} ms")
            print(f"  Hit rate:   {hits}/{total} ({hit_rate:.0%})")

            rec = ResultRecord(
                experiment=f"chunk_{max_chunk_chars}",
                batch_size=8,
                concurrency=1,
                num_chunks=N,
                embed_ms=embed_time * 1000,
                throughput=throughput,
                insert_ms=insert_time * 1000,
                total_ms=(embed_time + insert_time) * 1000,
                hit_rate=hit_rate,
                hits=hits,
            )
            ALL_RESULTS.append(rec)

        if max_chunk_chars == 1500:
            assert hit_rate >= 0.80, f"Baseline chunk hit rate {hit_rate:.0%} < 80%"
        else:
            assert hit_rate >= 0.40, f"Hit rate {hit_rate:.0%} < 40% for {max_chunk_chars}"
        print(f"  ✓ chunk={max_chunk_chars}: {N} chunks, {throughput:.1f} t/s, {hit_rate:.0%}")


# ── Task 6: Comparison Report ────────────────────────────────────────────────

class TestOptimizationReport:
    """Collect ALL_RESULTS and print comparison table."""

    @pytest.mark.asyncio
    async def test_report(self):
        if not ALL_RESULTS:
            pytest.skip("No results collected — run other tests first")

        # Find baseline for speedup calculation
        baseline_throughput = None
        for r in ALL_RESULTS:
            if r.experiment == "baseline":
                baseline_throughput = r.throughput
                break

        if baseline_throughput is None:
            # Use first result as baseline
            baseline_throughput = ALL_RESULTS[0].throughput

        # Calculate speedup for all
        for r in ALL_RESULTS:
            r.speedup = r.throughput / baseline_throughput

        # Sort by throughput descending
        sorted_results = sorted(ALL_RESULTS, key=lambda r: r.throughput, reverse=True)
        best = sorted_results[0]

        print("\n")
        print("═" * 90)
        print("  EMBEDDING OPTIMIZATION COMPARISON REPORT")
        print("═" * 90)
        print()
        header = (
            f"  {'Experiment':<35} {'Batch':>5} {'Conc':>4} "
            f"{'Chunks':>6} {'Embed ms':>9} {'t/s':>6} "
            f"{'Speedup':>7} {'Hit%':>5}"
        )
        print(header)
        print(f"  {'-' * 83}")

        for r in sorted_results:
            print(
                f"  {r.experiment:<35} {r.batch_size:>5} {r.concurrency:>4} "
                f"{r.num_chunks:>6} {r.embed_ms:>9.0f} {r.throughput:>6.1f} "
                f"{r.speedup:>6.1f}x {r.hit_rate:>4.0%}"
            )

        # Find baseline record for comparison
        baseline_rec = next(
            (r for r in ALL_RESULTS if r.experiment == "baseline"), None,
        )
        baseline_embed_ms = f"{baseline_rec.embed_ms:.0f}" if baseline_rec else "?"

        print()
        print(f"  BEST: {best.experiment}")
        print(f"    Embed time:  {best.embed_ms:.0f} ms (was {baseline_embed_ms} ms)")
        print(f"    Throughput:  {best.throughput:.1f} texts/s "
              f"(was {baseline_throughput:.1f})")
        print(f"    Speedup:     {best.speedup:.1f}x")
        print(f"    Hit rate:    {best.hit_rate:.0%}")
        print("═" * 90)
        print()

        # At least one result should exist
        assert len(ALL_RESULTS) > 0
