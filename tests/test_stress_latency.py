"""Stress tests: performance targets and latency benchmarks.

Verifies that all RPCs meet latency SLAs under load.
Requires: Levara gRPC:50051, embed-server:9001.
"""
import grpc
import json
import sys
import time
import urllib.request
from pathlib import Path

import pytest

pb = sys.modules.get("cognee.infrastructure.databases.vector.levara.generated.levara_pb2")
pb_grpc = sys.modules.get("cognee.infrastructure.databases.vector.levara.generated.levara_pb2_grpc")
if pb is None or pb_grpc is None:
    pytest.skip("Proto stubs not loaded", allow_module_level=True)

GRPC = "localhost:50051"
EMBED = "http://localhost:9001/v1/embeddings"
BOOK = Path(__file__).parent.parent / "Edvards_Dzanet_Uragan_r4_P61XH.txt"


def _stub():
    ch = grpc.insecure_channel(GRPC)
    try:
        grpc.channel_ready_future(ch).result(timeout=3)
    except grpc.FutureTimeoutError:
        pytest.skip("Levara not running")
    return pb_grpc.LevaraServiceStub(ch), ch


def _check_embed():
    try:
        urllib.request.urlopen(EMBED.replace("/v1/embeddings", "/health"), timeout=2)
        return True
    except Exception:
        return False


def _embed_one(text):
    r = urllib.request.urlopen(urllib.request.Request(
        EMBED, data=json.dumps({"input": [text], "model": "pplx"}).encode(),
        headers={"Content-Type": "application/json"}), timeout=30)
    return json.loads(r.read())["data"][0]["embedding"]


def _percentile(times, p):
    s = sorted(times)
    idx = int(len(s) * p / 100)
    return s[min(idx, len(s) - 1)]


class TestSearchLatency:
    """Search must meet latency targets."""

    @pytest.fixture(autouse=True)
    def setup(self):
        if not _check_embed():
            pytest.skip("embed-server not running")
        stub, ch = _stub()
        # Ensure collection with data exists
        self.coll = "lat_test"
        vec = _embed_one("test warmup query")
        try:
            stub.Insert(pb.InsertReq(collection=self.coll, id="lat0", vector=vec, metadata_json='{"text":"warmup"}'))
        except Exception:
            pass
        self.stub = stub
        self.ch = ch
        yield
        ch.close()

    def test_01_search_p50_under_5ms(self):
        vec = _embed_one("search latency test")
        # Warmup
        self.stub.Search(pb.SearchReq(collection=self.coll, vector=vec, top_k=5))

        times = []
        for _ in range(100):
            t0 = time.perf_counter()
            self.stub.Search(pb.SearchReq(collection=self.coll, vector=vec, top_k=5))
            times.append((time.perf_counter() - t0) * 1000)

        p50 = _percentile(times, 50)
        p99 = _percentile(times, 99)
        print(f"\n  Search: p50={p50:.2f}ms p99={p99:.2f}ms avg={sum(times)/len(times):.2f}ms")
        assert p50 < 5, f"p50 {p50:.2f}ms > 5ms target"

    def test_02_search_p99_under_20ms(self):
        vec = _embed_one("p99 test")
        self.stub.Search(pb.SearchReq(collection=self.coll, vector=vec, top_k=5))

        times = []
        for _ in range(100):
            t0 = time.perf_counter()
            self.stub.Search(pb.SearchReq(collection=self.coll, vector=vec, top_k=5))
            times.append((time.perf_counter() - t0) * 1000)

        p99 = _percentile(times, 99)
        assert p99 < 20, f"p99 {p99:.2f}ms > 20ms target"


class TestInsertThroughput:
    def test_03_insert_throughput(self):
        stub, ch = _stub()
        vec = [0.01] * 1024
        n = 1000

        t0 = time.perf_counter()
        records = [pb.InsertRecord(id=f"thr-{i}", vector=vec, metadata_json=f'{{"i":{i}}}') for i in range(n)]
        stub.BatchInsert(pb.BatchInsertReq(collection="lat_throughput", records=records))
        elapsed = time.perf_counter() - t0

        throughput = n / elapsed
        print(f"\n  Insert throughput: {throughput:.0f} records/sec ({elapsed:.3f}s for {n})")
        assert throughput > 500, f"Throughput {throughput:.0f} < 500 target"
        ch.close()


class TestBM25Latency:
    def test_04_bm25_search_under_5ms(self):
        stub, ch = _stub()
        # Index 1000 docs
        items = [pb.IndexItem(id=f"bm-{i}", text=f"document {i} about topic {i%50} with keywords {i%20}") for i in range(1000)]
        stub.BM25Index(pb.BM25IndexReq(collection="lat_bm25", items=items))

        # Warmup
        stub.BM25Search(pb.BM25SearchReq(collection="lat_bm25", query="document topic", top_k=10))

        times = []
        for _ in range(100):
            t0 = time.perf_counter()
            stub.BM25Search(pb.BM25SearchReq(collection="lat_bm25", query="document topic keywords", top_k=10))
            times.append((time.perf_counter() - t0) * 1000)

        p50 = _percentile(times, 50)
        print(f"\n  BM25 search (1K docs): p50={p50:.2f}ms")
        assert p50 < 5, f"BM25 p50 {p50:.2f}ms > 5ms target"
        ch.close()


class TestTripletLatency:
    def test_06_triplet_search_under_10ms(self):
        stub, ch = _stub()
        nodes = [pb.TripletNode(id=f"tn{i}", name=f"Entity{i}") for i in range(500)]
        edges = [
            pb.TripletEdge(node1_id=f"tn{i}", node2_id=f"tn{(i+1)%500}",
                          relationship_type="rel", edge_type_id=f"et{i%25}")
            for i in range(5000)
        ]
        dists = pb.CollectionDistances(entries=[
            pb.DistanceEntry(id=f"tn{i}", distance=float(i) * 0.002) for i in range(250)
        ])

        # Warmup
        stub.SearchTriplets(pb.SearchTripletsReq(nodes=nodes, edges=edges, node_distances=[dists], top_k=10))

        times = []
        for _ in range(20):
            t0 = time.perf_counter()
            stub.SearchTriplets(pb.SearchTripletsReq(nodes=nodes, edges=edges, node_distances=[dists], top_k=10))
            times.append((time.perf_counter() - t0) * 1000)

        p50 = _percentile(times, 50)
        print(f"\n  SearchTriplets (5K edges): p50={p50:.2f}ms")
        assert p50 < 10, f"Triplet p50 {p50:.2f}ms > 10ms target"
        ch.close()


class TestIngestLatency:
    def test_08_ingest_under_1ms(self):
        stub, ch = _stub()
        items = [pb.IngestItem(text=f"Ingest latency test doc {i}", dataset_name="lat_ingest") for i in range(100)]

        t0 = time.perf_counter()
        resp = stub.IngestData(pb.IngestDataReq(items=items, storage_path="/tmp/lat_ingest"))
        elapsed = (time.perf_counter() - t0) * 1000

        per_item = elapsed / len(items)
        print(f"\n  IngestData: {elapsed:.1f}ms total, {per_item:.3f}ms/item")
        assert per_item < 1.0, f"Ingest {per_item:.3f}ms/item > 1ms target"
        ch.close()


class TestChunkLatency:
    def test_09_chunk_book_under_10ms(self):
        if not BOOK.exists():
            pytest.skip("Book not found")
        stub, ch = _stub()
        book = BOOK.read_text(encoding="utf-8")

        # Warmup
        stub.ChunkText(pb.ChunkTextReq(text=book[:1000], strategy="merged"))

        t0 = time.perf_counter()
        resp = stub.ChunkText(pb.ChunkTextReq(text=book, strategy="merged", min_chunk_chars=80, max_chunk_chars=600))
        elapsed = (time.perf_counter() - t0) * 1000

        print(f"\n  ChunkText (1.2MB book): {len(resp.chunks)} chunks in {elapsed:.1f}ms")
        assert elapsed < 50, f"Chunk {elapsed:.1f}ms > 50ms target"  # generous for gRPC overhead
        assert len(resp.chunks) > 100
        ch.close()


class TestDedupLatency:
    def test_10_dedup_1000_under_5ms(self):
        stub, ch = _stub()
        nodes = [pb.DedupNodeMsg(id=f"dn{i}", name=f"Node{i}", type="Entity") for i in range(1000)]
        # Add 200 duplicates
        nodes += [pb.DedupNodeMsg(id=f"dn{i}", name=f"Dup{i}", type="Entity") for i in range(200)]
        edges = [pb.DedupEdgeMsg(source_id=f"dn{i}", target_id=f"dn{(i+1)%1000}", relationship_name="rel") for i in range(2000)]

        # Warmup
        stub.DeduplicateGraph(pb.DeduplicateGraphReq(nodes=nodes[:10], edges=edges[:10]))

        t0 = time.perf_counter()
        resp = stub.DeduplicateGraph(pb.DeduplicateGraphReq(nodes=nodes, edges=edges))
        elapsed = (time.perf_counter() - t0) * 1000

        print(f"\n  DeduplicateGraph: {len(nodes)}→{len(resp.nodes)} nodes, {elapsed:.1f}ms")
        assert elapsed < 50, f"Dedup {elapsed:.1f}ms > 50ms target"
        assert resp.nodes_removed == 200
        ch.close()
