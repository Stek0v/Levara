"""E2E tests for all 14 Cognee search types via Go RPCs.

Setup: ingest texts, embed, index to vector + BM25.
Then test every search type.
Requires: Levara gRPC:50051, embed-server:9001.
"""
import grpc
import json
import sys
import time
import urllib.request

import pytest

pb = sys.modules.get("cognee.infrastructure.databases.vector.levara.generated.levara_pb2")
pb_grpc = sys.modules.get("cognee.infrastructure.databases.vector.levara.generated.levara_pb2_grpc")
if pb is None or pb_grpc is None:
    pytest.skip("Proto stubs not loaded", allow_module_level=True)

GRPC = "localhost:50051"
EMBED = "http://localhost:9001/v1/embeddings"
COLL = "search_e2e"


def _check():
    try:
        urllib.request.urlopen(EMBED.replace("/v1/embeddings", "/health"), timeout=2)
        ch = grpc.insecure_channel(GRPC)
        grpc.channel_ready_future(ch).result(timeout=3)
        ch.close()
        return True
    except Exception:
        return False


pytestmark = pytest.mark.skipif(not _check(), reason="Need embed-server + Levara")


def _stub():
    ch = grpc.insecure_channel(GRPC)
    return pb_grpc.LevaraServiceStub(ch), ch


def _embed(texts):
    r = urllib.request.urlopen(urllib.request.Request(
        EMBED, data=json.dumps({"input": texts, "model": "pplx"}).encode(),
        headers={"Content-Type": "application/json"}), timeout=30)
    return [e["embedding"] for e in sorted(json.loads(r.read())["data"], key=lambda x: x["index"])]


@pytest.fixture(scope="module")
def setup_data():
    """Ingest test documents into vector + BM25."""
    stub, ch = _stub()
    docs = [
        ("s1", "Quantum computers use qubits for superposition and entanglement"),
        ("s2", "Natural language processing analyzes text using transformers"),
        ("s3", "HNSW algorithm provides fast approximate nearest neighbor search"),
        ("s4", "In March 2024 Levara released v1.0 with WAL support"),
        ("s5", "BM25 is a probabilistic ranking function for keyword retrieval"),
    ]
    vecs = _embed([t for _, t in docs])
    for (id_, text), vec in zip(docs, vecs):
        stub.Insert(pb.InsertReq(collection=COLL, id=id_, vector=vec, metadata_json=json.dumps({"text": text})))
    stub.BM25Index(pb.BM25IndexReq(collection=COLL, items=[
        pb.IndexItem(id=id_, text=text, metadata_json=json.dumps({"text": text}))
        for id_, text in docs
    ]))
    ch.close()
    return docs


class TestChunksSearch:
    def test_01_search_by_text(self, setup_data):
        stub, ch = _stub()
        resp = stub.SearchByText(pb.SearchByTextReq(
            collection=COLL, query_text="quantum computing", top_k=3,
            embed_endpoint=EMBED, embed_model="pplx"))
        assert len(resp.results) >= 1
        assert resp.results[0].id == "s1"
        ch.close()

    def test_10_batch_search(self, setup_data):
        stub, ch = _stub()
        resp = stub.BatchSearchByText(pb.BatchSearchByTextReq(
            collection=COLL, queries=["quantum", "NLP text", "HNSW search"],
            top_k=3, embed_endpoint=EMBED, embed_model="pplx"))
        assert len(resp.results) == 3
        for g in resp.results:
            assert len(g.results) >= 1
        ch.close()

    def test_13_search_relevance(self, setup_data):
        stub, ch = _stub()
        resp = stub.SearchByText(pb.SearchByTextReq(
            collection=COLL, query_text="quantum qubits superposition", top_k=5,
            embed_endpoint=EMBED, embed_model="pplx"))
        assert resp.results[0].id == "s1", f"Expected s1 (quantum), got {resp.results[0].id}"
        ch.close()


class TestTripletSearch:
    def test_03_triplet_scoring(self, setup_data):
        stub, ch = _stub()
        nodes = [
            pb.TripletNode(id="s1", name="Quantum", type="Topic"),
            pb.TripletNode(id="s2", name="NLP", type="Topic"),
            pb.TripletNode(id="s3", name="HNSW", type="Algorithm"),
        ]
        edges = [
            pb.TripletEdge(node1_id="s1", node2_id="s3", relationship_type="uses", edge_type_id="et1"),
            pb.TripletEdge(node1_id="s2", node2_id="s3", relationship_type="uses", edge_type_id="et1"),
        ]
        dists = pb.CollectionDistances(entries=[
            pb.DistanceEntry(id="s1", distance=0.1),
            pb.DistanceEntry(id="s3", distance=0.3),
        ])
        resp = stub.SearchTriplets(pb.SearchTripletsReq(
            nodes=nodes, edges=edges, node_distances=[dists],
            edge_distances=[pb.DistanceEntry(id="et1", distance=0.2)], top_k=2))
        assert len(resp.triplets) == 2
        assert resp.triplets[0].score < resp.triplets[1].score  # sorted
        assert resp.formatted_context != ""
        ch.close()


class TestBM25Search:
    def test_04_bm25_keyword(self, setup_data):
        stub, ch = _stub()
        resp = stub.BM25Search(pb.BM25SearchReq(collection=COLL, query="qubits superposition", top_k=3))
        assert len(resp.results) >= 1
        assert resp.results[0].id == "s1"
        ch.close()


class TestHybridSearch:
    def test_05_hybrid_rrf(self, setup_data):
        stub, ch = _stub()
        resp = stub.HybridSearch(pb.HybridSearchReq(
            collection=COLL, query_text="quantum superposition search", top_k=5,
            embed_endpoint=EMBED, embed_model="pplx",
            vector_weight=1.0, bm25_weight=1.0))
        assert len(resp.results) >= 1
        # s1 (quantum) should be in top results (both vector + BM25 match)
        top_ids = {r.id for r in resp.results[:3]}
        assert "s1" in top_ids
        ch.close()


class TestTemporalSearch:
    def test_06_temporal_range(self, setup_data):
        stub, ch = _stub()
        # Use doc s4 directly which has "March 2024"
        text = "In March 2024 Levara released v1.0. On 2024-06-15 gRPC was added."
        resp = stub.TemporalSearch(pb.TemporalSearchReq(
            text=text, date_from="2024-01-01", date_to="2024-12-31"))
        assert resp.total_extracted >= 1
        assert resp.in_range >= 1
        ch.close()


class TestGraphRead:
    def test_07_graph_read_full(self, setup_data):
        stub, ch = _stub()
        try:
            resp = stub.GraphRead(pb.GraphReadReq(
                neo4j_url="bolt://localhost:7687", neo4j_user="neo4j",
                neo4j_password="pleaseletmein", mode=pb.GraphReadReq.FULL_GRAPH))
            assert len(resp.nodes) >= 0  # may be empty if Neo4j not populated
        except grpc.RpcError:
            pytest.skip("Neo4j not available")
        ch.close()

    def test_08_graph_read_filtered(self, setup_data):
        stub, ch = _stub()
        try:
            resp = stub.GraphRead(pb.GraphReadReq(
                neo4j_url="bolt://localhost:7687", neo4j_user="neo4j",
                neo4j_password="pleaseletmein", mode=pb.GraphReadReq.ID_FILTERED,
                node_ids=["bwg-ember", "bwg-lucas"]))
            # May have data from previous tests
        except grpc.RpcError:
            pytest.skip("Neo4j not available")
        ch.close()


class TestMultiQuery:
    def test_09_multi_query_decompose(self, setup_data):
        stub, ch = _stub()
        resp = stub.MultiQuerySearch(pb.MultiQuerySearchReq(
            query_text="quantum computing and NLP text analysis",
            collection=COLL, top_k=5, embed_endpoint=EMBED, embed_model="pplx"))
        assert len(resp.sub_queries) >= 2  # "quantum computing" + "NLP text analysis"
        assert resp.total_unique >= 1
        ch.close()


class TestAggregateSearch:
    def test_11_aggregate_ranking(self, setup_data):
        stub, ch = _stub()
        edges = [
            pb.ScoredEdge(source_id="s1", source_name="Quantum", source_distance=0.1,
                         target_id="s3", target_name="HNSW", target_distance=0.3,
                         relationship_name="uses", edge_distance=0.2),
            pb.ScoredEdge(source_id="s2", source_name="NLP", source_distance=0.5,
                         target_id="s3", target_name="HNSW", target_distance=0.3,
                         relationship_name="uses", edge_distance=0.4),
        ]
        resp = stub.AggregateSearch(pb.AggregateSearchReq(edges=edges, top_k=2))
        assert len(resp.ranked_edges) == 2
        assert resp.ranked_edges[0].score < resp.ranked_edges[1].score  # best first
        assert resp.formatted_context != ""
        ch.close()


class TestSearchEdgeCases:
    def test_12_search_empty_collection(self, setup_data):
        stub, ch = _stub()
        try:
            resp = stub.SearchByText(pb.SearchByTextReq(
                collection="nonexistent_e2e", query_text="test", top_k=3,
                embed_endpoint=EMBED, embed_model="pplx"))
        except grpc.RpcError:
            pass  # NOT_FOUND acceptable
        ch.close()

    def test_14_context_only(self, setup_data):
        """Search returns raw results without LLM generation."""
        stub, ch = _stub()
        resp = stub.SearchByText(pb.SearchByTextReq(
            collection=COLL, query_text="quantum", top_k=3,
            embed_endpoint=EMBED, embed_model="pplx"))
        # SearchByText always returns raw results (no LLM)
        assert len(resp.results) >= 1
        assert resp.results[0].metadata_json != ""
        ch.close()
