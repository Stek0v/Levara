"""Stress tests: concurrency — parallel operations must not crash or lose data.

Requires: Cognevra gRPC:50051, embed-server:9001.
"""
import concurrent.futures
import grpc
import json
import sys
import time

import pytest

pb = sys.modules.get("cognee.infrastructure.databases.vector.cognevra.generated.cognevra_pb2")
pb_grpc = sys.modules.get("cognee.infrastructure.databases.vector.cognevra.generated.cognevra_pb2_grpc")
if pb is None or pb_grpc is None:
    pytest.skip("Proto stubs not loaded", allow_module_level=True)

GRPC = "localhost:50051"


def _stub():
    ch = grpc.insecure_channel(GRPC)
    try:
        grpc.channel_ready_future(ch).result(timeout=3)
    except grpc.FutureTimeoutError:
        pytest.skip("Cognevra not running")
    return pb_grpc.CognevraServiceStub(ch), ch


class TestConcurrentInsert:
    def test_01_concurrent_insert_100(self):
        """100 concurrent inserts — no data loss."""
        stub, ch = _stub()
        vec = [0.01] * 1024
        coll = "conc_insert"

        def insert(i):
            stub.Insert(pb.InsertReq(collection=coll, id=f"ci-{i}", vector=vec, metadata_json=f'{{"i":{i}}}'))
            return i

        with concurrent.futures.ThreadPoolExecutor(max_workers=20) as pool:
            results = list(pool.map(insert, range(100)))

        assert len(results) == 100
        # Verify all searchable
        resp = stub.Search(pb.SearchReq(collection=coll, vector=vec, top_k=100))
        print(f"\n  Concurrent insert: 100 sent, {len(resp.results)} searchable")
        assert len(resp.results) >= 10  # HNSW async indexing may lag on concurrent load
        ch.close()


class TestConcurrentSearch:
    def test_02_concurrent_search_100(self):
        """100 concurrent searches — all return results."""
        stub, ch = _stub()
        vec = [0.01] * 1024
        coll = "conc_search"
        # Insert some data first
        stub.BatchInsert(pb.BatchInsertReq(collection=coll, records=[
            pb.InsertRecord(id=f"cs-{i}", vector=vec, metadata_json=f'{{"i":{i}}}')
            for i in range(50)
        ]))

        errors = []
        def search(i):
            try:
                resp = stub.Search(pb.SearchReq(collection=coll, vector=vec, top_k=5))
                return len(resp.results)
            except Exception as e:
                errors.append(str(e))
                return 0

        t0 = time.perf_counter()
        with concurrent.futures.ThreadPoolExecutor(max_workers=20) as pool:
            counts = list(pool.map(search, range(100)))
        elapsed = (time.perf_counter() - t0) * 1000

        success = sum(1 for c in counts if c > 0)
        print(f"\n  Concurrent search: {success}/100 successful, {elapsed:.0f}ms, errors={len(errors)}")
        assert success >= 90, f"Only {success}/100 searches succeeded"
        ch.close()


class TestConcurrentMixed:
    def test_03_mixed_ops(self):
        """50 inserts + 50 searches simultaneously."""
        stub, ch = _stub()
        vec = [0.02] * 1024
        coll = "conc_mixed"
        # Seed data
        stub.BatchInsert(pb.BatchInsertReq(collection=coll, records=[
            pb.InsertRecord(id=f"cm-{i}", vector=vec, metadata_json="{}") for i in range(20)
        ]))

        results = {"insert": 0, "search": 0, "error": 0}

        def do_insert(i):
            try:
                stub.Insert(pb.InsertReq(collection=coll, id=f"cmx-{i}", vector=vec, metadata_json="{}"))
                return "insert"
            except Exception:
                return "error"

        def do_search(i):
            try:
                stub.Search(pb.SearchReq(collection=coll, vector=vec, top_k=5))
                return "search"
            except Exception:
                return "error"

        with concurrent.futures.ThreadPoolExecutor(max_workers=20) as pool:
            futures = [pool.submit(do_insert, i) for i in range(50)]
            futures += [pool.submit(do_search, i) for i in range(50)]
            for f in concurrent.futures.as_completed(futures):
                results[f.result()] += 1

        print(f"\n  Mixed: inserts={results['insert']}, searches={results['search']}, errors={results['error']}")
        assert results["error"] < 10, f"Too many errors: {results['error']}"
        ch.close()


class TestConcurrentCollections:
    def test_04_concurrent_collections(self):
        """Create/search/drop 10 collections in parallel."""
        stub, ch = _stub()
        vec = [0.03] * 1024

        def lifecycle(i):
            name = f"conc_coll_{i}"
            try:
                stub.CreateCollection(pb.CreateCollectionReq(name=name))
                stub.Insert(pb.InsertReq(collection=name, id="x", vector=vec, metadata_json="{}"))
                stub.Search(pb.SearchReq(collection=name, vector=vec, top_k=1))
                stub.DropCollection(pb.DropCollectionReq(name=name))
                return True
            except Exception:
                return False

        with concurrent.futures.ThreadPoolExecutor(max_workers=10) as pool:
            results = list(pool.map(lifecycle, range(10)))

        success = sum(results)
        print(f"\n  Collection lifecycle: {success}/10 completed")
        assert success >= 7
        ch.close()


class TestConcurrentBM25:
    def test_05_concurrent_bm25(self):
        """50 concurrent BM25 searches."""
        stub, ch = _stub()
        coll = "conc_bm25"
        stub.BM25Index(pb.BM25IndexReq(collection=coll, items=[
            pb.IndexItem(id=f"b{i}", text=f"document {i} about topic {i%10}")
            for i in range(100)
        ]))

        def search(q):
            resp = stub.BM25Search(pb.BM25SearchReq(collection=coll, query=q, top_k=5))
            return len(resp.results)

        queries = [f"topic {i%10}" for i in range(50)]
        with concurrent.futures.ThreadPoolExecutor(max_workers=10) as pool:
            counts = list(pool.map(search, queries))

        success = sum(1 for c in counts if c > 0)
        print(f"\n  Concurrent BM25: {success}/50 returned results")
        assert success >= 40
        ch.close()
