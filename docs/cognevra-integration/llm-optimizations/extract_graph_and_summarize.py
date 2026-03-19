import asyncio
from typing import Dict, List, Type, Optional
from uuid import uuid5

from pydantic import BaseModel

from cognee.infrastructure.engine import DataPoint
from cognee.modules.chunking.models.DocumentChunk import DocumentChunk
from cognee.modules.ontology.ontology_config import Config
from cognee.tasks.graph.extract_graph_from_data import extract_graph_from_data
from cognee.tasks.summarization.summarize_text import summarize_text
from cognee.tasks.summarization.models import TextSummary


async def extract_graph_and_summarize(
    data_chunks: List[DocumentChunk],
    context: Dict,
    graph_model: Type[BaseModel],
    config: Optional[Config] = None,
    custom_prompt: Optional[str] = None,
    summarization_model: Type[BaseModel] = None,
    use_combined: bool = True,
    **kwargs,
) -> List[DataPoint]:
    """Run graph extraction and text summarization, optionally in a single LLM call.

    When use_combined=True (default), each chunk is processed with one LLM call
    that returns both graph and summary fields, halving the number of LLM calls
    compared to the legacy two-call approach.

    When use_combined=False, the original parallel approach is used: one call for
    graph extraction and one for summarization, running concurrently via asyncio.gather.

    Args:
        data_chunks: Document chunks to process.
        context: Pipeline context dict (user, dataset, data, pipeline_name, etc.).
        graph_model: Pydantic model for graph extraction structured output.
        config: Optional ontology configuration.
        custom_prompt: Optional custom system prompt for graph extraction.
        summarization_model: Pydantic model for summarization structured output.
        use_combined: If True, use a single LLM call per chunk for both graph and
            summary (default True).  Set to False to fall back to two concurrent calls.
        **kwargs: Forwarded to extract_content_graph / extract_graph_with_summary.
    """

    if use_combined:
        return await _extract_combined(
            data_chunks=data_chunks,
            context=context,
            graph_model=graph_model,
            config=config,
            custom_prompt=custom_prompt,
            summarization_model=summarization_model,
            **kwargs,
        )
    else:
        # Legacy: 2 separate concurrent calls
        graph_result, summary_result = await asyncio.gather(
            extract_graph_from_data(
                data_chunks, context, graph_model, config, custom_prompt, **kwargs
            ),
            summarize_text(data_chunks, summarization_model),
        )
        return list(graph_result) + list(summary_result)


async def _extract_combined(
    data_chunks: List[DocumentChunk],
    context: Dict,
    graph_model: Type[BaseModel],
    config: Optional[Config],
    custom_prompt: Optional[str],
    summarization_model: Type[BaseModel],
    **kwargs,
) -> List[DataPoint]:
    """Single-call path: one LLM call per chunk returns graph + summary together.

    Strategy:
    1. Call extract_graph_with_summary for each chunk (concurrently) to get a
       combined KnowledgeGraphWithSummary result.
    2. Feed the graph portion into the existing integrate_chunk_graphs pipeline
       (ontology resolution, DB writes, edge indexing).
    3. Build TextSummary objects from the summary portion, matching what
       summarize_text() would have returned.
    4. Return the combined list of DataPoints.
    """

    from cognee.infrastructure.llm.extraction.knowledge_graph.extract_graph_with_summary import (
        KnowledgeGraphWithSummary,
        extract_graph_with_summary,
    )
    from cognee.infrastructure.llm.concurrency import gather_with_concurrency
    from cognee.tasks.graph.extract_graph_from_data import integrate_chunk_graphs
    from cognee.modules.ontology.ontology_env_config import get_ontology_env_config
    from cognee.modules.ontology.get_default_ontology_resolver import (
        get_default_ontology_resolver,
        get_ontology_resolver_from_env,
    )
    from cognee.shared.data_models import KnowledgeGraph
    from cognee.modules.cognify.config import get_cognify_config

    # ------------------------------------------------------------------
    # Determine the response model for the combined call.
    # If the caller passed a custom graph_model that already contains
    # summary/description fields we use it; otherwise we use
    # KnowledgeGraphWithSummary which always has those fields.
    # ------------------------------------------------------------------
    combined_model = KnowledgeGraphWithSummary

    # ------------------------------------------------------------------
    # Fire one LLM call per chunk, all concurrently.
    # ------------------------------------------------------------------
    combined_results = await gather_with_concurrency(
        *[
            extract_graph_with_summary(
                chunk.text,
                response_model=combined_model,
                custom_prompt=custom_prompt,
                **kwargs,
            )
            for chunk in data_chunks
        ]
    )

    # ------------------------------------------------------------------
    # Adapt combined results into plain KnowledgeGraph objects so the
    # existing integrate_chunk_graphs() function works unchanged.
    # ------------------------------------------------------------------
    if graph_model is KnowledgeGraph:
        chunk_graphs = []
        for combined in combined_results:
            kg = KnowledgeGraph(
                nodes=combined.nodes,
                edges=combined.edges,
            )
            # Filter edges whose source or target nodes are missing (same
            # as the guard in extract_graph_from_data).
            valid_node_ids = {node.id for node in kg.nodes}
            kg.edges = [
                edge
                for edge in kg.edges
                if edge.source_node_id in valid_node_ids
                and edge.target_node_id in valid_node_ids
            ]
            chunk_graphs.append(kg)
    else:
        # Custom graph model — pass the combined result directly; the
        # caller is responsible for ensuring compatibility.
        chunk_graphs = list(combined_results)

    # ------------------------------------------------------------------
    # Resolve ontology config (mirrors extract_graph_from_data logic).
    # ------------------------------------------------------------------
    if config is None:
        ontology_config = get_ontology_env_config()
        if (
            ontology_config.ontology_file_path
            and ontology_config.ontology_resolver
            and ontology_config.matching_strategy
        ):
            resolved_config: Config = {
                "ontology_config": {
                    "ontology_resolver": get_ontology_resolver_from_env(
                        **ontology_config.to_dict()
                    )
                }
            }
        else:
            resolved_config: Config = {
                "ontology_config": {"ontology_resolver": get_default_ontology_resolver()}
            }
    else:
        resolved_config = config

    ontology_resolver = resolved_config["ontology_config"]["ontology_resolver"]
    pipeline_name = context.get("pipeline_name") if isinstance(context, dict) else None
    task_name = "extract_graph_and_summarize"

    updated_chunks = await integrate_chunk_graphs(
        data_chunks,
        chunk_graphs,
        graph_model,
        ontology_resolver,
        context,
        pipeline_name=pipeline_name,
        task_name=task_name,
    )

    # ------------------------------------------------------------------
    # Build TextSummary objects from the summary portion of combined results.
    # ------------------------------------------------------------------
    summaries = [
        TextSummary(
            id=uuid5(chunk.id, "TextSummary"),
            made_from=chunk,
            text=combined_results[chunk_index].summary,
        )
        for chunk_index, chunk in enumerate(data_chunks)
    ]

    return list(updated_chunks) + summaries
