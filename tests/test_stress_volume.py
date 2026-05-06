"""Stress tests: volume — large data sets, scale limits.

Requires: Levara gRPC:50051, embed-server:9001 (for some).
"""
import grpc
import json
import sys
import time
from pathlib import Path

import pytest

pb = sys.modules.get("cognee.infrastructure.databases.vector.levara.generated.levara_pb2")
pb_grpc = sys.modules.get("cognee.infrastructure.databases.vector.levara.generated.levara_pb2_grpc")
if pb is None or pb_grpc is None:
    pytest.skip("Proto stubs not loaded", allow_module_level=True)

GRPC = "localhost:50051"
BOOK = Path(__file__).parent.parent / "Edvards_Dzanet_Uragan_r4_P61XH.txt"


def _stub():
    ch = grpc.insecure_channel(GRPC)
    try:
        grpc.channel_ready_future(ch).result(timeout=3)
    except grpc.FutureTimeoutError:
        pytest.skip("Levara not running")
    return pb_grpc.LevaraServiceStub(ch), ch


class TestVolumeInsert:
    def test_01_insert_10k_vectors(self):
        """10,000 vectors — search must still work."""
        stub, ch = _stub()
        coll = "vol_10k"
        vec = [0.01] * 1024
        batch_size = 500

        t0 = time.perf_counter()
        total_inserted = 0
        for start in range(0, 10000, batch_size):
            records = [
                pb.InsertRecord(id=f"v10k-{i}", vector=vec, metadata_json=f'{{"i":{i}}}')
                for i in range(start, min(start + batch_size, 10000))
            ]
            resp = stub.BatchInsert(pb.BatchInsertReq(collection=coll, records=records))
            total_inserted += resp.inserted
        elapsed = (time.perf_counter() - t0) * 1000

        print(f"\n  10K insert: {total_inserted} in {elapsed:.0f}ms ({total_inserted/(elapsed/1000):.0f}/sec)")

        # Search
        t0 = time.perf_counter()
        resp = stub.Search(pb.SearchReq(collection=coll, vector=vec, top_k=10))
        search_ms = (time.perf_counter() - t0) * 1000
        print(f"  Search 10K: {search_ms:.1f}ms, {len(resp.results)} results")
        assert len(resp.results) >= 1
        assert search_ms < 50, f"Search too slow: {search_ms:.1f}ms"
        ch.close()


class TestVolumeBM25:
    def test_03_bm25_index_10k(self):
        """10K documents in BM25 — search must be fast."""
        stub, ch = _stub()
        coll = "vol_bm25"

        items = [
            pb.IndexItem(id=f"bm-{i}", text=f"document {i} about topic {i%100} with category {i%20}")
            for i in range(10000)
        ]
        t0 = time.perf_counter()
        stub.BM25Index(pb.BM25IndexReq(collection=coll, items=items))
        index_ms = (time.perf_counter() - t0) * 1000
        print(f"\n  BM25 index 10K: {index_ms:.0f}ms")

        # Search
        t0 = time.perf_counter()
        resp = stub.BM25Search(pb.BM25SearchReq(collection=coll, query="topic 42 category 7", top_k=10))
        search_ms = (time.perf_counter() - t0) * 1000
        print(f"  BM25 search 10K: {search_ms:.1f}ms, {len(resp.results)} results")
        assert len(resp.results) >= 1
        assert search_ms < 20, f"BM25 search too slow: {search_ms:.1f}ms"
        ch.close()


class TestVolumeIngest:
    def test_04_large_book_ingest(self):
        """1.2MB Russian book — IngestData handles large files."""
        if not BOOK.exists():
            pytest.skip("Book not found")
        stub, ch = _stub()
        book = BOOK.read_text(encoding="utf-8")

        t0 = time.perf_counter()
        resp = stub.IngestData(pb.IngestDataReq(
            items=[pb.IngestItem(text=book, dataset_name="vol_book")],
            storage_path="/tmp/vol_book",
        ))
        ms = (time.perf_counter() - t0) * 1000
        assert resp.results[0].file_size > 1_000_000
        print(f"\n  Book ingest: {resp.results[0].file_size} bytes in {ms:.0f}ms")
        ch.close()


class TestVolumeGraph:
    def test_07_graph_10k_triplet_search(self):
        """10K nodes + 20K edges — SearchTriplets must stay fast."""
        stub, ch = _stub()
        nodes = [pb.TripletNode(id=f"gn{i}", name=f"Entity{i}") for i in range(10000)]
        edges = [
            pb.TripletEdge(node1_id=f"gn{i}", node2_id=f"gn{(i+1)%10000}",
                          relationship_type="rel", edge_type_id=f"et{i%50}")
            for i in range(20000)
        ]
        dists = pb.CollectionDistances(entries=[
            pb.DistanceEntry(id=f"gn{i}", distance=float(i)*0.0001) for i in range(5000)
        ])

        t0 = time.perf_counter()
        resp = stub.SearchTriplets(pb.SearchTripletsReq(
            nodes=nodes, edges=edges, node_distances=[dists], top_k=10))
        ms = (time.perf_counter() - t0) * 1000

        assert len(resp.triplets) == 10
        print(f"\n  SearchTriplets 10K+20K: {ms:.0f}ms")
        assert ms < 100, f"Triplet search too slow: {ms:.0f}ms"
        ch.close()


class TestVolumeDedup:
    def test_08_dedup_5k_with_duplicates(self):
        """5K nodes with 20% duplicates — SemanticDedup performance."""
        stub, ch = _stub()
        nodes = [pb.DedupNodeMsg(id=f"dd{i}", name=f"Node{i}", type="E") for i in range(5000)]
        # Add 1000 duplicates
        nodes += [pb.DedupNodeMsg(id=f"dd{i}", name=f"Dup{i}", type="E") for i in range(1000)]
        edges = [
            pb.DedupEdgeMsg(source_id=f"dd{i}", target_id=f"dd{(i+1)%5000}", relationship_name="r")
            for i in range(10000)
        ]

        t0 = time.perf_counter()
        resp = stub.DeduplicateGraph(pb.DeduplicateGraphReq(nodes=nodes, edges=edges))
        ms = (time.perf_counter() - t0) * 1000

        print(f"\n  Dedup 6K→{len(resp.nodes)} nodes: {ms:.0f}ms, removed={resp.nodes_removed}")
        assert resp.nodes_removed == 1000
        assert ms < 200, f"Dedup too slow: {ms:.0f}ms"
        ch.close()
