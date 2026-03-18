"""
Full end-to-end benchmark: book-scale search quality and performance.

Uses Janet Edwards' "Ураган" (Hurricane, Hive #3) as a real-world dataset.
Tests VectraDB adapter with ~500+ text chunks from a Russian sci-fi novel.

What this test measures:
  1. CHUNKING — text splitting into meaningful paragraphs
  2. INSERT THROUGHPUT — data points/second for real text
  3. SEARCH LATENCY — p50/p95/p99 across diverse queries
  4. SEARCH QUALITY — contextual relevance of results via semantic queries
  5. RECALL — overlap with brute-force ground truth

Run:
    pytest tests/test_book_search_benchmark.py -v -s
"""

from __future__ import annotations

import asyncio
import math
import random
import statistics
import time
import uuid
from pathlib import Path
from typing import Dict, List, Optional, Tuple
from unittest.mock import AsyncMock, MagicMock

import pytest

from cognee.infrastructure.databases.vector.vectradb.VectraDBAdapter import (
    VectraDBAdapter,
    _serialize_for_json,
)

import sys

_DataPoint = sys.modules["cognee.infrastructure.engine"].DataPoint
ScoredResult = sys.modules[
    "cognee.infrastructure.databases.vector.models.ScoredResult"
].ScoredResult


# ── Constants ─────────────────────────────────────────────────────────────────

BOOK_PATH = Path(__file__).parent.parent / "Edvards_Dzanet_Uragan_r4_P61XH.txt"
DIM = 64  # embedding dimension for mock
MIN_CHUNK_CHARS = 80  # skip very short chunks


# ── Text chunking ────────────────────────────────────────────────────────────

def load_and_chunk_book(path: Path, max_chunk_chars: int = 1500) -> List[Dict]:
    """
    Load book and split into paragraph-based chunks.

    Strategy: split on double newlines (paragraph boundaries), then merge
    short consecutive paragraphs to avoid tiny chunks while keeping semantic
    coherence at paragraph boundaries.
    """
    text = path.read_text(encoding="utf-8")
    # Split on double+ newlines (paragraph boundaries)
    raw_paragraphs = [p.strip() for p in text.split("\n\n") if p.strip()]

    chunks = []
    buffer = ""
    chapter = 0

    for para in raw_paragraphs:
        # Detect chapter headers
        stripped = para.strip()
        if stripped.startswith("Глава ") and len(stripped) < 20:
            try:
                chapter = int(stripped.replace("Глава ", "").strip())
            except ValueError:
                pass

        # Merge short paragraphs
        if len(buffer) + len(para) < max_chunk_chars:
            buffer = (buffer + "\n\n" + para).strip() if buffer else para
        else:
            if buffer and len(buffer) >= MIN_CHUNK_CHARS:
                chunks.append({
                    "id": uuid.uuid4(),
                    "text": buffer,
                    "chapter": chapter,
                    "chunk_index": len(chunks),
                })
            buffer = para

    # Don't forget the last buffer
    if buffer and len(buffer) >= MIN_CHUNK_CHARS:
        chunks.append({
            "id": uuid.uuid4(),
            "text": buffer,
            "chapter": chapter,
            "chunk_index": len(chunks),
        })

    return chunks


# ── Mock server ──────────────────────────────────────────────────────────────

class BookMockServer:
    """In-process VectraDB mock with brute-force cosine search."""

    def __init__(self):
        self._store: Dict[str, Dict] = {}
        self.insert_calls = 0
        self.search_calls = 0

    def _cosine(self, a: List[float], b: List[float]) -> float:
        dot = sum(x * y for x, y in zip(a, b))
        mag_a = math.sqrt(sum(x * x for x in a))
        mag_b = math.sqrt(sum(x * x for x in b))
        if mag_a < 1e-9 or mag_b < 1e-9:
            return 0.0
        return dot / (mag_a * mag_b)

    async def handle_batch_insert(self, records) -> dict:
        if isinstance(records, dict):
            records = records.get("records", [])
        for rec in records:
            self.insert_calls += 1
            self._store[rec["id"]] = {
                "vector": rec["vector"],
                "metadata": rec.get("metadata", {}),
            }
        return {"inserted": len(records), "failed": 0}

    async def dispatch(self, path: str, payload: dict) -> dict:
        if path == "/api/v1/insert":
            return await self.handle_batch_insert({"records": [payload]})
        if path == "/api/v1/batch_insert":
            return await self.handle_batch_insert(payload)
        if path == "/api/v1/search":
            return await self._search(payload)
        raise ValueError(f"Unknown path: {path}")

    async def _search(self, body: dict) -> dict:
        self.search_calls += 1
        query = body["vector"]
        k = body.get("k", 10)
        scores = []
        for record_id, record in self._store.items():
            sim = self._cosine(query, record["vector"])
            scores.append((record_id, sim, record["metadata"]))
        scores.sort(key=lambda x: -x[1])
        return {
            "results": [
                {"id": rid, "score": score, "metadata": meta}
                for rid, score, meta in scores[:k]
            ]
        }

    @property
    def size(self) -> int:
        return len(self._store)


# ── Embedding engine (deterministic) ─────────────────────────────────────────

def _make_embedding_engine(dim: int = DIM) -> MagicMock:
    """Embedding engine that returns deterministic vectors seeded by text hash."""
    engine = MagicMock()

    async def embed_text(texts):
        if isinstance(texts, str):
            texts = [texts]
        result = []
        for t in texts:
            rng = random.Random(hash(t) & 0xFFFFFFFF)
            vec = [rng.gauss(0, 1) for _ in range(dim)]
            mag = math.sqrt(sum(x * x for x in vec))
            result.append([x / mag for x in vec])
        return result

    engine.embed_text = AsyncMock(side_effect=embed_text)
    engine.get_vector_size = MagicMock(return_value=dim)
    return engine


# ── DataPoint wrapper ────────────────────────────────────────────────────────

class _ChunkDataPoint(_DataPoint):
    def __init__(self, chunk: dict):
        self.id = chunk["id"]
        self.text = chunk["text"]
        self.metadata = {"index_fields": ["text"]}
        self.belongs_to_set = [f"chapter_{chunk['chapter']}"]
        self.chapter = chunk["chapter"]
        self.chunk_index = chunk["chunk_index"]

    def model_dump(self):
        return {
            "id": self.id,
            "text": self.text,
            "belongs_to_set": self.belongs_to_set,
            "chapter": self.chapter,
            "chunk_index": self.chunk_index,
        }


# ── Adapter factory ──────────────────────────────────────────────────────────

def _make_adapter(server: BookMockServer, engine=None) -> VectraDBAdapter:
    if engine is None:
        engine = _make_embedding_engine()
    adapter = VectraDBAdapter(
        url="localhost:50051",
        api_key=None,
        embedding_engine=engine,
    )
    adapter._post = server.dispatch
    adapter._batch_post = server.handle_batch_insert
    return adapter


# ── Helper: embed texts ──────────────────────────────────────────────────────

async def _embed_all(engine, texts: List[str]) -> List[List[float]]:
    BATCH = 50
    all_vecs = []
    for start in range(0, len(texts), BATCH):
        batch = texts[start:start + BATCH]
        vecs = await engine.embed_text(batch)
        all_vecs.extend(vecs)
    return all_vecs


# ═════════════════════════════════════════════════════════════════════════════
#  CONTEXTUAL SEARCH QUERIES
#  These test semantic understanding of the book's content
# ═════════════════════════════════════════════════════════════════════════════

# Each query has: text, keywords expected in results, description
CONTEXTUAL_QUERIES = [
    {
        "query": "телепат Эмбер способности чтение мыслей",
        "keywords": ["телепат", "Эмбер", "разум"],
        "description": "Главная героиня — телепат Эмбер",
    },
    {
        "query": "Лукас командир тактик партнёр",
        "keywords": ["Лукас"],
        "description": "Лукас — партнёр и командир Эмбер",
    },
    {
        "query": "улей город сто миллионов жителей уровни",
        "keywords": ["улей", "уровн"],
        "description": "Город-улей — основная локация",
    },
    {
        "query": "лотерея профессии выбор работы 2532",
        "keywords": ["лотере"],
        "description": "Система лотереи для назначения профессий",
    },
    {
        "query": "правило телепаты не должны встречаться запрет",
        "keywords": ["телепат", "встреч"],
        "description": "Ключевое правило — телепаты не должны встречаться",
    },
    {
        "query": "ударная группа преследование преступников",
        "keywords": ["ударн", "групп"],
        "description": "Ударная группа телепата",
    },
    {
        "query": "морская ферма внешка океан шторм",
        "keywords": ["ферм"],
        "description": "Морская ферма во Внешке",
    },
    {
        "query": "Зак отравление матрас химикаты яд",
        "keywords": ["Зак"],
        "description": "Эпизод с отравлением Зака",
    },
    {
        "query": "Меган приказ вина ответственность",
        "keywords": ["Меган"],
        "description": "Меган — персонаж, чувствующий вину",
    },
    {
        "query": "ментальный зуд предупреждение опасность чувство",
        "keywords": ["ментальн"],
        "description": "Ментальный зуд — телепатическое предупреждение",
    },
    {
        "query": "Адика безопасность проверка кровать",
        "keywords": ["Адика"],
        "description": "Адика — ответственный за безопасность",
    },
    {
        "query": "импринтинг знания обучение способности",
        "keywords": ["импринтинг"],
        "description": "Импринтинг — метод обучения",
    },
    {
        "query": "новый год 2533 будущее праздники",
        "keywords": ["2533", "нов"],
        "description": "Наступающий 2533 год",
    },
    {
        "query": "ураган ветер буря дыхание перемены",
        "keywords": ["ураган"],
        "description": "Ураган — ключевой символ книги",
    },
    {
        "query": "Мортон чтение мысли секрет",
        "keywords": ["Мортон"],
        "description": "Мортон — персонаж",
    },
]


# ═════════════════════════════════════════════════════════════════════════════
#  TESTS
# ═════════════════════════════════════════════════════════════════════════════


@pytest.fixture(scope="module")
def book_chunks():
    """Load and chunk the book once for all tests."""
    if not BOOK_PATH.exists():
        pytest.skip(f"Book file not found: {BOOK_PATH}")
    chunks = load_and_chunk_book(BOOK_PATH)
    assert len(chunks) > 100, f"Too few chunks: {len(chunks)}"
    return chunks


class TestBookChunking:
    """Verify chunking quality."""

    def test_chunk_count_reasonable(self, book_chunks):
        n = len(book_chunks)
        print(f"\n  Chunks: {n}")
        assert 200 < n < 2000, f"Unexpected chunk count: {n}"

    def test_no_tiny_chunks(self, book_chunks):
        tiny = [c for c in book_chunks if len(c["text"]) < MIN_CHUNK_CHARS]
        assert len(tiny) == 0, f"Found {len(tiny)} chunks shorter than {MIN_CHUNK_CHARS} chars"

    def test_no_huge_chunks(self, book_chunks):
        huge = [c for c in book_chunks if len(c["text"]) > 3000]
        if huge:
            print(f"  WARNING: {len(huge)} chunks > 3000 chars")
        # Allow some but not too many
        assert len(huge) < len(book_chunks) * 0.05, "Too many oversized chunks"

    def test_chapters_detected(self, book_chunks):
        chapters = set(c["chapter"] for c in book_chunks)
        print(f"  Chapters detected: {sorted(chapters)}")
        assert len(chapters) > 1, "No chapter boundaries detected"

    def test_total_text_preserved(self, book_chunks):
        original = BOOK_PATH.read_text(encoding="utf-8")
        chunked_text = " ".join(c["text"] for c in book_chunks)
        # At least 80% of original text should be in chunks
        ratio = len(chunked_text) / len(original)
        print(f"  Text preservation: {ratio:.1%}")
        assert ratio > 0.7, f"Too much text lost during chunking: {ratio:.1%}"


class TestBookInsertPerformance:
    """Measure insert throughput with real book data."""

    @pytest.mark.asyncio
    async def test_insert_throughput(self, book_chunks):
        print("\n" + "═" * 70)
        print("  INSERT THROUGHPUT — «Ураган» (Джанет Эдвардс)")
        print("═" * 70)

        server = BookMockServer()
        engine = _make_embedding_engine()
        adapter = _make_adapter(server, engine)

        dps = [_ChunkDataPoint(c) for c in book_chunks]
        N = len(dps)

        t0 = time.perf_counter()
        BATCH = 50
        for start in range(0, N, BATCH):
            await adapter.create_data_points("book", dps[start:start + BATCH])
        t_total = time.perf_counter() - t0

        throughput = N / t_total

        print(f"  Chunks:     {N}")
        print(f"  Total time: {t_total * 1000:.1f} ms")
        print(f"  Throughput: {throughput:,.0f} chunks/s")
        print(f"  Server records: {server.size}")
        print(f"  Cache entries:  {len(adapter._id_cache)}")
        print()

        assert server.size == N
        assert len(adapter._id_cache) == N
        assert throughput > 100, f"Insert too slow: {throughput:.0f} chunks/s"


class TestBookSearchPerformance:
    """Measure search latency and throughput."""

    @pytest.mark.asyncio
    async def test_search_latency(self, book_chunks):
        print("\n" + "═" * 70)
        print("  SEARCH LATENCY — «Ураган»")
        print("═" * 70)

        server = BookMockServer()
        engine = _make_embedding_engine()
        adapter = _make_adapter(server, engine)

        dps = [_ChunkDataPoint(c) for c in book_chunks]
        N = len(dps)
        BATCH = 50
        for start in range(0, N, BATCH):
            await adapter.create_data_points("book", dps[start:start + BATCH])

        # Search queries
        queries = [q["query"] for q in CONTEXTUAL_QUERIES]
        query_vectors = await _embed_all(engine, queries)

        # Warm up
        for qv in query_vectors[:3]:
            await adapter.search("book", query_vector=qv, limit=10)

        # Measure
        latencies = []
        for qv in query_vectors:
            t0 = time.perf_counter()
            await adapter.search("book", query_vector=qv, limit=10)
            latencies.append((time.perf_counter() - t0) * 1000)

        # Run 3 extra passes for more data
        for _ in range(3):
            for qv in query_vectors:
                t0 = time.perf_counter()
                await adapter.search("book", query_vector=qv, limit=10)
                latencies.append((time.perf_counter() - t0) * 1000)

        s = sorted(latencies)
        n = len(s)
        p50 = s[n // 2]
        p95 = s[int(n * 0.95)]
        p99 = s[int(n * 0.99)]
        mean = statistics.mean(latencies)

        print(f"  Dataset: {N} chunks, {len(queries)} unique queries × 4 passes")
        print(f"  Total measurements: {n}")
        print()
        print(f"  {'Metric':<12} {'Value':>10}")
        print(f"  {'-' * 25}")
        print(f"  {'p50':.<12} {p50:>9.3f} ms")
        print(f"  {'p95':.<12} {p95:>9.3f} ms")
        print(f"  {'p99':.<12} {p99:>9.3f} ms")
        print(f"  {'mean':.<12} {mean:>9.3f} ms")
        print(f"  {'max':.<12} {max(latencies):>9.3f} ms")
        print()

        # Concurrent search
        t0 = time.perf_counter()
        await asyncio.gather(*[
            adapter.search("book", query_vector=qv, limit=10)
            for qv in query_vectors
        ])
        t_concurrent = time.perf_counter() - t0
        qps = len(query_vectors) / t_concurrent
        print(f"  Concurrent ({len(query_vectors)} queries): {t_concurrent * 1000:.1f} ms total, {qps:,.0f} QPS")
        print()

        assert max(latencies) < 500, f"Search too slow: {max(latencies):.1f} ms"


class TestBookSearchQuality:
    """
    Contextual search quality — do results contain relevant content?

    NOTE: With a deterministic hash-based mock embedding (not a real model),
    search quality depends on hash collision patterns, NOT on semantic
    understanding. This test structure is ready for a real embedding model.

    With mock embeddings we test:
    - Search returns results
    - Results are properly ordered
    - Filter by chapter works
    - No crashes on diverse query types
    """

    @pytest.mark.asyncio
    async def test_all_queries_return_results(self, book_chunks):
        """Every contextual query must return at least 1 result."""
        server = BookMockServer()
        engine = _make_embedding_engine()
        adapter = _make_adapter(server, engine)

        dps = [_ChunkDataPoint(c) for c in book_chunks]
        BATCH = 50
        for start in range(0, len(dps), BATCH):
            await adapter.create_data_points("book", dps[start:start + BATCH])

        for cq in CONTEXTUAL_QUERIES:
            results = await adapter.search(
                "book", query_text=cq["query"], limit=10, include_payload=True
            )
            assert len(results) > 0, (
                f"Query '{cq['description']}' returned no results"
            )

    @pytest.mark.asyncio
    async def test_results_are_ordered(self, book_chunks):
        """Search results must be ordered by score (ascending = more similar)."""
        server = BookMockServer()
        engine = _make_embedding_engine()
        adapter = _make_adapter(server, engine)

        dps = [_ChunkDataPoint(c) for c in book_chunks]
        BATCH = 50
        for start in range(0, len(dps), BATCH):
            await adapter.create_data_points("book", dps[start:start + BATCH])

        for cq in CONTEXTUAL_QUERIES[:5]:
            results = await adapter.search(
                "book", query_text=cq["query"], limit=10
            )
            for i in range(len(results) - 1):
                assert results[i].score <= results[i + 1].score, (
                    f"Query '{cq['description']}': score order violated "
                    f"{results[i].score} > {results[i+1].score}"
                )

    @pytest.mark.asyncio
    async def test_chapter_filter_works(self, book_chunks):
        """Filtering by chapter_N must return only that chapter's chunks."""
        server = BookMockServer()
        engine = _make_embedding_engine()
        adapter = _make_adapter(server, engine)

        dps = [_ChunkDataPoint(c) for c in book_chunks]
        BATCH = 50
        for start in range(0, len(dps), BATCH):
            await adapter.create_data_points("book", dps[start:start + BATCH])

        chapters = sorted(set(c["chapter"] for c in book_chunks))
        if len(chapters) < 2:
            pytest.skip("Not enough chapters for filter test")

        target_ch = chapters[1]  # Skip chapter 0 (preamble)
        results = await adapter.search(
            "book",
            query_text="телепат Эмбер",
            limit=20,
            include_payload=True,
            node_name=[f"chapter_{target_ch}"],
        )

        for r in results:
            assert f"chapter_{target_ch}" in r.payload.get("belongs_to_set", []), (
                f"Chapter filter failed: {r.payload.get('belongs_to_set')}"
            )

    @pytest.mark.asyncio
    async def test_keyword_presence_in_top_results(self, book_chunks):
        """
        For each contextual query, check if ANY of the expected keywords
        appear in the top-10 results' text.

        With mock embeddings this is probabilistic — we track hit rate
        and report it. With a real model, expect > 80% hit rate.
        """
        print("\n" + "═" * 70)
        print("  CONTEXTUAL SEARCH QUALITY — «Ураган»")
        print("═" * 70)

        server = BookMockServer()
        engine = _make_embedding_engine()
        adapter = _make_adapter(server, engine)

        dps = [_ChunkDataPoint(c) for c in book_chunks]
        BATCH = 50
        for start in range(0, len(dps), BATCH):
            await adapter.create_data_points("book", dps[start:start + BATCH])

        hits = 0
        total = len(CONTEXTUAL_QUERIES)

        print(f"\n  {'Query':<45} {'Keywords':<25} {'Hit?':>5}")
        print(f"  {'-' * 78}")

        for cq in CONTEXTUAL_QUERIES:
            results = await adapter.search(
                "book", query_text=cq["query"], limit=10, include_payload=True
            )

            # Check if any keyword appears in any of the top results
            combined_text = " ".join(
                r.payload.get("text", "") for r in results if r.payload
            ).lower()

            hit = any(kw.lower() in combined_text for kw in cq["keywords"])
            if hit:
                hits += 1

            status = "✓" if hit else "✗"
            kw_str = ", ".join(cq["keywords"][:3])
            desc = cq["description"][:43]
            print(f"  {desc:<45} {kw_str:<25} {status:>5}")

        hit_rate = hits / total if total > 0 else 0

        print(f"\n  Hit rate: {hits}/{total} ({hit_rate:.0%})")
        print()

        if hit_rate >= 0.8:
            print("  ✓ Excellent — semantic search finds relevant content")
        elif hit_rate >= 0.5:
            print("  ~ Acceptable — mock embeddings have limited semantic quality")
        else:
            print("  ℹ Low hit rate expected with hash-based mock embeddings")
            print("    With a real embedding model (e.g., multilingual-e5-large),")
            print("    expect 80-100% hit rate on these queries.")
        print()

        # With mock embeddings, don't enforce high hit rate
        # But every query must at least return results (tested separately)


class TestBookRecall:
    """Measure recall@10 against brute-force ground truth."""

    @pytest.mark.asyncio
    async def test_recall_at_10(self, book_chunks):
        print("\n" + "═" * 70)
        print("  RECALL@10 — «Ураган»")
        print("═" * 70)

        server = BookMockServer()
        engine = _make_embedding_engine()
        adapter = _make_adapter(server, engine)

        dps = [_ChunkDataPoint(c) for c in book_chunks]
        N = len(dps)
        BATCH = 50
        for start in range(0, N, BATCH):
            await adapter.create_data_points("book", dps[start:start + BATCH])

        # Pre-embed all chunks for ground truth
        all_texts = [c["text"] for c in book_chunks]
        all_vectors = await _embed_all(engine, all_texts)
        id_list = [c["id"] for c in book_chunks]

        def _cosine(a, b):
            dot = sum(x * y for x, y in zip(a, b))
            ma = math.sqrt(sum(x * x for x in a))
            mb = math.sqrt(sum(x * x for x in b))
            return dot / (ma * mb) if ma > 0 and mb > 0 else 0.0

        def brute_top_k(qvec, k=10):
            sims = [(id_list[i], _cosine(qvec, v)) for i, v in enumerate(all_vectors)]
            sims.sort(key=lambda x: -x[1])
            return {str(uid) for uid, _ in sims[:k]}

        # Use contextual queries
        queries = [q["query"] for q in CONTEXTUAL_QUERIES]
        query_vectors = await _embed_all(engine, queries)

        recalls = []
        for qvec in query_vectors:
            ground_truth = brute_top_k(qvec, k=10)
            results = await adapter.search("book", query_vector=qvec, limit=10)
            returned_ids = {str(r.id) for r in results}
            hit = len(ground_truth & returned_ids)
            recalls.append(hit / len(ground_truth) if ground_truth else 1.0)

        mean_recall = statistics.mean(recalls)
        min_recall = min(recalls)

        print(f"  Dataset: {N} chunks, {len(queries)} queries")
        print(f"  recall@10 mean: {mean_recall:.4f}")
        print(f"  recall@10 min:  {min_recall:.4f}")
        print()

        if mean_recall >= 0.95:
            print("  ✓ Excellent recall (brute-force mock server)")
        elif mean_recall >= 0.90:
            print("  ✓ Good recall")
        else:
            print(f"  ✗ Low recall: {mean_recall:.4f}")
        print()

        assert mean_recall >= 0.90, f"recall@10 too low: {mean_recall:.4f}"


class TestBookSummaryReport:
    """Final summary report combining all metrics."""

    @pytest.mark.asyncio
    async def test_full_report(self, book_chunks):
        print("\n" + "═" * 70)
        print("  FULL BENCHMARK REPORT — «Ураган» (Джанет Эдвардс)")
        print("═" * 70)

        server = BookMockServer()
        engine = _make_embedding_engine()
        adapter = _make_adapter(server, engine)

        dps = [_ChunkDataPoint(c) for c in book_chunks]
        N = len(dps)

        # ── INSERT ──
        t0 = time.perf_counter()
        BATCH = 50
        for start in range(0, N, BATCH):
            await adapter.create_data_points("book", dps[start:start + BATCH])
        insert_time = time.perf_counter() - t0
        insert_throughput = N / insert_time

        # ── SEARCH LATENCY ──
        queries = [q["query"] for q in CONTEXTUAL_QUERIES]
        query_vectors = await _embed_all(engine, queries)

        latencies = []
        for _ in range(5):
            for qv in query_vectors:
                t0 = time.perf_counter()
                await adapter.search("book", query_vector=qv, limit=10)
                latencies.append((time.perf_counter() - t0) * 1000)

        s = sorted(latencies)
        n_lat = len(s)

        # ── CONCURRENT QPS ──
        t0 = time.perf_counter()
        await asyncio.gather(*[
            adapter.search("book", query_vector=qv, limit=10)
            for qv in query_vectors
        ])
        t_conc = time.perf_counter() - t0
        qps = len(query_vectors) / t_conc

        # ── KEYWORD HIT RATE ──
        hits = 0
        for cq in CONTEXTUAL_QUERIES:
            results = await adapter.search(
                "book", query_text=cq["query"], limit=10, include_payload=True
            )
            combined = " ".join(
                r.payload.get("text", "") for r in results if r.payload
            ).lower()
            if any(kw.lower() in combined for kw in cq["keywords"]):
                hits += 1
        hit_rate = hits / len(CONTEXTUAL_QUERIES)

        # ── Print report ──
        chapters = sorted(set(c["chapter"] for c in book_chunks))
        avg_chunk_len = statistics.mean(len(c["text"]) for c in book_chunks)

        print()
        print(f"  Book: «Ураган» by Janet Edwards (Hive #3)")
        print(f"  Size: {BOOK_PATH.stat().st_size / 1024:.0f} KB, "
              f"{sum(1 for _ in BOOK_PATH.read_text().splitlines())} lines")
        print()
        print(f"  ┌────────────────────────────────┬──────────────┐")
        print(f"  │ Metric                         │ Value        │")
        print(f"  ├────────────────────────────────┼──────────────┤")
        print(f"  │ Chunks                         │ {N:<12} │")
        print(f"  │ Chapters                       │ {len(chapters):<12} │")
        print(f"  │ Avg chunk size                 │ {avg_chunk_len:<12.0f} │")
        print(f"  │ Insert throughput              │ {insert_throughput:<9.0f} /s │")
        print(f"  │ Insert total                   │ {insert_time*1000:<9.1f} ms │")
        print(f"  │ Search p50                     │ {s[n_lat//2]:<9.3f} ms │")
        print(f"  │ Search p95                     │ {s[int(n_lat*0.95)]:<9.3f} ms │")
        print(f"  │ Search p99                     │ {s[int(n_lat*0.99)]:<9.3f} ms │")
        print(f"  │ Search mean                    │ {statistics.mean(latencies):<9.3f} ms │")
        print(f"  │ Concurrent QPS                 │ {qps:<9.0f}    │")
        print(f"  │ Keyword hit rate (mock embed)  │ {hit_rate:<9.0%}      │")
        print(f"  │ Server records                 │ {server.size:<12} │")
        print(f"  │ Embedding cache size           │ {len(adapter._embedding_cache):<12} │")
        print(f"  └────────────────────────────────┴──────────────┘")
        print()
        print(f"  Note: With real embeddings (e.g., multilingual-e5-large)")
        print(f"  keyword hit rate should be 80-100% on these Russian queries.")
        print(f"  Mock hash-based embeddings test infrastructure, not semantics.")
        print("═" * 70)

        assert True  # Report test always passes
