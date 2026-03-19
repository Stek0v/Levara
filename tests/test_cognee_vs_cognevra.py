"""B5: Cognee vs Cognevra side-by-side comparison.

Same text (Quantum_computers.txt) → both solutions → compare.
Requires: Cognee API:8002, Cognevra gRPC:50051, embed-server:9001.
"""
import asyncio
import io
import json
import sys
import time
from pathlib import Path

import aiohttp
import grpc
import pytest

COGNEE_URL = "http://localhost:8002"
EMBED_URL = "http://localhost:9001/v1/embeddings"
GRPC_ADDR = "localhost:50051"
QUANTUM_PATH = Path(__file__).parent.parent / "cognee" / "cognee" / "tests" / "test_data" / "Quantum_computers.txt"

pb = sys.modules.get("cognee.infrastructure.databases.vector.cognevra.generated.cognevra_pb2")
pb_grpc = sys.modules.get("cognee.infrastructure.databases.vector.cognevra.generated.cognevra_pb2_grpc")

QUERIES = [
    {"q": "quantum computer how it works", "keywords": ["quantum", "comput"]},
    {"q": "qubit superposition", "keywords": ["qubit", "superposition"]},
    {"q": "quantum error correction", "keywords": ["error", "correct"]},
]

MIN_CHUNK, MAX_CHUNK, BATCH_SIZE = 80, 600, 16


def _check(url):
    try:
        import urllib.request
        return urllib.request.urlopen(url, timeout=5).status == 200
    except Exception:
        return False


pytestmark = pytest.mark.skipif(
    not (_check(f"{COGNEE_URL}/health") and _check(f"{EMBED_URL.replace('/v1/embeddings', '/health')}")),
    reason="Need Cognee:8002 + embed-server:9001",
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


class TestCogneeVsCognevra:
    """Side-by-side: same text, both solutions."""

    @pytest.fixture(scope="class")
    def results(self):
        return asyncio.run(self._run_both())

    @staticmethod
    async def _run_both():
        text = QUANTUM_PATH.read_text(encoding="utf-8")
        timeout = aiohttp.ClientTimeout(total=600)
        res = {}

        async with aiohttp.ClientSession(timeout=timeout) as s:
            # === COGNEVRA STANDALONE ===
            print("\n  === COGNEVRA STANDALONE ===")
            chunks = chunk_text(text, "cmp-q:")

            # Embed
            t0 = time.time()
            vecs = await embed_texts(s, [c["text"] for c in chunks])
            embed_time = time.time() - t0
            print(f"  Embed: {len(vecs)} chunks in {embed_time:.1f}s")

            # Insert via gRPC
            ch = grpc.insecure_channel(GRPC_ADDR)
            grpc.channel_ready_future(ch).result(timeout=5)
            stub = pb_grpc.CognevraServiceStub(ch)

            t0 = time.time()
            records = [
                pb.InsertRecord(id=c["id"], vector=v, metadata_json=json.dumps({"text": c["text"][:200]}))
                for c, v in zip(chunks, vecs)
            ]
            stub.BatchInsert(pb.BatchInsertReq(collection="cmp_quantum", records=records))
            insert_time = time.time() - t0

            # Search
            cognevra_search_times = []
            cognevra_hits = 0
            for q in QUERIES:
                qvec = (await embed_texts(s, [q["q"]]))[0]
                t0 = time.perf_counter()
                sr = stub.Search(pb.SearchReq(collection="cmp_quantum", vector=qvec, top_k=5))
                cognevra_search_times.append((time.perf_counter() - t0) * 1000)
                found = " ".join(r.metadata_json for r in sr.results).lower()
                if any(kw in found for kw in q["keywords"]):
                    cognevra_hits += 1

            ch.close()

            res["cognevra"] = {
                "embed_time": embed_time,
                "insert_time": insert_time,
                "search_avg_ms": sum(cognevra_search_times) / len(cognevra_search_times),
                "search_p50_ms": sorted(cognevra_search_times)[len(cognevra_search_times) // 2],
                "hit_rate": cognevra_hits / len(QUERIES) * 100,
                "chunks": len(chunks),
            }

            # === COGNEE FULL PIPELINE ===
            print("  === COGNEE FULL PIPELINE ===")
            dataset = "cmp_quantum_cognee"

            # Add
            data = aiohttp.FormData()
            data.add_field('data', io.BytesIO(text.encode()),
                          filename='quantum.txt', content_type='text/plain')
            data.add_field('datasetName', dataset)

            t0 = time.time()
            r = await s.post(f"{COGNEE_URL}/api/v1/add", data=data)
            add_time = time.time() - t0

            # Cognify
            t0 = time.time()
            r = await s.post(f"{COGNEE_URL}/api/v1/cognify", json={"datasets": [dataset]})
            cognify_time = time.time() - t0
            cognify_status = r.status

            # Search CHUNKS
            cognee_search_times = []
            cognee_hits = 0
            for q in QUERIES:
                t0 = time.perf_counter()
                r = await s.post(f"{COGNEE_URL}/api/v1/search",
                                json={"query_text": q["q"], "query_type": "CHUNKS"})
                cognee_search_times.append((time.perf_counter() - t0) * 1000)
                try:
                    body = await r.json()
                    if isinstance(body, list) and body:
                        found = str(body).lower()
                        if any(kw in found for kw in q["keywords"]):
                            cognee_hits += 1
                except Exception:
                    pass

            res["cognee"] = {
                "add_time": add_time,
                "cognify_time": cognify_time,
                "cognify_status": cognify_status,
                "total_pipeline_time": add_time + cognify_time,
                "search_avg_ms": sum(cognee_search_times) / max(len(cognee_search_times), 1),
                "hit_rate": cognee_hits / len(QUERIES) * 100,
            }

        return res

    def test_cognevra_search_fast(self, results):
        avg = results["cognevra"]["search_avg_ms"]
        assert avg < 20, f"Cognevra search too slow: {avg:.1f}ms"
        print(f"\n  Cognevra search avg: {avg:.1f}ms")

    def test_cognevra_hit_rate(self, results):
        rate = results["cognevra"]["hit_rate"]
        print(f"\n  Cognevra hit rate: {rate:.0f}%")
        assert rate >= 50, f"Cognevra hit rate too low: {rate:.0f}%"

    def test_cognee_pipeline_completes(self, results):
        status = results["cognee"]["cognify_status"]
        assert status in (200, 409), f"Cognee cognify: {status}"
        print(f"\n  Cognee pipeline: {results['cognee']['total_pipeline_time']:.1f}s")

    def test_comparison_table(self, results):
        """Print side-by-side comparison."""
        cg = results["cognevra"]
        cn = results["cognee"]

        print(f"""
  ╔══════════════════════════════╦═══════════════╦═══════════════╗
  ║ Metric                       ║ Cognevra      ║ Cognee        ║
  ╠══════════════════════════════╬═══════════════╬═══════════════╣
  ║ Pipeline time                ║ {cg['embed_time']+cg['insert_time']:>8.1f}s     ║ {cn['total_pipeline_time']:>8.1f}s     ║
  ║ Search latency (avg)         ║ {cg['search_avg_ms']:>8.1f}ms    ║ {cn['search_avg_ms']:>8.1f}ms    ║
  ║ Keyword hit rate             ║ {cg['hit_rate']:>8.0f}%      ║ {cn['hit_rate']:>8.0f}%      ║
  ║ Graph enrichment             ║     No        ║    Yes         ║
  ╚══════════════════════════════╩═══════════════╩═══════════════╝
""")
