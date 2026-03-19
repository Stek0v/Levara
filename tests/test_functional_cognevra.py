"""Functional tests for Cognevra standalone (direct gRPC + embed-server).

Mirror of test_functional_cognee.py — same operations, same data, same assertions.
Uses: Cognevra gRPC:50051, embed-server:9001.
"""
import asyncio
import json
import sys
import time
from pathlib import Path

import aiohttp
import grpc
import pytest

pb = sys.modules.get("cognee.infrastructure.databases.vector.cognevra.generated.cognevra_pb2")
pb_grpc = sys.modules.get("cognee.infrastructure.databases.vector.cognevra.generated.cognevra_pb2_grpc")
if pb is None or pb_grpc is None:
    pytest.skip("Proto stubs not loaded", allow_module_level=True)

EMBED_URL = "http://localhost:9001/v1/embeddings"
GRPC_ADDR = "localhost:50051"
BATCH_SIZE = 16
COLLECTION = "func_test_cognevra"

QUANTUM_PATH = Path(__file__).parent.parent / "cognee" / "cognee" / "tests" / "test_data" / "Quantum_computers.txt"
NLP_PATH = Path(__file__).parent.parent / "cognee" / "cognee" / "tests" / "test_data" / "Natural_language_processing.txt"

SEARCH_QUERIES = [
    {"query": "quantum computer how it works", "keywords": ["quantum"]},
    {"query": "natural language processing text analysis", "keywords": ["language", "processing"]},
    {"query": "qubit superposition entanglement", "keywords": ["qubit"]},
]


def _check():
    try:
        import urllib.request
        urllib.request.urlopen(EMBED_URL.replace("/v1/embeddings", "/health"), timeout=3)
        ch = grpc.insecure_channel(GRPC_ADDR)
        grpc.channel_ready_future(ch).result(timeout=3)
        ch.close()
        return True
    except Exception:
        return False


pytestmark = pytest.mark.skipif(not _check(), reason="Need embed-server:9001 + Cognevra:50051")


# ── Helpers ──

def _stub():
    ch = grpc.insecure_channel(GRPC_ADDR)
    grpc.channel_ready_future(ch).result(timeout=5)
    return pb_grpc.CognevraServiceStub(ch), ch


async def _embed(session, texts):
    vecs = []
    for i in range(0, len(texts), BATCH_SIZE):
        batch = texts[i:i + BATCH_SIZE]
        async with session.post(EMBED_URL, json={"input": batch, "model": "pplx"}) as r:
            r.raise_for_status()
            data = await r.json()
        vecs.extend(e["embedding"] for e in sorted(data["data"], key=lambda x: x["index"]))
    return vecs


def _chunk(text, min_c=80, max_c=600):
    chunks, buf, idx = [], "", 0
    for para in [p.strip() for p in text.split("\n\n") if p.strip()]:
        if len(buf) + len(para) + 2 < max_c:
            buf = (buf + "\n\n" + para).strip()
        else:
            if len(buf) >= min_c:
                chunks.append({"id": f"cg-{idx}", "text": buf})
                idx += 1
            buf = para
    if len(buf) >= min_c:
        chunks.append({"id": f"cg-{idx}", "text": buf})
    return chunks


# ── Test class ──

class TestFunctionalCognevra:
    """Functional tests: ingest → search → delete → re-search."""

    @pytest.fixture(scope="class")
    def state(self):
        """Ingest both text files into Cognevra."""
        return asyncio.run(self._ingest())

    @staticmethod
    async def _ingest():
        quantum = QUANTUM_PATH.read_text(encoding="utf-8")
        nlp = NLP_PATH.read_text(encoding="utf-8")
        all_chunks = _chunk(quantum + "\n\n" + nlp)

        timeout = aiohttp.ClientTimeout(total=300)
        async with aiohttp.ClientSession(timeout=timeout) as s:
            texts = [c["text"] for c in all_chunks]
            t0 = time.time()
            vecs = await _embed(s, texts)
            embed_time = time.time() - t0

        stub, ch = _stub()

        # Drop collection if exists
        try:
            stub.DropCollection(pb.DropCollectionReq(name=COLLECTION))
        except Exception:
            pass

        t0 = time.time()
        records = [
            pb.InsertRecord(id=c["id"], vector=v, metadata_json=json.dumps({"text": c["text"]}))
            for c, v in zip(all_chunks, vecs)
        ]
        resp = stub.BatchInsert(pb.BatchInsertReq(collection=COLLECTION, records=records))
        insert_time = time.time() - t0

        ch.close()
        print(f"\n  Ingest: {len(all_chunks)} chunks, embed={embed_time:.1f}s, insert={insert_time:.3f}s")

        return {
            "chunks": all_chunks,
            "vectors": vecs,
            "embed_time": embed_time,
            "insert_time": insert_time,
            "inserted": resp.inserted,
        }

    # ── 1. Ingest ──

    def test_01_ingest_count(self, state):
        """All chunks should be inserted."""
        assert state["inserted"] == len(state["chunks"])
        print(f"\n  Inserted: {state['inserted']} chunks")

    def test_02_ingest_time(self, state):
        """Insert should complete in < 1s (excluding embedding)."""
        assert state["insert_time"] < 1.0, f"Insert too slow: {state['insert_time']:.3f}s"
        print(f"\n  Insert time: {state['insert_time']:.3f}s")

    # ── 2. Search ──

    def test_03_search_returns_results(self, state):
        """Each query should return ≥1 result."""
        results = asyncio.run(self._search_all())
        for r in results:
            assert r["count"] > 0, f"Query '{r['query']}' returned 0 results"
            print(f"\n  '{r['query'][:40]}': {r['count']} results, {r['latency_ms']:.1f}ms")

    def test_04_search_latency(self, state):
        """Search should be < 10ms average."""
        results = asyncio.run(self._search_all())
        avg = sum(r["latency_ms"] for r in results) / len(results)
        print(f"\n  Search avg latency: {avg:.1f}ms")
        assert avg < 10, f"Search too slow: {avg:.1f}ms"

    def test_05_search_relevance(self, state):
        """At least 2/3 queries should find keyword matches in top-5."""
        results = asyncio.run(self._search_all())
        hits = sum(1 for r in results if r["hit"])
        rate = hits / len(results) * 100
        print(f"\n  Keyword hit rate: {hits}/{len(results)} = {rate:.0f}%")
        assert rate >= 50, f"Hit rate too low: {rate:.0f}%"

    @staticmethod
    async def _search_all():
        stub, ch = _stub()
        timeout = aiohttp.ClientTimeout(total=60)
        results = []
        async with aiohttp.ClientSession(timeout=timeout) as s:
            for q in SEARCH_QUERIES:
                qvec = (await _embed(s, [q["query"]]))[0]
                t0 = time.perf_counter()
                resp = stub.Search(pb.SearchReq(collection=COLLECTION, vector=qvec, top_k=5))
                latency = (time.perf_counter() - t0) * 1000

                found_text = ""
                for r in resp.results:
                    try:
                        meta = json.loads(r.metadata_json)
                        if isinstance(meta, str):
                            meta = json.loads(meta)
                        found_text += " " + meta.get("text", "")
                    except Exception:
                        found_text += " " + r.metadata_json

                hit = any(kw.lower() in found_text.lower() for kw in q["keywords"])
                results.append({
                    "query": q["query"], "count": len(resp.results),
                    "latency_ms": latency, "hit": hit,
                })
        ch.close()
        return results

    # ── 3. Delete ──

    def test_06_delete_subset(self, state):
        """Delete first chunk, verify it's gone from search."""
        stub, ch = _stub()
        ids_to_delete = [state["chunks"][0]["id"]]
        resp = stub.Delete(pb.DeleteReq(collection=COLLECTION, ids=ids_to_delete))
        assert resp.deleted == 1, f"Deleted {resp.deleted} != 1"
        print(f"\n  Deleted: {resp.deleted} chunk(s)")
        ch.close()

    def test_07_deleted_not_in_search(self, state):
        """Deleted chunks should not appear in search results."""
        deleted_ids = {state["chunks"][0]["id"]}
        stub, ch = _stub()
        # Search with first chunk's vector
        resp = stub.Search(pb.SearchReq(
            collection=COLLECTION, vector=state["vectors"][0], top_k=10))
        found_ids = {r.id for r in resp.results}
        overlap = deleted_ids & found_ids
        assert len(overlap) == 0, f"Deleted IDs still in search: {overlap}"
        print(f"\n  Verified: deleted IDs not in search results")
        ch.close()

    # ── 4. Collection management ──

    def test_08_collection_exists(self, state):
        stub, ch = _stub()
        resp = stub.HasCollection(pb.HasCollectionReq(name=COLLECTION))
        assert resp.exists, f"Collection {COLLECTION} should exist"
        ch.close()

    def test_09_drop_collection(self, state):
        stub, ch = _stub()
        stub.DropCollection(pb.DropCollectionReq(name=COLLECTION))
        resp = stub.HasCollection(pb.HasCollectionReq(name=COLLECTION))
        assert not resp.exists, f"Collection {COLLECTION} should be dropped"
        print(f"\n  Collection dropped")
        ch.close()
