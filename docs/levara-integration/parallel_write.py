"""Parallel database write coordinator for Cognee pipeline.

Executes graph, vector, and relational writes in parallel phases,
respecting data dependencies (graph edges require nodes first).

Phases:
  Phase 1 (parallel): graph.add_nodes + vector.index(nodes) + relational.upsert_nodes
  Phase 2 (parallel): graph.add_edges + relational.upsert_edges
  Phase 3 (parallel): vector.index_edges + vector.index_triplets (if enabled)
"""

import asyncio
import logging
from typing import Any

logger = logging.getLogger(__name__)


async def parallel_write_data_points(
    *,
    nodes: list,
    edges: list,
    custom_edges: list | None = None,
    triplets: list | None = None,
    graph_engine: Any,
    vector_engine: Any,
    # Callables for each write operation
    add_graph_nodes,      # async fn(nodes) → None
    add_graph_edges,      # async fn(edges) → None
    index_nodes,          # async fn(nodes) → None
    index_edges,          # async fn(edges) → None
    upsert_nodes,         # async fn(nodes) → None
    upsert_edges,         # async fn(edges) → None
    index_triplets=None,  # async fn(triplets) → None (optional)
):
    """Execute multi-DB writes in parallel phases.

    Phase 1: Write nodes to all 3 DBs simultaneously
    Phase 2: Write edges (graph needs nodes first)
    Phase 3: Index edges + triplets in vector DB
    """
    errors = []

    # Phase 1: Nodes — all 3 DBs in parallel
    phase1_tasks = [
        add_graph_nodes(nodes),
        index_nodes(nodes),
        upsert_nodes(nodes),
    ]
    results = await asyncio.gather(*phase1_tasks, return_exceptions=True)
    for i, r in enumerate(results):
        if isinstance(r, Exception):
            logger.error(f"Phase 1 task {i} failed: {r}")
            errors.append(r)

    # Phase 2: Edges — graph + relational in parallel (graph depends on phase 1 nodes)
    phase2_tasks = [
        add_graph_edges(edges),
        upsert_edges(edges),
    ]
    if custom_edges:
        phase2_tasks.append(add_graph_edges(custom_edges))

    results = await asyncio.gather(*phase2_tasks, return_exceptions=True)
    for i, r in enumerate(results):
        if isinstance(r, Exception):
            logger.error(f"Phase 2 task {i} failed: {r}")
            errors.append(r)

    # Phase 3: Vector index edges + triplets in parallel
    phase3_tasks = [index_edges(edges)]
    if triplets and index_triplets:
        phase3_tasks.append(index_triplets(triplets))

    results = await asyncio.gather(*phase3_tasks, return_exceptions=True)
    for i, r in enumerate(results):
        if isinstance(r, Exception):
            logger.error(f"Phase 3 task {i} failed: {r}")
            errors.append(r)

    if errors:
        logger.warning(f"Parallel write completed with {len(errors)} errors")

    return errors
