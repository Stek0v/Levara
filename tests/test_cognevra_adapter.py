"""
Unit tests for CognevraAdapter (gRPC transport).

Stubs for the heavy cognee import chain are set up in conftest.py.
Generated protobuf modules are loaded by conftest.py from the generated/ directory.
"""

import asyncio
import json
import sys
import uuid
from unittest.mock import AsyncMock, MagicMock, patch

import grpc
import grpc.aio
import pytest

from cognee.infrastructure.databases.vector.cognevra.CognevraAdapter import (
    CognevraAdapter,
    _serialize_for_json,
)
from cognee.infrastructure.databases.exceptions import MissingQueryParameterError

# Load protobuf types from conftest-registered modules
pb = sys.modules["cognee.infrastructure.databases.vector.cognevra.generated.cognevra_pb2"]
pb_grpc = sys.modules["cognee.infrastructure.databases.vector.cognevra.generated.cognevra_pb2_grpc"]

_DataPoint = sys.modules["cognee.infrastructure.engine"].DataPoint
ScoredResult = sys.modules[
    "cognee.infrastructure.databases.vector.models.ScoredResult"
].ScoredResult


# ─── helpers ──────────────────────────────────────────────────────────────────

def _make_embedding_engine(dim: int = 4) -> MagicMock:
    engine = MagicMock()
    engine.embed_text = AsyncMock(return_value=[[0.1] * dim])
    engine.get_vector_size = MagicMock(return_value=dim)
    return engine


def _make_adapter() -> CognevraAdapter:
    adapter = CognevraAdapter(
        url="localhost:50051",
        api_key=None,
        embedding_engine=_make_embedding_engine(),
    )
    # Replace stub with mock for unit testing (no real gRPC server)
    adapter._stub = MagicMock()
    return adapter


class _FakeDataPoint(_DataPoint):
    def __init__(self, text: str = "hello", dp_id=None):
        self.id = dp_id or uuid.uuid4()
        self.text = text
        self.metadata = {"index_fields": ["text"]}
        self.belongs_to_set = []
        self.created_at = 0

    def model_dump(self):
        return {
            "id": self.id,
            "text": self.text,
            "belongs_to_set": self.belongs_to_set,
            "created_at": self.created_at,
        }


# ─── serialization ───────────────────────────────────────────────────────────

class TestSerializeForJson:
    def test_uuid_to_str(self):
        uid = uuid.uuid4()
        assert _serialize_for_json({"id": uid}) == {"id": str(uid)}

    def test_nested_uuid(self):
        uid = uuid.uuid4()
        result = _serialize_for_json({"outer": {"id": uid, "val": 42}})
        assert result["outer"]["id"] == str(uid)

    def test_list_with_uuid(self):
        uid = uuid.uuid4()
        result = _serialize_for_json([uid, "hello", 3])
        assert result == [str(uid), "hello", 3]

    def test_non_serializable_becomes_str(self):
        class WithAttr:
            pass
        result = _serialize_for_json({"w": WithAttr()})
        assert result["w"] == {}

        result2 = _serialize_for_json(int.__add__)
        assert isinstance(result2, str)

    def test_roundtrip_cognee_fields(self):
        dp_id = uuid.uuid4()
        payload = {
            "id": dp_id, "text": "sample",
            "belongs_to_set": ["set_a", "set_b"], "created_at": 1700000000000,
        }
        result = _serialize_for_json(payload)
        assert result["id"] == str(dp_id)
        json.dumps(result)


# ─── collections (gRPC) ─────────────────────────────────────────────────────

class TestCollections:
    @pytest.mark.asyncio
    async def test_has_collection_false(self):
        adapter = _make_adapter()
        adapter._stub.HasCollection = AsyncMock(
            return_value=pb.HasCollectionResp(exists=False)
        )
        assert await adapter.has_collection("col") is False

    @pytest.mark.asyncio
    async def test_has_collection_true_after_create(self):
        adapter = _make_adapter()
        adapter._stub.CreateCollection = AsyncMock(
            return_value=pb.StatusResp(ok=True)
        )
        adapter._stub.HasCollection = AsyncMock(
            return_value=pb.HasCollectionResp(exists=True)
        )
        await adapter.create_collection("col")
        assert await adapter.has_collection("col") is True


# ─── insert and retrieve (gRPC) ─────────────────────────────────────────────

class TestInsertAndRetrieve:
    @pytest.mark.asyncio
    async def test_insert_calls_batch_insert_rpc(self):
        adapter = _make_adapter()
        dp = _FakeDataPoint(text="hello")

        adapter._stub.BatchInsert = AsyncMock(
            return_value=pb.BatchInsertResp(inserted=1, failed=0)
        )
        await adapter.create_data_points("col", [dp])

        adapter._stub.BatchInsert.assert_called_once()
        req = adapter._stub.BatchInsert.call_args[0][0]
        assert req.collection == "col"
        assert len(req.records) == 1
        assert req.records[0].id == str(dp.id)
        assert len(req.records[0].vector) == 4

    @pytest.mark.asyncio
    async def test_retrieve_from_server(self):
        adapter = _make_adapter()
        uid = uuid.uuid4()
        meta = json.dumps({"text": "hi", "id": str(uid)})

        adapter._stub.GetByID = AsyncMock(
            return_value=pb.GetByIDResp(records=[
                pb.RecordEntry(id=str(uid), metadata_json=meta, found=True),
            ])
        )
        results = await adapter.retrieve("col", [str(uid)])
        assert len(results) == 1
        assert str(results[0].id) == str(uid)
        assert results[0].score == 0.0
        assert results[0].payload["text"] == "hi"

    @pytest.mark.asyncio
    async def test_retrieve_not_found_returns_empty(self):
        adapter = _make_adapter()
        adapter._stub.GetByID = AsyncMock(
            return_value=pb.GetByIDResp(records=[
                pb.RecordEntry(id="missing", metadata_json="", found=False),
            ])
        )
        results = await adapter.retrieve("col", ["missing"])
        assert results == []

    @pytest.mark.asyncio
    async def test_insert_retrieve_roundtrip(self):
        """After insert, retrieve returns the same payload."""
        adapter = _make_adapter()
        dp = _FakeDataPoint(text="sync", dp_id=uuid.uuid4())
        dp.belongs_to_set = ["grp_a"]

        adapter._stub.BatchInsert = AsyncMock(
            return_value=pb.BatchInsertResp(inserted=1, failed=0)
        )
        await adapter.create_data_points("col", [dp])

        # Capture what was sent
        req = adapter._stub.BatchInsert.call_args[0][0]
        sent_meta = req.records[0].metadata_json

        adapter._stub.GetByID = AsyncMock(
            return_value=pb.GetByIDResp(records=[
                pb.RecordEntry(id=str(dp.id), metadata_json=sent_meta, found=True),
            ])
        )
        results = await adapter.retrieve("col", [str(dp.id)])
        assert results[0].payload["belongs_to_set"] == ["grp_a"]


# ─── search (gRPC) ──────────────────────────────────────────────────────────

class TestSearch:
    @pytest.mark.asyncio
    async def test_search_returns_results(self):
        adapter = _make_adapter()
        uid = str(uuid.uuid4())
        adapter._stub.Search = AsyncMock(
            return_value=pb.SearchResp(results=[
                pb.SearchResult(id=uid, score=0.9, metadata_json='{"x":1}'),
            ])
        )
        results = await adapter.search("col", query_vector=[0.1]*4, limit=5)
        assert len(results) == 1
        assert str(results[0].id) == uid

    @pytest.mark.asyncio
    async def test_search_score_inverted(self):
        adapter = _make_adapter()
        uid = str(uuid.uuid4())
        adapter._stub.Search = AsyncMock(
            return_value=pb.SearchResp(results=[
                pb.SearchResult(id=uid, score=0.9, metadata_json="{}"),
            ])
        )
        results = await adapter.search("col", query_vector=[0.1]*4, limit=5)
        assert abs(results[0].score - 0.1) < 1e-6

    @pytest.mark.asyncio
    async def test_search_filters_by_node_name(self):
        adapter = _make_adapter()
        uid_a, uid_b = str(uuid.uuid4()), str(uuid.uuid4())
        adapter._stub.Search = AsyncMock(
            return_value=pb.SearchResp(results=[
                pb.SearchResult(id=uid_a, score=0.9,
                    metadata_json=json.dumps({"belongs_to_set": ["x"]})),
                pb.SearchResult(id=uid_b, score=0.8,
                    metadata_json=json.dumps({"belongs_to_set": ["y"]})),
            ])
        )
        results = await adapter.search("col", query_vector=[0.1]*4, limit=10, node_name=["x"])
        assert len(results) == 1
        assert str(results[0].id) == uid_a

    @pytest.mark.asyncio
    async def test_search_no_query_raises(self):
        adapter = _make_adapter()
        with pytest.raises(MissingQueryParameterError):
            await adapter.search("col", limit=5)

    @pytest.mark.asyncio
    async def test_search_payload_excluded(self):
        adapter = _make_adapter()
        uid = str(uuid.uuid4())
        adapter._stub.Search = AsyncMock(
            return_value=pb.SearchResp(results=[
                pb.SearchResult(id=uid, score=0.9, metadata_json='{"x":1}'),
            ])
        )
        results = await adapter.search("col", query_vector=[0.1]*4, limit=5, include_payload=False)
        assert results[0].payload is None

    @pytest.mark.asyncio
    async def test_search_payload_included(self):
        adapter = _make_adapter()
        uid = str(uuid.uuid4())
        adapter._stub.Search = AsyncMock(
            return_value=pb.SearchResp(results=[
                pb.SearchResult(id=uid, score=0.9, metadata_json='{"x":1}'),
            ])
        )
        results = await adapter.search("col", query_vector=[0.1]*4, limit=5, include_payload=True)
        assert results[0].payload == {"x": 1}


# ─── delete / prune (gRPC) ──────────────────────────────────────────────────

class TestDeletePrune:
    @pytest.mark.asyncio
    async def test_delete_calls_server(self):
        adapter = _make_adapter()
        uid = uuid.uuid4()
        adapter._stub.Delete = AsyncMock(
            return_value=pb.DeleteResp(deleted=1, failed=0)
        )
        await adapter.delete_data_points("col", [uid])
        adapter._stub.Delete.assert_called_once()
        req = adapter._stub.Delete.call_args[0][0]
        assert req.collection == "col"
        assert str(uid) in req.ids

    @pytest.mark.asyncio
    async def test_prune_drops_all_collections(self):
        adapter = _make_adapter()
        adapter._stub.ListCollections = AsyncMock(
            return_value=pb.ListCollectionsResp(collections=["a", "b"])
        )
        adapter._stub.DropCollection = AsyncMock(
            return_value=pb.StatusResp(ok=True)
        )
        await adapter.prune()
        assert adapter._stub.DropCollection.call_count == 2


# ─── error handling ─────────────────────────────────────────────────────────

class TestErrorHandling:
    @pytest.mark.asyncio
    async def test_connection_error_on_unavailable(self):
        """UNAVAILABLE gRPC status -> ConnectionError."""
        adapter = _make_adapter()

        async def raise_unavailable(*args, **kwargs):
            raise grpc.aio.AioRpcError(
                code=grpc.StatusCode.UNAVAILABLE,
                initial_metadata=None,
                trailing_metadata=None,
                details="server down",
                debug_error_string=None,
            )

        adapter._stub.HasCollection = raise_unavailable
        with pytest.raises(ConnectionError, match="unavailable"):
            await adapter.has_collection("col")

    @pytest.mark.asyncio
    async def test_batch_insert_partial_failure_raises(self):
        adapter = _make_adapter()
        dp = _FakeDataPoint(text="fail")
        adapter._stub.BatchInsert = AsyncMock(
            return_value=pb.BatchInsertResp(inserted=0, failed=1, errors=["disk full"])
        )
        with pytest.raises(RuntimeError, match="partial failure"):
            await adapter.create_data_points("col", [dp])
