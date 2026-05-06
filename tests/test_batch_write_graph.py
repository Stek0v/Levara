"""A3: BatchWriteGraph integration test — real Neo4j, book entities.

Tests Go→Neo4j batch write via gRPC with Cypher verification.
Requires: Levara gRPC:50051, Neo4j:7687 (Docker).
"""
import grpc
import json
import subprocess
import sys
import time

import pytest

pb = sys.modules.get("cognee.infrastructure.databases.vector.levara.generated.levara_pb2")
pb_grpc = sys.modules.get("cognee.infrastructure.databases.vector.levara.generated.levara_pb2_grpc")
if pb is None or pb_grpc is None:
    pytest.skip("Proto stubs not loaded", allow_module_level=True)

NEO4J_URL = "bolt://localhost:7687"
NEO4J_USER = "neo4j"
NEO4J_PASS = "pleaseletmein"
NEO4J_DB = "neo4j"


def _cypher(query):
    """Run Cypher via docker exec and return stdout."""
    r = subprocess.run(
        ["docker", "exec", "new_db-neo4j-1", "cypher-shell",
         "-u", NEO4J_USER, "-p", NEO4J_PASS, query],
        capture_output=True, text=True, timeout=10,
    )
    return r.stdout.strip()


def _check_services():
    try:
        ch = grpc.insecure_channel("localhost:50051")
        grpc.channel_ready_future(ch).result(timeout=3)
        ch.close()
    except Exception:
        return False
    try:
        _cypher("RETURN 1")
        return True
    except Exception:
        return False


pytestmark = pytest.mark.skipif(not _check_services(), reason="Need Levara:50051 + Neo4j:7687")

# Book characters
CHARACTERS = [
    ("bwg-ember", "Character", {"name": "Эмбер", "role": "телепат", "chapter_intro": 1}),
    ("bwg-lucas", "Character", {"name": "Лукас", "role": "командир", "chapter_intro": 1}),
    ("bwg-zak", "Character", {"name": "Зак", "role": "техник", "chapter_intro": 2}),
    ("bwg-megan", "Character", {"name": "Меган", "role": "командир", "chapter_intro": 3}),
    ("bwg-morton", "Character", {"name": "Мортон", "role": "секретный телепат", "chapter_intro": 5}),
    ("bwg-adika", "Character", {"name": "Адика", "role": "охрана", "chapter_intro": 4}),
]

LOCATIONS = [
    ("bwg-hive", "Location", {"name": "Город-Улей", "population": "100 миллионов"}),
    ("bwg-farm", "Location", {"name": "Морская ферма", "type": "океан"}),
    ("bwg-base", "Location", {"name": "База ударной группы", "type": "военная"}),
]

CHAPTERS = [
    (f"bwg-ch{i}", "Chapter", {"name": f"Глава {i}", "number": i})
    for i in range(1, 11)
]

EDGES = [
    ("bwg-ember", "bwg-lucas", "PARTNER_OF", {"since": "начало книги"}),
    ("bwg-ember", "bwg-zak", "TEAMMATE", {"team": "ударная группа"}),
    ("bwg-ember", "bwg-megan", "REPORTS_TO", {}),
    ("bwg-morton", "bwg-ember", "HIDES_SECRET_FROM", {"secret": "телепатия"}),
    ("bwg-adika", "bwg-base", "GUARDS", {}),
    ("bwg-ember", "bwg-hive", "LIVES_IN", {}),
    ("bwg-ember", "bwg-ch1", "APPEARS_IN", {}),
    ("bwg-lucas", "bwg-ch1", "APPEARS_IN", {}),
    ("bwg-zak", "bwg-ch2", "APPEARS_IN", {}),
    ("bwg-megan", "bwg-ch3", "APPEARS_IN", {}),
    ("bwg-morton", "bwg-ch5", "APPEARS_IN", {}),
    ("bwg-ember", "bwg-farm", "VISITS", {"chapter": 7}),
]


def _stub():
    ch = grpc.insecure_channel("localhost:50051")
    grpc.channel_ready_future(ch).result(timeout=5)
    return pb_grpc.LevaraServiceStub(ch), ch


def _write_graph(stub):
    all_nodes = CHARACTERS + LOCATIONS + CHAPTERS
    nodes = [
        pb.GraphNodeWrite(id=nid, label=label, properties_json=json.dumps(props))
        for nid, label, props in all_nodes
    ]
    edges = [
        pb.GraphEdgeWrite(source_id=src, target_id=tgt,
                         relationship_name=rel, properties_json=json.dumps(props))
        for src, tgt, rel, props in EDGES
    ]
    return stub.BatchWriteGraph(pb.BatchWriteGraphReq(
        neo4j_url=NEO4J_URL, neo4j_user=NEO4J_USER,
        neo4j_password=NEO4J_PASS, neo4j_database=NEO4J_DB,
        nodes=nodes, edges=edges,
    ))


class TestBatchWriteGraph:

    @pytest.fixture(autouse=True)
    def cleanup(self):
        """Clean up test nodes before each test class run."""
        _cypher("MATCH (n) WHERE n.id STARTS WITH 'bwg-' DETACH DELETE n")
        yield
        # Don't clean after — let verify tests inspect

    def test_write_nodes_and_edges(self):
        stub, ch = _stub()
        t0 = time.perf_counter()
        resp = _write_graph(stub)
        ms = (time.perf_counter() - t0) * 1000

        total_nodes = len(CHARACTERS) + len(LOCATIONS) + len(CHAPTERS)
        assert resp.nodes_written == total_nodes, f"nodes: {resp.nodes_written} != {total_nodes}"
        assert resp.edges_written == len(EDGES), f"edges: {resp.edges_written} != {len(EDGES)}"
        assert len(resp.errors) == 0, f"errors: {resp.errors}"
        print(f"\n  BatchWriteGraph: {resp.nodes_written} nodes + {resp.edges_written} edges in {ms:.0f}ms")
        ch.close()

    def test_verify_characters_in_neo4j(self):
        stub, ch = _stub()
        _write_graph(stub)
        ch.close()

        out = _cypher("MATCH (n:Character) WHERE n.id STARTS WITH 'bwg-' RETURN count(n) AS cnt")
        # Parse "cnt\n6" format
        cnt = int(out.strip().split("\n")[-1])
        assert cnt == len(CHARACTERS), f"Characters: {cnt} != {len(CHARACTERS)}"
        print(f"\n  Neo4j Characters: {cnt}")

    def test_verify_locations_in_neo4j(self):
        stub, ch = _stub()
        _write_graph(stub)
        ch.close()

        out = _cypher("MATCH (n:Location) WHERE n.id STARTS WITH 'bwg-' RETURN count(n) AS cnt")
        cnt = int(out.strip().split("\n")[-1])
        assert cnt == len(LOCATIONS), f"Locations: {cnt} != {len(LOCATIONS)}"

    def test_verify_edges_in_neo4j(self):
        stub, ch = _stub()
        _write_graph(stub)
        ch.close()

        out = _cypher("MATCH (a)-[r]->(b) WHERE a.id STARTS WITH 'bwg-' RETURN count(r) AS cnt")
        cnt = int(out.strip().split("\n")[-1])
        assert cnt == len(EDGES), f"Edges: {cnt} != {len(EDGES)}"
        print(f"\n  Neo4j Edges: {cnt}")

    def test_idempotent_writes(self):
        """Writing same data twice should not duplicate nodes/edges."""
        stub, ch = _stub()
        _write_graph(stub)
        _write_graph(stub)  # second write — should be idempotent (MERGE)
        ch.close()

        out = _cypher("MATCH (n) WHERE n.id STARTS WITH 'bwg-' RETURN count(n) AS cnt")
        cnt = int(out.strip().split("\n")[-1])
        expected = len(CHARACTERS) + len(LOCATIONS) + len(CHAPTERS)
        assert cnt == expected, f"Idempotent: {cnt} != {expected} (duplicated!)"
        print(f"\n  Idempotent check: {cnt} nodes (expected {expected})")

    def test_properties_persisted(self):
        """Node properties should be readable from Neo4j."""
        stub, ch = _stub()
        _write_graph(stub)
        ch.close()

        out = _cypher("MATCH (n:Character {id: 'bwg-ember'}) RETURN n.name AS name, n.role AS role")
        lines = out.strip().split("\n")
        # Format: "name, role\n\"Эмбер\", \"телепат\""
        assert "Эмбер" in out, f"Expected Эмбер in output, got: {out}"
        assert "телепат" in out, f"Expected телепат in output, got: {out}"
        print(f"\n  Properties: {lines[-1]}")

    def test_relationship_properties(self):
        """Edge properties should be persisted."""
        stub, ch = _stub()
        _write_graph(stub)
        ch.close()

        out = _cypher(
            "MATCH (a:Character {id:'bwg-morton'})-[r:HIDES_SECRET_FROM]->(b:Character {id:'bwg-ember'}) "
            "RETURN r.secret AS secret"
        )
        assert "телепатия" in out, f"Expected телепатия, got: {out}"
        print(f"\n  Edge property: {out.split(chr(10))[-1]}")
