"""
Layer 3 tests: VectraDBAdapter.health_check() — dimension validation via /api/v1/info.

Tests the most autonomous Python validation layer: direct HTTP call to VectraDB
server to compare server dimension with embedding engine dimension.
"""

import logging
from unittest.mock import AsyncMock, MagicMock, patch

import aiohttp
import pytest

from cognee.infrastructure.databases.vector.vectradb.VectraDBAdapter import VectraDBAdapter


# ── Fake HTTP response for mocking aiohttp ────────────────────────────────────

class _FakeResponse:
    """Minimal mock of aiohttp.ClientResponse as async context manager."""

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


def _make_adapter(engine_dim: int = 768) -> VectraDBAdapter:
    """Create adapter with a mock embedding engine."""
    engine = MagicMock()
    engine.get_vector_size = MagicMock(return_value=engine_dim)
    adapter = VectraDBAdapter(
        url="http://localhost:8080",
        api_key=None,
        embedding_engine=engine,
    )
    return adapter


def _patch_session(adapter: VectraDBAdapter, fake_resp: _FakeResponse):
    """Patch the adapter's session to return a fake response for GET requests."""
    mock_session = MagicMock()
    mock_session.get = MagicMock(return_value=fake_resp)
    mock_session.closed = False
    adapter._session = mock_session
    return mock_session


# ── Tests ─────────────────────────────────────────────────────────────────────


class TestHealthCheckLayer3:
    @pytest.mark.asyncio
    async def test_matching_dims_passes(self):
        adapter = _make_adapter(engine_dim=768)
        resp = _FakeResponse(200, {"dimension": 768, "shards": 1, "status": "ready"})
        _patch_session(adapter, resp)

        # Should not raise
        await adapter.health_check()

    @pytest.mark.asyncio
    async def test_mismatched_dims_raises(self):
        adapter = _make_adapter(engine_dim=768)
        resp = _FakeResponse(200, {"dimension": 512, "shards": 1, "status": "ready"})
        _patch_session(adapter, resp)

        with pytest.raises(RuntimeError, match="DIMENSION MISMATCH"):
            await adapter.health_check()

    @pytest.mark.asyncio
    async def test_error_message_has_fix_instructions(self):
        adapter = _make_adapter(engine_dim=768)
        resp = _FakeResponse(200, {"dimension": 512, "shards": 1})
        _patch_session(adapter, resp)

        with pytest.raises(RuntimeError, match="Fix EMBEDDING_DIMENSIONS or VectraDB -dim flag"):
            await adapter.health_check()

    @pytest.mark.asyncio
    async def test_404_graceful_degradation(self):
        adapter = _make_adapter(engine_dim=768)
        resp = _FakeResponse(404, {})
        _patch_session(adapter, resp)

        # Should return None without error
        result = await adapter.health_check()
        assert result is None

    @pytest.mark.asyncio
    async def test_404_logs_warning(self, caplog):
        adapter = _make_adapter(engine_dim=768)
        resp = _FakeResponse(404, {})
        _patch_session(adapter, resp)

        with caplog.at_level(logging.WARNING):
            await adapter.health_check()

        assert any("old server version" in r.message for r in caplog.records), (
            f"Expected 'old server version' warning, got: {[r.message for r in caplog.records]}"
        )

    @pytest.mark.asyncio
    async def test_server_dim_none_passes(self):
        adapter = _make_adapter(engine_dim=768)
        resp = _FakeResponse(200, {"dimension": None, "shards": 1})
        _patch_session(adapter, resp)

        # None is not checked — should pass
        await adapter.health_check()

    @pytest.mark.asyncio
    async def test_server_5xx_raises(self):
        adapter = _make_adapter(engine_dim=768)
        resp = _FakeResponse(500, {})
        _patch_session(adapter, resp)

        with pytest.raises(aiohttp.ClientResponseError):
            await adapter.health_check()

    @pytest.mark.asyncio
    async def test_connection_refused_propagates(self):
        adapter = _make_adapter(engine_dim=768)
        mock_session = MagicMock()
        mock_session.closed = False

        class _RaisingCtx:
            async def __aenter__(self):
                raise aiohttp.ClientConnectorError(
                    connection_key=MagicMock(), os_error=OSError("Connection refused")
                )

            async def __aexit__(self, *a):
                pass

        mock_session.get = MagicMock(return_value=_RaisingCtx())
        adapter._session = mock_session

        with pytest.raises(aiohttp.ClientConnectorError):
            await adapter.health_check()

    @pytest.mark.asyncio
    async def test_correct_url_used(self):
        adapter = _make_adapter(engine_dim=768)
        resp = _FakeResponse(200, {"dimension": 768, "shards": 1})
        mock_session = _patch_session(adapter, resp)

        await adapter.health_check()

        mock_session.get.assert_called_once_with("http://localhost:8080/api/v1/info")
