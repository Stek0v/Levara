"""
Head-to-head: VectraDB vs LanceDB on real book data with real embeddings.

Same book «Ураган», same embed-server, same queries.
VectraDB: real Go HNSW server on :8080
LanceDB:  real Arrow file I/O (in-process)

Requires:
  - embed-server on :9001 (pplx-embed-context-v1-0.6b, dim=1024)
  - VectraDB on :8080 (dim=1024, 3 shards)

Run:
    pytest tests/test_book_head_to_head.py -v -s
"""

from __future__ import annotations

import asyncio
import json
import math
import statistics
import tempfile
import time
import uuid
from pathlib import Path
from typing import Dict, List

import aiohttp
import lancedb
from lancedb.pydantic import LanceModel, Vector
from pydantic import BaseModel

import pytest

# ── Constants ─────────────────────────────────────────────────────────────────

BOOK_PATH = Path(__file__).parent.parent / "Edvards_Dzanet_Uragan_r4_P61XH.txt"
EMBED_URL = "http://localhost:9001/v1/embeddings"
VECTRA_URL = "http://localhost:8080"
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
    reason="Need embed-server:9001 + VectraDB:8080",
)


# ── Fixtures ──────────────────────────────────────────────────────────────────

@pytest.fixture(scope="module")
def book_data():
    """Load book, chunk, embed once — shared between VectraDB and LanceDB."""
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
    return chunks, vecs, q_vecs


# ── VectraDB helpers ──────────────────────────────────────────────────────────

async def vectra_insert(session, records):
    async with session.post(f"{VECTRA_URL}/api/v1/batch_insert", json={"records": records}) as r:
        r.raise_for_status()
        return await r.json()


async def vectra_search(session, vector, k=K):
    async with session.post(f"{VECTRA_URL}/api/v1/search", json={"vector": vector, "k": k}) as r:
        r.raise_for_status()
        data = await r.json()
    return data.get("results", [])


# ══════════════════════════════════════════════════════════════════════════════


class TestBookHeadToHead:

    @pytest.mark.asyncio
    async def test_1_insert(self, book_data):
        """Insert: VectraDB (real Go server) vs LanceDB (real Arrow)."""
        chunks, vecs, _ = book_data
        N = len(chunks)

        print("\n" + "=" * 72)
        print(f"  1. INSERT THROUGHPUT  ({N} real book chunks, dim={DIM})")
        print(f"     Embeddings: pre-computed (same vectors for both)")
        print("=" * 72)

        # ── VectraDB ──
        async with aiohttp.ClientSession() as session:
            t0 = time.perf_counter()
            for start in range(0, N, 50):
                batch = [{
                    "id": f"book:{chunks[i]['id']}",
                    "vector": vecs[i],
                    "metadata": {"text": chunks[i]["text"][:500], "chapter": chunks[i]["chapter"]},
                } for i in range(start, min(start + 50, N))]
                await vectra_insert(session, batch)
            t_v = time.perf_counter() - t0

        # ── LanceDB ──
        with tempfile.TemporaryDirectory() as tmpdir:
            conn = await lancedb.connect_async(tmpdir)
            tbl = await conn.create_table("book", schema=LanceRecord, exist_ok=True)

            t0 = time.perf_counter()
            for start in range(0, N, 50):
                batch = [
                    LanceRecord(
                        id=chunks[i]["id"],
                        vector=vecs[i],
                        payload=LancePayload(id=chunks[i]["id"], text=chunks[i]["text"][:500], chapter=chunks[i]["chapter"]),
                    ) for i in range(start, min(start + 50, N))
                ]
                await tbl.merge_insert("id").when_matched_update_all().when_not_matched_insert_all().execute(batch)
            t_l = time.perf_counter() - t0

        v_dps, l_dps = N / t_v, N / t_l
        print(f"\n  {'Provider':<35} {'dp/s':>8}  {'total ms':>10}")
        print(f"  {'-'*58}")
        print(f"  {'VectraDB (standalone/WAL)':<35} {v_dps:>8,.0f}  {t_v*1000:>10.1f}")
        print(f"  {'LanceDB (real Arrow)':<35} {l_dps:>8,.0f}  {t_l*1000:>10.1f}")
        ratio = max(v_dps, l_dps) / min(v_dps, l_dps)
        winner = "VectraDB" if v_dps > l_dps else "LanceDB"
        print(f"\n  Winner: {winner} ({ratio:.1f}x)")

        # Store for later tests
        self.__class__._lance_tmpdir = tempfile.mkdtemp()
        conn2 = await lancedb.connect_async(self.__class__._lance_tmpdir)
        tbl2 = await conn2.create_table("book", schema=LanceRecord, exist_ok=True)
        for start in range(0, N, 50):
            batch = [
                LanceRecord(
                    id=chunks[i]["id"],
                    vector=vecs[i],
                    payload=LancePayload(id=chunks[i]["id"], text=chunks[i]["text"][:500], chapter=chunks[i]["chapter"]),
                ) for i in range(start, min(start + 50, N))
            ]
            await tbl2.merge_insert("id").when_matched_update_all().when_not_matched_insert_all().execute(batch)
        self.__class__._lance_tbl = tbl2

    @pytest.mark.asyncio
    async def test_2_search_latency(self, book_data):
        """Search latency: 15 semantic queries, 5 passes."""
        chunks, vecs, q_vecs = book_data

        print("\n" + "=" * 72)
        print(f"  2. SEARCH LATENCY  ({len(QUERIES)} queries × 5 passes, {len(chunks)} chunks)")
        print("=" * 72)

        # Allow async HNSW indexer to finish
        await asyncio.sleep(2)

        # ── VectraDB ──
        v_lats = []
        async with aiohttp.ClientSession() as session:
            for _ in range(5):
                for qv in q_vecs:
                    t0 = time.perf_counter()
                    await vectra_search(session, qv, k=K)
                    v_lats.append((time.perf_counter() - t0) * 1000)

        # ── LanceDB ──
        tbl = self.__class__._lance_tbl
        l_lats = []
        for _ in range(5):
            for qv in q_vecs:
                t0 = time.perf_counter()
                await tbl.vector_search(qv).limit(K).to_list()
                l_lats.append((time.perf_counter() - t0) * 1000)

        def stats(t):
            s = sorted(t)
            n = len(s)
            return s[n // 2], s[int(n * 0.95)], s[int(n * 0.99)], statistics.mean(t)

        vp50, vp95, vp99, vm = stats(v_lats)
        lp50, lp95, lp99, lm = stats(l_lats)

        print(f"\n  {'Provider':<35} {'p50':>7} {'p95':>7} {'p99':>7} {'mean':>7}  (ms)")
        print(f"  {'-'*72}")
        print(f"  {'VectraDB (standalone/WAL)':<35} {vp50:>7.1f} {vp95:>7.1f} {vp99:>7.1f} {vm:>7.1f}")
        print(f"  {'LanceDB (real Arrow)':<35} {lp50:>7.1f} {lp95:>7.1f} {lp99:>7.1f} {lm:>7.1f}")
        ratio = max(vm, lm) / min(vm, lm)
        winner = "VectraDB" if vm < lm else "LanceDB"
        print(f"\n  Winner: {winner} ({ratio:.1f}x faster)")

    @pytest.mark.asyncio
    async def test_3_concurrent_qps(self, book_data):
        """Concurrent QPS: all 15 queries at once."""
        chunks, vecs, q_vecs = book_data

        print("\n" + "=" * 72)
        print(f"  3. CONCURRENT QPS  ({len(QUERIES)} queries at once)")
        print("=" * 72)

        # ── VectraDB ──
        async with aiohttp.ClientSession() as session:
            t0 = time.perf_counter()
            await asyncio.gather(*[vectra_search(session, qv, k=K) for qv in q_vecs])
            t_v = time.perf_counter() - t0

        # ── LanceDB ──
        tbl = self.__class__._lance_tbl
        t0 = time.perf_counter()
        await asyncio.gather(*[tbl.vector_search(qv).limit(K).to_list() for qv in q_vecs])
        t_l = time.perf_counter() - t0

        qps_v, qps_l = len(q_vecs) / t_v, len(q_vecs) / t_l
        print(f"\n  {'Provider':<35} {'QPS':>8}  {'total ms':>10}")
        print(f"  {'-'*58}")
        print(f"  {'VectraDB (standalone/WAL)':<35} {qps_v:>8,.0f}  {t_v*1000:>10.1f}")
        print(f"  {'LanceDB (real Arrow)':<35} {qps_l:>8,.0f}  {t_l*1000:>10.1f}")
        ratio = max(qps_v, qps_l) / min(qps_v, qps_l)
        winner = "VectraDB" if qps_v > qps_l else "LanceDB"
        print(f"\n  Winner: {winner} ({ratio:.1f}x)")

    @pytest.mark.asyncio
    async def test_4_keyword_hit_rate(self, book_data):
        """Semantic search quality: keyword hit rate on 15 contextual queries."""
        chunks, vecs, q_vecs = book_data

        print("\n" + "=" * 72)
        print(f"  4. KEYWORD HIT RATE  ({len(QUERIES)} queries, k={K})")
        print("=" * 72)

        # ── VectraDB ──
        v_hits = 0
        async with aiohttp.ClientSession() as session:
            for cq, qv in zip(QUERIES, q_vecs):
                results = await vectra_search(session, qv, k=K)
                combined = " ".join(
                    json.loads(r.get("metadata", "{}")).get("text", "")
                    if isinstance(r.get("metadata"), str)
                    else r.get("metadata", {}).get("text", "")
                    for r in results
                ).lower()
                if any(kw.lower() in combined for kw in cq["keywords"]):
                    v_hits += 1

        # ── LanceDB ──
        tbl = self.__class__._lance_tbl
        l_hits = 0
        for cq, qv in zip(QUERIES, q_vecs):
            rows = await tbl.vector_search(qv).limit(K).to_list()
            combined = " ".join(
                r.get("payload", {}).get("text", "") if isinstance(r.get("payload"), dict)
                else json.loads(r.get("payload", "{}")).get("text", "") if isinstance(r.get("payload"), str)
                else ""
                for r in rows
            ).lower()
            if any(kw.lower() in combined for kw in cq["keywords"]):
                l_hits += 1

        v_rate = v_hits / len(QUERIES)
        l_rate = l_hits / len(QUERIES)

        print(f"\n  {'Provider':<35} {'Hits':>5} {'Rate':>8}")
        print(f"  {'-'*52}")
        print(f"  {'VectraDB (standalone/WAL)':<35} {v_hits:>3}/{len(QUERIES)}  {v_rate:>7.0%}")
        print(f"  {'LanceDB (real Arrow)':<35} {l_hits:>3}/{len(QUERIES)}  {l_rate:>7.0%}")

    @pytest.mark.asyncio
    async def test_5_summary(self, book_data):
        """Final comparison table."""
        chunks, _, _ = book_data
        print("\n" + "=" * 72)
        print(f"  ИТОГО: VectraDB vs LanceDB на реальной книге")
        print(f"  «Ураган» (Джанет Эдвардс), {len(chunks)} чанков, dim={DIM}")
        print(f"  Embeddings: pplx-embed-context-v1-0.6b (реальные)")
        print("=" * 72)
        print("""
  Оба движка работают с ОДИНАКОВЫМИ embedding-ами от одного
  и того же embed-server. Никаких моков — реальный текст,
  реальные вектора, реальные семантические запросы.

  VectraDB: Go HNSW + WAL + HTTP (standalone mode)
  LanceDB:  Rust Arrow + in-process (no network overhead)
""")
        print("=" * 72)
