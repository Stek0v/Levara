"""
Edge case tests for dimension validation across all layers.
"""

from unittest.mock import AsyncMock, MagicMock

import aiohttp
import pytest

from cognee.infrastructure.databases.vector.vectradb.VectraDBAdapter import VectraDBAdapter


# ── Helpers ───────────────────────────────────────────────────────────────────

class _FakeResponse:
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


def _make_adapter(engine_dim: int) -> VectraDBAdapter:
    engine = MagicMock()
    engine.get_vector_size = MagicMock(return_value=engine_dim)
    return VectraDBAdapter(
        url="http://localhost:8080",
        api_key=None,
        embedding_engine=engine,
    )


def _patch_session(adapter, resp):
    mock_session = MagicMock()
    mock_session.get = MagicMock(return_value=resp)
    mock_session.closed = False
    adapter._session = mock_session


# ── Edge case tests ───────────────────────────────────────────────────────────


class TestDimensionEdgeCases:
    @pytest.mark.asyncio
    async def test_dim_1_valid(self):
        """dim=1 is valid — smallest possible vector."""
        adapter = _make_adapter(engine_dim=1)
        resp = _FakeResponse(200, {"dimension": 1, "shards": 1})
        _patch_session(adapter, resp)
        await adapter.health_check()

    @pytest.mark.asyncio
    async def test_dim_4096_valid(self):
        """Large dimension (e.g., GPT-3 ada) → OK."""
        adapter = _make_adapter(engine_dim=4096)
        resp = _FakeResponse(200, {"dimension": 4096, "shards": 1})
        _patch_session(adapter, resp)
        await adapter.health_check()

    @pytest.mark.asyncio
    async def test_empty_embed_result(self):
        """embed_text returns [] → should cause an error."""
        from unittest.mock import patch

        embedding_engine = MagicMock()
        embedding_engine.embed_text = AsyncMock(return_value=[])
        embedding_engine.get_vector_size = MagicMock(return_value=768)

        vector_engine = MagicMock()
        vector_engine.embedding_engine = embedding_engine

        with patch(
            "cognee.infrastructure.databases.vector.get_vector_engine",
            return_value=vector_engine,
        ):
            from cognee.infrastructure.llm.utils import test_embedding_connection

            with pytest.raises((IndexError, RuntimeError)):
                await test_embedding_connection()

    @pytest.mark.asyncio
    async def test_nested_empty_embed(self):
        """embed_text returns [[]] → actual_dim=0 vs configured → RuntimeError."""
        from unittest.mock import patch

        embedding_engine = MagicMock()
        embedding_engine.embed_text = AsyncMock(return_value=[[]])
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

    @pytest.mark.asyncio
    async def test_server_missing_dimension_key(self):
        """Server returns {"shards": 4} without dimension key → server_dim=None → passes."""
        adapter = _make_adapter(engine_dim=768)
        resp = _FakeResponse(200, {"shards": 4})
        _patch_session(adapter, resp)
        # .get("dimension") returns None → passes
        await adapter.health_check()

    @pytest.mark.asyncio
    async def test_server_dim_zero(self):
        """Server dim=0 vs engine=768 → RuntimeError."""
        adapter = _make_adapter(engine_dim=768)
        resp = _FakeResponse(200, {"dimension": 0, "shards": 1})
        _patch_session(adapter, resp)
        with pytest.raises(RuntimeError, match="DIMENSION MISMATCH"):
            await adapter.health_check()

    @pytest.mark.asyncio
    async def test_server_dim_negative(self):
        """Server dim=-1 → RuntimeError (mismatch)."""
        adapter = _make_adapter(engine_dim=768)
        resp = _FakeResponse(200, {"dimension": -1, "shards": 1})
        _patch_session(adapter, resp)
        with pytest.raises(RuntimeError, match="DIMENSION MISMATCH"):
            await adapter.health_check()

    @pytest.mark.asyncio
    async def test_server_dim_string_type(self):
        """Server returns {"dimension": "768"} (string) → "768" != 768 → RuntimeError."""
        adapter = _make_adapter(engine_dim=768)
        resp = _FakeResponse(200, {"dimension": "768", "shards": 1})
        _patch_session(adapter, resp)
        with pytest.raises(RuntimeError, match="DIMENSION MISMATCH"):
            await adapter.health_check()
