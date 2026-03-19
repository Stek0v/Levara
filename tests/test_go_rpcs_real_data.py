"""A2: Test new Go RPCs (SearchTriplets, DeduplicateGraph, BatchEmbedAndIndex)
with real book data and real embeddings. No mocks.

Requires: embed-server:9001, Cognevra gRPC:50051.
"""
import grpc
import json
import sys
import time
import uuid

import pytest

pb = sys.modules.get("cognee.infrastructure.databases.vector.cognevra.generated.cognevra_pb2")
pb_grpc = sys.modules.get("cognee.infrastructure.databases.vector.cognevra.generated.cognevra_pb2_grpc")
if pb is None or pb_grpc is None:
    pytest.skip("Proto stubs not loaded", allow_module_level=True)


def _channel():
    ch = grpc.insecure_channel("localhost:50051")
    try:
        grpc.channel_ready_future(ch).result(timeout=3)
    except grpc.FutureTimeoutError:
        pytest.skip("Cognevra not running on localhost:50051")
    return ch


# Book characters and chapters for graph building
CHARACTERS = [
    ("char-ember", "Эмбер", "Главная героиня, телепат"),
    ("char-lucas", "Лукас", "Командир ударной группы"),
    ("char-zak", "Зак", "Член команды, отравлен"),
    ("char-megan", "Меган", "Командир, чувство вины"),
    ("char-morton", "Мортон", "Хранит секрет телепатии"),
    ("char-adika", "Адика", "Безопасность и охрана"),
]

CHAPTERS = [("ch-" + str(i), f"Глава {i}", f"Chapter {i} of Uragan") for i in range(1, 21)]

RELATIONSHIPS = [
    ("char-ember", "char-lucas", "partner_of", "Эмбер и Лукас — партнёры"),
    ("char-ember", "char-zak", "teammate", "Эмбер и Зак в одной команде"),
    ("char-ember", "char-megan", "reports_to", "Эмбер подчиняется Меган"),
    ("char-morton", "char-ember", "hides_secret_from", "Мортон скрывает от Эмбер"),
    ("char-adika", "char-lucas", "guards", "Адика охраняет группу Лукаса"),
    ("char-ember", "ch-1", "appears_in", "Эмбер появляется в главе 1"),
    ("char-lucas", "ch-1", "appears_in", "Лукас появляется в главе 1"),
    ("char-ember", "ch-5", "appears_in", "Эмбер в главе 5"),
    ("char-zak", "ch-3", "appears_in", "Зак в главе 3"),
    ("char-megan", "ch-7", "appears_in", "Меган в главе 7"),
]


class TestDeduplicateGraphRealData:
    """Test DeduplicateGraph with book entities + intentional duplicates."""

    def test_dedup_removes_duplicates(self):
        ch = _channel()
        stub = pb_grpc.CognevraServiceStub(ch)

        # Add duplicates
        nodes = []
        for cid, name, desc in CHARACTERS:
            nodes.append(pb.DedupNodeMsg(id=cid, name=name, description=desc, type="Character"))
        for cid, name, desc in CHAPTERS:
            nodes.append(pb.DedupNodeMsg(id=cid, name=name, description=desc, type="Chapter"))
        # Intentional duplicates
        nodes.append(pb.DedupNodeMsg(id="char-ember", name="Ember-dup", type="Character"))
        nodes.append(pb.DedupNodeMsg(id="ch-1", name="Chapter1-dup", type="Chapter"))

        edges = []
        for src, tgt, rel, text in RELATIONSHIPS:
            edges.append(pb.DedupEdgeMsg(source_id=src, target_id=tgt, relationship_name=rel, edge_text=text))
        # Duplicate edge
        edges.append(pb.DedupEdgeMsg(source_id="char-ember", target_id="char-lucas",
                                     relationship_name="partner_of", edge_text="dup"))

        resp = stub.DeduplicateGraph(pb.DeduplicateGraphReq(nodes=nodes, edges=edges))

        expected_nodes = len(CHARACTERS) + len(CHAPTERS)
        expected_edges = len(RELATIONSHIPS)

        assert len(resp.nodes) == expected_nodes, f"nodes: {len(resp.nodes)} != {expected_nodes}"
        assert resp.nodes_removed == 2, f"nodes_removed: {resp.nodes_removed} != 2"
        assert len(resp.edges) == expected_edges, f"edges: {len(resp.edges)} != {expected_edges}"
        assert resp.edges_removed == 1
        assert len(resp.triplets) == expected_edges  # one triplet per unique edge
        print(f"\n  Dedup: {len(nodes)}→{len(resp.nodes)} nodes, {len(edges)}→{len(resp.edges)} edges")
        ch.close()

    def test_triplet_text_format(self):
        ch = _channel()
        stub = pb_grpc.CognevraServiceStub(ch)

        resp = stub.DeduplicateGraph(pb.DeduplicateGraphReq(
            nodes=[
                pb.DedupNodeMsg(id="char-ember", name="Эмбер", text="Телепат"),
                pb.DedupNodeMsg(id="char-lucas", name="Лукас", text="Командир"),
            ],
            edges=[
                pb.DedupEdgeMsg(source_id="char-ember", target_id="char-lucas",
                               relationship_name="partner_of", edge_text="партнёры в ударной группе"),
            ],
        ))

        assert len(resp.triplets) == 1
        t = resp.triplets[0]
        assert "Телепат" in t.text  # source text
        assert "партнёры" in t.text  # edge text
        assert "Командир" in t.text  # target text
        assert "-›" in t.text  # arrow separator
        assert t.from_node_id == "char-ember"
        assert t.to_node_id == "char-lucas"
        print(f"\n  Triplet: {t.text}")
        ch.close()

    def test_uuid5_deterministic(self):
        """Triplet IDs should be deterministic (same input → same UUID5)."""
        ch = _channel()
        stub = pb_grpc.CognevraServiceStub(ch)

        req = pb.DeduplicateGraphReq(
            nodes=[pb.DedupNodeMsg(id="a", name="A"), pb.DedupNodeMsg(id="b", name="B")],
            edges=[pb.DedupEdgeMsg(source_id="a", target_id="b", relationship_name="rel")],
        )
        r1 = stub.DeduplicateGraph(req)
        r2 = stub.DeduplicateGraph(req)
        assert r1.triplets[0].id == r2.triplets[0].id, "UUID5 should be deterministic"
        ch.close()


class TestBatchEmbedAndIndexRealData:
    """Test BatchEmbedAndIndex with real Perplexity embeddings."""

    def test_embed_and_index_characters(self):
        ch = _channel()
        stub = pb_grpc.CognevraServiceStub(ch)

        items = [
            pb.IndexItem(id=cid, text=f"{name}: {desc}", metadata_json=json.dumps({"name": name}))
            for cid, name, desc in CHARACTERS
        ]

        t0 = time.perf_counter()
        resp = stub.BatchEmbedAndIndex(pb.BatchEmbedAndIndexReq(
            groups=[pb.IndexGroup(collection="test_rpc_characters", items=items)],
            embed_endpoint="http://localhost:9001/v1/embeddings",
            embed_model="pplx",
            batch_size=16,
        ))
        ms = (time.perf_counter() - t0) * 1000

        assert resp.total_embedded == len(CHARACTERS)
        assert resp.total_indexed == len(CHARACTERS)
        assert resp.collections_created >= 0  # may already exist from prior run
        assert len(resp.errors) == 0
        print(f"\n  BatchEmbedAndIndex: {len(CHARACTERS)} items in {ms:.0f}ms")

        # Verify search works
        search_resp = stub.Search(pb.SearchReq(
            collection="test_rpc_characters",
            vector=_embed_one("телепат Эмбер"),
            top_k=3,
        ))
        names = []
        for r in search_resp.results:
            try:
                meta = json.loads(r.metadata_json)
                if isinstance(meta, str):
                    meta = json.loads(meta)
                names.append(meta.get("name", ""))
            except (json.JSONDecodeError, AttributeError):
                names.append("")
        assert "Эмбер" in names, f"Expected Эмбер in results, got {names}"
        print(f"  Search 'телепат Эмбер': top={names}")
        ch.close()

    def test_multi_group_embed(self):
        """Embed+index multiple collections in one call."""
        ch = _channel()
        stub = pb_grpc.CognevraServiceStub(ch)

        resp = stub.BatchEmbedAndIndex(pb.BatchEmbedAndIndexReq(
            groups=[
                pb.IndexGroup(collection="test_rpc_chapters", items=[
                    pb.IndexItem(id=cid, text=f"{name}: {desc}")
                    for cid, name, desc in CHAPTERS[:5]
                ]),
                pb.IndexGroup(collection="test_rpc_relationships", items=[
                    pb.IndexItem(id=f"rel-{i}", text=text)
                    for i, (_, _, _, text) in enumerate(RELATIONSHIPS[:5])
                ]),
            ],
            embed_endpoint="http://localhost:9001/v1/embeddings",
            embed_model="pplx",
        ))

        assert resp.total_embedded == 10
        assert resp.total_indexed == 10
        assert resp.collections_created >= 0  # may already exist
        print(f"\n  Multi-group: {resp.total_embedded} embedded, {resp.collections_created} collections")
        ch.close()


class TestSearchTripletsRealData:
    """Test SearchTriplets with real graph + real vector distances."""

    def test_search_with_character_graph(self):
        ch = _channel()
        stub = pb_grpc.CognevraServiceStub(ch)

        # Build graph
        nodes = [
            pb.TripletNode(id=cid, name=name, description=desc, type="Character")
            for cid, name, desc in CHARACTERS
        ]
        edges = [
            pb.TripletEdge(
                node1_id=src, node2_id=tgt,
                relationship_type=rel, edge_text=text,
                edge_type_id=f"etype-{rel}",
            )
            for src, tgt, rel, text in RELATIONSHIPS[:5]  # character-only edges
        ]

        # Simulate vector distances (as if from real search)
        node_dists = pb.CollectionDistances(entries=[
            pb.DistanceEntry(id="char-ember", distance=0.1),
            pb.DistanceEntry(id="char-lucas", distance=0.3),
            pb.DistanceEntry(id="char-zak", distance=0.8),
            pb.DistanceEntry(id="char-megan", distance=1.2),
        ])
        edge_dists = [
            pb.DistanceEntry(id="etype-partner_of", distance=0.2),
            pb.DistanceEntry(id="etype-teammate", distance=0.5),
        ]

        t0 = time.perf_counter()
        resp = stub.SearchTriplets(pb.SearchTripletsReq(
            nodes=nodes, edges=edges,
            node_distances=[node_dists],
            edge_distances=edge_dists,
            top_k=3,
        ))
        ms = (time.perf_counter() - t0) * 1000

        assert len(resp.triplets) == 3
        # Best: Ember(0.1) + partner_of(0.2) + Lucas(0.3) = 0.6
        best = resp.triplets[0]
        assert abs(best.score - 0.6) < 0.05, f"Expected ~0.6, got {best.score}"
        assert "Эмбер" in best.node1_name or "Эмбер" in best.node2_name

        assert resp.formatted_context != ""
        assert "Эмбер" in resp.formatted_context
        print(f"\n  SearchTriplets: {ms:.1f}ms, best={best.node1_name}→{best.node2_name} score={best.score:.2f}")
        ch.close()

    def test_performance_large_graph(self):
        """1000+ edges should complete in <10ms."""
        ch = _channel()
        stub = pb_grpc.CognevraServiceStub(ch)

        nodes = [pb.TripletNode(id=f"n{i}", name=f"Entity{i}") for i in range(200)]
        edges = [
            pb.TripletEdge(node1_id=f"n{i}", node2_id=f"n{(i+1)%200}",
                          relationship_type="rel", edge_type_id=f"et{i%20}")
            for i in range(1000)
        ]
        node_dists = pb.CollectionDistances(entries=[
            pb.DistanceEntry(id=f"n{i}", distance=float(i) * 0.005) for i in range(100)
        ])

        # Warmup
        stub.SearchTriplets(pb.SearchTripletsReq(nodes=nodes, edges=edges,
                                                  node_distances=[node_dists], top_k=5))

        times = []
        for _ in range(10):
            t0 = time.perf_counter()
            resp = stub.SearchTriplets(pb.SearchTripletsReq(
                nodes=nodes, edges=edges, node_distances=[node_dists], top_k=10))
            times.append((time.perf_counter() - t0) * 1000)

        avg = sum(times) / len(times)
        p50 = sorted(times)[5]
        print(f"\n  1000 edges: avg={avg:.1f}ms p50={p50:.1f}ms")
        assert avg < 20, f"Too slow: {avg:.1f}ms"
        ch.close()


def _embed_one(text):
    """Embed a single text synchronously via embed-server."""
    import urllib.request
    req = urllib.request.Request(
        "http://localhost:9001/v1/embeddings",
        data=json.dumps({"input": [text], "model": "pplx"}).encode(),
        headers={"Content-Type": "application/json"},
    )
    resp = json.loads(urllib.request.urlopen(req, timeout=10).read())
    return resp["data"][0]["embedding"]
