"""
Health endpoint tests: check_embedding_service() from health.py.

Tests that embedding dimension mismatches result in DEGRADED status
(not UNHEALTHY — embedding is non-critical).
"""

from unittest.mock import AsyncMock, patch

import pytest

from cognee.api.v1.health.health import HealthChecker, HealthStatus


# check_embedding_service() does a lazy `from cognee.infrastructure.llm.utils import
# test_embedding_connection` inside the method body, so we must patch at the source
# module, not at the health module level.
_PATCH_TARGET = "cognee.infrastructure.llm.utils.test_embedding_connection"


class TestHealthEmbeddingService:
    @pytest.mark.asyncio
    async def test_embedding_service_healthy_on_match(self):
        checker = HealthChecker()
        with patch(_PATCH_TARGET, new_callable=AsyncMock) as mock_test:
            mock_test.return_value = None
            result = await checker.check_embedding_service()

        assert result.status == HealthStatus.HEALTHY
        assert "working" in result.details.lower() or "generation" in result.details.lower()

    @pytest.mark.asyncio
    async def test_embedding_service_degraded_on_mismatch(self):
        checker = HealthChecker()
        with patch(
            _PATCH_TARGET,
            new_callable=AsyncMock,
            side_effect=RuntimeError("EMBEDDING DIMENSION MISMATCH: dim=384, configured=768"),
        ):
            result = await checker.check_embedding_service()

        assert result.status == HealthStatus.DEGRADED
        assert "MISMATCH" in result.details or "failed" in result.details.lower()

    @pytest.mark.asyncio
    async def test_embedding_service_degraded_on_timeout(self):
        checker = HealthChecker()
        with patch(
            _PATCH_TARGET,
            new_callable=AsyncMock,
            side_effect=TimeoutError("timed out"),
        ):
            result = await checker.check_embedding_service()

        assert result.status == HealthStatus.DEGRADED

    @pytest.mark.asyncio
    async def test_embedding_is_non_critical(self):
        """When embedding fails, overall status should be DEGRADED, not UNHEALTHY."""
        checker = HealthChecker()

        from cognee.api.v1.health.health import ComponentHealth

        healthy = ComponentHealth(
            status=HealthStatus.HEALTHY,
            provider="test",
            response_time_ms=1,
            details="ok",
        )
        with patch.object(checker, "check_relational_db", return_value=healthy), \
             patch.object(checker, "check_vector_db", return_value=healthy), \
             patch.object(checker, "check_graph_db", return_value=healthy), \
             patch.object(checker, "check_file_storage", return_value=healthy), \
             patch.object(checker, "check_llm_provider", return_value=healthy), \
             patch(
                 _PATCH_TARGET,
                 new_callable=AsyncMock,
                 side_effect=RuntimeError("DIMENSION MISMATCH"),
             ):
            health = await checker.get_health_status(detailed=True)

        # Overall status should NOT be UNHEALTHY — embedding is non-critical
        assert health.status != HealthStatus.UNHEALTHY, (
            f"Expected DEGRADED or HEALTHY, got {health.status}"
        )
        assert health.components["embedding_service"].status == HealthStatus.DEGRADED
