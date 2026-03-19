"""Tests for parallel write coordinator."""

import asyncio
import time
from unittest.mock import AsyncMock

import pytest

# Import directly (no conftest needed — this is a standalone utility)
import sys
sys.path.insert(0, 'cognee/cognee/tasks/storage')
from parallel_write import parallel_write_data_points


class TestParallelWrite:

    @pytest.mark.asyncio
    async def test_all_phases_called(self):
        """All write functions are called."""
        add_graph_nodes = AsyncMock()
        add_graph_edges = AsyncMock()
        index_nodes = AsyncMock()
        index_edges = AsyncMock()
        upsert_nodes = AsyncMock()
        upsert_edges = AsyncMock()

        await parallel_write_data_points(
            nodes=["n1", "n2"],
            edges=["e1"],
            graph_engine=None,
            vector_engine=None,
            add_graph_nodes=add_graph_nodes,
            add_graph_edges=add_graph_edges,
            index_nodes=index_nodes,
            index_edges=index_edges,
            upsert_nodes=upsert_nodes,
            upsert_edges=upsert_edges,
        )

        add_graph_nodes.assert_called_once_with(["n1", "n2"])
        add_graph_edges.assert_called_once_with(["e1"])
        index_nodes.assert_called_once_with(["n1", "n2"])
        index_edges.assert_called_once_with(["e1"])
        upsert_nodes.assert_called_once_with(["n1", "n2"])
        upsert_edges.assert_called_once_with(["e1"])

    @pytest.mark.asyncio
    async def test_phase1_runs_in_parallel(self):
        """Phase 1 tasks run concurrently, not sequentially."""
        call_times = []

        async def slow_task(items):
            call_times.append(time.monotonic())
            await asyncio.sleep(0.05)  # 50ms each

        t0 = time.monotonic()
        await parallel_write_data_points(
            nodes=["n1"], edges=["e1"],
            graph_engine=None, vector_engine=None,
            add_graph_nodes=slow_task,
            add_graph_edges=AsyncMock(),
            index_nodes=slow_task,
            index_edges=AsyncMock(),
            upsert_nodes=slow_task,
            upsert_edges=AsyncMock(),
        )
        elapsed = time.monotonic() - t0

        # 3 tasks x 50ms sequential = 150ms. Parallel should be ~50-70ms.
        assert elapsed < 0.12, f"Phase 1 took {elapsed:.3f}s — not parallel!"

    @pytest.mark.asyncio
    async def test_phase2_waits_for_phase1(self):
        """Phase 2 starts only after phase 1 completes."""
        order = []

        async def phase1_task(items):
            await asyncio.sleep(0.02)
            order.append("phase1")

        async def phase2_task(items):
            order.append("phase2")

        await parallel_write_data_points(
            nodes=["n1"], edges=["e1"],
            graph_engine=None, vector_engine=None,
            add_graph_nodes=phase1_task,
            add_graph_edges=phase2_task,
            index_nodes=AsyncMock(),
            index_edges=AsyncMock(),
            upsert_nodes=AsyncMock(),
            upsert_edges=AsyncMock(),
        )

        # Phase 2 should come after at least one phase 1
        assert order.index("phase2") > order.index("phase1")

    @pytest.mark.asyncio
    async def test_triplets_processed_in_phase3(self):
        """Optional triplets are indexed in phase 3."""
        index_triplets = AsyncMock()

        await parallel_write_data_points(
            nodes=["n1"], edges=["e1"],
            triplets=["t1", "t2"],
            graph_engine=None, vector_engine=None,
            add_graph_nodes=AsyncMock(),
            add_graph_edges=AsyncMock(),
            index_nodes=AsyncMock(),
            index_edges=AsyncMock(),
            upsert_nodes=AsyncMock(),
            upsert_edges=AsyncMock(),
            index_triplets=index_triplets,
        )

        index_triplets.assert_called_once_with(["t1", "t2"])

    @pytest.mark.asyncio
    async def test_error_in_phase1_still_runs_phase2(self):
        """Errors in phase 1 don't block phase 2."""
        add_graph_edges = AsyncMock()

        async def failing_task(items):
            raise RuntimeError("graph down")

        errors = await parallel_write_data_points(
            nodes=["n1"], edges=["e1"],
            graph_engine=None, vector_engine=None,
            add_graph_nodes=failing_task,
            add_graph_edges=add_graph_edges,
            index_nodes=AsyncMock(),
            index_edges=AsyncMock(),
            upsert_nodes=AsyncMock(),
            upsert_edges=AsyncMock(),
        )

        assert len(errors) == 1
        add_graph_edges.assert_called_once()  # Phase 2 still ran

    @pytest.mark.asyncio
    async def test_custom_edges_added_in_phase2(self):
        """Custom edges are written alongside regular edges."""
        add_graph_edges = AsyncMock()

        await parallel_write_data_points(
            nodes=["n1"], edges=["e1"],
            custom_edges=["ce1", "ce2"],
            graph_engine=None, vector_engine=None,
            add_graph_nodes=AsyncMock(),
            add_graph_edges=add_graph_edges,
            index_nodes=AsyncMock(),
            index_edges=AsyncMock(),
            upsert_nodes=AsyncMock(),
            upsert_edges=AsyncMock(),
        )

        # Called twice: once for edges, once for custom_edges
        assert add_graph_edges.call_count == 2

    @pytest.mark.asyncio
    async def test_no_triplets_skips_phase3_index(self):
        """Without triplets, index_triplets is not called."""
        index_triplets = AsyncMock()

        await parallel_write_data_points(
            nodes=["n1"], edges=["e1"],
            graph_engine=None, vector_engine=None,
            add_graph_nodes=AsyncMock(),
            add_graph_edges=AsyncMock(),
            index_nodes=AsyncMock(),
            index_edges=AsyncMock(),
            upsert_nodes=AsyncMock(),
            upsert_edges=AsyncMock(),
            index_triplets=index_triplets,
        )

        index_triplets.assert_not_called()
