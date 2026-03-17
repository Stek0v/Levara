"""
Layer 1 tests: test_embedding_connection() from utils.py.

Tests startup validation that embeds a probe text and compares
actual dimension with configured EMBEDDING_DIMENSIONS.
"""

import asyncio
import sys
from unittest.mock import AsyncMock, MagicMock, patch

import pytest


class TestEmbeddingConnectionLayer1:
    """Tests for test_embedding_connection() — Layer 1 dimension validation."""

    def _make_mock_engine(self, embed_result, configured_dim: int = 768):
        """Create a mock vector engine with embedding engine inside."""
        embedding_engine = MagicMock()
        embedding_engine.embed_text = AsyncMock(return_value=embed_result)
        embedding_engine.get_vector_size = MagicMock(return_value=configured_dim)

        vector_engine = MagicMock()
        vector_engine.embedding_engine = embedding_engine
        return vector_engine

    @pytest.mark.asyncio
    async def test_matching_dims_passes(self):
        engine = self._make_mock_engine(
            embed_result=[[0.1] * 768], configured_dim=768
        )
        with patch(
            "cognee.infrastructure.databases.vector.get_vector_engine",
            return_value=engine,
        ):
            from cognee.infrastructure.llm.utils import test_embedding_connection

            # Should not raise
            await test_embedding_connection()

    @pytest.mark.asyncio
    async def test_mismatched_dims_raises(self):
        engine = self._make_mock_engine(
            embed_result=[[0.1] * 384], configured_dim=768
        )
        with patch(
            "cognee.infrastructure.databases.vector.get_vector_engine",
            return_value=engine,
        ):
            from cognee.infrastructure.llm.utils import test_embedding_connection

            with pytest.raises(RuntimeError, match="EMBEDDING DIMENSION MISMATCH"):
                await test_embedding_connection()

    @pytest.mark.asyncio
    async def test_error_has_remediation(self):
        engine = self._make_mock_engine(
            embed_result=[[0.1] * 384], configured_dim=768
        )
        with patch(
            "cognee.infrastructure.databases.vector.get_vector_engine",
            return_value=engine,
        ):
            from cognee.infrastructure.llm.utils import test_embedding_connection

            with pytest.raises(RuntimeError, match="Update .env to match your embedding model"):
                await test_embedding_connection()

    @pytest.mark.asyncio
    async def test_timeout_raises(self):
        async def slow_embed(text):
            await asyncio.sleep(60)
            return [[0.1] * 768]

        embedding_engine = MagicMock()
        embedding_engine.embed_text = slow_embed
        embedding_engine.get_vector_size = MagicMock(return_value=768)

        vector_engine = MagicMock()
        vector_engine.embedding_engine = embedding_engine

        with patch(
            "cognee.infrastructure.databases.vector.get_vector_engine",
            return_value=vector_engine,
        ), patch(
            "cognee.infrastructure.llm.utils.CONNECTION_TEST_TIMEOUT_SECONDS", 0.01
        ):
            from cognee.infrastructure.llm.utils import test_embedding_connection

            with pytest.raises(TimeoutError):
                await test_embedding_connection()

    @pytest.mark.asyncio
    async def test_connection_error_propagates(self):
        embedding_engine = MagicMock()
        embedding_engine.embed_text = AsyncMock(
            side_effect=ConnectionError("refused")
        )
        embedding_engine.get_vector_size = MagicMock(return_value=768)

        vector_engine = MagicMock()
        vector_engine.embedding_engine = embedding_engine

        with patch(
            "cognee.infrastructure.databases.vector.get_vector_engine",
            return_value=vector_engine,
        ):
            from cognee.infrastructure.llm.utils import test_embedding_connection

            with pytest.raises(ConnectionError):
                await test_embedding_connection()

    @pytest.mark.asyncio
    async def test_runtime_error_not_swallowed(self):
        """RuntimeError from dim mismatch must not be caught by generic except Exception."""
        engine = self._make_mock_engine(
            embed_result=[[0.1] * 384], configured_dim=768
        )
        with patch(
            "cognee.infrastructure.databases.vector.get_vector_engine",
            return_value=engine,
        ):
            from cognee.infrastructure.llm.utils import test_embedding_connection

            with pytest.raises(RuntimeError):
                await test_embedding_connection()

    @pytest.mark.asyncio
    async def test_flat_result_handled(self):
        """embed_text returning flat [0.1, ...] instead of nested [[0.1, ...]]."""
        engine = self._make_mock_engine(
            embed_result=[0.1] * 768, configured_dim=768
        )
        with patch(
            "cognee.infrastructure.databases.vector.get_vector_engine",
            return_value=engine,
        ):
            from cognee.infrastructure.llm.utils import test_embedding_connection

            # Flat list: len(result) = 768, isinstance(result, list) = True
            # so actual_dim = len(result[0]) = len(0.1) → TypeError
            # OR if result is not nested: len(result) = 768
            # The code does: actual_dim = len(result[0]) if isinstance(result, list)
            # result[0] = 0.1 (float) → len(float) → TypeError
            # This documents the current behavior
            with pytest.raises((TypeError, RuntimeError)):
                await test_embedding_connection()
