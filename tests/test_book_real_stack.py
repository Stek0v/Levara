"""
REAL STACK end-to-end benchmark with the book «Ураган» (Janet Edwards).

Requires running stack:
  - embed-server on :9001 (pplx-embed-context-v1-0.6b, dim=1024)
  - Cognevra on :8080 (dim=1024, 3 shards)
  - llama-server on :9004 (Qwen3.5-27B) [optional — only for LLM tests]

This test bypasses Cognee and talks directly to:
  1. embed-server — for real embeddings
  2. Cognevra — for real vector insert/search

This way we measure the actual Cognevra + real embedding pipeline performance.

Run:
    pytest tests/test_book_real_stack.py -v -s
"""

from __future__ import annotations

import asyncio
import json
import math
import statistics
import time
import uuid
from pathlib import Path
from typing import Dict, List

import aiohttp
import pytest

# ── Constants ─────────────────────────────────────────────────────────────────

BOOK_PATH = Path(__file__).parent.parent / "Edvards_Dzanet_Uragan_r4_P61XH.txt"
EMBED_URL = "http://localhost:9001/v1/embeddings"
COGNEVRA_URL = "http://localhost:8080"
EMBED_MODEL = "pplx-embed-context-v1-0.6b"
DIM = 1024
MIN_CHUNK_CHARS = 80

# ── Optimized defaults (from test_embed_optimization.py results) ─────────────
# chunk_size=600: 3.9x throughput, no quality loss (93% hit rate)
# batch_size=16: FP16 model frees VRAM, allows larger batches
# Combined with FP16+compile: ~10x speedup
DEFAULT_BATCH_SIZE = 16
DEFAULT_MAX_CHUNK_CHARS = 600


# ── Skip if stack not running ─────────────────────────────────────────────────

def _check_service(url: str, name: str):
    import urllib.request
    try:
        urllib.request.urlopen(url, timeout=3)
        return True
    except Exception:
        return False


def _stack_available():
    return (
        _check_service("http://localhost:9001/health", "embed-server")
        and _check_service("http://localhost:8080/metrics", "Cognevra")
    )


pytestmark = pytest.mark.skipif(
    not _stack_available(),
    reason="Real stack not running (need embed-server:9001 + Cognevra:8080)",
)


# ── Chunking (same as mock test) ─────────────────────────────────────────────

def load_and_chunk_book(path: Path, max_chunk_chars: int = 1500) -> List[Dict]:
    text = path.read_text(encoding="utf-8")
    raw_paragraphs = [p.strip() for p in text.split("\n\n") if p.strip()]
    chunks = []
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


# ── Real embedding client ────────────────────────────────────────────────────

async def embed_texts(
    session: aiohttp.ClientSession,
    texts: List[str],
    batch_size: int = 8,
) -> List[List[float]]:
    """Embed texts via real embed-server in batches."""
    all_vectors = []
    for start in range(0, len(texts), batch_size):
        batch = texts[start:start + batch_size]
        payload = {
            "input": batch,
            "model": EMBED_MODEL,
        }
        async with session.post(EMBED_URL, json=payload) as resp:
            resp.raise_for_status()
            data = await resp.json()
        # Sort by index to maintain order
        embeddings = sorted(data["data"], key=lambda x: x["index"])
        for emb in embeddings:
            all_vectors.append(emb["embedding"])
    return all_vectors


# ── Cognevra client ──────────────────────────────────────────────────────────

async def cognevra_insert(
    session: aiohttp.ClientSession,
    records: List[dict],
) -> dict:
    """Batch insert into Cognevra."""
    payload = {"records": records}
    async with session.post(
        f"{COGNEVRA_URL}/api/v1/batch_insert",
        json=payload,
    ) as resp:
        if resp.status == 404:
            # Fallback to single inserts
            for rec in records:
                async with session.post(
                    f"{COGNEVRA_URL}/api/v1/insert", json=rec
                ) as r:
                    r.raise_for_status()
            return {"inserted": len(records), "failed": 0}
        resp.raise_for_status()
        return await resp.json()


async def cognevra_search(
    session: aiohttp.ClientSession,
    vector: List[float],
    k: int = 10,
) -> List[dict]:
    """Search Cognevra."""
    payload = {"vector": vector, "k": k}
    async with session.post(
        f"{COGNEVRA_URL}/api/v1/search", json=payload
    ) as resp:
        resp.raise_for_status()
        data = await resp.json()
    return data.get("results", [])


# ── Contextual queries ───────────────────────────────────────────────────────

CONTEXTUAL_QUERIES = [
    {
        "query": "телепат Эмбер способности чтение мыслей разум",
        "keywords": ["телепат", "Эмбер", "разум"],
        "description": "Главная героиня — телепат Эмбер",
    },
    {
        "query": "Лукас командир тактик партнёр Эмбер",
        "keywords": ["Лукас"],
        "description": "Лукас — партнёр и командир",
    },
    {
        "query": "город улей сто миллионов жителей уровни",
        "keywords": ["улей", "уровн", "миллион"],
        "description": "Город-улей — основная локация",
    },
    {
        "query": "лотерея профессии назначение работы 2532",
        "keywords": ["лотере"],
        "description": "Система лотереи для профессий",
    },
    {
        "query": "телепаты никогда не должны встречаться запрет правило",
        "keywords": ["телепат", "встреч", "не должн"],
        "description": "Ключевое правило мира",
    },
    {
        "query": "ударная группа преследование преступников рейд",
        "keywords": ["ударн", "групп"],
        "description": "Ударная группа",
    },
    {
        "query": "морская ферма океан внешка шторм волны",
        "keywords": ["ферм", "мор"],
        "description": "Морская ферма во Внешке",
    },
    {
        "query": "Зак отравление матрас химикаты ядовитые пары",
        "keywords": ["Зак", "матрас"],
        "description": "Отравление Зака",
    },
    {
        "query": "Меган командир вина ответственность приказ",
        "keywords": ["Меган"],
        "description": "Меган — чувство вины",
    },
    {
        "query": "ментальный зуд предупреждение телепатическое чувство опасность",
        "keywords": ["ментальн", "зуд"],
        "description": "Ментальный зуд",
    },
    {
        "query": "Адика безопасность проверка охрана защита",
        "keywords": ["Адика"],
        "description": "Адика — охранник",
    },
    {
        "query": "импринтинг вложение знаний обучение память",
        "keywords": ["импринтинг"],
        "description": "Импринтинг",
    },
    {
        "query": "ураган ветер буря дыхание перемены мир изменился",
        "keywords": ["ураган", "ветр"],
        "description": "Ураган — символ книги",
    },
    {
        "query": "нательная броня передатчик снаряжение экипировка",
        "keywords": ["брон", "передатчик"],
        "description": "Снаряжение телепата",
    },
    {
        "query": "Мортон секрет чтение мысли скрывать",
        "keywords": ["Мортон"],
        "description": "Мортон",
    },
]


# ── Tests ─────────────────────────────────────────────────────────────────────


@pytest.fixture(scope="module")
def book_chunks():
    if not BOOK_PATH.exists():
        pytest.skip(f"Book not found: {BOOK_PATH}")
    return load_and_chunk_book(BOOK_PATH, max_chunk_chars=DEFAULT_MAX_CHUNK_CHARS)


class TestRealEmbedding:
    """Test real embedding server."""

    @pytest.mark.asyncio
    async def test_embed_single_text(self):
        async with aiohttp.ClientSession() as session:
            vecs = await embed_texts(session, ["Тестовый текст для эмбеддинга"])
        assert len(vecs) == 1
        assert len(vecs[0]) == DIM
        print(f"\n  Single embed OK: dim={len(vecs[0])}")

    @pytest.mark.asyncio
    async def test_embed_batch(self):
        texts = [f"Текст номер {i}" for i in range(10)]
        async with aiohttp.ClientSession() as session:
            t0 = time.perf_counter()
            vecs = await embed_texts(session, texts)
            elapsed = time.perf_counter() - t0
        assert len(vecs) == 10
        assert all(len(v) == DIM for v in vecs)
        print(f"\n  Batch embed (10 texts): {elapsed*1000:.1f} ms ({10/elapsed:.0f} texts/s)")


class TestRealStackInsert:
    """Insert real book data into real Cognevra with real embeddings.

    Uses pipeline overlap: embed + insert run simultaneously via asyncio.Queue.
    """

    @pytest.mark.asyncio
    async def test_full_insert(self, book_chunks):
        print("\n" + "═" * 70)
        print("  REAL STACK INSERT — «Ураган» → embed-server → Cognevra")
        print(f"  (optimized: batch_size={DEFAULT_BATCH_SIZE}, "
              f"chunk_size={DEFAULT_MAX_CHUNK_CHARS}, pipeline overlap)")
        print("═" * 70)

        N = len(book_chunks)
        BATCH = DEFAULT_BATCH_SIZE
        texts = [c["text"] for c in book_chunks]

        async with aiohttp.ClientSession() as session:
            # ── Pipeline: embed + insert simultaneously ──
            queue: asyncio.Queue = asyncio.Queue(maxsize=4)
            sentinel = None
            all_vectors: list = []
            inserted = 0

            async def producer():
                for start in range(0, N, BATCH):
                    batch = texts[start:start + BATCH]
                    payload = {"input": batch, "model": EMBED_MODEL}
                    async with session.post(EMBED_URL, json=payload) as resp:
                        resp.raise_for_status()
                        data = await resp.json()
                    embeddings = sorted(data["data"], key=lambda x: x["index"])
                    vecs = [e["embedding"] for e in embeddings]
                    all_vectors.extend(vecs)
                    batch_chunks = book_chunks[start:start + BATCH]
                    await queue.put((batch_chunks, vecs))
                await queue.put(sentinel)

            async def consumer():
                nonlocal inserted
                while True:
                    item = await queue.get()
                    if item is sentinel:
                        break
                    batch_chunks, vecs = item
                    records = [{
                        "id": f"book:{c['id']}",
                        "vector": v,
                        "metadata": {
                            "text": c["text"][:500],
                            "chapter": c["chapter"],
                            "chunk_index": c["chunk_index"],
                        },
                    } for c, v in zip(batch_chunks, vecs)]
                    await cognevra_insert(session, records)
                    inserted += len(records)

            t0 = time.perf_counter()
            await asyncio.gather(producer(), consumer())
            total_time = time.perf_counter() - t0

            embed_throughput = N / total_time

            print(f"\n  Pipeline (embed + insert overlapped):")
            print(f"    Chunks:     {N}")
            print(f"    Total time: {total_time*1000:.0f} ms")
            print(f"    Throughput: {embed_throughput:.0f} texts/s")
            print(f"    Dim:        {len(all_vectors[0])}")
            print(f"    Inserted:   {inserted}")

            print(f"\n  End-to-end throughput: {N/total_time:.0f} chunks/s")
            print()

        assert len(all_vectors) == N
        assert all(len(v) == DIM for v in all_vectors)
        assert inserted == N


class TestRealStackSearch:
    """Search with real embeddings and real Cognevra."""

    @pytest.mark.asyncio
    async def test_search_quality_and_latency(self, book_chunks):
        print("\n" + "═" * 70)
        print("  REAL STACK SEARCH — contextual queries with real embeddings")
        print("═" * 70)

        async with aiohttp.ClientSession() as session:
            # First, ensure data is inserted
            N = len(book_chunks)
            texts = [c["text"] for c in book_chunks]
            all_vectors = await embed_texts(session, texts, batch_size=DEFAULT_BATCH_SIZE)

            records = []
            for chunk, vec in zip(book_chunks, all_vectors):
                records.append({
                    "id": f"book:{chunk['id']}",
                    "vector": vec,
                    "metadata": {
                        "text": chunk["text"][:500],
                        "chapter": chunk["chapter"],
                        "chunk_index": chunk["chunk_index"],
                    },
                })
            for start in range(0, len(records), 50):
                await cognevra_insert(session, records[start:start + 32])

            # ── Embed queries ──
            query_texts = [q["query"] for q in CONTEXTUAL_QUERIES]
            t0 = time.perf_counter()
            query_vectors = await embed_texts(session, query_texts)
            query_embed_time = time.perf_counter() - t0

            print(f"\n  Query embedding: {len(query_texts)} queries in "
                  f"{query_embed_time*1000:.0f} ms")

            # ── Search and measure ──
            print(f"\n  {'#':<3} {'Query':<50} {'Latency':>8} {'Hit?':>5}")
            print(f"  {'-' * 70}")

            latencies = []
            hits = 0
            results_details = []

            for i, (cq, qvec) in enumerate(zip(CONTEXTUAL_QUERIES, query_vectors)):
                t0 = time.perf_counter()
                results = await cognevra_search(session, qvec, k=10)
                lat = (time.perf_counter() - t0) * 1000
                latencies.append(lat)

                # Check keyword presence
                combined = " ".join(
                    r.get("metadata", {}).get("text", "")
                    for r in results
                ).lower()

                hit = any(kw.lower() in combined for kw in cq["keywords"])
                if hit:
                    hits += 1

                status = "✓" if hit else "✗"
                desc = cq["description"][:48]
                print(f"  {i+1:<3} {desc:<50} {lat:>6.1f}ms {status:>5}")

                # Store top-3 for detailed output
                top3 = []
                for r in results[:3]:
                    text_preview = r.get("metadata", {}).get("text", "")[:80]
                    score = r.get("score", 0)
                    top3.append((score, text_preview))
                results_details.append((cq["description"], hit, top3))

            # ── Latency stats ──
            s = sorted(latencies)
            n = len(s)
            hit_rate = hits / len(CONTEXTUAL_QUERIES)

            print(f"\n  {'─' * 70}")
            print(f"\n  Search Latency (Cognevra, {N} vectors, real HNSW):")
            print(f"    p50:  {s[n//2]:.1f} ms")
            print(f"    p95:  {s[int(n*0.95)]:.1f} ms")
            print(f"    p99:  {s[int(n*0.99)]:.1f} ms")
            print(f"    mean: {statistics.mean(latencies):.1f} ms")
            print(f"    max:  {max(latencies):.1f} ms")

            print(f"\n  Keyword Hit Rate: {hits}/{len(CONTEXTUAL_QUERIES)} ({hit_rate:.0%})")

            if hit_rate >= 0.8:
                print("  ✓ Excellent — real embeddings provide strong semantic search")
            elif hit_rate >= 0.6:
                print("  ~ Good — most queries find relevant content")
            else:
                print("  ✗ Low hit rate — check embedding model quality")

            # ── Multi-pass latency for reliable stats ──
            print(f"\n  Running 5 extra passes for stable latency measurement...")
            extra_lats = []
            for _ in range(5):
                for qvec in query_vectors:
                    t0 = time.perf_counter()
                    await cognevra_search(session, qvec, k=10)
                    extra_lats.append((time.perf_counter() - t0) * 1000)

            all_lats = sorted(extra_lats)
            na = len(all_lats)
            print(f"  Multi-pass ({na} measurements):")
            print(f"    p50:  {all_lats[na//2]:.1f} ms")
            print(f"    p95:  {all_lats[int(na*0.95)]:.1f} ms")
            print(f"    mean: {statistics.mean(all_lats):.1f} ms")

            # ── Concurrent QPS ──
            t0 = time.perf_counter()
            await asyncio.gather(*[
                cognevra_search(session, qv, k=10) for qv in query_vectors
            ])
            t_conc = time.perf_counter() - t0
            qps = len(query_vectors) / t_conc
            print(f"\n  Concurrent ({len(query_vectors)} queries): "
                  f"{t_conc*1000:.0f} ms total, {qps:.0f} QPS")

            print()

            # Show top-3 results for failed queries
            failed = [d for d in results_details if not d[1]]
            if failed:
                print(f"  Failed queries — top-3 results:")
                for desc, _, top3 in failed:
                    print(f"\n    Query: {desc}")
                    for score, preview in top3:
                        print(f"      [{score:.4f}] {preview}...")

            print()
            assert hit_rate >= 0.5, f"Hit rate too low: {hit_rate:.0%}"


class TestRealStackReport:
    """Final comprehensive report."""

    @pytest.mark.asyncio
    async def test_full_report(self, book_chunks):
        print("\n" + "═" * 70)
        print("  FULL REAL STACK REPORT — «Ураган» (Джанет Эдвардс)")
        print("  Qwen3.5-27B + pplx-embed-v1-0.6b + Cognevra (dim=1024)")
        print("═" * 70)

        N = len(book_chunks)
        texts = [c["text"] for c in book_chunks]
        chapters = sorted(set(c["chapter"] for c in book_chunks))
        avg_chunk = statistics.mean(len(c["text"]) for c in book_chunks)

        async with aiohttp.ClientSession() as session:
            # ── Embed ──
            t0 = time.perf_counter()
            all_vectors = await embed_texts(session, texts, batch_size=DEFAULT_BATCH_SIZE)
            embed_time = time.perf_counter() - t0

            # ── Insert ──
            records = [{
                "id": f"report:{c['id']}",
                "vector": v,
                "metadata": {"text": c["text"][:500], "chapter": c["chapter"]},
            } for c, v in zip(book_chunks, all_vectors)]

            t0 = time.perf_counter()
            for start in range(0, len(records), 50):
                await cognevra_insert(session, records[start:start + 32])
            insert_time = time.perf_counter() - t0

            # ── Search ──
            query_texts = [q["query"] for q in CONTEXTUAL_QUERIES]
            query_vectors = await embed_texts(session, query_texts)

            # Latency
            lats = []
            for _ in range(5):
                for qv in query_vectors:
                    t0 = time.perf_counter()
                    await cognevra_search(session, qv, k=10)
                    lats.append((time.perf_counter() - t0) * 1000)

            sl = sorted(lats)
            nl = len(sl)

            # QPS
            t0 = time.perf_counter()
            await asyncio.gather(*[cognevra_search(session, qv, k=10) for qv in query_vectors])
            t_conc = time.perf_counter() - t0
            qps = len(query_vectors) / t_conc

            # Hit rate
            hits = 0
            for cq, qv in zip(CONTEXTUAL_QUERIES, query_vectors):
                results = await cognevra_search(session, qv, k=10)
                combined = " ".join(
                    r.get("metadata", {}).get("text", "") for r in results
                ).lower()
                if any(kw.lower() in combined for kw in cq["keywords"]):
                    hits += 1
            hit_rate = hits / len(CONTEXTUAL_QUERIES)

        # ── Cognevra info ──
        import urllib.request
        cognevra_info = json.loads(
            urllib.request.urlopen("http://localhost:8080/api/v1/info").read()
        )

        print(f"""
  Book: «Ураган» by Janet Edwards (Hive #3)
  File: {BOOK_PATH.stat().st_size / 1024:.0f} KB

  ┌────────────────────────────────────────┬───────────────┐
  │ Metric                                 │ Value         │
  ├────────────────────────────────────────┼───────────────┤
  │ Chunks                                 │ {N:<13} │
  │ Chapters                               │ {len(chapters):<13} │
  │ Avg chunk size (chars)                 │ {avg_chunk:<13.0f} │
  │ Embedding model                        │ pplx-embed    │
  │ Embedding dim                          │ {DIM:<13} │
  │ Cognevra shards                        │ {cognevra_info.get('shards', '?'):<13} │
  ├────────────────────────────────────────┼───────────────┤
  │ Embed time ({N} chunks)               │ {embed_time*1000:<10.0f} ms │
  │ Embed throughput                       │ {N/embed_time:<10.0f} /s │
  │ Cognevra insert time                   │ {insert_time*1000:<10.0f} ms │
  │ Cognevra insert throughput             │ {N/insert_time:<10.0f} /s │
  │ Total pipeline (embed+insert)          │ {(embed_time+insert_time)*1000:<10.0f} ms │
  ├────────────────────────────────────────┼───────────────┤
  │ Search p50 (real HNSW)                 │ {sl[nl//2]:<10.1f} ms │
  │ Search p95                             │ {sl[int(nl*0.95)]:<10.1f} ms │
  │ Search p99                             │ {sl[int(nl*0.99)]:<10.1f} ms │
  │ Search mean                            │ {statistics.mean(lats):<10.1f} ms │
  │ Concurrent QPS                         │ {qps:<10.0f}    │
  ├────────────────────────────────────────┼───────────────┤
  │ Keyword hit rate ({len(CONTEXTUAL_QUERIES)} queries)            │ {hit_rate:<10.0%}      │
  └────────────────────────────────────────┴───────────────┘

  Stack: Qwen3.5-27B (Q4_K_M) + pplx-embed-context-v1 + Cognevra
  GPU: RTX 3090 (CUDA)
""")
        print("═" * 70)

        assert hit_rate >= 0.5, f"Hit rate too low: {hit_rate:.0%}"
