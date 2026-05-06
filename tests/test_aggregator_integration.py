"""Tests for Go Search Aggregator via gRPC."""
import sys
from unittest.mock import AsyncMock, MagicMock
import pytest

from cognee.infrastructure.databases.vector.levara.LevaraAdapter import LevaraAdapter
pb = sys.modules["cognee.infrastructure.databases.vector.levara.generated.levara_pb2"]


def _make_adapter():
    adapter = LevaraAdapter(url="localhost:50051", api_key=None, embedding_engine=MagicMock())
    adapter._stub = MagicMock()
    return adapter


class TestAggregateSearch:
    @pytest.mark.asyncio
    async def test_basic_ranking(self):
        adapter = _make_adapter()
        adapter._stub.AggregateSearch = AsyncMock(return_value=pb.AggregateSearchResp(
            ranked_edges=[
                pb.RankedEdge(source_id="c", source_name="Charlie", target_id="d", target_name="Dave", relationship_name="works", score=0.3),
                pb.RankedEdge(source_id="a", source_name="Alice", target_id="b", target_name="Bob", relationship_name="knows", score=1.0),
            ],
            formatted_context="Nodes:\n...\nConnections:\n...",
            unique_nodes=4,
        ))

        result = await adapter.aggregate_search([
            {"source_id": "a", "source_name": "Alice", "target_id": "b", "target_name": "Bob",
             "relationship_name": "knows", "source_distance": 0.5, "target_distance": 0.3, "edge_distance": 0.2},
            {"source_id": "c", "source_name": "Charlie", "target_id": "d", "target_name": "Dave",
             "relationship_name": "works", "source_distance": 0.1, "target_distance": 0.1, "edge_distance": 0.1},
        ], top_k=10)

        assert len(result["ranked_edges"]) == 2
        assert result["ranked_edges"][0]["source_name"] == "Charlie"  # lower score first
        assert result["ranked_edges"][0]["score"] < result["ranked_edges"][1]["score"]
        assert result["unique_nodes"] == 4

    @pytest.mark.asyncio
    async def test_top_k_forwarded(self):
        adapter = _make_adapter()
        adapter._stub.AggregateSearch = AsyncMock(return_value=pb.AggregateSearchResp(
            ranked_edges=[], formatted_context="", unique_nodes=0,
        ))

        await adapter.aggregate_search([], top_k=5)
        req = adapter._stub.AggregateSearch.call_args[0][0]
        assert req.top_k == 5

    @pytest.mark.asyncio
    async def test_formatted_context_returned(self):
        adapter = _make_adapter()
        adapter._stub.AggregateSearch = AsyncMock(return_value=pb.AggregateSearchResp(
            ranked_edges=[],
            formatted_context="Nodes:\nNode: Alice\nConnections:\nAlice --[knows]--> Bob",
            unique_nodes=2,
        ))

        result = await adapter.aggregate_search([])
        assert "Nodes:" in result["formatted_context"]
        assert "Connections:" in result["formatted_context"]

    @pytest.mark.asyncio
    async def test_empty_edges(self):
        adapter = _make_adapter()
        adapter._stub.AggregateSearch = AsyncMock(return_value=pb.AggregateSearchResp(
            ranked_edges=[], formatted_context="", unique_nodes=0,
        ))

        result = await adapter.aggregate_search([])
        assert result["ranked_edges"] == []
        assert result["unique_nodes"] == 0

    @pytest.mark.asyncio
    async def test_result_dict_keys(self):
        adapter = _make_adapter()
        adapter._stub.AggregateSearch = AsyncMock(return_value=pb.AggregateSearchResp(
            ranked_edges=[pb.RankedEdge(source_id="a", source_name="A", target_id="b", target_name="B", relationship_name="r", score=0.5)],
            formatted_context="ctx", unique_nodes=2,
        ))

        result = await adapter.aggregate_search([{"source_id": "a"}])
        assert set(result.keys()) == {"ranked_edges", "formatted_context", "unique_nodes"}
        edge = result["ranked_edges"][0]
        assert set(edge.keys()) == {"source_id", "source_name", "target_id", "target_name", "relationship_name", "score"}

    @pytest.mark.asyncio
    async def test_edge_fields_coerced(self):
        """Numeric IDs and missing fields handled gracefully."""
        adapter = _make_adapter()
        adapter._stub.AggregateSearch = AsyncMock(return_value=pb.AggregateSearchResp(
            ranked_edges=[], formatted_context="", unique_nodes=0,
        ))

        await adapter.aggregate_search([{"source_id": 123, "source_distance": "0.5"}])
        req = adapter._stub.AggregateSearch.call_args[0][0]
        assert req.edges[0].source_id == "123"
        assert req.edges[0].source_distance == 0.5
