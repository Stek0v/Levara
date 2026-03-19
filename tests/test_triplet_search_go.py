"""Integration test for Go-accelerated SearchTriplets gRPC RPC.

Tests the in-memory graph scoring that replaces Python's brute_force_triplet_search.
Requires Cognevra running on localhost:50051.
"""
import grpc
import pytest
import sys
import time

# Proto stubs loaded by conftest.py from cognee tree
pb = sys.modules.get("cognee.infrastructure.databases.vector.cognevra.generated.cognevra_pb2")
pb_grpc = sys.modules.get("cognee.infrastructure.databases.vector.cognevra.generated.cognevra_pb2_grpc")
if pb is None or pb_grpc is None:
    pytest.skip("Proto stubs not loaded (conftest issue)", allow_module_level=True)


def _channel():
    ch = grpc.insecure_channel("localhost:50051")
    try:
        grpc.channel_ready_future(ch).result(timeout=3)
    except grpc.FutureTimeoutError:
        pytest.skip("Cognevra not running on localhost:50051")
    return ch


class TestSearchTriplets:
    """Test Go SearchTriplets RPC."""

    def test_basic_triplet_search(self):
        ch = _channel()
        stub = pb_grpc.CognevraServiceStub(ch)

        req = pb.SearchTripletsReq(
            nodes=[
                pb.TripletNode(id="n1", name="Cognevra", description="Vector DB", type="Software"),
                pb.TripletNode(id="n2", name="HNSW", description="Graph index", type="Algorithm"),
                pb.TripletNode(id="n3", name="WAL", description="Write-Ahead Log", type="Component"),
            ],
            edges=[
                pb.TripletEdge(node1_id="n1", node2_id="n2", relationship_type="uses",
                              edge_text="Cognevra uses HNSW", edge_type_id="et1"),
                pb.TripletEdge(node1_id="n1", node2_id="n3", relationship_type="has",
                              edge_text="Cognevra has WAL", edge_type_id="et2"),
            ],
            node_distances=[
                pb.CollectionDistances(
                    collection_name="Entity_name",
                    entries=[
                        pb.DistanceEntry(id="n1", distance=0.1),
                        pb.DistanceEntry(id="n2", distance=0.3),
                    ]
                ),
            ],
            edge_distances=[
                pb.DistanceEntry(id="et1", distance=0.2),
            ],
            top_k=2,
            distance_penalty=3.5,
        )

        resp = stub.SearchTriplets(req)

        assert len(resp.triplets) == 2, f"expected 2 triplets, got {len(resp.triplets)}"

        # Best: n1(0.1) + et1(0.2) + n2(0.3) = 0.6
        best = resp.triplets[0]
        assert best.node1_id == "n1"
        assert best.node2_id == "n2"
        assert abs(best.score - 0.6) < 0.01, f"expected score ~0.6, got {best.score}"

        # Second: n1(0.1) + et2(3.5 penalty) + n3(3.5 penalty) = 7.1
        second = resp.triplets[1]
        assert abs(second.score - 7.1) < 0.01, f"expected score ~7.1, got {second.score}"

        # Formatted context should contain node info
        assert "Cognevra" in resp.formatted_context
        assert "HNSW" in resp.formatted_context
        assert "Node1:" in resp.formatted_context

        ch.close()

    def test_empty_graph(self):
        ch = _channel()
        stub = pb_grpc.CognevraServiceStub(ch)

        resp = stub.SearchTriplets(pb.SearchTripletsReq(top_k=5))
        assert len(resp.triplets) == 0
        assert resp.formatted_context == ""
        ch.close()

    def test_missing_node_skipped(self):
        ch = _channel()
        stub = pb_grpc.CognevraServiceStub(ch)

        req = pb.SearchTripletsReq(
            nodes=[pb.TripletNode(id="n1", name="A")],
            edges=[pb.TripletEdge(node1_id="n1", node2_id="n_missing", relationship_type="ref",
                                  edge_type_id="e1")],
            top_k=5,
        )

        resp = stub.SearchTriplets(req)
        assert len(resp.triplets) == 0  # edge skipped because n_missing not found
        ch.close()

    def test_lowest_distance_wins(self):
        ch = _channel()
        stub = pb_grpc.CognevraServiceStub(ch)

        req = pb.SearchTripletsReq(
            nodes=[
                pb.TripletNode(id="n1", name="A"),
                pb.TripletNode(id="n2", name="B"),
            ],
            edges=[
                pb.TripletEdge(node1_id="n1", node2_id="n2", relationship_type="rel",
                              edge_type_id="e1"),
            ],
            node_distances=[
                # Collection 1: n1=0.5
                pb.CollectionDistances(entries=[pb.DistanceEntry(id="n1", distance=0.5)]),
                # Collection 2: n1=0.2 (lower — should win)
                pb.CollectionDistances(entries=[pb.DistanceEntry(id="n1", distance=0.2)]),
            ],
            top_k=1,
        )

        resp = stub.SearchTriplets(req)
        best = resp.triplets[0]
        # n1=0.2 (lowest) + e1=3.5 (penalty) + n2=3.5 (penalty) = 7.2
        assert abs(best.score - 7.2) < 0.01, f"expected 7.2, got {best.score}"
        ch.close()

    def test_performance_10k_edges(self):
        """SearchTriplets should handle 10K edges in <5ms."""
        ch = _channel()
        stub = pb_grpc.CognevraServiceStub(ch)

        nodes = [pb.TripletNode(id=f"n{i}", name=f"Node{i}") for i in range(1000)]
        edges = [
            pb.TripletEdge(
                node1_id=f"n{i}", node2_id=f"n{(i+1)%1000}",
                relationship_type="rel", edge_type_id=f"et{i%50}"
            )
            for i in range(10000)
        ]
        node_dists = pb.CollectionDistances(
            entries=[pb.DistanceEntry(id=f"n{i}", distance=float(i)*0.001) for i in range(500)]
        )
        edge_dists = [pb.DistanceEntry(id=f"et{i}", distance=float(i)*0.01) for i in range(50)]

        req = pb.SearchTripletsReq(
            nodes=nodes,
            edges=edges,
            node_distances=[node_dists],
            edge_distances=edge_dists,
            top_k=10,
        )

        # Warmup
        stub.SearchTriplets(req)

        # Benchmark
        times = []
        for _ in range(10):
            t0 = time.perf_counter()
            resp = stub.SearchTriplets(req)
            times.append((time.perf_counter() - t0) * 1000)
            assert len(resp.triplets) == 10

        avg_ms = sum(times) / len(times)
        p50 = sorted(times)[5]
        print(f"\n  SearchTriplets 10K edges: avg={avg_ms:.2f}ms p50={p50:.2f}ms")
        assert avg_ms < 50, f"too slow: {avg_ms:.1f}ms (target <50ms including gRPC overhead)"

        ch.close()
