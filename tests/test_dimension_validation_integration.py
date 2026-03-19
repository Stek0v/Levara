"""
Integration tests: verify full dimension validation chain across layers.

Uses GrpcMockServer pattern so that adapter._stub = server is sufficient.
health_check() calls stub.Info(pb.Empty()) — no HTTP session needed.
"""

import asyncio
import logging
from unittest.mock import AsyncMock, MagicMock

import pytest

from cognee.infrastructure.databases.vector.cognevra.CognevraAdapter import CognevraAdapter

import sys

_DataPoint = sys.modules["cognee.infrastructure.engine"].DataPoint
pb = sys.modules["cognee.infrastructure.databases.vector.cognevra.generated.cognevra_pb2"]


# ── GrpcMockServer with Info RPC ─────────────────────────────────────────────

class GrpcMockServerWithInfo:
    """
    Mock Cognevra gRPC stub that also handles the Info RPC
    for dimension validation.
    """

    def __init__(self, dim: int = 768):
        self.dim = dim
        self._store = {}
        self._collections: set = set()
        self.insert_calls = 0
        self.info_calls = 0
        # Override to simulate Info RPC errors (None = succeed normally)
        self._info_error = None

    async def HasCollection(self, req):
        return pb.HasCollectionResp(exists=(req.name in self._collections))

    async def CreateCollection(self, req):
        self._collections.add(req.name)
        return pb.StatusResp(ok=True)

    async def DropCollection(self, req):
        self._collections.discard(req.name)
        return pb.StatusResp(ok=True)

    async def ListCollections(self, req):
        return pb.ListCollectionsResp(collections=list(self._collections))

    async def BatchInsert(self, req):
        self._collections.add(req.collection)
        inserted = 0
        for rec in req.records:
            self.insert_calls += 1
            self._store[rec.id] = {
                "vector": list(rec.vector),
                "metadata_json": rec.metadata_json,
            }
            inserted += 1
        return pb.BatchInsertResp(inserted=inserted, failed=0)

    async def Search(self, req):
        return pb.SearchResp(results=[])

    async def GetByID(self, req):
        records = []
        for rid in req.ids:
            if rid in self._store:
                records.append(pb.RecordEntry(
                    id=rid,
                    metadata_json=self._store[rid]["metadata_json"],
                    found=True,
                ))
            else:
                records.append(pb.RecordEntry(id=rid, found=False))
        return pb.GetByIDResp(records=records)

    async def Delete(self, req):
        deleted = sum(1 for rid in req.ids if self._store.pop(rid, None) is not None)
        return pb.DeleteResp(deleted=deleted, failed=0)

    async def Info(self, req):
        self.info_calls += 1
        if self._info_error is not None:
            raise self._info_error
        return pb.InfoResp(dimension=self.dim, shards=1, status="ready")


def _make_engine(dim: int = 768):
    engine = MagicMock()
    engine.get_vector_size = MagicMock(return_value=dim)

    async def embed_text(texts):
        if isinstance(texts, str):
            texts = [texts]
        return [[0.1] * dim for _ in texts]

    engine.embed_text = AsyncMock(side_effect=embed_text)
    return engine


def _make_adapter_with_info_server(
    server: GrpcMockServerWithInfo,
    engine_dim: int = 768,
) -> CognevraAdapter:
    engine = _make_engine(engine_dim)
    adapter = CognevraAdapter(
        url="localhost:50051",
        api_key=None,
        embedding_engine=engine,
    )
    # Wire the gRPC stub to our in-process mock
    adapter._stub = server
    return adapter


# ── Integration tests ─────────────────────────────────────────────────────────


class TestDimensionIntegration:
    @pytest.mark.asyncio
    async def test_full_chain_matching_dims(self):
        """All layers pass with matching dimensions."""
        server = GrpcMockServerWithInfo(dim=768)
        adapter = _make_adapter_with_info_server(server, engine_dim=768)

        # health_check calls stub.Info
        await adapter.health_check()

        # Verify Info was called
        assert server.info_calls == 1

    @pytest.mark.asyncio
    async def test_mismatch_caught_at_layer3(self):
        """Engine dim != server dim -> RuntimeError at Layer 3."""
        server = GrpcMockServerWithInfo(dim=512)
        adapter = _make_adapter_with_info_server(server, engine_dim=768)

        with pytest.raises(RuntimeError, match="DIMENSION MISMATCH"):
            await adapter.health_check()

    @pytest.mark.asyncio
    async def test_old_server_degrades_gracefully(self, caplog):
        """
        Server returns dimension=0 (e.g. old server version that doesn't set dim)
        -> no crash, just a log message.
        """
        # When server_dim == 0, the adapter skips the mismatch check and logs info.
        # Simulate old server that returns dimension=0.
        server = GrpcMockServerWithInfo(dim=0)
        adapter = _make_adapter_with_info_server(server, engine_dim=768)

        with caplog.at_level(logging.INFO):
            await adapter.health_check()

        # No RuntimeError raised — graceful degradation
        assert server.info_calls == 1

    @pytest.mark.asyncio
    async def test_dimension_change_detected(self):
        """Dims OK initially, then engine changes -> Layer 3 catches it."""
        server = GrpcMockServerWithInfo(dim=768)
        adapter = _make_adapter_with_info_server(server, engine_dim=768)

        # First check passes
        await adapter.health_check()

        # Simulate engine dimension change (e.g., model swap)
        adapter.embedding_engine.get_vector_size = MagicMock(return_value=384)

        with pytest.raises(RuntimeError, match="DIMENSION MISMATCH"):
            await adapter.health_check()

    @pytest.mark.asyncio
    async def test_concurrent_health_checks(self):
        """20 parallel health_checks -> no race conditions."""
        server = GrpcMockServerWithInfo(dim=768)
        adapter = _make_adapter_with_info_server(server, engine_dim=768)

        results = await asyncio.gather(
            *[adapter.health_check() for _ in range(20)],
            return_exceptions=True,
        )

        errors = [r for r in results if isinstance(r, Exception)]
        assert len(errors) == 0, f"Concurrent health checks produced errors: {errors}"
        assert server.info_calls == 20

    @pytest.mark.asyncio
    async def test_mismatch_caught_at_layer1(self):
        """embed dim != configured -> RuntimeError at Layer 1 before Layer 3/4."""
        from unittest.mock import patch

        embedding_engine = MagicMock()
        # embed returns 384-dim but configured is 768
        embedding_engine.embed_text = AsyncMock(return_value=[[0.1] * 384])
        embedding_engine.get_vector_size = MagicMock(return_value=768)

        vector_engine = MagicMock()
        vector_engine.embedding_engine = embedding_engine

        with patch(
            "cognee.infrastructure.databases.vector.get_vector_engine",
            return_value=vector_engine,
        ):
            from cognee.infrastructure.llm.utils import test_embedding_connection

            with pytest.raises(RuntimeError, match="EMBEDDING DIMENSION MISMATCH"):
                await test_embedding_connection()
