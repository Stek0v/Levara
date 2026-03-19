"""
RAG Cases gap-closing tests: covers cases.md scenarios not yet tested.

Tests here target vector DB layer (no LLM required):
  1. Multi-hop retrieval (information from 2+ chunks)
  2. Noise robustness (garbage chunks don't degrade search)
  3. Needle-in-haystack (find unique chunk among 5000+)
  4. Multilingual queries (EN queries against RU data, mixed)
  5. Adversarial / typo robustness (misspelled queries)
  6. Chunking strategy comparison (fixed vs paragraph vs sentence)

Requires:
  - embed-server on :9001 (pplx-embed-context-v1-0.6b, dim=1024)
  - Cognevra on :8080 (dim=1024, 3 shards)

Run:
    pytest tests/test_rag_cases.py -v -s
"""

from __future__ import annotations

import asyncio
import json
import math
import random
import re
import statistics
import string
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
COGNEVRA_URL = "http://localhost:8080"
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


def chunk_by_paragraph(text: str) -> List[Dict]:
    """Split text by double newline -- paragraph-based chunking."""
    paragraphs = [p.strip() for p in text.split("\n\n") if p.strip()]
    chunks = []
    chapter = 0
    for para in paragraphs:
        if para.startswith("Глава ") and len(para) < 20:
            try:
                chapter = int(para.replace("Глава ", "").strip())
            except ValueError:
                pass
        if len(para) >= MIN_CHUNK_CHARS:
            chunks.append({"id": str(uuid.uuid4()), "text": para,
                           "chapter": chapter, "chunk_index": len(chunks)})
    return chunks


def chunk_by_sentence(text: str) -> List[Dict]:
    """Split text by sentence boundaries (. ! ?) and merge small ones."""
    sentences = re.split(r'(?<=[.!?])\s+', text)
    chunks, buffer, chapter = [], "", 0
    for sent in sentences:
        sent = sent.strip()
        if not sent:
            continue
        if sent.startswith("Глава ") and len(sent) < 20:
            try:
                chapter = int(sent.replace("Глава ", "").strip())
            except ValueError:
                pass
        if len(buffer) + len(sent) < MAX_CHUNK_CHARS:
            buffer = (buffer + " " + sent).strip() if buffer else sent
        else:
            if buffer and len(buffer) >= MIN_CHUNK_CHARS:
                chunks.append({"id": str(uuid.uuid4()), "text": buffer,
                               "chapter": chapter, "chunk_index": len(chunks)})
            buffer = sent
    if buffer and len(buffer) >= MIN_CHUNK_CHARS:
        chunks.append({"id": str(uuid.uuid4()), "text": buffer,
                       "chapter": chapter, "chunk_index": len(chunks)})
    return chunks


# ── Embedding client ──────────────────────────────────────────────────────────


async def embed_texts(session: aiohttp.ClientSession,
                      texts: List[str]) -> List[List[float]]:
    all_vecs = []
    for start in range(0, len(texts), BATCH_SIZE):
        batch = texts[start:start + BATCH_SIZE]
        async with session.post(
            EMBED_URL, json={"input": batch, "model": EMBED_MODEL}
        ) as resp:
            resp.raise_for_status()
            data = await resp.json()
        embeddings = sorted(data["data"], key=lambda x: x["index"])
        all_vecs.extend(e["embedding"] for e in embeddings)
    return all_vecs


# ── Cognevra helpers ──────────────────────────────────────────────────────────


async def cognevra_insert(session: aiohttp.ClientSession,
                        records: List[Dict]) -> Dict:
    async with session.post(
        f"{COGNEVRA_URL}/api/v1/batch_insert", json={"records": records}
    ) as r:
        r.raise_for_status()
        return await r.json()


async def cognevra_search(session: aiohttp.ClientSession,
                        vector: List[float], k: int = K) -> List[Dict]:
    async with session.post(
        f"{COGNEVRA_URL}/api/v1/search", json={"vector": vector, "k": k}
    ) as r:
        r.raise_for_status()
        data = await r.json()
    return data.get("results", [])


async def cognevra_delete(session: aiohttp.ClientSession,
                        ids: List[str]) -> Dict:
    async with session.post(
        f"{COGNEVRA_URL}/api/v1/delete", json={"ids": ids}
    ) as r:
        r.raise_for_status()
        return await r.json()


def _extract_text(result: Dict) -> str:
    """Extract text from Cognevra or LanceDB result."""
    meta = result.get("metadata", result.get("payload", {}))
    if isinstance(meta, str):
        try:
            meta = json.loads(meta)
        except (json.JSONDecodeError, TypeError):
            return ""
    if isinstance(meta, dict):
        return meta.get("text", "")
    return ""


# ── Math helpers ──────────────────────────────────────────────────────────────


def cosine_similarity(a: List[float], b: List[float]) -> float:
    dot_prod = sum(x * y for x, y in zip(a, b))
    na = math.sqrt(sum(x * x for x in a))
    nb = math.sqrt(sum(x * x for x in b))
    if na == 0 or nb == 0:
        return 0.0
    return dot_prod / (na * nb)


def brute_force_topk(query_vec: List[float], all_vecs: List[List[float]],
                     k: int) -> List[Tuple[int, float]]:
    sims = [(i, cosine_similarity(query_vec, v)) for i, v in enumerate(all_vecs)]
    sims.sort(key=lambda x: x[1], reverse=True)
    return sims[:k]


# ── Original queries (from test_comprehensive_comparison.py) ─────────────────

QUERIES = [
    {"query": "телепат Эмбер способности чтение мыслей разум",
     "keywords": ["телепат", "Эмбер", "разум"], "desc": "Телепат Эмбер"},
    {"query": "Лукас командир тактик партнёр Эмбер",
     "keywords": ["Лукас"], "desc": "Лукас -- командир"},
    {"query": "город улей сто миллионов жителей уровни",
     "keywords": ["улей", "уровн", "миллион"], "desc": "Город-улей"},
    {"query": "лотерея профессии назначение работы 2532",
     "keywords": ["лотере"], "desc": "Лотерея профессий"},
    {"query": "телепаты никогда не должны встречаться запрет",
     "keywords": ["телепат", "встреч", "не должн"], "desc": "Запрет на встречу"},
    {"query": "ударная группа преследование преступников рейд",
     "keywords": ["ударн", "групп"], "desc": "Ударная группа"},
    {"query": "морская ферма океан внешка шторм волны",
     "keywords": ["ферм", "мор"], "desc": "Морская ферма"},
    {"query": "Зак отравление матрас химикаты ядовитые пары",
     "keywords": ["Зак", "матрас"], "desc": "Отравление Зака"},
    {"query": "Меган командир вина ответственность приказ",
     "keywords": ["Меган"], "desc": "Вина Меган"},
    {"query": "ментальный зуд предупреждение телепатическое чувство",
     "keywords": ["ментальн", "зуд"], "desc": "Ментальный зуд"},
    {"query": "Адика безопасность проверка охрана защита",
     "keywords": ["Адика"], "desc": "Адика -- охранник"},
    {"query": "импринтинг вложение знаний обучение память",
     "keywords": ["импринтинг"], "desc": "Импринтинг"},
    {"query": "ураган ветер буря дыхание перемены мир изменился",
     "keywords": ["ураган", "ветр"], "desc": "Ураган -- символ"},
    {"query": "нательная броня передатчик снаряжение экипировка",
     "keywords": ["брон", "передатчик"], "desc": "Снаряжение"},
    {"query": "Мортон секрет чтение мысли скрывать",
     "keywords": ["Мортон"], "desc": "Мортон"},
]

# ── Multi-hop queries (require info from 2+ chunks) ─────────────────────────

MULTIHOP_QUERIES = [
    {
        "query": "Какие телепатические способности проявляет Эмбер в городе-улье?",
        "required_keywords": [["телепат", "Эмбер"], ["улей", "город"]],
        "desc": "Телепат + город-улей (2 темы)",
    },
    {
        "query": "Как лотерея профессий влияет на членов ударной группы?",
        "required_keywords": [["лотере"], ["ударн", "групп"]],
        "desc": "Лотерея + ударная группа (2 темы)",
    },
    {
        "query": "Какое снаряжение используют телепаты во время рейдов?",
        "required_keywords": [["брон", "передатчик", "снаряж"], ["телепат"]],
        "desc": "Снаряжение + телепаты (2 темы)",
    },
    {
        "query": "Импринтинг знаний и ментальный зуд -- как они связаны?",
        "required_keywords": [["импринтинг"], ["ментальн", "зуд"]],
        "desc": "Импринтинг + ментальный зуд (2 темы)",
    },
    {
        "query": "Как Лукас и Меган командуют группой на морской ферме?",
        "required_keywords": [["Лукас", "Меган"], ["ферм", "мор"]],
        "desc": "Командиры + морская ферма (2 темы)",
    },
]

# ── English / mixed-language queries ─────────────────────────────────────────

EN_QUERIES = [
    {"query": "telepath abilities mind reading mental powers",
     "keywords": ["телепат"], "desc": "EN: telepathy"},
    {"query": "hive city hundred million inhabitants levels underground",
     "keywords": ["улей", "уровн"], "desc": "EN: hive city"},
    {"query": "profession lottery assignment work year 2532",
     "keywords": ["лотере"], "desc": "EN: profession lottery"},
    {"query": "strike team pursuit criminals raid mission",
     "keywords": ["ударн", "групп"], "desc": "EN: strike team"},
    {"query": "hurricane wind storm breath of change world transformed",
     "keywords": ["ураган", "ветр"], "desc": "EN: hurricane symbol"},
]

MIXED_QUERIES = [
    {"query": "телепат Эмбер mind reading abilities",
     "keywords": ["телепат", "Эмбер"], "desc": "MIX: телепат + mind reading"},
    {"query": "город-улей hive city миллионов inhabitants",
     "keywords": ["улей", "миллион"], "desc": "MIX: город-улей + hive"},
    {"query": "ударная группа strike team рейд pursuit",
     "keywords": ["ударн", "групп"], "desc": "MIX: ударная группа + strike"},
]

# ── Adversarial / typo queries ───────────────────────────────────────────────

TYPO_QUERIES = [
    {"query": "телепта Эмбре способности чтенеи мыслей",
     "keywords": ["телепат", "Эмбер", "разум"], "desc": "Typo: телепат"},
    {"query": "Лукса командир тактки партнёр",
     "keywords": ["Лукас"], "desc": "Typo: Лукас"},
    {"query": "горд улей сто миллонов жителей урвни",
     "keywords": ["улей", "уровн", "миллион"], "desc": "Typo: город"},
    {"query": "лотреея профсесии назанчение рабтоы",
     "keywords": ["лотере"], "desc": "Typo: лотерея"},
    {"query": "удрная групап преследвоание преступнкиов",
     "keywords": ["ударн", "групп"], "desc": "Typo: ударная"},
    {"query": "мрская ферам окена шторм влоны",
     "keywords": ["ферм", "мор"], "desc": "Typo: морская"},
    {"query": "Зка отравлнеие мтрас химкаты",
     "keywords": ["Зак", "матрас"], "desc": "Typo: Зак"},
    {"query": "Мегна комнадир вниа отвтственность",
     "keywords": ["Меган"], "desc": "Typo: Меган"},
    {"query": "менатльный зду предупрежднеие",
     "keywords": ["ментальн", "зуд"], "desc": "Typo: ментальный"},
    {"query": "импринтигн вложнеие знаинй обучнеие пмять",
     "keywords": ["импринтинг"], "desc": "Typo: импринтинг"},
]

TRANSLIT_QUERIES = [
    {"query": "telepat Ember sposobnosti chteniye mysley razum",
     "keywords": ["телепат", "Эмбер"], "desc": "Translit: телепат Эмбер"},
    {"query": "gorod uley sto millionov zhiteley urovni",
     "keywords": ["улей", "уровн"], "desc": "Translit: город улей"},
    {"query": "lotereya professiy naznacheniye raboty",
     "keywords": ["лотере"], "desc": "Translit: лотерея профессий"},
    {"query": "udarnaya gruppa presledovaniye prestupnikov reyd",
     "keywords": ["ударн", "групп"], "desc": "Translit: ударная группа"},
    {"query": "uragan veter burya dykhaniye peremen",
     "keywords": ["ураган", "ветр"], "desc": "Translit: ураган"},
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
    not (_check("http://localhost:9001/health")
         and _check("http://localhost:8080/metrics")),
    reason="Need embed-server:9001 + Cognevra:8080",
)


# ── Fixtures ──────────────────────────────────────────────────────────────────


@pytest.fixture(scope="module")
def book_data():
    """Load book, chunk, embed -- shared across all tests."""
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

    print(f"\n  Book data: {len(chunks)} chunks, dim={DIM}, "
          f"{len(QUERIES)} queries")
    return chunks, vecs, q_vecs


@pytest.fixture(scope="module")
def inserted_data(book_data):
    """Insert book data into Cognevra and LanceDB, return handles."""
    chunks, vecs, q_vecs = book_data
    N = len(chunks)

    async def _insert_vectra():
        async with aiohttp.ClientSession() as session:
            for start in range(0, N, 50):
                batch = [{
                    "id": f"rag:{chunks[i]['id']}",
                    "vector": vecs[i],
                    "metadata": {"text": chunks[i]["text"][:500],
                                 "chapter": chunks[i]["chapter"]},
                } for i in range(start, min(start + 50, N))]
                await cognevra_insert(session, batch)
        # Wait for HNSW indexer
        await asyncio.sleep(2)

    asyncio.run(_insert_vectra())

    # LanceDB
    tmpdir = tempfile.mkdtemp()

    async def _insert_lance():
        conn = await lancedb.connect_async(tmpdir)
        tbl = await conn.create_table(
            "rag", schema=LanceRecord, exist_ok=True
        )
        for start in range(0, N, 50):
            batch = [
                LanceRecord(
                    id=chunks[i]["id"],
                    vector=vecs[i],
                    payload=LancePayload(
                        id=chunks[i]["id"],
                        text=chunks[i]["text"][:500],
                        chapter=chunks[i]["chapter"],
                    ),
                ) for i in range(start, min(start + 50, N))
            ]
            await tbl.merge_insert("id") \
                .when_matched_update_all() \
                .when_not_matched_insert_all() \
                .execute(batch)
        return tbl

    lance_tbl = asyncio.run(_insert_lance())

    print(f"  Inserted {N} chunks into Cognevra and LanceDB")
    return {
        "chunks": chunks,
        "vecs": vecs,
        "q_vecs": q_vecs,
        "lance_tbl": lance_tbl,
        "lance_tmpdir": tmpdir,
    }


# ══════════════════════════════════════════════════════════════════════════════

def _lance_text(row: Dict) -> str:
    """Extract text from a LanceDB search result row."""
    payload = row.get("payload", {})
    if isinstance(payload, str):
        try:
            payload = json.loads(payload)
        except (json.JSONDecodeError, TypeError):
            return ""
    if isinstance(payload, dict):
        return payload.get("text", "")
    return ""


class TestRAGCases:
    """Tests closing gaps from cases.md at the vector DB level."""

    # ── Test 1: Multi-hop Retrieval ───────────────────────────────────────

    @pytest.mark.asyncio
    async def test_01_multihop_retrieval(self, inserted_data):
        """Multi-hop: queries requiring info from 2+ different topic chunks.

        For each query we define 2+ required keyword groups. Each group
        represents a distinct "hop" (topic). We check that ALL groups
        have at least one matching chunk in top-K results.

        Metric: multi-hop recall = queries where ALL hops found / total.
        """
        lance_tbl = inserted_data["lance_tbl"]

        print("\n" + "=" * 72)
        print(f"  TEST 1: MULTI-HOP RETRIEVAL  "
              f"({len(MULTIHOP_QUERIES)} queries, k={K})")
        print("=" * 72)

        # Embed multi-hop queries
        async with aiohttp.ClientSession() as session:
            mh_vecs = await embed_texts(
                session, [q["query"] for q in MULTIHOP_QUERIES]
            )

        v_full_hits, l_full_hits = 0, 0

        for q, qv in zip(MULTIHOP_QUERIES, mh_vecs):
            # -- Cognevra --
            async with aiohttp.ClientSession() as session:
                v_results = await cognevra_search(session, qv, k=K)
            v_text = " ".join(
                _extract_text(r) for r in v_results
            ).lower()

            v_hops_found = sum(
                1 for kw_group in q["required_keywords"]
                if any(kw.lower() in v_text for kw in kw_group)
            )
            if v_hops_found == len(q["required_keywords"]):
                v_full_hits += 1

            # -- LanceDB --
            rows = await lance_tbl.vector_search(qv).limit(K).to_list()
            l_text = " ".join(_lance_text(r) for r in rows).lower()

            l_hops_found = sum(
                1 for kw_group in q["required_keywords"]
                if any(kw.lower() in l_text for kw in kw_group)
            )
            if l_hops_found == len(q["required_keywords"]):
                l_full_hits += 1

            total_hops = len(q["required_keywords"])
            status_v = "ALL" if v_hops_found == total_hops \
                else f"{v_hops_found}/{total_hops}"
            status_l = "ALL" if l_hops_found == total_hops \
                else f"{l_hops_found}/{total_hops}"
            print(f"  {q['desc']:<45} V:{status_v}  L:{status_l}")

        n = len(MULTIHOP_QUERIES)
        v_rate = v_full_hits / n
        l_rate = l_full_hits / n

        print(f"\n  {'Provider':<35} {'Full hits':>10} {'Rate':>8}")
        print(f"  {'-'*58}")
        print(f"  {'Cognevra':<35} {v_full_hits:>5}/{n}    {v_rate:>7.0%}")
        print(f"  {'LanceDB':<35} {l_full_hits:>5}/{n}    {l_rate:>7.0%}")

        # At least some multi-hop queries should succeed
        assert v_full_hits + l_full_hits > 0, \
            "No multi-hop queries succeeded on either engine"

    # ── Test 2: Noise Robustness ──────────────────────────────────────────

    @pytest.mark.asyncio
    async def test_02_noise_robustness(self, inserted_data):
        """Noise robustness: add garbage chunks, verify search quality holds.

        1. Measure baseline hit rate (clean data, already inserted)
        2. Insert 500 noise chunks (random text + foreign language)
        3. Re-measure hit rate
        4. Metric: noise_degradation = (clean - noisy) / clean
        """
        q_vecs = inserted_data["q_vecs"]

        print("\n" + "=" * 72)
        print(f"  TEST 2: NOISE ROBUSTNESS  "
              f"({len(QUERIES)} queries, 500 noise chunks)")
        print("=" * 72)

        # -- Baseline hit rate (Cognevra) --
        v_hits_clean = 0
        async with aiohttp.ClientSession() as session:
            for cq, qv in zip(QUERIES, q_vecs):
                results = await cognevra_search(session, qv, k=K)
                combined = " ".join(
                    _extract_text(r) for r in results
                ).lower()
                if any(kw.lower() in combined for kw in cq["keywords"]):
                    v_hits_clean += 1

        clean_rate = v_hits_clean / len(QUERIES)
        print(f"  Baseline hit rate (clean):  "
              f"{v_hits_clean}/{len(QUERIES)} = {clean_rate:.0%}")

        # -- Generate and insert noise chunks --
        noise_texts = []
        rng = random.Random(42)
        for i in range(500):
            if i % 3 == 0:
                # Random ASCII gibberish
                noise = "".join(
                    rng.choices(string.ascii_letters + " ", k=300)
                )
            elif i % 3 == 1:
                # English Wikipedia-like noise
                topics = [
                    "quantum physics", "medieval history",
                    "tropical fish", "jazz music theory",
                    "volcanic eruptions", "space tourism",
                ]
                noise = (
                    f"Article about {rng.choice(topics)}: "
                    + "".join(
                        rng.choices(string.ascii_lowercase + " ", k=250)
                    )
                )
            else:
                # Numeric noise
                noise = " ".join(
                    str(rng.randint(0, 99999)) for _ in range(50)
                )
            noise_texts.append(noise)

        async with aiohttp.ClientSession() as session:
            noise_vecs = await embed_texts(session, noise_texts)

        noise_ids = []
        async with aiohttp.ClientSession() as session:
            for start in range(0, len(noise_texts), 50):
                batch = []
                for i in range(start, min(start + 50, len(noise_texts))):
                    nid = f"noise:{uuid.uuid4()}"
                    noise_ids.append(nid)
                    batch.append({
                        "id": nid,
                        "vector": noise_vecs[i],
                        "metadata": {
                            "text": noise_texts[i][:500],
                            "chapter": -1,
                        },
                    })
                await cognevra_insert(session, batch)

        await asyncio.sleep(2)  # Let HNSW index

        # -- Noisy hit rate --
        v_hits_noisy = 0
        async with aiohttp.ClientSession() as session:
            for cq, qv in zip(QUERIES, q_vecs):
                results = await cognevra_search(session, qv, k=K)
                combined = " ".join(
                    _extract_text(r) for r in results
                ).lower()
                if any(kw.lower() in combined for kw in cq["keywords"]):
                    v_hits_noisy += 1

        noisy_rate = v_hits_noisy / len(QUERIES)
        print(f"  Noisy hit rate (+500 noise): "
              f"{v_hits_noisy}/{len(QUERIES)} = {noisy_rate:.0%}")

        if clean_rate > 0:
            degradation = (clean_rate - noisy_rate) / clean_rate
        else:
            degradation = 0.0

        print(f"  Noise degradation: {degradation:.1%}")

        # -- Cleanup noise --
        async with aiohttp.ClientSession() as session:
            for start in range(0, len(noise_ids), 50):
                batch_ids = noise_ids[start:start + 50]
                try:
                    await cognevra_delete(session, batch_ids)
                except Exception:
                    pass  # Best-effort cleanup

        assert degradation < 0.25, \
            f"Noise degradation {degradation:.0%} exceeds 25% threshold"

    # ── Test 3: Needle in Haystack ────────────────────────────────────────

    @pytest.mark.asyncio
    async def test_03_needle_in_haystack(self, inserted_data):
        """Needle in haystack: hide a unique chunk among many vectors.

        1. Duplicate existing book chunks 3x with different IDs
        2. Insert a unique "needle" chunk with a specific keyword
        3. Search for that keyword
        4. Verify needle is found in top-1, top-5, top-10
        """
        chunks = inserted_data["chunks"]
        vecs = inserted_data["vecs"]

        print("\n" + "=" * 72)
        print(f"  TEST 3: NEEDLE IN HAYSTACK  "
              f"(base={len(chunks)} chunks, 3x padding)")
        print("=" * 72)

        NEEDLE_TEXT = (
            "Секретный протокол Феникс-7 был активирован ровно в полночь. "
            "Координаты базы: широта 73.215, долгота минус 42.887. "
            "Код доступа: ZETA-OMEGA-9. Только агент Валькирия имеет допуск."
        )
        NEEDLE_KEYWORD = "Феникс"

        # Embed the needle
        async with aiohttp.ClientSession() as session:
            needle_vec = (await embed_texts(session, [NEEDLE_TEXT]))[0]
            needle_query_vec = (await embed_texts(
                session,
                [f"секретный протокол {NEEDLE_KEYWORD} координаты база"]
            ))[0]

        # Insert padding (duplicated book chunks with different IDs)
        padding_ids = []
        async with aiohttp.ClientSession() as session:
            for copy_idx in range(3):
                for start in range(0, len(chunks), 50):
                    batch = []
                    for i in range(start, min(start + 50, len(chunks))):
                        pid = f"pad{copy_idx}:{uuid.uuid4()}"
                        padding_ids.append(pid)
                        batch.append({
                            "id": pid,
                            "vector": vecs[i],
                            "metadata": {
                                "text": chunks[i]["text"][:500],
                                "chapter": chunks[i]["chapter"],
                            },
                        })
                    await cognevra_insert(session, batch)

        # Insert needle
        needle_id = f"needle:{uuid.uuid4()}"
        async with aiohttp.ClientSession() as session:
            await cognevra_insert(session, [{
                "id": needle_id,
                "vector": needle_vec,
                "metadata": {"text": NEEDLE_TEXT, "chapter": 99},
            }])

        total_vecs = len(chunks) + len(padding_ids) + 1
        print(f"  Total vectors: {total_vecs} "
              f"(book={len(chunks)}, padding={len(padding_ids)}, needle=1)")

        await asyncio.sleep(3)  # Let HNSW index all new vectors

        # -- Search for needle (Cognevra) --
        async with aiohttp.ClientSession() as session:
            results = await cognevra_search(session, needle_query_vec, k=K)

        found_positions = []
        for i, r in enumerate(results):
            txt = _extract_text(r)
            if NEEDLE_KEYWORD in txt:
                found_positions.append(i + 1)  # 1-indexed

        in_top1 = any(p == 1 for p in found_positions)
        in_top5 = any(p <= 5 for p in found_positions)
        in_top10 = any(p <= 10 for p in found_positions)

        print(f"  Cognevra needle positions: "
              f"{found_positions or 'NOT FOUND'}")
        print(f"  In top-1:  {'YES' if in_top1 else 'NO'}")
        print(f"  In top-5:  {'YES' if in_top5 else 'NO'}")
        print(f"  In top-10: {'YES' if in_top10 else 'NO'}")

        # Also test on LanceDB
        lance_tmpdir2 = tempfile.mkdtemp()
        conn = await lancedb.connect_async(lance_tmpdir2)
        tbl2 = await conn.create_table(
            "haystack", schema=LanceRecord, exist_ok=True
        )

        # Insert all original chunks + padding + needle
        for start in range(0, len(chunks), 50):
            batch = [
                LanceRecord(
                    id=chunks[i]["id"],
                    vector=vecs[i],
                    payload=LancePayload(
                        id=chunks[i]["id"],
                        text=chunks[i]["text"][:500],
                        chapter=chunks[i]["chapter"],
                    ),
                ) for i in range(start, min(start + 50, len(chunks)))
            ]
            await tbl2.merge_insert("id").when_matched_update_all() \
                .when_not_matched_insert_all().execute(batch)

        for copy_idx in range(3):
            for start in range(0, len(chunks), 50):
                batch = [
                    LanceRecord(
                        id=f"lpad{copy_idx}_{chunks[i]['id']}",
                        vector=vecs[i],
                        payload=LancePayload(
                            id=f"lpad{copy_idx}_{chunks[i]['id']}",
                            text=chunks[i]["text"][:500],
                            chapter=chunks[i]["chapter"],
                        ),
                    ) for i in range(start, min(start + 50, len(chunks)))
                ]
                await tbl2.merge_insert("id").when_matched_update_all() \
                    .when_not_matched_insert_all().execute(batch)

        needle_lance_id = f"needle_{uuid.uuid4()}"
        await tbl2.merge_insert("id").when_matched_update_all() \
            .when_not_matched_insert_all().execute([
                LanceRecord(
                    id=needle_lance_id,
                    vector=needle_vec,
                    payload=LancePayload(
                        id=needle_lance_id,
                        text=NEEDLE_TEXT,
                        chapter=99,
                    ),
                )
            ])

        lance_results = await tbl2.vector_search(
            needle_query_vec
        ).limit(K).to_list()
        l_found = []
        for i, r in enumerate(lance_results):
            txt = _lance_text(r)
            if NEEDLE_KEYWORD in txt:
                l_found.append(i + 1)

        l_in_top10 = any(p <= 10 for p in l_found)
        print(f"\n  LanceDB needle positions: {l_found or 'NOT FOUND'}")
        print(f"  LanceDB in top-10: {'YES' if l_in_top10 else 'NO'}")

        # -- Cleanup padding + needle from Cognevra --
        async with aiohttp.ClientSession() as session:
            all_cleanup = padding_ids + [needle_id]
            for start in range(0, len(all_cleanup), 50):
                try:
                    await cognevra_delete(
                        session, all_cleanup[start:start + 50]
                    )
                except Exception:
                    pass

        assert in_top10 or l_in_top10, \
            "Needle not found in top-10 by either engine"

    # ── Test 4: Multilingual Queries ──────────────────────────────────────

    @pytest.mark.asyncio
    async def test_04_multilingual_queries(self, inserted_data):
        """Multilingual: EN-only and RU+EN mixed queries against RU data.

        Measures cross-lingual retrieval gap:
        - RU queries -> RU data (baseline, expected high)
        - EN queries -> RU data (cross-lingual, expected lower)
        - Mixed queries -> RU data (partial overlap)
        """
        q_vecs = inserted_data["q_vecs"]
        lance_tbl = inserted_data["lance_tbl"]

        print("\n" + "=" * 72)
        print(f"  TEST 4: MULTILINGUAL QUERIES")
        print(f"    RU: {len(QUERIES)}, EN: {len(EN_QUERIES)}, "
              f"Mixed: {len(MIXED_QUERIES)}")
        print("=" * 72)

        # Embed EN and mixed queries
        async with aiohttp.ClientSession() as session:
            en_vecs = await embed_texts(
                session, [q["query"] for q in EN_QUERIES]
            )
            mixed_vecs = await embed_texts(
                session, [q["query"] for q in MIXED_QUERIES]
            )

        async def measure_hit_rate(queries, query_vecs, label):
            v_hits, l_hits = 0, 0
            async with aiohttp.ClientSession() as session:
                for cq, qv in zip(queries, query_vecs):
                    # Cognevra
                    results = await cognevra_search(session, qv, k=K)
                    v_text = " ".join(
                        _extract_text(r) for r in results
                    ).lower()
                    if any(kw.lower() in v_text for kw in cq["keywords"]):
                        v_hits += 1

                    # LanceDB
                    rows = await lance_tbl.vector_search(
                        qv
                    ).limit(K).to_list()
                    l_text = " ".join(
                        _lance_text(r) for r in rows
                    ).lower()
                    if any(kw.lower() in l_text for kw in cq["keywords"]):
                        l_hits += 1

            n = len(queries)
            v_rate = v_hits / n if n else 0
            l_rate = l_hits / n if n else 0
            print(f"  {label:<20} Cognevra: {v_hits}/{n} ({v_rate:.0%})  "
                  f"LanceDB: {l_hits}/{n} ({l_rate:.0%})")
            return v_rate, l_rate

        ru_v, ru_l = await measure_hit_rate(QUERIES, q_vecs, "RU -> RU")
        en_v, en_l = await measure_hit_rate(EN_QUERIES, en_vecs, "EN -> RU")
        mx_v, mx_l = await measure_hit_rate(
            MIXED_QUERIES, mixed_vecs, "Mixed -> RU"
        )

        # Cross-lingual gap
        if ru_v > 0:
            gap_v = (ru_v - en_v) / ru_v
            print(f"\n  Cross-lingual gap (Cognevra): {gap_v:.0%}")
        if ru_l > 0:
            gap_l = (ru_l - en_l) / ru_l
            print(f"  Cross-lingual gap (LanceDB):  {gap_l:.0%}")

        print(f"\n  Expected: RU > Mixed >= EN")
        print(f"  Cognevra: {ru_v:.0%} > {mx_v:.0%} >= {en_v:.0%}")
        print(f"  LanceDB:  {ru_l:.0%} > {mx_l:.0%} >= {en_l:.0%}")

        # RU baseline should be decent
        assert ru_v >= 0.5 or ru_l >= 0.5, \
            "RU baseline hit rate too low on both engines"

    # ── Test 5: Adversarial / Typo Robustness ─────────────────────────────

    @pytest.mark.asyncio
    async def test_05_adversarial_typo_robustness(self, inserted_data):
        """Adversarial inputs: typos, transliteration.

        Measures:
        - typo_degradation = clean_hit_rate - typo_hit_rate
        - translit_degradation = clean_hit_rate - translit_hit_rate
        """
        q_vecs = inserted_data["q_vecs"]
        lance_tbl = inserted_data["lance_tbl"]

        print("\n" + "=" * 72)
        print(f"  TEST 5: ADVERSARIAL / TYPO ROBUSTNESS")
        print(f"    Typo: {len(TYPO_QUERIES)}, "
              f"Translit: {len(TRANSLIT_QUERIES)}")
        print("=" * 72)

        # -- Baseline (clean RU queries) --
        v_hits_clean, l_hits_clean = 0, 0
        async with aiohttp.ClientSession() as session:
            for cq, qv in zip(QUERIES, q_vecs):
                results = await cognevra_search(session, qv, k=K)
                combined = " ".join(
                    _extract_text(r) for r in results
                ).lower()
                if any(kw.lower() in combined for kw in cq["keywords"]):
                    v_hits_clean += 1

                rows = await lance_tbl.vector_search(
                    qv
                ).limit(K).to_list()
                l_combined = " ".join(
                    _lance_text(r) for r in rows
                ).lower()
                if any(kw.lower() in l_combined for kw in cq["keywords"]):
                    l_hits_clean += 1

        clean_v = v_hits_clean / len(QUERIES)
        clean_l = l_hits_clean / len(QUERIES)
        print(f"  Baseline (clean):    V={clean_v:.0%}  L={clean_l:.0%}")

        # -- Typo queries --
        async with aiohttp.ClientSession() as session:
            typo_vecs = await embed_texts(
                session, [q["query"] for q in TYPO_QUERIES]
            )

        v_hits_typo, l_hits_typo = 0, 0
        async with aiohttp.ClientSession() as session:
            for cq, qv in zip(TYPO_QUERIES, typo_vecs):
                results = await cognevra_search(session, qv, k=K)
                combined = " ".join(
                    _extract_text(r) for r in results
                ).lower()
                if any(kw.lower() in combined for kw in cq["keywords"]):
                    v_hits_typo += 1

                rows = await lance_tbl.vector_search(
                    qv
                ).limit(K).to_list()
                l_combined = " ".join(
                    _lance_text(r) for r in rows
                ).lower()
                if any(kw.lower() in l_combined for kw in cq["keywords"]):
                    l_hits_typo += 1

        typo_v = v_hits_typo / len(TYPO_QUERIES)
        typo_l = l_hits_typo / len(TYPO_QUERIES)
        print(f"  Typo queries:        V={typo_v:.0%}  L={typo_l:.0%}")

        # -- Transliteration queries --
        async with aiohttp.ClientSession() as session:
            translit_vecs = await embed_texts(
                session, [q["query"] for q in TRANSLIT_QUERIES]
            )

        v_hits_tr, l_hits_tr = 0, 0
        async with aiohttp.ClientSession() as session:
            for cq, qv in zip(TRANSLIT_QUERIES, translit_vecs):
                results = await cognevra_search(session, qv, k=K)
                combined = " ".join(
                    _extract_text(r) for r in results
                ).lower()
                if any(kw.lower() in combined for kw in cq["keywords"]):
                    v_hits_tr += 1

                rows = await lance_tbl.vector_search(
                    qv
                ).limit(K).to_list()
                l_combined = " ".join(
                    _lance_text(r) for r in rows
                ).lower()
                if any(kw.lower() in l_combined for kw in cq["keywords"]):
                    l_hits_tr += 1

        translit_v = v_hits_tr / len(TRANSLIT_QUERIES)
        translit_l = l_hits_tr / len(TRANSLIT_QUERIES)
        print(f"  Translit queries:    V={translit_v:.0%}  "
              f"L={translit_l:.0%}")

        # -- Degradation metrics --
        if clean_v > 0:
            typo_deg_v = (clean_v - typo_v) / clean_v
            translit_deg_v = (clean_v - translit_v) / clean_v
        else:
            typo_deg_v = translit_deg_v = 0

        if clean_l > 0:
            typo_deg_l = (clean_l - typo_l) / clean_l
            translit_deg_l = (clean_l - translit_l) / clean_l
        else:
            typo_deg_l = translit_deg_l = 0

        print(f"\n  {'Metric':<30} {'Cognevra':>10} {'LanceDB':>10}")
        print(f"  {'-'*55}")
        print(f"  {'Typo degradation':<30} "
              f"{typo_deg_v:>9.0%} {typo_deg_l:>9.0%}")
        print(f"  {'Translit degradation':<30} "
              f"{translit_deg_v:>9.0%} {translit_deg_l:>9.0%}")

    # ── Test 6: Chunking Strategy Comparison ──────────────────────────────

    @pytest.mark.asyncio
    async def test_06_chunking_strategy_comparison(self, book_data):
        """Compare 3 chunking strategies on same text:
        - Fixed-size (600 chars) -- current approach
        - Paragraph-based (split by double newline)
        - Sentence-based (split by . ! ?)

        For each: insert into LanceDB, search 15 queries,
        measure hit rate + NDCG@10.
        """
        chunks_fixed, vecs_fixed, q_vecs = book_data
        book_text = BOOK_PATH.read_text(encoding="utf-8")

        print("\n" + "=" * 72)
        print(f"  TEST 6: CHUNKING STRATEGY COMPARISON")
        print("=" * 72)

        # -- Generate chunks with each strategy --
        chunks_para = chunk_by_paragraph(book_text)
        chunks_sent = chunk_by_sentence(book_text)

        strategies = [
            ("Fixed-600", chunks_fixed, vecs_fixed),
            ("Paragraph", chunks_para, None),
            ("Sentence", chunks_sent, None),
        ]

        print(f"  Fixed-600:  {len(chunks_fixed)} chunks")
        print(f"  Paragraph:  {len(chunks_para)} chunks")
        print(f"  Sentence:   {len(chunks_sent)} chunks")

        # -- Embed non-fixed strategies --
        async with aiohttp.ClientSession() as session:
            para_vecs = await embed_texts(
                session, [c["text"] for c in chunks_para]
            )
            sent_vecs = await embed_texts(
                session, [c["text"] for c in chunks_sent]
            )

        strategies[1] = ("Paragraph", chunks_para, para_vecs)
        strategies[2] = ("Sentence", chunks_sent, sent_vecs)

        results_table = []

        for name, s_chunks, s_vecs in strategies:
            # Insert into LanceDB
            tmpdir = tempfile.mkdtemp()
            conn = await lancedb.connect_async(tmpdir)
            tbl = await conn.create_table(
                f"chunk_{name}", schema=LanceRecord, exist_ok=True
            )

            for start in range(0, len(s_chunks), 50):
                batch = [
                    LanceRecord(
                        id=s_chunks[i]["id"],
                        vector=s_vecs[i],
                        payload=LancePayload(
                            id=s_chunks[i]["id"],
                            text=s_chunks[i]["text"][:500],
                            chapter=s_chunks[i]["chapter"],
                        ),
                    ) for i in range(start, min(start + 50, len(s_chunks)))
                ]
                await tbl.merge_insert("id").when_matched_update_all() \
                    .when_not_matched_insert_all().execute(batch)

            # -- Search + hit rate + NDCG --
            hits = 0
            ndcg_scores = []
            for cq, qv in zip(QUERIES, q_vecs):
                rows = await tbl.vector_search(qv).limit(K).to_list()
                combined = " ".join(
                    _lance_text(r) for r in rows
                ).lower()
                if any(kw.lower() in combined for kw in cq["keywords"]):
                    hits += 1

                # NDCG@10 vs brute-force
                ideal = brute_force_topk(qv, s_vecs, K)
                ideal_indices = [idx for idx, _ in ideal]
                sims = {idx: sim for idx, sim in ideal}

                retrieved_indices = []
                for r in rows:
                    payload = r.get("payload", {})
                    if isinstance(payload, str):
                        try:
                            payload = json.loads(payload)
                        except (json.JSONDecodeError, TypeError):
                            payload = {}
                    rid = payload.get("id", "") \
                        if isinstance(payload, dict) else ""
                    for ci, c in enumerate(s_chunks):
                        if c["id"] == rid:
                            retrieved_indices.append(ci)
                            break

                if retrieved_indices:
                    r_rels = [
                        sims.get(idx, 0.0)
                        for idx in retrieved_indices[:K]
                    ]
                    i_rels = [
                        sims.get(idx, 0.0)
                        for idx in ideal_indices[:K]
                    ]
                    dcg_r = sum(
                        rel / math.log2(i + 2)
                        for i, rel in enumerate(r_rels)
                    )
                    dcg_i = sum(
                        rel / math.log2(i + 2)
                        for i, rel in enumerate(i_rels)
                    )
                    ndcg_val = dcg_r / dcg_i if dcg_i > 0 else 1.0
                    ndcg_scores.append(ndcg_val)

            hit_rate = hits / len(QUERIES)
            avg_ndcg = statistics.mean(ndcg_scores) \
                if ndcg_scores else 0.0

            results_table.append({
                "name": name,
                "chunks": len(s_chunks),
                "hit_rate": hit_rate,
                "hits": hits,
                "ndcg": avg_ndcg,
            })

        # -- Print comparison table --
        print(f"\n  {'Strategy':<15} {'Chunks':>7} "
              f"{'Hit Rate':>10} {'NDCG@10':>9}")
        print(f"  {'-'*45}")
        for r in results_table:
            print(
                f"  {r['name']:<15} {r['chunks']:>7} "
                f"{r['hits']:>3}/{len(QUERIES)} ({r['hit_rate']:.0%}) "
                f"{r['ndcg']:>8.3f}"
            )

        # Best strategy
        best = max(
            results_table, key=lambda x: (x["hit_rate"], x["ndcg"])
        )
        print(f"\n  Best strategy: {best['name']} "
              f"(hit_rate={best['hit_rate']:.0%}, NDCG={best['ndcg']:.3f})")

    # ── Final Summary ─────────────────────────────────────────────────────

    @pytest.mark.asyncio
    async def test_07_summary(self, book_data):
        """Summary of all RAG cases gap tests."""
        print("\n" + "=" * 72)
        print("  RAG CASES GAP ANALYSIS -- SUMMARY")
        print("=" * 72)
        print("""
  Tests covering cases.md gaps at the vector DB level:

  1. Multi-hop Retrieval    -- queries needing 2+ topic chunks
  2. Noise Robustness       -- search quality with garbage data
  3. Needle in Haystack     -- find 1 unique chunk among 5000+
  4. Multilingual Queries   -- EN/Mixed against RU data
  5. Adversarial/Typo       -- misspelled + transliterated queries
  6. Chunking Strategies    -- fixed vs paragraph vs sentence

  Remaining gaps (require LLM pipeline, out of scope):
  - Grounded answers (faithfulness)
  - Hallucination detection
  - Answer relevancy
  - Sensitive/malicious refusal
  - LLM-as-judge CI/CD
""")
        print("=" * 72)
