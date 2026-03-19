"""Integration tests for process_triplets() on CognevraAdapter.

Uses the gRPC stub pattern established in test_cognevra_adapter.py
and test_chunker_integration.py: no real server, AsyncMock on _stub.
"""

import sys
from unittest.mock import AsyncMock, MagicMock

import pytest

from cognee.infrastructure.databases.vector.cognevra.CognevraAdapter import CognevraAdapter

pb = sys.modules["cognee.infrastructure.databases.vector.cognevra.generated.cognevra_pb2"]


# ─── helpers ──────────────────────────────────────────────────────────────────

def _make_adapter() -> CognevraAdapter:
    adapter = CognevraAdapter(url="localhost:50051", api_key=None, embedding_engine=MagicMock())
    adapter._stub = MagicMock()
    return adapter


# ─── tests ────────────────────────────────────────────────────────────────────

class TestProcessTriplets:

    @pytest.mark.asyncio
    async def test_basic_triplet_creation(self):
        adapter = _make_adapter()
        adapter._stub.ProcessTriplets = AsyncMock(return_value=pb.ProcessTripletsResp(
            triplets=[
                pb.TripletResult(id="t1", from_node_id="n1", to_node_id="n2", text="Alice -› knows -› Bob"),
            ],
            created=1, skipped=0,
        ))

        result = await adapter.process_triplets(
            nodes=[{"id": "n1", "text": "Alice"}, {"id": "n2", "text": "Bob"}],
            edges=[{"source_id": "n1", "target_id": "n2", "relationship_name": "knows"}],
        )

        assert len(result) == 1
        assert result[0]["from_node_id"] == "n1"
        assert result[0]["to_node_id"] == "n2"
        assert result[0]["id"] == "t1"
        assert "Alice" in result[0]["text"]

    @pytest.mark.asyncio
    async def test_grpc_request_contains_correct_fields(self):
        """Adapter builds correct protobuf request from Python dicts."""
        adapter = _make_adapter()
        adapter._stub.ProcessTriplets = AsyncMock(return_value=pb.ProcessTripletsResp(
            triplets=[], created=0, skipped=0,
        ))

        await adapter.process_triplets(
            nodes=[{"id": "n1", "text": "Alice"}, {"id": "n2", "text": "Bob"}],
            edges=[{"source_id": "n1", "target_id": "n2", "relationship_name": "knows"}],
        )

        adapter._stub.ProcessTriplets.assert_called_once()
        req = adapter._stub.ProcessTriplets.call_args[0][0]
        assert len(req.nodes) == 2
        assert req.nodes[0].id == "n1"
        assert req.nodes[0].text == "Alice"
        assert req.nodes[1].id == "n2"
        assert req.nodes[1].text == "Bob"
        assert len(req.edges) == 1
        assert req.edges[0].source_id == "n1"
        assert req.edges[0].target_id == "n2"
        assert req.edges[0].relationship_name == "knows"

    @pytest.mark.asyncio
    async def test_dedup_skips_duplicate_edges(self):
        """When Go deduplicates, only unique triplets are returned."""
        adapter = _make_adapter()
        adapter._stub.ProcessTriplets = AsyncMock(return_value=pb.ProcessTripletsResp(
            triplets=[pb.TripletResult(id="t1", from_node_id="n1", to_node_id="n2", text="A -› B")],
            created=1, skipped=1,
        ))

        result = await adapter.process_triplets(
            nodes=[{"id": "n1", "text": "A"}, {"id": "n2", "text": "B"}],
            edges=[
                {"source_id": "n1", "target_id": "n2", "relationship_name": "rel"},
                {"source_id": "n1", "target_id": "n2", "relationship_name": "rel"},  # dup
            ],
        )

        assert len(result) == 1
        assert result[0]["from_node_id"] == "n1"
        assert result[0]["to_node_id"] == "n2"

    @pytest.mark.asyncio
    async def test_missing_node_returns_empty(self):
        """Edge referencing unknown node: Go returns no triplets."""
        adapter = _make_adapter()
        adapter._stub.ProcessTriplets = AsyncMock(return_value=pb.ProcessTripletsResp(
            triplets=[], created=0, skipped=1,
        ))

        result = await adapter.process_triplets(
            nodes=[{"id": "n1", "text": "A"}],
            edges=[{"source_id": "n1", "target_id": "n999", "relationship_name": "rel"}],
        )

        assert result == []

    @pytest.mark.asyncio
    async def test_edge_text_forwarded_to_grpc(self):
        """edge_text field is passed through in the protobuf request."""
        adapter = _make_adapter()
        adapter._stub.ProcessTriplets = AsyncMock(return_value=pb.ProcessTripletsResp(
            triplets=[pb.TripletResult(id="t1", from_node_id="n1", to_node_id="n2", text="A -› is friends with -› B")],
            created=1, skipped=0,
        ))

        result = await adapter.process_triplets(
            nodes=[{"id": "n1", "text": "A"}, {"id": "n2", "text": "B"}],
            edges=[{"source_id": "n1", "target_id": "n2", "relationship_name": "knows", "edge_text": "is friends with"}],
        )

        # Verify edge_text was sent in the request
        req = adapter._stub.ProcessTriplets.call_args[0][0]
        assert req.edges[0].edge_text == "is friends with"
        # Verify response text reflects the custom edge_text
        assert "is friends with" in result[0]["text"]

    @pytest.mark.asyncio
    async def test_empty_input(self):
        """Empty nodes and edges list returns empty list."""
        adapter = _make_adapter()
        adapter._stub.ProcessTriplets = AsyncMock(return_value=pb.ProcessTripletsResp(
            triplets=[], created=0, skipped=0,
        ))

        result = await adapter.process_triplets(nodes=[], edges=[])

        assert result == []
        req = adapter._stub.ProcessTriplets.call_args[0][0]
        assert len(req.nodes) == 0
        assert len(req.edges) == 0

    @pytest.mark.asyncio
    async def test_multiple_triplets_returned(self):
        """Multiple TripletResult entries are all returned."""
        adapter = _make_adapter()
        adapter._stub.ProcessTriplets = AsyncMock(return_value=pb.ProcessTripletsResp(
            triplets=[
                pb.TripletResult(id="t1", from_node_id="n1", to_node_id="n2", text="A -› rel1 -› B"),
                pb.TripletResult(id="t2", from_node_id="n2", to_node_id="n3", text="B -› rel2 -› C"),
                pb.TripletResult(id="t3", from_node_id="n1", to_node_id="n3", text="A -› rel3 -› C"),
            ],
            created=3, skipped=0,
        ))

        result = await adapter.process_triplets(
            nodes=[
                {"id": "n1", "text": "A"},
                {"id": "n2", "text": "B"},
                {"id": "n3", "text": "C"},
            ],
            edges=[
                {"source_id": "n1", "target_id": "n2", "relationship_name": "rel1"},
                {"source_id": "n2", "target_id": "n3", "relationship_name": "rel2"},
                {"source_id": "n1", "target_id": "n3", "relationship_name": "rel3"},
            ],
        )

        assert len(result) == 3
        ids = [r["id"] for r in result]
        assert "t1" in ids
        assert "t2" in ids
        assert "t3" in ids

    @pytest.mark.asyncio
    async def test_result_dict_keys(self):
        """Every result dict has exactly the four expected keys."""
        adapter = _make_adapter()
        adapter._stub.ProcessTriplets = AsyncMock(return_value=pb.ProcessTripletsResp(
            triplets=[
                pb.TripletResult(id="t1", from_node_id="n1", to_node_id="n2", text="some text"),
            ],
            created=1, skipped=0,
        ))

        result = await adapter.process_triplets(
            nodes=[{"id": "n1", "text": "X"}, {"id": "n2", "text": "Y"}],
            edges=[{"source_id": "n1", "target_id": "n2", "relationship_name": "r"}],
        )

        assert set(result[0].keys()) == {"id", "from_node_id", "to_node_id", "text"}

    @pytest.mark.asyncio
    async def test_node_id_coerced_to_str(self):
        """Non-string id values (e.g., int) are coerced to str before sending."""
        adapter = _make_adapter()
        adapter._stub.ProcessTriplets = AsyncMock(return_value=pb.ProcessTripletsResp(
            triplets=[], created=0, skipped=0,
        ))

        await adapter.process_triplets(
            nodes=[{"id": 42, "text": "numeric id"}, {"id": 99, "text": "another"}],
            edges=[{"source_id": 42, "target_id": 99, "relationship_name": "rel"}],
        )

        req = adapter._stub.ProcessTriplets.call_args[0][0]
        assert req.nodes[0].id == "42"
        assert req.edges[0].source_id == "42"
        assert req.edges[0].target_id == "99"

    @pytest.mark.asyncio
    async def test_missing_edge_text_defaults_to_empty_string(self):
        """Edges without edge_text key send empty string (not None) to gRPC."""
        adapter = _make_adapter()
        adapter._stub.ProcessTriplets = AsyncMock(return_value=pb.ProcessTripletsResp(
            triplets=[], created=0, skipped=0,
        ))

        await adapter.process_triplets(
            nodes=[{"id": "n1", "text": "A"}, {"id": "n2", "text": "B"}],
            edges=[{"source_id": "n1", "target_id": "n2", "relationship_name": "rel"}],
        )

        req = adapter._stub.ProcessTriplets.call_args[0][0]
        assert req.edges[0].edge_text == ""
