"""
Unit tests for VectraDBAdapter (Phase 1.1 + 1.2 + 1.3).

Stubs for the heavy cognee import chain are set up in conftest.py
which runs before this file is imported by pytest.

Run with:
    pytest cognee/tests/unit/infrastructure/databases/vector/test_vectradb_adapter.py -v
"""

import asyncio
import json
import sys
import uuid
from unittest.mock import AsyncMock, MagicMock, patch

import pytest

# conftest.py has already stubbed all cognee deps in sys.modules — safe to import now
from cognee.infrastructure.databases.vector.vectradb.VectraDBAdapter import (
    VectraDBAdapter,
    _serialize_payload,
)
from cognee.infrastructure.databases.exceptions import MissingQueryParameterError

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


def _make_adapter() -> VectraDBAdapter:
    return VectraDBAdapter(
        url="http://localhost:8080",
        api_key=None,
        embedding_engine=_make_embedding_engine(),
    )


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


# ─── Task 1.2: payload serialization (schema unification) ─────────────────────

class TestSerializePayload:
    def test_uuid_to_str(self):
        uid = uuid.uuid4()
        assert _serialize_payload({"id": uid}) == {"id": str(uid)}

    def test_nested_uuid(self):
        uid = uuid.uuid4()
        result = _serialize_payload({"outer": {"id": uid, "val": 42}})
        assert result["outer"]["id"] == str(uid)

    def test_list_with_uuid(self):
        uid = uuid.uuid4()
        result = _serialize_payload([uid, "hello", 3])
        assert result == [str(uid), "hello", 3]

    def test_non_serializable_becomes_str(self):
        # Objects with __dict__ are serialized as dict via vars()
        class WithAttr:
            pass  # vars(WithAttr()) == {}
        result = _serialize_payload({"w": WithAttr()})
        assert result["w"] == {}  # empty dict (not str) — correct behavior

        # Built-in method descriptors have no __dict__ and aren't JSON-safe → str
        result2 = _serialize_payload(int.__add__)
        assert isinstance(result2, str)

    def test_roundtrip_cognee_fields(self):
        """Task 1.2 DoD: all Cognee metadata fields survive serialization without loss."""
        dp_id = uuid.uuid4()
        payload = {
            "id": dp_id,
            "text": "sample",
            "belongs_to_set": ["set_a", "set_b"],
            "created_at": 1700000000000,
        }
        result = _serialize_payload(payload)
        assert result["id"] == str(dp_id)
        assert result["text"] == "sample"
        assert result["belongs_to_set"] == ["set_a", "set_b"]
        assert result["created_at"] == 1700000000000
        json.dumps(result)  # must be JSON-serializable, no exception


# ─── Task 1.1: collection management ─────────────────────────────────────────

class TestCollections:
    @pytest.mark.asyncio
    async def test_no_collections_initially(self):
        adapter = _make_adapter()
        result = await adapter.has_collection("col")
        assert result is False

    @pytest.mark.asyncio
    async def test_create_collection(self):
        adapter = _make_adapter()
        await adapter.create_collection("col")
        assert await adapter.has_collection("col") is True

    def test_prefixed_id_format(self):
        adapter = _make_adapter()
        uid = uuid.uuid4()
        assert adapter._prefixed_id("col", uid) == f"col:{uid}"

    def test_strip_prefix(self):
        adapter = _make_adapter()
        uid = str(uuid.uuid4())
        assert adapter._strip_prefix("col", f"col:{uid}") == uid


# ─── Task 1.1: insert and retrieve ───────────────────────────────────────────

class TestInsertAndRetrieve:
    @pytest.mark.asyncio
    async def test_insert_calls_vectradb_batch_api(self):
        """create_data_points must use /api/v1/batch_insert (one call per batch)."""
        adapter = _make_adapter()
        dp = _FakeDataPoint(text="hello")

        with patch.object(adapter, "_batch_post", new_callable=AsyncMock) as mock_batch:
            mock_batch.return_value = {"inserted": 1, "failed": 0}
            await adapter.create_data_points("col", [dp])

        # _batch_post receives the records list directly
        records = mock_batch.call_args[0][0]
        assert len(records) == 1
        assert records[0]["id"].startswith("col:")
        assert len(records[0]["vector"]) == 4
        assert isinstance(records[0]["metadata"], dict)

    @pytest.mark.asyncio
    async def test_insert_populates_id_cache(self):
        adapter = _make_adapter()
        dp = _FakeDataPoint(text="cached")

        with patch.object(adapter, "_batch_post", new_callable=AsyncMock) as mock_batch:
            mock_batch.return_value = {"inserted": 1, "failed": 0}
            await adapter.create_data_points("col", [dp])

        assert f"col:{dp.id}" in adapter._id_cache
        assert adapter._id_cache[f"col:{dp.id}"]["text"] == "cached"

    @pytest.mark.asyncio
    async def test_retrieve_from_cache(self):
        adapter = _make_adapter()
        uid = uuid.uuid4()
        adapter._id_cache[f"col:{uid}"] = {"text": "hi", "id": str(uid)}

        results = await adapter.retrieve("col", [str(uid)])
        assert len(results) == 1
        assert str(results[0].id) == str(uid)
        assert results[0].score == 0.0

    @pytest.mark.asyncio
    async def test_retrieve_cache_miss_returns_empty(self):
        adapter = _make_adapter()
        results = await adapter.retrieve("col", [str(uuid.uuid4())])
        assert results == []


# ─── Task 1.1: search ────────────────────────────────────────────────────────

class TestSearch:
    @pytest.mark.asyncio
    async def test_search_filters_by_collection_prefix(self):
        adapter = _make_adapter()
        uid_a, uid_b = str(uuid.uuid4()), str(uuid.uuid4())

        with patch.object(adapter, "_post", new_callable=AsyncMock) as mock_post:
            mock_post.return_value = {
                "results": [
                    {"id": f"col_a:{uid_a}", "score": 0.9, "metadata": {}},
                    {"id": f"col_b:{uid_b}", "score": 0.8, "metadata": {}},
                ]
            }
            results = await adapter.search("col_a", query_text="test", limit=10)

        assert len(results) == 1
        assert str(results[0].id) == uid_a

    @pytest.mark.asyncio
    async def test_search_score_inverted(self):
        """VectraDB similarity score (higher=better) → Cognee score (lower=better)."""
        adapter = _make_adapter()
        uid = str(uuid.uuid4())

        with patch.object(adapter, "_post", new_callable=AsyncMock) as mock_post:
            mock_post.return_value = {"results": [{"id": f"col:{uid}", "score": 0.9, "metadata": {}}]}
            results = await adapter.search("col", query_vector=[0.1]*4, limit=5)

        assert abs(results[0].score - 0.1) < 1e-6  # 1.0 - 0.9

    @pytest.mark.asyncio
    async def test_search_filters_by_node_name(self):
        adapter = _make_adapter()
        uid_a, uid_b = str(uuid.uuid4()), str(uuid.uuid4())

        with patch.object(adapter, "_post", new_callable=AsyncMock) as mock_post:
            mock_post.return_value = {
                "results": [
                    {"id": f"col:{uid_a}", "score": 0.9, "metadata": {"belongs_to_set": ["x"]}},
                    {"id": f"col:{uid_b}", "score": 0.8, "metadata": {"belongs_to_set": ["y"]}},
                ]
            }
            results = await adapter.search("col", query_vector=[0.1]*4, limit=10, node_name=["x"])

        assert len(results) == 1
        assert str(results[0].id) == uid_a

    @pytest.mark.asyncio
    async def test_search_no_query_raises(self):
        adapter = _make_adapter()
        with pytest.raises(MissingQueryParameterError):
            await adapter.search("col", limit=5)

    @pytest.mark.asyncio
    async def test_search_payload_excluded_when_not_requested(self):
        adapter = _make_adapter()
        uid = str(uuid.uuid4())

        with patch.object(adapter, "_post", new_callable=AsyncMock) as mock_post:
            mock_post.return_value = {"results": [{"id": f"col:{uid}", "score": 0.9, "metadata": {"x": 1}}]}
            results = await adapter.search("col", query_vector=[0.1]*4, limit=5, include_payload=False)

        assert results[0].payload is None


# ─── delete / prune ──────────────────────────────────────────────────────────

class TestDeletePrune:
    @pytest.mark.asyncio
    async def test_delete_removes_from_cache(self):
        adapter = _make_adapter()
        uid = uuid.uuid4()
        adapter._id_cache[f"col:{uid}"] = {"x": 1}
        await adapter.delete_data_points("col", [uid])
        assert f"col:{uid}" not in adapter._id_cache

    @pytest.mark.asyncio
    async def test_prune_clears_state(self):
        adapter = _make_adapter()
        adapter._collections.add("col")
        adapter._id_cache["col:abc"] = {}
        await adapter.prune()
        assert not adapter._collections
        assert not adapter._id_cache


# ─── Task 1.3: cross-store error propagation ─────────────────────────────────

class TestErrorPropagation:
    @pytest.mark.asyncio
    async def test_vectradb_error_propagates(self):
        """Task 1.3 DoD: VectraDB insert failure raises exception for pipeline rollback."""
        import aiohttp

        adapter = _make_adapter()
        dp = _FakeDataPoint(text="fail")

        with patch.object(
            adapter, "_batch_post", new_callable=AsyncMock,
            side_effect=aiohttp.ClientError("server error"),
        ):
            with pytest.raises(aiohttp.ClientError):
                await adapter.create_data_points("col", [dp])

    @pytest.mark.asyncio
    async def test_successful_insert_payload_available_immediately(self):
        """Task 1.3 DoD: after successful insert, payload is retrievable without extra queries."""
        adapter = _make_adapter()
        dp = _FakeDataPoint(text="sync", dp_id=uuid.uuid4())
        dp.belongs_to_set = ["grp_a"]

        with patch.object(adapter, "_batch_post", new_callable=AsyncMock) as mock_batch:
            mock_batch.return_value = {"inserted": 1, "failed": 0}
            await adapter.create_data_points("col", [dp])

        results = await adapter.retrieve("col", [str(dp.id)])
        assert results[0].payload["belongs_to_set"] == ["grp_a"]
