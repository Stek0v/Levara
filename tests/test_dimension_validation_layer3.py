"""
Layer 3 tests: LevaraAdapter.health_check() — dimension validation via gRPC Info RPC.

Tests the most autonomous Python validation layer: gRPC call to Levara
server to compare server dimension with embedding engine dimension.
"""

import logging
import sys
from unittest.mock import AsyncMock, MagicMock

import grpc
import grpc.aio
import pytest

from cognee.infrastructure.databases.vector.levara.LevaraAdapter import LevaraAdapter

pb = sys.modules["cognee.infrastructure.databases.vector.levara.generated.levara_pb2"]


# ── Helpers ───────────────────────────────────────────────────────────────────

def _make_adapter(engine_dim: int = 768) -> LevaraAdapter:
    """Create adapter with a mock embedding engine."""
    engine = MagicMock()
    engine.get_vector_size = MagicMock(return_value=engine_dim)
    adapter = LevaraAdapter(
        url="localhost:50051",
        api_key=None,
        embedding_engine=engine,
    )
    return adapter


def _patch_stub(adapter: LevaraAdapter, dimension: int, shards: int = 1, status: str = "ready"):
    """Replace adapter._stub with a MagicMock whose Info returns pb.InfoResp."""
    stub = MagicMock()
    stub.Info = AsyncMock(return_value=pb.InfoResp(
        dimension=dimension,
        shards=shards,
        status=status,
    ))
    adapter._stub = stub
    return stub


# ── Tests ─────────────────────────────────────────────────────────────────────


class TestHealthCheckLayer3:
    @pytest.mark.asyncio
    async def test_matching_dims_passes(self):
        adapter = _make_adapter(engine_dim=768)
        _patch_stub(adapter, dimension=768, shards=1, status="ready")

        # Should not raise
        await adapter.health_check()

    @pytest.mark.asyncio
    async def test_mismatched_dims_raises(self):
        adapter = _make_adapter(engine_dim=768)
        _patch_stub(adapter, dimension=512, shards=1, status="ready")

        with pytest.raises(RuntimeError, match="DIMENSION MISMATCH"):
            await adapter.health_check()

    @pytest.mark.asyncio
    async def test_error_message_has_fix_instructions(self):
        adapter = _make_adapter(engine_dim=768)
        _patch_stub(adapter, dimension=512, shards=1)

        with pytest.raises(RuntimeError, match="Fix EMBEDDING_DIMENSIONS or Levara -dim flag"):
            await adapter.health_check()

    @pytest.mark.asyncio
    async def test_server_dim_zero_passes(self):
        """dim=0 is the protobuf default (falsy), so health_check skips the check."""
        adapter = _make_adapter(engine_dim=768)
        _patch_stub(adapter, dimension=0, shards=1)

        # server_dim=0 is falsy → condition `if server_dim and ...` skips → passes
        await adapter.health_check()

    @pytest.mark.asyncio
    async def test_grpc_unavailable_raises_connection_error(self):
        """gRPC UNAVAILABLE status → ConnectionError."""
        adapter = _make_adapter(engine_dim=768)
        stub = MagicMock()
        err = grpc.aio.AioRpcError(
            code=grpc.StatusCode.UNAVAILABLE,
            initial_metadata=MagicMock(),
            trailing_metadata=MagicMock(),
            details="connection refused",
            debug_error_string="",
        )
        stub.Info = AsyncMock(side_effect=err)
        adapter._stub = stub

        with pytest.raises(ConnectionError):
            await adapter.health_check()

    @pytest.mark.asyncio
    async def test_grpc_deadline_exceeded_raises_connection_error(self):
        """gRPC DEADLINE_EXCEEDED → ConnectionError."""
        adapter = _make_adapter(engine_dim=768)
        stub = MagicMock()
        err = grpc.aio.AioRpcError(
            code=grpc.StatusCode.DEADLINE_EXCEEDED,
            initial_metadata=MagicMock(),
            trailing_metadata=MagicMock(),
            details="deadline exceeded",
            debug_error_string="",
        )
        stub.Info = AsyncMock(side_effect=err)
        adapter._stub = stub

        with pytest.raises(ConnectionError):
            await adapter.health_check()

    @pytest.mark.asyncio
    async def test_info_rpc_was_called(self):
        """health_check() must call _stub.Info exactly once."""
        adapter = _make_adapter(engine_dim=768)
        stub = _patch_stub(adapter, dimension=768, shards=1)

        await adapter.health_check()

        stub.Info.assert_called_once()
