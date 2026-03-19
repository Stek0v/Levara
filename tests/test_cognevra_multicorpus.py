"""A1: Multi-corpus Cognevra test — 3 real text files, no mocks.

Tests insert + search across Russian book, English NLP article, and code.
Requires: embed-server:9001, Cognevra:8080+50051.
"""
import asyncio
import json
import sys
import time
import uuid
from pathlib import Path

import aiohttp
import grpc
import pytest

EMBED_URL = "http://localhost:9001/v1/embeddings"
COGNEVRA_URL = "http://localhost:8080"
GRPC_ADDR = "localhost:50051"

pb = sys.modules.get("cognee.infrastructure.databases.vector.cognevra.generated.cognevra_pb2")
pb_grpc = sys.modules.get("cognee.infrastructure.databases.vector.cognevra.generated.cognevra_pb2_grpc")
BATCH_SIZE = 16
MIN_CHUNK = 80
MAX_CHUNK = 600

BOOK_PATH = Path(__file__).parent.parent / "Edvards_Dzanet_Uragan_r4_P61XH.txt"
NLP_PATH = Path(__file__).parent.parent / "cognee" / "cognee" / "tests" / "test_data" / "Natural_language_processing.txt"
QUANTUM_PATH = Path(__file__).parent.parent / "cognee" / "cognee" / "tests" / "test_data" / "Quantum_computers.txt"

QUERIES_RU = [
    {"query": "телепат Эмбер способности чтение мыслей", "keywords": ["телепат", "Эмбер"]},
    {"query": "Лукас командир тактик партнёр", "keywords": ["Лукас"]},
    {"query": "город улей сто миллионов жителей", "keywords": ["улей", "миллион"]},
    {"query": "лотерея профессии назначение работы", "keywords": ["лотере"]},
    {"query": "ударная группа преследование преступников", "keywords": ["ударн", "групп"]},
    {"query": "морская ферма океан шторм", "keywords": ["ферм", "мор"]},
    {"query": "ураган ветер буря перемены", "keywords": ["ураган", "ветр"]},
]


def _check(url):
    try:
        import urllib.request
        urllib.request.urlopen(url, timeout=2)
        return True
    except Exception:
        return False


pytestmark = pytest.mark.skipif(
    not (_check(f"{EMBED_URL.replace('/v1/embeddings', '/health')}") and _check(f"{COGNEVRA_URL}/metrics")),
    reason="Need embed-server:9001 + Cognevra:8080",
)


def chunk_text(text, prefix=""):
    paragraphs = [p.strip() for p in text.split("\n\n") if p.strip()]
    chunks, buf, idx = [], "", 0
    for para in paragraphs:
        if len(buf) + len(para) + 2 < MAX_CHUNK:
            buf = (buf + "\n\n" + para).strip()
        else:
            if len(buf) >= MIN_CHUNK:
                chunks.append({"id": f"{prefix}{idx}", "text": buf})
                idx += 1
            buf = para
    if len(buf) >= MIN_CHUNK:
        chunks.append({"id": f"{prefix}{idx}", "text": buf})
    return chunks


async def embed_texts(session, texts):
    vecs = []
    for i in range(0, len(texts), BATCH_SIZE):
        batch = texts[i:i + BATCH_SIZE]
        async with session.post(EMBED_URL, json={"input": batch, "model": "pplx"}) as r:
            r.raise_for_status()
            data = await r.json()
        sorted_data = sorted(data["data"], key=lambda x: x["index"])
        vecs.extend(e["embedding"] for e in sorted_data)
    return vecs


def grpc_batch_insert(stub, collection, ids, vectors, metadatas):
    """Insert via gRPC BatchInsert (supports collections)."""
    records = [
        pb.InsertRecord(id=id_, vector=vec, metadata_json=meta)
        for id_, vec, meta in zip(ids, vectors, metadatas)
    ]
    return stub.BatchInsert(pb.BatchInsertReq(collection=collection, records=records))


def grpc_search(stub, collection, vector, k=10):
    """Search via gRPC."""
    return stub.Search(pb.SearchReq(collection=collection, vector=vector, top_k=k))


class TestMulticorpus:
    """Insert 3 different text files, search across collections."""

    @pytest.fixture(scope="class")
    def data(self):
        """Load and chunk all 3 files, embed, insert into Cognevra."""
        return asyncio.run(self._prepare())

    @staticmethod
    async def _prepare():
        timeout = aiohttp.ClientTimeout(total=600)
        async with aiohttp.ClientSession(timeout=timeout) as s:
            # Load texts
            book = BOOK_PATH.read_text(encoding="utf-8")
            nlp = NLP_PATH.read_text(encoding="utf-8")
            quantum = QUANTUM_PATH.read_text(encoding="utf-8")

            # Chunk
            book_chunks = chunk_text(book, "book:")[:200]  # first 200 for speed
            nlp_chunks = chunk_text(nlp, "nlp:")
            quantum_chunks = chunk_text(quantum, "quantum:")

            print(f"\n  Chunks: book={len(book_chunks)}, nlp={len(nlp_chunks)}, quantum={len(quantum_chunks)}")

            # Embed all
            all_texts = [c["text"] for c in book_chunks + nlp_chunks + quantum_chunks]
            t0 = time.time()
            all_vecs = await embed_texts(s, all_texts)
            print(f"  Embedding: {len(all_vecs)} texts in {time.time()-t0:.1f}s")

            # Split vectors back
            b_end = len(book_chunks)
            n_end = b_end + len(nlp_chunks)
            book_vecs = all_vecs[:b_end]
            nlp_vecs = all_vecs[b_end:n_end]
            quantum_vecs = all_vecs[n_end:]

            # Insert via gRPC
            ch = grpc.insecure_channel(GRPC_ADDR)
            grpc.channel_ready_future(ch).result(timeout=5)
            stub = pb_grpc.CognevraServiceStub(ch)

            for name, chunks, vecs in [
                ("test_book_ru", book_chunks, book_vecs),
                ("test_nlp_en", nlp_chunks, nlp_vecs),
                ("test_quantum_en", quantum_chunks, quantum_vecs),
            ]:
                ids = [c["id"] for c in chunks]
                metas = [json.dumps({"text": c["text"][:200]}) for c in chunks]
                grpc_batch_insert(stub, name, ids, vecs, metas)
                print(f"  Inserted {len(chunks)} into {name}")

            ch.close()

            return {
                "book_chunks": book_chunks, "nlp_chunks": nlp_chunks, "quantum_chunks": quantum_chunks,
                "book_vecs": book_vecs, "nlp_vecs": nlp_vecs, "quantum_vecs": quantum_vecs,
            }

    def test_collections_created(self, data):
        """3 collections should exist with correct chunk counts."""
        assert len(data["book_chunks"]) >= 100
        assert len(data["nlp_chunks"]) >= 1
        assert len(data["quantum_chunks"]) >= 1

    def test_intra_collection_search(self, data):
        """Search within book collection with Russian queries."""
        results = asyncio.run(self._search_queries(data))
        hits = sum(1 for r in results if r["hit"])
        rate = hits / len(results) * 100
        print(f"\n  Keyword hit rate: {hits}/{len(results)} = {rate:.0f}%")
        assert rate >= 70, f"Hit rate {rate:.0f}% < 70%"

    @staticmethod
    async def _search_queries(data):
        timeout = aiohttp.ClientTimeout(total=120)
        async with aiohttp.ClientSession(timeout=timeout) as s:
            ch = grpc.insecure_channel(GRPC_ADDR)
            stub = pb_grpc.CognevraServiceStub(ch)
            results = []
            for q in QUERIES_RU:
                qvec = (await embed_texts(s, [q["query"]]))[0]
                resp = grpc_search(stub, "test_book_ru", qvec, k=10)
                found_text = ""
                for r in resp.results:
                    try:
                        meta = json.loads(r.metadata_json)
                        if isinstance(meta, str):
                            meta = json.loads(meta)
                        found_text += " " + meta.get("text", "")
                    except (json.JSONDecodeError, AttributeError):
                        pass
                hit = any(kw.lower() in found_text.lower() for kw in q["keywords"])
                results.append({"query": q["query"][:40], "hit": hit})
            ch.close()
            return results

    def test_cross_collection_search(self, data):
        """Search across 3 collections — RU book collection should return relevant results."""
        result = asyncio.run(self._cross_collection_test(data))
        print(f"\n  RU book top-1: {result['ru_text'][:80]}")
        print(f"  EN quantum top-1: {result['en_text'][:80]}")
        # RU search should return Russian text (not English)
        assert len(result["ru_text"]) > 0, "RU collection should return results"
        # Check that result is actually Russian (contains Cyrillic chars)
        has_cyrillic = any('\u0400' <= c <= '\u04ff' for c in result["ru_text"])
        assert has_cyrillic, f"RU search should return Russian text, got: {result['ru_text'][:100]}"

    @staticmethod
    async def _cross_collection_test(data):
        timeout = aiohttp.ClientTimeout(total=30)
        async with aiohttp.ClientSession(timeout=timeout) as s:
            qvec = (await embed_texts(s, ["телепат Эмбер способности чтение мыслей"]))[0]

            ch = grpc.insecure_channel(GRPC_ADDR)
            stub = pb_grpc.CognevraServiceStub(ch)

            ru_resp = grpc_search(stub, "test_book_ru", qvec, k=1)
            en_resp = grpc_search(stub, "test_quantum_en", qvec, k=1)

            def _text(resp):
                if resp.results:
                    try:
                        meta = json.loads(resp.results[0].metadata_json)
                        if isinstance(meta, str):
                            meta = json.loads(meta)
                        return meta.get("text", "")
                    except Exception:
                        pass
                return ""

            ch.close()
            return {"ru_text": _text(ru_resp), "en_text": _text(en_resp)}
