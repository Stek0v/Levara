"""
Integration tests: verify full dimension validation chain across layers.

Extends MockVectraServer from test_vectradb_integration.py with /api/v1/info
endpoint to test the complete dimension validation flow.
"""

import asyncio
import logging
from unittest.mock import AsyncMock, MagicMock

import aiohttp
import pytest

from cognee.infrastructure.databases.vector.vectradb.VectraDBAdapter import VectraDBAdapter

import sys

_DataPoint = sys.modules["cognee.infrastructure.engine"].DataPoint


# ── Extended MockVectraServer with /api/v1/info ──────────────────────────────

class MockVectraServerWithInfo:
    """
    Mock VectraDB server that also handles /api/v1/info
    for dimension validation.
    """

    def __init__(self, dim: int = 768):
        self.dim = dim
        self._store = {}
        self.insert_calls = 0
        self.info_calls = 0
        self._info_status = 200  # Override to simulate 404 etc.

    async def handle_insert(self, body: dict) -> dict:
        self.insert_calls += 1
        self._store[body["id"]] = {
            "vector": body["vector"],
            "metadata": body.get("metadata", {}),
        }
        return {"message": "ok"}

    async def handle_batch_insert(self, records) -> dict:
        if isinstance(records, dict):
            records = records.get("records", [])
        for rec in records:
            self.insert_calls += 1
            self._store[rec["id"]] = {
                "vector": rec["vector"],
                "metadata": rec.get("metadata", {}),
            }
        return {"inserted": len(records), "failed": 0}

    async def handle_search(self, body: dict) -> dict:
        return {"results": []}

    def get_info_response(self):
        return {"dimension": self.dim, "shards": 1, "status": "ready"}

    async def dispatch(self, path: str, payload: dict) -> dict:
        if path == "/api/v1/insert":
            return await self.handle_insert(payload)
        if path == "/api/v1/batch_insert":
            return await self.handle_batch_insert(payload)
        if path == "/api/v1/search":
            return await self.handle_search(payload)
        raise ValueError(f"Unknown path: {path}")


class _FakeInfoResponse:
    """Mock aiohttp response for /api/v1/info."""

    def __init__(self, status, json_body):
        self.status = status
        self._json = json_body

    def raise_for_status(self):
        if self.status >= 400 and self.status != 404:
            raise aiohttp.ClientResponseError(
                request_info=MagicMock(),
                history=(),
                status=self.status,
                message=f"HTTP {self.status}",
            )

    async def json(self, **kw):
        return self._json

    async def __aenter__(self):
        return self

    async def __aexit__(self, *a):
        pass


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
    server: MockVectraServerWithInfo,
    engine_dim: int = 768,
):
    engine = _make_engine(engine_dim)
    adapter = VectraDBAdapter(
        url="localhost:50051",
        api_key=None,
        embedding_engine=engine,
    )
    adapter._post = server.dispatch
    adapter._batch_post = server.handle_batch_insert

    # Wire session.get to return /api/v1/info responses
    mock_session = MagicMock()
    mock_session.closed = False

    def fake_get(url):
        server.info_calls += 1
        resp = _FakeInfoResponse(
            server._info_status, server.get_info_response()
        )
        return resp

    mock_session.get = MagicMock(side_effect=fake_get)
    adapter._session = mock_session

    return adapter


# ── Integration tests ─────────────────────────────────────────────────────────


class TestDimensionIntegration:
    @pytest.mark.asyncio
    async def test_full_chain_matching_dims(self):
        """All layers pass with matching dimensions."""
        server = MockVectraServerWithInfo(dim=768)
        adapter = _make_adapter_with_info_server(server, engine_dim=768)

        # Layer 3: health_check
        await adapter.health_check()

        # Verify info endpoint was called
        assert server.info_calls == 1

    @pytest.mark.asyncio
    async def test_mismatch_caught_at_layer3(self):
        """Engine dim != server dim → RuntimeError at Layer 3."""
        server = MockVectraServerWithInfo(dim=512)
        adapter = _make_adapter_with_info_server(server, engine_dim=768)

        with pytest.raises(RuntimeError, match="DIMENSION MISMATCH"):
            await adapter.health_check()

    @pytest.mark.asyncio
    async def test_old_server_degrades_gracefully(self, caplog):
        """404 from /api/v1/info → warning, no crash."""
        server = MockVectraServerWithInfo(dim=768)
        server._info_status = 404
        adapter = _make_adapter_with_info_server(server, engine_dim=768)

        with caplog.at_level(logging.WARNING):
            await adapter.health_check()

        assert any("old server version" in r.message for r in caplog.records)

    @pytest.mark.asyncio
    async def test_dimension_change_detected(self):
        """Dims OK initially, then engine changes → Layer 3 catches it."""
        server = MockVectraServerWithInfo(dim=768)
        adapter = _make_adapter_with_info_server(server, engine_dim=768)

        # First check passes
        await adapter.health_check()

        # Simulate engine dimension change (e.g., model swap)
        adapter.embedding_engine.get_vector_size = MagicMock(return_value=384)

        with pytest.raises(RuntimeError, match="DIMENSION MISMATCH"):
            await adapter.health_check()

    @pytest.mark.asyncio
    async def test_concurrent_health_checks(self):
        """20 parallel health_checks → no race conditions."""
        server = MockVectraServerWithInfo(dim=768)
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
        """embed dim != configured → RuntimeError at Layer 1 before Layer 3/4."""
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
