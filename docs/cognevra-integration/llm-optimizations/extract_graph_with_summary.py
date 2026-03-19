"""Combined graph extraction + summarization in a single LLM call.

Saves 50% of LLM calls vs separate extract_content_graph + extract_summary.

The default response model is KnowledgeGraphWithSummary, which extends
KnowledgeGraph (nodes + edges) with summary fields matching SummarizedContent.
When a custom graph_model is passed it must already contain summary/description
fields, otherwise use KnowledgeGraphWithSummary directly.
"""

import os
from typing import List, Optional, Type

from pydantic import BaseModel, Field

from cognee.infrastructure.llm.LLMGateway import LLMGateway
from cognee.infrastructure.llm.prompts import render_prompt
from cognee.infrastructure.llm.config import get_llm_config


# ---------------------------------------------------------------------------
# Combined response model
# ---------------------------------------------------------------------------


class _Node(BaseModel):
    """Entity node in the knowledge graph."""

    id: str
    name: str
    type: str
    description: str


class _Edge(BaseModel):
    """Directed relationship between two nodes."""

    source_node_id: str
    target_node_id: str
    relationship_name: str


class KnowledgeGraphWithSummary(BaseModel):
    """Combined response: knowledge graph (nodes + edges) plus text summary.

    Field names are intentionally compatible with both KnowledgeGraph and
    SummarizedContent so that callers can treat this object as either.
    """

    # Graph fields — match KnowledgeGraph schema
    nodes: List[_Node] = Field(default_factory=list, description="Extracted entities/concepts")
    edges: List[_Edge] = Field(
        default_factory=list, description="Relationships between entities"
    )

    # Summary fields — match SummarizedContent schema
    summary: str = Field(default="", description="Concise summary of the text (2-3 sentences)")
    description: str = Field(
        default="", description="Additional context or elaboration on the summary"
    )


# ---------------------------------------------------------------------------
# Extraction function
# ---------------------------------------------------------------------------


async def extract_graph_with_summary(
    content: str,
    response_model: Optional[Type[BaseModel]] = None,
    custom_prompt: Optional[str] = None,
    **kwargs,
) -> KnowledgeGraphWithSummary:
    """Extract a knowledge graph AND a text summary in a single LLM call.

    Args:
        content: The raw text to process.
        response_model: Pydantic model to use for structured output.  Must
            contain both graph fields (nodes/edges) and summary fields.
            Defaults to KnowledgeGraphWithSummary.
        custom_prompt: Override the default system prompt entirely.
        **kwargs: Forwarded to LLMGateway.acreate_structured_output.

    Returns:
        An instance of response_model (default: KnowledgeGraphWithSummary)
        with both graph and summary populated.
    """

    if response_model is None:
        response_model = KnowledgeGraphWithSummary

    if custom_prompt:
        system_prompt = custom_prompt
    else:
        # Build combined prompt by appending summary instructions to the
        # existing graph prompt so graph-extraction quality is preserved.
        llm_config = get_llm_config()
        prompt_path = llm_config.graph_prompt_path

        if os.path.isabs(prompt_path):
            base_directory = os.path.dirname(prompt_path)
            prompt_path = os.path.basename(prompt_path)
        else:
            base_directory = None

        graph_prompt = render_prompt(prompt_path, {}, base_directory=base_directory)

        summary_instructions = (
            "\n\n# 5. Text Summary\n"
            "In addition to the knowledge graph, also provide:\n"
            "- summary: A concise 2-3 sentence summary of the text, using synonym words "
            "where possible to vary the wording while preserving meaning.\n"
            "- description: A brief elaboration (1-2 sentences) giving additional context.\n"
            "Be brief, concise, and keep the important information and the subject."
        )

        system_prompt = graph_prompt + summary_instructions

    result = await LLMGateway.acreate_structured_output(
        content, system_prompt, response_model, **kwargs
    )

    return result
