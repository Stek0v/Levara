"""
Comprehensive Levara vs LanceDB comparison: 10-step benchmark.

Same book «Ураган», same embed-server, same queries.
Covers: throughput, latency, quality (NDCG/MRR), concurrency,
        multi-collection isolation, crash recovery, scale.

Requires:
  - embed-server on :9001 (pplx-embed-context-v1-0.6b, dim=1024)
  - Levara on :8080 (dim=1024, 3 shards)

Run:
    pytest tests/test_comprehensive_comparison.py -v -s
"""

from __future__ import annotations

import asyncio
import json
import math
import os
import statistics
import subprocess
import tempfile
import time
import uuid
from pathlib import Path
from typing import Dict, List, Tuple

import aiohttp
import lancedb
from lancedb.pydantic import LanceModel, Vector
from pydantic import BaseModel

import pytest

# ── Constants ─────────────────────────────────────────────────────────────────

BOOK_PATH = Path(__file__).parent.parent / "Edvards_Dzanet_Uragan_r4_P61XH.txt"
EMBED_URL = "http://localhost:9001/v1/embeddings"
LEVARA_URL = "http://localhost:8080"
EMBED_MODEL = "pplx-embed-context-v1-0.6b"
DIM = 1024
MIN_CHUNK_CHARS = 80
MAX_CHUNK_CHARS = 600
BATCH_SIZE = 16
K = 10

# ── LanceDB schema ───────────────────────────────────────────────────────────

class LancePayload(BaseModel):
    id: str
    text: str
    chapter: int


class LanceRecord(LanceModel):
    id: str
    vector: Vector(DIM)
    payload: LancePayload


# ── Chunking ──────────────────────────────────────────────────────────────────

def load_and_chunk_book(path: Path) -> List[Dict]:
    text = path.read_text(encoding="utf-8")
    raw_paragraphs = [p.strip() for p in text.split("\n\n") if p.strip()]
    chunks, buffer, chapter = [], "", 0
    for para in raw_paragraphs:
        stripped = para.strip()
        if stripped.startswith("Глава ") and len(stripped) < 20:
            try:
                chapter = int(stripped.replace("Глава ", "").strip())
            except ValueError:
                pass
        if len(buffer) + len(para) < MAX_CHUNK_CHARS:
            buffer = (buffer + "\n\n" + para).strip() if buffer else para
        else:
            if buffer and len(buffer) >= MIN_CHUNK_CHARS:
                chunks.append({"id": str(uuid.uuid4()), "text": buffer,
                               "chapter": chapter, "chunk_index": len(chunks)})
            buffer = para
    if buffer and len(buffer) >= MIN_CHUNK_CHARS:
        chunks.append({"id": str(uuid.uuid4()), "text": buffer,
                       "chapter": chapter, "chunk_index": len(chunks)})
    return chunks


# ── Embedding client ──────────────────────────────────────────────────────────

async def embed_texts(session: aiohttp.ClientSession, texts: List[str]) -> List[List[float]]:
    all_vecs = []
    for start in range(0, len(texts), BATCH_SIZE):
        batch = texts[start:start + BATCH_SIZE]
        async with session.post(EMBED_URL, json={"input": batch, "model": EMBED_MODEL}) as resp:
            resp.raise_for_status()
            data = await resp.json()
        embeddings = sorted(data["data"], key=lambda x: x["index"])
        all_vecs.extend(e["embedding"] for e in embeddings)
    return all_vecs


# ── Queries ───────────────────────────────────────────────────────────────────

QUERIES = [
    {"query": "телепат Эмбер способности чтение мыслей разум", "keywords": ["телепат", "Эмбер", "разум"], "desc": "Телепат Эмбер"},
    {"query": "Лукас командир тактик партнёр Эмбер", "keywords": ["Лукас"], "desc": "Лукас — командир"},
    {"query": "город улей сто миллионов жителей уровни", "keywords": ["улей", "уровн", "миллион"], "desc": "Город-улей"},
    {"query": "лотерея профессии назначение работы 2532", "keywords": ["лотере"], "desc": "Лотерея профессий"},
    {"query": "телепаты никогда не должны встречаться запрет", "keywords": ["телепат", "встреч", "не должн"], "desc": "Запрет на встречу"},
    {"query": "ударная группа преследование преступников рейд", "keywords": ["ударн", "групп"], "desc": "Ударная группа"},
    {"query": "морская ферма океан внешка шторм волны", "keywords": ["ферм", "мор"], "desc": "Морская ферма"},
    {"query": "Зак отравление матрас химикаты ядовитые пары", "keywords": ["Зак", "матрас"], "desc": "Отравление Зака"},
    {"query": "Меган командир вина ответственность приказ", "keywords": ["Меган"], "desc": "Вина Меган"},
    {"query": "ментальный зуд предупреждение телепатическое чувство", "keywords": ["ментальн", "зуд"], "desc": "Ментальный зуд"},
    {"query": "Адика безопасность проверка охрана защита", "keywords": ["Адика"], "desc": "Адика — охранник"},
    {"query": "импринтинг вложение знаний обучение память", "keywords": ["импринтинг"], "desc": "Импринтинг"},
    {"query": "ураган ветер буря дыхание перемены мир изменился", "keywords": ["ураган", "ветр"], "desc": "Ураган — символ"},
    {"query": "нательная броня передатчик снаряжение экипировка", "keywords": ["брон", "передатчик"], "desc": "Снаряжение"},
    {"query": "Мортон секрет чтение мысли скрывать", "keywords": ["Мортон"], "desc": "Мортон"},
]


# ── Skip checks ───────────────────────────────────────────────────────────────

def _check(url):
    import urllib.request
    try:
        urllib.request.urlopen(url, timeout=3)
        return True
    except Exception:
        return False


pytestmark = pytest.mark.skipif(
    not (_check("http://localhost:9001/health") and _check("http://localhost:8080/metrics")),
    reason="Need embed-server:9001 + Levara:8080",
)


# ── Math helpers ──────────────────────────────────────────────────────────────

def cosine_similarity(a: List[float], b: List[float]) -> float:
    dot = sum(x * y for x, y in zip(a, b))
    na = math.sqrt(sum(x * x for x in a))
    nb = math.sqrt(sum(x * x for x in b))
    if na == 0 or nb == 0:
        return 0.0
    return dot / (na * nb)


def brute_force_topk(query_vec: List[float], all_vecs: List[List[float]],
                     k: int) -> List[Tuple[int, float]]:
    """Return top-K (index, similarity) pairs by cosine similarity, descending."""
    sims = [(i, cosine_similarity(query_vec, v)) for i, v in enumerate(all_vecs)]
    sims.sort(key=lambda x: x[1], reverse=True)
    return sims[:k]


def dcg(relevances: List[float]) -> float:
    return sum(rel / math.log2(i + 2) for i, rel in enumerate(relevances))


def ndcg_at_k(retrieved_indices: List[int], ideal_indices: List[int],
              similarities: Dict[int, float], k: int) -> float:
    """NDCG@K: compare retrieved ranking against ideal (brute-force) ranking."""
    retrieved = retrieved_indices[:k]
    ideal = ideal_indices[:k]

    # Relevance = cosine similarity (from brute-force)
    retrieved_rels = [similarities.get(idx, 0.0) for idx in retrieved]
    ideal_rels = [similarities.get(idx, 0.0) for idx in ideal]

    ideal_dcg = dcg(ideal_rels)
    if ideal_dcg == 0:
        return 1.0  # No relevant docs → perfect score
    return dcg(retrieved_rels) / ideal_dcg


def mrr(retrieved_indices: List[int], relevant_set: set) -> float:
    """Mean Reciprocal Rank for a single query."""
    for i, idx in enumerate(retrieved_indices):
        if idx in relevant_set:
            return 1.0 / (i + 1)
    return 0.0


def percentile_stats(values: List[float]) -> Dict[str, float]:
    s = sorted(values)
    n = len(s)
    if n == 0:
        return {"p50": 0, "p95": 0, "p99": 0, "mean": 0, "max": 0}
    return {
        "p50": s[n // 2],
        "p95": s[int(n * 0.95)],
        "p99": s[min(int(n * 0.99), n - 1)],
        "mean": statistics.mean(values),
        "max": max(values),
    }


# ── Levara helpers ──────────────────────────────────────────────────────────

async def levara_insert(session: aiohttp.ClientSession, records: List[Dict]) -> Dict:
    async with session.post(f"{LEVARA_URL}/api/v1/batch_insert", json={"records": records}) as r:
        r.raise_for_status()
        return await r.json()


async def levara_search(session: aiohttp.ClientSession, vector: List[float],
                        k: int = K) -> List[Dict]:
    async with session.post(f"{LEVARA_URL}/api/v1/search", json={"vector": vector, "k": k}) as r:
        r.raise_for_status()
        data = await r.json()
    return data.get("results", [])


async def levara_delete(session: aiohttp.ClientSession, ids: List[str]) -> Dict:
    async with session.post(f"{LEVARA_URL}/api/v1/delete", json={"ids": ids}) as r:
        r.raise_for_status()
        return await r.json()


async def levara_info(session: aiohttp.ClientSession) -> Dict:
    async with session.get(f"{LEVARA_URL}/api/v1/info") as r:
        r.raise_for_status()
        return await r.json()


async def ensure_levara_healthy(max_wait: int = 30) -> bool:
    """Check Levara health, restart if needed. Returns True if healthy."""
    # First check if already healthy
    try:
        async with aiohttp.ClientSession() as session:
            async with session.get(
                f"{LEVARA_URL}/api/v1/info",
                timeout=aiohttp.ClientTimeout(total=2),
            ) as r:
                if r.status == 200:
                    return True
    except Exception:
        pass

    # Try to restart via docker compose
    cwd = str(Path(__file__).parent.parent)
    subprocess.run(
        ["docker", "compose", "up", "-d", "levara"],
        capture_output=True, text=True, timeout=60,
        cwd=cwd,
    )

    # Wait for recovery
    for _ in range(max_wait):
        try:
            async with aiohttp.ClientSession() as session:
                async with session.get(
                    f"{LEVARA_URL}/api/v1/info",
                    timeout=aiohttp.ClientTimeout(total=2),
                ) as r:
                    if r.status == 200:
                        return True
        except Exception:
            pass
        await asyncio.sleep(1)
    return False


async def levara_search_safe(session: aiohttp.ClientSession, vector: List[float],
                              k: int = K) -> List[Dict]:
    """Search with error handling — returns empty list on connection error."""
    try:
        return await levara_search(session, vector, k)
    except (aiohttp.ClientError, ConnectionError):
        return []


def _extract_text(result: Dict) -> str:
    """Extract text from Levara or LanceDB result metadata/payload."""
    meta = result.get("metadata", result.get("payload", {}))
    if isinstance(meta, str):
        try:
            meta = json.loads(meta)
        except (json.JSONDecodeError, TypeError):
            return ""
    if isinstance(meta, dict):
        return meta.get("text", "")
    return ""


# ── Fixtures ──────────────────────────────────────────────────────────────────

@pytest.fixture(scope="module")
def book_data():
    """Step 1: Load book, chunk, embed once — shared across all tests."""
    if not BOOK_PATH.exists():
        pytest.skip(f"Book not found: {BOOK_PATH}")

    chunks = load_and_chunk_book(BOOK_PATH)
    texts = [c["text"] for c in chunks]

    async def _embed():
        async with aiohttp.ClientSession() as session:
            vecs = await embed_texts(session, texts)
            q_vecs = await embed_texts(session, [q["query"] for q in QUERIES])
        return vecs, q_vecs

    vecs, q_vecs = asyncio.run(_embed())

    print("\n" + "=" * 72)
    print(f"  STEP 1: DATA PREPARATION")
    print(f"  Book: «Ураган» (Джанет Эдвардс)")
    print(f"  Chunks: {len(chunks)}, Dim: {DIM}")
    print(f"  Queries: {len(QUERIES)} semantic")
    print(f"  Embeddings: pre-computed (shared for both engines)")
    print("=" * 72)

    return chunks, vecs, q_vecs


# ══════════════════════════════════════════════════════════════════════════════


class TestComprehensiveComparison:
    """10-step comprehensive benchmark: Levara vs LanceDB."""

    # Class-level storage for cross-test state
    _lance_tmpdir: str = None
    _lance_tbl = None
    _insert_results: Dict = {}
    _latency_results: Dict = {}
    _qps_results: Dict = {}
    _hit_rate_results: Dict = {}
    _ranking_results: Dict = {}
    _concurrent_rw_results: Dict = {}
    _isolation_results: Dict = {}
    _crash_results: Dict = {}
    _scale_results: Dict = {}

    # ── Step 2: Insert Throughput ──────────────────────────────────────────

    @pytest.mark.asyncio
    async def test_02_insert_throughput(self, book_data):
        """Step 2: Insert throughput — Levara (HTTP) vs LanceDB (in-process)."""
        chunks, vecs, _ = book_data
        N = len(chunks)
        BATCH = 50

        print("\n" + "=" * 72)
        print(f"  STEP 2: INSERT THROUGHPUT  ({N} chunks, dim={DIM}, batch={BATCH})")
        print("=" * 72)

        # ── Levara ──
        batch_latencies_v = []
        async with aiohttp.ClientSession() as session:
            t0_total = time.perf_counter()
            for start in range(0, N, BATCH):
                batch = [{
                    "id": f"comp:{chunks[i]['id']}",
                    "vector": vecs[i],
                    "metadata": {"text": chunks[i]["text"][:500],
                                 "chapter": chunks[i]["chapter"]},
                } for i in range(start, min(start + BATCH, N))]
                t0 = time.perf_counter()
                await levara_insert(session, batch)
                batch_latencies_v.append((time.perf_counter() - t0) * 1000)
            t_v = time.perf_counter() - t0_total

        # ── LanceDB ──
        self.__class__._lance_tmpdir = tempfile.mkdtemp()
        conn = await lancedb.connect_async(self.__class__._lance_tmpdir)
        tbl = await conn.create_table("comp", schema=LanceRecord, exist_ok=True)

        batch_latencies_l = []
        t0_total = time.perf_counter()
        for start in range(0, N, BATCH):
            batch = [
                LanceRecord(
                    id=chunks[i]["id"],
                    vector=vecs[i],
                    payload=LancePayload(id=chunks[i]["id"],
                                         text=chunks[i]["text"][:500],
                                         chapter=chunks[i]["chapter"]),
                ) for i in range(start, min(start + BATCH, N))
            ]
            t0 = time.perf_counter()
            await tbl.merge_insert("id").when_matched_update_all().when_not_matched_insert_all().execute(batch)
            batch_latencies_l.append((time.perf_counter() - t0) * 1000)
        t_l = time.perf_counter() - t0_total

        self.__class__._lance_tbl = tbl

        v_dps, l_dps = N / t_v, N / t_l
        v_stats = percentile_stats(batch_latencies_v)
        l_stats = percentile_stats(batch_latencies_l)

        self.__class__._insert_results = {
            "levara_dps": v_dps, "lance_dps": l_dps,
            "levara_ms": t_v * 1000, "lance_ms": t_l * 1000,
        }

        print(f"\n  {'Provider':<30} {'dp/s':>8} {'total ms':>10} {'p50 ms':>8} {'p95 ms':>8} {'p99 ms':>8}")
        print(f"  {'-'*75}")
        print(f"  {'Levara':<30} {v_dps:>8,.0f} {t_v*1000:>10.1f} {v_stats['p50']:>8.1f} {v_stats['p95']:>8.1f} {v_stats['p99']:>8.1f}")
        print(f"  {'LanceDB':<30} {l_dps:>8,.0f} {t_l*1000:>10.1f} {l_stats['p50']:>8.1f} {l_stats['p95']:>8.1f} {l_stats['p99']:>8.1f}")
        ratio = max(v_dps, l_dps) / max(min(v_dps, l_dps), 1)
        winner = "Levara" if v_dps > l_dps else "LanceDB"
        print(f"\n  Winner: {winner} ({ratio:.1f}x)")

    # ── Step 3: Search Latency (single-threaded) ──────────────────────────

    @pytest.mark.asyncio
    async def test_03_search_latency(self, book_data):
        """Step 3: Search latency — 15 queries × 10 passes = 150 measurements."""
        chunks, vecs, q_vecs = book_data
        PASSES = 10

        print("\n" + "=" * 72)
        print(f"  STEP 3: SEARCH LATENCY  ({len(QUERIES)} queries × {PASSES} passes)")
        print("=" * 72)

        # Let async HNSW indexer finish
        await asyncio.sleep(3)

        # ── Levara ──
        v_lats = []
        async with aiohttp.ClientSession() as session:
            for _ in range(PASSES):
                for qv in q_vecs:
                    t0 = time.perf_counter()
                    await levara_search(session, qv, k=K)
                    v_lats.append((time.perf_counter() - t0) * 1000)

        # ── LanceDB ──
        tbl = self.__class__._lance_tbl
        l_lats = []
        for _ in range(PASSES):
            for qv in q_vecs:
                t0 = time.perf_counter()
                await tbl.vector_search(qv).limit(K).to_list()
                l_lats.append((time.perf_counter() - t0) * 1000)

        v_stats = percentile_stats(v_lats)
        l_stats = percentile_stats(l_lats)

        self.__class__._latency_results = {"vectra": v_stats, "lance": l_stats}

        print(f"\n  {'Provider':<30} {'p50':>7} {'p95':>7} {'p99':>7} {'mean':>7} {'max':>7}  (ms)")
        print(f"  {'-'*75}")
        print(f"  {'Levara':<30} {v_stats['p50']:>7.1f} {v_stats['p95']:>7.1f} {v_stats['p99']:>7.1f} {v_stats['mean']:>7.1f} {v_stats['max']:>7.1f}")
        print(f"  {'LanceDB':<30} {l_stats['p50']:>7.1f} {l_stats['p95']:>7.1f} {l_stats['p99']:>7.1f} {l_stats['mean']:>7.1f} {l_stats['max']:>7.1f}")
        ratio = max(v_stats['mean'], l_stats['mean']) / max(min(v_stats['mean'], l_stats['mean']), 0.001)
        winner = "Levara" if v_stats['mean'] < l_stats['mean'] else "LanceDB"
        print(f"\n  Winner: {winner} ({ratio:.1f}x faster)")

    # ── Step 4: Concurrent QPS ────────────────────────────────────────────

    @pytest.mark.asyncio
    async def test_04_concurrent_qps(self, book_data):
        """Step 4: Concurrent QPS — all 15 queries at once, 5 rounds."""
        _, _, q_vecs = book_data
        ROUNDS = 5

        print("\n" + "=" * 72)
        print(f"  STEP 4: CONCURRENT QPS  ({len(QUERIES)} queries × {ROUNDS} rounds)")
        print("=" * 72)

        # ── Levara ──
        v_times = []
        async with aiohttp.ClientSession() as session:
            for _ in range(ROUNDS):
                t0 = time.perf_counter()
                await asyncio.gather(*[levara_search(session, qv, k=K) for qv in q_vecs])
                v_times.append(time.perf_counter() - t0)

        # ── LanceDB ──
        tbl = self.__class__._lance_tbl
        l_times = []
        for _ in range(ROUNDS):
            t0 = time.perf_counter()
            await asyncio.gather(*[tbl.vector_search(qv).limit(K).to_list() for qv in q_vecs])
            l_times.append(time.perf_counter() - t0)

        v_qps = [len(q_vecs) / t for t in v_times]
        l_qps = [len(q_vecs) / t for t in l_times]

        v_mean_qps = statistics.mean(v_qps)
        l_mean_qps = statistics.mean(l_qps)
        v_mean_ms = statistics.mean(v_times) * 1000
        l_mean_ms = statistics.mean(l_times) * 1000

        self.__class__._qps_results = {
            "levara_qps": v_mean_qps, "lance_qps": l_mean_qps,
            "levara_ms": v_mean_ms, "lance_ms": l_mean_ms,
        }

        print(f"\n  {'Provider':<30} {'avg QPS':>10} {'avg round ms':>14} {'best QPS':>10}")
        print(f"  {'-'*68}")
        print(f"  {'Levara':<30} {v_mean_qps:>10,.0f} {v_mean_ms:>14.1f} {max(v_qps):>10,.0f}")
        print(f"  {'LanceDB':<30} {l_mean_qps:>10,.0f} {l_mean_ms:>14.1f} {max(l_qps):>10,.0f}")
        ratio = max(v_mean_qps, l_mean_qps) / max(min(v_mean_qps, l_mean_qps), 1)
        winner = "Levara" if v_mean_qps > l_mean_qps else "LanceDB"
        print(f"\n  Winner: {winner} ({ratio:.1f}x)")

    # ── Step 5: Keyword Hit Rate ──────────────────────────────────────────

    @pytest.mark.asyncio
    async def test_05_keyword_hit_rate(self, book_data):
        """Step 5: Semantic search quality — keyword hit rate on 15 queries."""
        _, _, q_vecs = book_data

        print("\n" + "=" * 72)
        print(f"  STEP 5: KEYWORD HIT RATE  ({len(QUERIES)} queries, k={K})")
        print("=" * 72)

        # ── Levara ──
        v_hits = 0
        v_detail = []
        async with aiohttp.ClientSession() as session:
            for cq, qv in zip(QUERIES, q_vecs):
                results = await levara_search(session, qv, k=K)
                combined = " ".join(_extract_text(r) for r in results).lower()
                hit = any(kw.lower() in combined for kw in cq["keywords"])
                if hit:
                    v_hits += 1
                v_detail.append(("V", cq["desc"], hit))

        # ── LanceDB ──
        tbl = self.__class__._lance_tbl
        l_hits = 0
        l_detail = []
        for cq, qv in zip(QUERIES, q_vecs):
            rows = await tbl.vector_search(qv).limit(K).to_list()
            combined = " ".join(_extract_text(r) for r in rows).lower()
            hit = any(kw.lower() in combined for kw in cq["keywords"])
            if hit:
                l_hits += 1
            l_detail.append(("L", cq["desc"], hit))

        v_rate = v_hits / len(QUERIES)
        l_rate = l_hits / len(QUERIES)

        self.__class__._hit_rate_results = {
            "levara_rate": v_rate, "lance_rate": l_rate,
            "levara_hits": v_hits, "lance_hits": l_hits,
        }

        print(f"\n  {'Query':<30} {'Levara':>10} {'LanceDB':>10}")
        print(f"  {'-'*52}")
        for i, cq in enumerate(QUERIES):
            v_ok = "HIT" if v_detail[i][2] else "MISS"
            l_ok = "HIT" if l_detail[i][2] else "MISS"
            print(f"  {cq['desc']:<30} {v_ok:>10} {l_ok:>10}")
        print(f"  {'-'*52}")
        print(f"  {'TOTAL':<30} {v_hits:>8}/{len(QUERIES)} {l_hits:>8}/{len(QUERIES)}")
        print(f"  {'Rate':<30} {v_rate:>9.0%} {l_rate:>9.0%}")

    # ── Step 6: Ranking Quality (NDCG@10, MRR) ───────────────────────────

    @pytest.mark.asyncio
    async def test_06_ranking_quality(self, book_data):
        """Step 6: Ranking quality — NDCG@10, MRR, recall@1/5/10 vs brute-force."""
        chunks, vecs, q_vecs = book_data

        print("\n" + "=" * 72)
        print(f"  STEP 6: RANKING QUALITY  (NDCG@10, MRR, recall@K)")
        print(f"  Ground truth: brute-force cosine similarity over {len(chunks)} chunks")
        print("=" * 72)

        # Build chunk ID → index mapping
        chunk_id_to_idx = {c["id"]: i for i, c in enumerate(chunks)}

        v_ndcgs, l_ndcgs = [], []
        v_mrrs, l_mrrs = [], []
        v_recall1, v_recall5, v_recall10 = [], [], []
        l_recall1, l_recall5, l_recall10 = [], [], []

        async with aiohttp.ClientSession() as session:
            for qi, qv in enumerate(q_vecs):
                # Ground truth: brute-force top-K
                bf_topk = brute_force_topk(qv, vecs, k=K)
                ideal_indices = [idx for idx, _ in bf_topk]
                sim_map = {idx: sim for idx, sim in bf_topk}
                relevant_set = set(ideal_indices[:K])

                # ── Levara results ──
                v_results = await levara_search(session, qv, k=K)
                v_indices = []
                for r in v_results:
                    rid = r.get("id", "")
                    # Strip prefix "comp:" to get original UUID
                    raw_id = rid.split(":", 1)[-1] if ":" in rid else rid
                    idx = chunk_id_to_idx.get(raw_id, -1)
                    v_indices.append(idx)

                # ── LanceDB results ──
                tbl = self.__class__._lance_tbl
                l_rows = await tbl.vector_search(qv).limit(K).to_list()
                l_indices = []
                for r in l_rows:
                    rid = r.get("id", "")
                    idx = chunk_id_to_idx.get(rid, -1)
                    l_indices.append(idx)

                # NDCG@10
                v_ndcgs.append(ndcg_at_k(v_indices, ideal_indices, sim_map, K))
                l_ndcgs.append(ndcg_at_k(l_indices, ideal_indices, sim_map, K))

                # MRR
                v_mrrs.append(mrr(v_indices, relevant_set))
                l_mrrs.append(mrr(l_indices, relevant_set))

                # Recall@K
                v_set = set(v_indices)
                l_set = set(l_indices)
                v_recall1.append(1.0 if ideal_indices[0] in v_set else 0.0)
                v_recall5.append(len(v_set & set(ideal_indices[:5])) / 5)
                v_recall10.append(len(v_set & set(ideal_indices[:K])) / K)
                l_recall1.append(1.0 if ideal_indices[0] in l_set else 0.0)
                l_recall5.append(len(l_set & set(ideal_indices[:5])) / 5)
                l_recall10.append(len(l_set & set(ideal_indices[:K])) / K)

        results = {
            "levara_ndcg": statistics.mean(v_ndcgs),
            "lance_ndcg": statistics.mean(l_ndcgs),
            "levara_mrr": statistics.mean(v_mrrs),
            "lance_mrr": statistics.mean(l_mrrs),
            "levara_recall1": statistics.mean(v_recall1),
            "levara_recall5": statistics.mean(v_recall5),
            "levara_recall10": statistics.mean(v_recall10),
            "lance_recall1": statistics.mean(l_recall1),
            "lance_recall5": statistics.mean(l_recall5),
            "lance_recall10": statistics.mean(l_recall10),
        }
        self.__class__._ranking_results = results

        print(f"\n  {'Metric':<30} {'Levara':>10} {'LanceDB':>10}")
        print(f"  {'-'*52}")
        print(f"  {'NDCG@10':<30} {results['levara_ndcg']:>10.4f} {results['lance_ndcg']:>10.4f}")
        print(f"  {'MRR':<30} {results['levara_mrr']:>10.4f} {results['lance_mrr']:>10.4f}")
        print(f"  {'Recall@1':<30} {results['levara_recall1']:>10.4f} {results['lance_recall1']:>10.4f}")
        print(f"  {'Recall@5':<30} {results['levara_recall5']:>10.4f} {results['lance_recall5']:>10.4f}")
        print(f"  {'Recall@10':<30} {results['levara_recall10']:>10.4f} {results['lance_recall10']:>10.4f}")

        # Per-query breakdown
        print(f"\n  Per-query NDCG@10:")
        print(f"  {'Query':<30} {'Levara':>10} {'LanceDB':>10}")
        print(f"  {'-'*52}")
        for i, cq in enumerate(QUERIES):
            print(f"  {cq['desc']:<30} {v_ndcgs[i]:>10.4f} {l_ndcgs[i]:>10.4f}")

    # ── Step 7: Concurrent Read+Write ─────────────────────────────────────

    @pytest.mark.asyncio
    async def test_07_concurrent_read_write(self, book_data):
        """Step 7: Search latency under concurrent write load."""
        chunks, vecs, q_vecs = book_data
        N_SEARCH = 100

        print("\n" + "=" * 72)
        print(f"  STEP 7: CONCURRENT READ+WRITE")
        print(f"  Background: insert new chunks every 50ms")
        print(f"  Foreground: {N_SEARCH} search queries")
        print("=" * 72)

        # Prepare extra chunks for background writes
        extra_chunks = []
        for i in range(200):
            src = chunks[i % len(chunks)]
            extra_chunks.append({
                "id": str(uuid.uuid4()),
                "text": f"[copy-{i}] " + src["text"][:400],
                "chapter": src["chapter"],
            })

        # Embed extra chunks (reuse existing vectors with slight noise won't
        # affect timing, so just reuse)
        extra_vecs = [vecs[i % len(vecs)] for i in range(200)]

        # ── Levara: search-only baseline ──
        baseline_lats = []
        async with aiohttp.ClientSession() as session:
            for i in range(N_SEARCH):
                qv = q_vecs[i % len(q_vecs)]
                t0 = time.perf_counter()
                await levara_search(session, qv, k=K)
                baseline_lats.append((time.perf_counter() - t0) * 1000)

        # ── Levara: search with concurrent writes ──
        write_done = asyncio.Event()
        loaded_lats = []

        async def background_writer():
            async with aiohttp.ClientSession() as ws:
                for i in range(0, 200, 10):
                    batch = [{
                        "id": f"bg:{extra_chunks[j]['id']}",
                        "vector": extra_vecs[j],
                        "metadata": {"text": extra_chunks[j]["text"][:300],
                                     "chapter": extra_chunks[j]["chapter"]},
                    } for j in range(i, min(i + 10, 200))]
                    await levara_insert(ws, batch)
                    await asyncio.sleep(0.05)
            write_done.set()

        async def foreground_searcher():
            async with aiohttp.ClientSession() as ss:
                for i in range(N_SEARCH):
                    qv = q_vecs[i % len(q_vecs)]
                    t0 = time.perf_counter()
                    await levara_search(ss, qv, k=K)
                    loaded_lats.append((time.perf_counter() - t0) * 1000)

        await asyncio.gather(background_writer(), foreground_searcher())

        baseline_stats = percentile_stats(baseline_lats)
        loaded_stats = percentile_stats(loaded_lats)

        self.__class__._concurrent_rw_results = {
            "baseline": baseline_stats,
            "loaded": loaded_stats,
        }

        print(f"\n  {'Scenario':<30} {'p50':>7} {'p95':>7} {'p99':>7} {'mean':>7}  (ms)")
        print(f"  {'-'*65}")
        print(f"  {'Search only':<30} {baseline_stats['p50']:>7.1f} {baseline_stats['p95']:>7.1f} {baseline_stats['p99']:>7.1f} {baseline_stats['mean']:>7.1f}")
        print(f"  {'Search + bg writes':<30} {loaded_stats['p50']:>7.1f} {loaded_stats['p95']:>7.1f} {loaded_stats['p99']:>7.1f} {loaded_stats['mean']:>7.1f}")

        if baseline_stats['mean'] > 0:
            overhead = ((loaded_stats['mean'] - baseline_stats['mean']) / baseline_stats['mean']) * 100
            print(f"\n  Write overhead on search latency: {overhead:+.1f}%")

    # ── Step 8: Multi-Collection Isolation ─────────────────────────────────

    @pytest.mark.asyncio
    async def test_08_multi_collection_isolation(self, book_data):
        """Step 8: Multi-collection isolation — 3 collections, no cross-leakage."""
        chunks, vecs, q_vecs = book_data

        print("\n" + "=" * 72)
        print(f"  STEP 8: MULTI-COLLECTION ISOLATION")
        print("=" * 72)

        # Split chunks into 3 collections by chapter
        collections = {"chapter_1": [], "chapter_2": [], "chapter_3": []}
        for i, c in enumerate(chunks):
            ch = c["chapter"]
            if ch <= 5:
                collections["chapter_1"].append((i, c))
            elif ch <= 10:
                collections["chapter_2"].append((i, c))
            else:
                collections["chapter_3"].append((i, c))

        # ── Levara: insert with collection prefixes ──
        async with aiohttp.ClientSession() as session:
            for col_name, col_chunks in collections.items():
                if not col_chunks:
                    continue
                for start in range(0, len(col_chunks), 50):
                    batch = [{
                        "id": f"{col_name}:{col_chunks[j][1]['id']}",
                        "vector": vecs[col_chunks[j][0]],
                        "metadata": {"text": col_chunks[j][1]["text"][:300],
                                     "chapter": col_chunks[j][1]["chapter"],
                                     "collection": col_name},
                    } for j in range(start, min(start + 50, len(col_chunks)))]
                    await levara_insert(session, batch)

        await asyncio.sleep(2)  # Let HNSW index

        # ── Levara: search and check isolation ──
        leakage_count = 0
        total_checks = 0

        async with aiohttp.ClientSession() as session:
            for col_name, col_chunks in collections.items():
                if not col_chunks:
                    continue

                # Use first chunk's vector as query
                query_idx = col_chunks[0][0]
                results = await levara_search(session, vecs[query_idx], k=20)

                # Filter results by collection prefix
                for r in results:
                    rid = r.get("id", "")
                    total_checks += 1
                    # Check if any result from THIS collection prefix is correct
                    if rid.startswith(f"{col_name}:") or rid.startswith("comp:") or rid.startswith("bg:"):
                        continue  # Expected: own collection or global
                    # Check if result belongs to another chapter collection
                    for other_col in collections:
                        if other_col != col_name and rid.startswith(f"{other_col}:"):
                            leakage_count += 1

        # ── LanceDB: multi-table isolation ──
        lance_tmpdir2 = tempfile.mkdtemp()
        conn2 = await lancedb.connect_async(lance_tmpdir2)
        lance_tables = {}

        for col_name, col_chunks in collections.items():
            if not col_chunks:
                continue
            tbl = await conn2.create_table(col_name, schema=LanceRecord, exist_ok=True)
            for start in range(0, len(col_chunks), 50):
                batch = [
                    LanceRecord(
                        id=col_chunks[j][1]["id"],
                        vector=vecs[col_chunks[j][0]],
                        payload=LancePayload(
                            id=col_chunks[j][1]["id"],
                            text=col_chunks[j][1]["text"][:300],
                            chapter=col_chunks[j][1]["chapter"]),
                    ) for j in range(start, min(start + 50, len(col_chunks)))
                ]
                await tbl.merge_insert("id").when_matched_update_all().when_not_matched_insert_all().execute(batch)
            lance_tables[col_name] = tbl

        lance_leakage = 0
        lance_total = 0
        for col_name, col_chunks in collections.items():
            if not col_chunks or col_name not in lance_tables:
                continue
            query_idx = col_chunks[0][0]
            results = await lance_tables[col_name].vector_search(vecs[query_idx]).limit(20).to_list()
            # LanceDB: separate tables → should be 0 leakage by design
            other_ids = set()
            for other_col, other_chunks in collections.items():
                if other_col != col_name:
                    other_ids.update(c[1]["id"] for c in other_chunks)
            for r in results:
                lance_total += 1
                if r.get("id", "") in other_ids:
                    lance_leakage += 1

        self.__class__._isolation_results = {
            "levara_leakage": leakage_count,
            "levara_total": total_checks,
            "lance_leakage": lance_leakage,
            "lance_total": lance_total,
        }

        leak_rate_v = leakage_count / max(total_checks, 1) * 100
        leak_rate_l = lance_leakage / max(lance_total, 1) * 100

        print(f"\n  Collections: {', '.join(f'{k}({len(v)} chunks)' for k, v in collections.items())}")
        print(f"\n  {'Provider':<30} {'Leakage':>10} {'Total checks':>14} {'Rate':>8}")
        print(f"  {'-'*65}")
        print(f"  {'Levara (prefix-based)':<30} {leakage_count:>10} {total_checks:>14} {leak_rate_v:>7.1f}%")
        print(f"  {'LanceDB (separate tables)':<30} {lance_leakage:>10} {lance_total:>14} {leak_rate_l:>7.1f}%")

        if leakage_count > 0:
            print(f"\n  WARNING: Levara cross-collection leakage detected!")
            print(f"  Levara uses prefix-based filtering, not physical isolation.")
            print(f"  For queries returning global IDs (comp:, bg:), this is expected.")

    # ── Step 9: Scale Test (5K-10K vectors) ──────────────────────────────

    @pytest.mark.asyncio
    async def test_09_scale_test(self, book_data):
        """Step 9: Scale test — duplicate book 5-7x, test at 7K-10K vectors."""
        chunks, vecs, q_vecs = book_data
        N_base = len(chunks)
        MULTIPLIER = 7
        TARGET = N_base * MULTIPLIER

        print("\n" + "=" * 72)
        print(f"  STEP 9: SCALE TEST")
        print(f"  Base: {N_base} chunks × {MULTIPLIER} = ~{TARGET} vectors")
        print("=" * 72)

        # Ensure Levara is healthy (may have crashed from accumulated data)
        print(f"  Checking Levara health...")
        if not await ensure_levara_healthy(max_wait=30):
            pytest.skip("Levara not available for scale test")

        # Generate scaled data (reuse vectors with unique IDs)
        scale_chunks = []
        scale_vecs = []
        for m in range(MULTIPLIER):
            for i, c in enumerate(chunks):
                scale_chunks.append({
                    "id": str(uuid.uuid4()),
                    "text": c["text"],
                    "chapter": c["chapter"],
                })
                scale_vecs.append(vecs[i])

        N_scale = len(scale_chunks)
        BATCH = 100

        # ── Levara: scale insert ──
        v_batch_lats = []
        async with aiohttp.ClientSession() as session:
            t0_total = time.perf_counter()
            for start in range(0, N_scale, BATCH):
                batch = [{
                    "id": f"scale:{scale_chunks[i]['id']}",
                    "vector": scale_vecs[i],
                    "metadata": {"text": scale_chunks[i]["text"][:300],
                                 "chapter": scale_chunks[i]["chapter"]},
                } for i in range(start, min(start + BATCH, N_scale))]
                t0 = time.perf_counter()
                await levara_insert(session, batch)
                v_batch_lats.append((time.perf_counter() - t0) * 1000)
            t_v_insert = time.perf_counter() - t0_total

        # ── LanceDB: scale insert ──
        lance_scale_dir = tempfile.mkdtemp()
        conn_scale = await lancedb.connect_async(lance_scale_dir)
        tbl_scale = await conn_scale.create_table("scale", schema=LanceRecord, exist_ok=True)

        l_batch_lats = []
        t0_total = time.perf_counter()
        for start in range(0, N_scale, BATCH):
            batch = [
                LanceRecord(
                    id=scale_chunks[i]["id"],
                    vector=scale_vecs[i],
                    payload=LancePayload(
                        id=scale_chunks[i]["id"],
                        text=scale_chunks[i]["text"][:300],
                        chapter=scale_chunks[i]["chapter"]),
                ) for i in range(start, min(start + BATCH, N_scale))
            ]
            t0 = time.perf_counter()
            await tbl_scale.merge_insert("id").when_matched_update_all().when_not_matched_insert_all().execute(batch)
            l_batch_lats.append((time.perf_counter() - t0) * 1000)
        t_l_insert = time.perf_counter() - t0_total

        v_insert_dps = N_scale / t_v_insert
        l_insert_dps = N_scale / t_l_insert

        print(f"\n  Insert throughput at scale ({N_scale} vectors):")
        print(f"  {'Provider':<30} {'dp/s':>10} {'total sec':>10}")
        print(f"  {'-'*52}")
        print(f"  {'Levara':<30} {v_insert_dps:>10,.0f} {t_v_insert:>10.1f}")
        print(f"  {'LanceDB':<30} {l_insert_dps:>10,.0f} {t_l_insert:>10.1f}")

        # Wait for HNSW indexing
        await asyncio.sleep(5)

        # ── Search latency at scale ──
        PASSES = 5
        v_lats = []
        async with aiohttp.ClientSession() as session:
            for _ in range(PASSES):
                for qv in q_vecs:
                    t0 = time.perf_counter()
                    await levara_search(session, qv, k=K)
                    v_lats.append((time.perf_counter() - t0) * 1000)

        l_lats = []
        for _ in range(PASSES):
            for qv in q_vecs:
                t0 = time.perf_counter()
                await tbl_scale.vector_search(qv).limit(K).to_list()
                l_lats.append((time.perf_counter() - t0) * 1000)

        v_search_stats = percentile_stats(v_lats)
        l_search_stats = percentile_stats(l_lats)

        print(f"\n  Search latency at scale ({N_scale} vectors):")
        print(f"  {'Provider':<30} {'p50':>7} {'p95':>7} {'p99':>7} {'mean':>7}  (ms)")
        print(f"  {'-'*65}")
        print(f"  {'Levara':<30} {v_search_stats['p50']:>7.1f} {v_search_stats['p95']:>7.1f} {v_search_stats['p99']:>7.1f} {v_search_stats['mean']:>7.1f}")
        print(f"  {'LanceDB':<30} {l_search_stats['p50']:>7.1f} {l_search_stats['p95']:>7.1f} {l_search_stats['p99']:>7.1f} {l_search_stats['mean']:>7.1f}")

        # ── Recall at scale ──
        v_recall10_vals = []
        l_recall10_vals = []

        chunk_id_to_idx_scale = {c["id"]: i for i, c in enumerate(scale_chunks)}

        async with aiohttp.ClientSession() as session:
            for qi, qv in enumerate(q_vecs[:5]):  # Sample 5 queries for speed
                bf_topk = brute_force_topk(qv, scale_vecs, k=K)
                ideal_set = set(idx for idx, _ in bf_topk)

                v_results = await levara_search(session, qv, k=K)
                v_indices = set()
                for r in v_results:
                    rid = r.get("id", "")
                    raw_id = rid.split(":", 1)[-1] if ":" in rid else rid
                    idx = chunk_id_to_idx_scale.get(raw_id, -1)
                    v_indices.add(idx)

                l_rows = await tbl_scale.vector_search(qv).limit(K).to_list()
                l_indices = set()
                for r in l_rows:
                    idx = chunk_id_to_idx_scale.get(r.get("id", ""), -1)
                    l_indices.add(idx)

                v_recall10_vals.append(len(v_indices & ideal_set) / K)
                l_recall10_vals.append(len(l_indices & ideal_set) / K)

        v_recall_mean = statistics.mean(v_recall10_vals) if v_recall10_vals else 0
        l_recall_mean = statistics.mean(l_recall10_vals) if l_recall10_vals else 0

        print(f"\n  Recall@10 at scale (sampled {len(v_recall10_vals)} queries):")
        print(f"  {'Provider':<30} {'Recall@10':>10}")
        print(f"  {'-'*42}")
        print(f"  {'Levara':<30} {v_recall_mean:>10.4f}")
        print(f"  {'LanceDB':<30} {l_recall_mean:>10.4f}")

        # Compare with baseline
        baseline_latency = self.__class__._latency_results.get("vectra", {})
        baseline_insert = self.__class__._insert_results

        self.__class__._scale_results = {
            "n_vectors": N_scale,
            "levara_insert_dps": v_insert_dps,
            "lance_insert_dps": l_insert_dps,
            "levara_search_mean": v_search_stats["mean"],
            "lance_search_mean": l_search_stats["mean"],
            "levara_recall10": v_recall_mean,
            "lance_recall10": l_recall_mean,
        }

        if baseline_latency and baseline_insert:
            v_lat_growth = ((v_search_stats["mean"] - baseline_latency.get("mean", 0))
                            / max(baseline_latency.get("mean", 1), 0.001)) * 100
            v_tp_change = ((v_insert_dps - baseline_insert.get("levara_dps", 0))
                           / max(baseline_insert.get("levara_dps", 1), 1)) * 100

            print(f"\n  Scale vs baseline ({N_base} → {N_scale} vectors, {MULTIPLIER}x):")
            print(f"  Levara latency change: {v_lat_growth:+.1f}%")
            print(f"  Levara throughput change: {v_tp_change:+.1f}%")

    # ── Step 10: Crash Recovery (Levara only) ───────────────────────────

    @pytest.mark.asyncio
    async def test_10_crash_recovery(self, book_data):
        """Step 10: Crash recovery — insert, restart container, verify data."""
        chunks, vecs, q_vecs = book_data

        print("\n" + "=" * 72)
        print(f"  STEP 10: CRASH RECOVERY (Levara only)")
        print("=" * 72)

        # Ensure Levara is healthy first
        if not await ensure_levara_healthy(max_wait=30):
            self.__class__._crash_results = {"status": "SKIP", "reason": "Levara not available"}
            pytest.skip("Levara not available for crash recovery test")

        # Insert recovery test data
        RECOVERY_N = 500
        recovery_ids = []

        async with aiohttp.ClientSession() as session:
            for start in range(0, min(RECOVERY_N, len(chunks)), 50):
                batch = []
                for i in range(start, min(start + 50, RECOVERY_N, len(chunks))):
                    rid = f"recovery:{uuid.uuid4()}"
                    recovery_ids.append(rid)
                    batch.append({
                        "id": rid,
                        "vector": vecs[i],
                        "metadata": {"text": chunks[i]["text"][:300],
                                     "chapter": chunks[i]["chapter"]},
                    })
                await levara_insert(session, batch)

        inserted = len(recovery_ids)
        print(f"  Inserted {inserted} records with 'recovery:' prefix")

        # Wait for WAL flush + HNSW index
        await asyncio.sleep(3)

        # Verify pre-restart search works
        async with aiohttp.ClientSession() as session:
            pre_results = await levara_search(session, vecs[0], k=50)
            pre_recovery = [r for r in pre_results if r.get("id", "").startswith("recovery:")]
            print(f"  Pre-restart: found {len(pre_recovery)} recovery records in top-50")

        # Restart Levara container
        print(f"  Restarting Levara container...")
        cwd = str(Path(__file__).parent.parent)
        restart_ok = False
        try:
            result = subprocess.run(
                ["docker", "compose", "restart", "levara"],
                capture_output=True, text=True, timeout=60,
                cwd=cwd,
            )
            if result.returncode != 0:
                for name in ["new_db-levara-1", "new-db-levara-1"]:
                    result = subprocess.run(
                        ["docker", "restart", name],
                        capture_output=True, text=True, timeout=60,
                    )
                    if result.returncode == 0:
                        break
            restart_ok = result.returncode == 0
            print(f"  Container restart: {'OK' if restart_ok else 'FAILED'}")
            if not restart_ok:
                print(f"  stderr: {result.stderr}")
                result = subprocess.run(
                    ["docker", "compose", "up", "-d", "levara"],
                    capture_output=True, text=True, timeout=60,
                    cwd=cwd,
                )
                restart_ok = result.returncode == 0
                print(f"  docker compose up: {'OK' if restart_ok else 'FAILED'}")
        except (subprocess.TimeoutExpired, FileNotFoundError) as e:
            print(f"  Container restart failed: {e}")

        if not restart_ok:
            self.__class__._crash_results = {"status": "SKIP", "reason": "container restart failed"}
            pytest.skip("Could not restart Levara container")

        # Wait for Levara to come back up (up to 60s)
        print(f"  Waiting for Levara to recover...")
        recovered = False
        for attempt in range(60):
            try:
                async with aiohttp.ClientSession() as session:
                    async with session.get(
                        f"{LEVARA_URL}/api/v1/info",
                        timeout=aiohttp.ClientTimeout(total=2),
                    ) as r:
                        if r.status == 200:
                            info = await r.json()
                            print(f"  Levara back online (attempt {attempt + 1}): {info}")
                            recovered = True
                            break
            except Exception:
                pass
            await asyncio.sleep(1)

        if not recovered:
            subprocess.run(
                ["docker", "compose", "up", "-d", "levara"],
                capture_output=True, text=True, timeout=60,
                cwd=cwd,
            )
            for attempt in range(30):
                try:
                    async with aiohttp.ClientSession() as session:
                        async with session.get(
                            f"{LEVARA_URL}/api/v1/info",
                            timeout=aiohttp.ClientTimeout(total=2),
                        ) as r:
                            if r.status == 200:
                                recovered = True
                                break
                except Exception:
                    pass
                await asyncio.sleep(1)

        if not recovered:
            self.__class__._crash_results = {"status": "FAIL", "reason": "Levara did not recover in 90s"}
            pytest.fail("Levara did not come back online after restart")

        # Allow WAL recovery + HNSW rebuild
        await asyncio.sleep(8)

        # Verify post-restart: search for same vectors
        found = 0
        crashed = False
        async with aiohttp.ClientSession() as session:
            for i in range(0, min(10, len(vecs))):
                results = await levara_search_safe(session, vecs[i], k=50)
                if not results and i == 0:
                    print(f"  WARNING: Levara crashed during post-restart search!")
                    print(f"  This indicates a DiskStore.Read bug in WAL recovery.")
                    crashed = True
                    break
                for r in results:
                    if r.get("id", "").startswith("recovery:"):
                        found += 1
                        break

        if crashed:
            self.__class__._crash_results = {
                "status": "CRASH",
                "inserted": inserted,
                "sampled": 0,
                "found": 0,
                "recovery_rate": 0,
                "note": "Levara panics on search after WAL recovery (DiskStore.Read makeslice)",
            }
            print(f"\n  {'Metric':<30} {'Value':>10}")
            print(f"  {'-'*42}")
            print(f"  {'Records inserted':<30} {inserted:>10}")
            print(f"  {'Post-restart search':<30} {'CRASH':>10}")
            print(f"  {'Recovery rate':<30} {'0%':>10}")
            print(f"\n  BUG FOUND: DiskStore.Read panics with 'makeslice: len out of range'")
            print(f"  after WAL recovery. Metadata offsets appear corrupted.")
        else:
            recovery_rate = found / 10 * 100
            self.__class__._crash_results = {
                "status": "OK",
                "inserted": inserted,
                "sampled": 10,
                "found": found,
                "recovery_rate": recovery_rate,
            }
            print(f"\n  {'Metric':<30} {'Value':>10}")
            print(f"  {'-'*42}")
            print(f"  {'Records inserted':<30} {inserted:>10}")
            print(f"  {'Sample queries':<30} {10:>10}")
            print(f"  {'Found after restart':<30} {found:>10}")
            print(f"  {'Recovery rate':<30} {recovery_rate:>9.0f}%")
            if recovery_rate < 80:
                print(f"\n  WARNING: Recovery rate {recovery_rate:.0f}% < 80%!")
                print(f"  Possible WAL corruption or incomplete replay.")

    # ── Step 11: Summary Report ───────────────────────────────────────────

    @pytest.mark.asyncio
    async def test_11_summary(self, book_data):
        """Final summary: comparison table across all 10 steps."""
        chunks, _, _ = book_data

        print("\n")
        print("=" * 80)
        print("  COMPREHENSIVE COMPARISON: Levara vs LanceDB")
        print(f"  Book: «Ураган» (Джанет Эдвардс), {len(chunks)} base chunks, dim={DIM}")
        print(f"  Embeddings: pplx-embed-context-v1-0.6b (real)")
        print("=" * 80)

        print(f"\n  {'#':<4} {'Test':<35} {'Levara':>12} {'LanceDB':>12} {'Winner':>10}")
        print(f"  {'-'*75}")

        # Step 2: Insert
        ir = self.__class__._insert_results
        if ir:
            c_val = f"{ir.get('levara_dps', 0):,.0f} dp/s"
            l_val = f"{ir.get('lance_dps', 0):,.0f} dp/s"
            winner = "Levara" if ir.get('levara_dps', 0) > ir.get('lance_dps', 0) else "LanceDB"
            print(f"  {'2':<4} {'Insert throughput':<35} {v_val:>12} {l_val:>12} {winner:>10}")

        # Step 3: Latency
        lr = self.__class__._latency_results
        if lr:
            v_val = f"{lr.get('vectra', {}).get('mean', 0):.1f} ms"
            l_val = f"{lr.get('lance', {}).get('mean', 0):.1f} ms"
            winner = "Levara" if lr.get('vectra', {}).get('mean', 999) < lr.get('lance', {}).get('mean', 999) else "LanceDB"
            print(f"  {'3':<4} {'Search latency (mean)':<35} {v_val:>12} {l_val:>12} {winner:>10}")

        # Step 4: QPS
        qr = self.__class__._qps_results
        if qr:
            c_val = f"{qr.get('levara_qps', 0):,.0f} qps"
            l_val = f"{qr.get('lance_qps', 0):,.0f} qps"
            winner = "Levara" if qr.get('levara_qps', 0) > qr.get('lance_qps', 0) else "LanceDB"
            print(f"  {'4':<4} {'Concurrent QPS':<35} {v_val:>12} {l_val:>12} {winner:>10}")

        # Step 5: Hit rate
        hr = self.__class__._hit_rate_results
        if hr:
            c_val = f"{hr.get('levara_rate', 0):.0%}"
            l_val = f"{hr.get('lance_rate', 0):.0%}"
            winner = "Levara" if hr.get('levara_rate', 0) >= hr.get('lance_rate', 0) else "LanceDB"
            print(f"  {'5':<4} {'Keyword hit rate':<35} {v_val:>12} {l_val:>12} {winner:>10}")

        # Step 6: Ranking
        rr = self.__class__._ranking_results
        if rr:
            c_val = f"{rr.get('levara_ndcg', 0):.4f}"
            l_val = f"{rr.get('lance_ndcg', 0):.4f}"
            winner = "Levara" if rr.get('levara_ndcg', 0) >= rr.get('lance_ndcg', 0) else "LanceDB"
            print(f"  {'6':<4} {'NDCG@10':<35} {v_val:>12} {l_val:>12} {winner:>10}")

            c_val = f"{rr.get('levara_mrr', 0):.4f}"
            l_val = f"{rr.get('lance_mrr', 0):.4f}"
            winner = "Levara" if rr.get('levara_mrr', 0) >= rr.get('lance_mrr', 0) else "LanceDB"
            print(f"  {'6':<4} {'MRR':<35} {v_val:>12} {l_val:>12} {winner:>10}")

            c_val = f"{rr.get('levara_recall10', 0):.4f}"
            l_val = f"{rr.get('lance_recall10', 0):.4f}"
            winner = "Levara" if rr.get('levara_recall10', 0) >= rr.get('lance_recall10', 0) else "LanceDB"
            print(f"  {'6':<4} {'Recall@10':<35} {v_val:>12} {l_val:>12} {winner:>10}")

        # Step 7: Concurrent R+W
        crw = self.__class__._concurrent_rw_results
        if crw:
            baseline_mean = crw.get("baseline", {}).get("mean", 0)
            loaded_mean = crw.get("loaded", {}).get("mean", 0)
            if baseline_mean > 0:
                overhead = ((loaded_mean - baseline_mean) / baseline_mean) * 100
                print(f"  {'7':<4} {'Write overhead on search':<35} {overhead:>+11.1f}% {'':>12} {'—':>10}")
            else:
                print(f"  {'7':<4} {'Write overhead on search':<35} {'N/A':>12} {'':>12} {'—':>10}")

        # Step 8: Isolation
        iso = self.__class__._isolation_results
        if iso:
            v_leak = iso.get("levara_leakage", 0)
            l_leak = iso.get("lance_leakage", 0)
            print(f"  {'8':<4} {'Cross-collection leakage':<35} {v_leak:>12} {l_leak:>12} {'LanceDB' if v_leak > l_leak else 'Tie':>10}")

        # Step 9: Scale
        sr = self.__class__._scale_results
        if sr:
            n = sr.get("n_vectors", 0)
            c_val = f"{sr.get('levara_search_mean', 0):.1f} ms"
            l_val = f"{sr.get('lance_search_mean', 0):.1f} ms"
            winner = "Levara" if sr.get('levara_search_mean', 999) < sr.get('lance_search_mean', 999) else "LanceDB"
            print(f"  {'9':<4} {'Scale search (' + str(n) + ' vecs)':<35} {v_val:>12} {l_val:>12} {winner:>10}")

        # Step 10: Crash recovery
        cr = self.__class__._crash_results
        if cr:
            status = cr.get("status", "N/A")
            if status == "OK":
                rate = cr.get("recovery_rate", 0)
                print(f"  {'10':<4} {'Crash recovery rate':<35} {rate:>11.0f}% {'N/A':>12} {'—':>10}")
            elif status == "CRASH":
                print(f"  {'10':<4} {'Crash recovery':<35} {'BUG:CRASH':>12} {'N/A':>12} {'—':>10}")
            else:
                print(f"  {'10':<4} {'Crash recovery':<35} {status:>12} {'N/A':>12} {'—':>10}")

        print(f"  {'-'*75}")

        print(f"""
  РЕКОМЕНДАЦИЯ:
  ┌──────────────────────────────────────────────────────────────────────┐
  │ Levara лучше когда:                                              │
  │   - Нужен отдельный сервер (microservice architecture)             │
  │   - Высокий concurrent throughput (HTTP pipelining)                │
  │   - WAL-based durability + crash recovery                          │
  │   - Масштабирование через шардирование                             │
  │                                                                    │
  │ LanceDB лучше когда:                                               │
  │   - In-process (no network overhead)                               │
  │   - Простота: pip install, нет Docker                              │
  │   - Native delete/prune (не только cache)                          │
  │   - Физическая изоляция коллекций (separate tables)                │
  └──────────────────────────────────────────────────────────────────────┘
""")
        print("=" * 80)
