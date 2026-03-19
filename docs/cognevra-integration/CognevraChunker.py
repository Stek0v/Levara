"""CognevraChunker — delegates text chunking to Go via gRPC.

7-27x faster than Python chunk_by_paragraph for large documents.
Requires Cognevra gRPC server running (default port 50051).
"""

from typing import AsyncGenerator
from uuid import NAMESPACE_OID, uuid5

from cognee.shared.logging_utils import get_logger
from cognee.modules.chunking.Chunker import Chunker
from cognee.modules.chunking.models.DocumentChunk import DocumentChunk

logger = get_logger()


class CognevraChunker(Chunker):
    """Chunker that uses Go gRPC ChunkText instead of Python chunk_by_paragraph.

    7-27x faster than Python chunker for large documents.
    Requires Cognevra gRPC server running on port 50051.

    Usage:
        await extract_chunks_from_documents(
            documents, max_chunk_size=600, chunker=CognevraChunker
        )
    """

    #: gRPC address for the Cognevra server. Override via subclass or monkey-patch.
    grpc_url: str = "localhost:50051"

    #: Chunking strategy forwarded to Go: "merged" | "paragraph" | "sentence"
    strategy: str = "merged"

    async def read(self) -> AsyncGenerator:
        """Chunk document text using Go chunker via gRPC.

        Yields DocumentChunk instances with deterministic UUID5 IDs.
        chunk_size is a word-count estimate; the caller (extract_chunks_from_documents)
        accumulates it as token_count — accurate enough for budget tracking without
        an extra tokenizer round-trip per chunk.
        """
        from cognee.infrastructure.databases.vector.cognevra.CognevraAdapter import (
            CognevraAdapter,
        )

        adapter = CognevraAdapter(
            url=self.grpc_url,
            api_key=None,
            embedding_engine=None,  # Not used for chunking
        )

        try:
            async for content_text in self.get_text():
                chunks = await adapter.chunk_text(
                    text=content_text,
                    max_chunk_size=self.max_chunk_size,
                    document_id=str(self.document.id),
                    strategy=self.strategy,
                )

                for chunk_data in chunks:
                    chunk_id = uuid5(
                        NAMESPACE_OID,
                        f"{str(self.document.id)}-{self.chunk_index}",
                    )

                    try:
                        yield DocumentChunk(
                            id=chunk_id,
                            text=chunk_data["text"],
                            chunk_size=chunk_data["chunk_size"],
                            is_part_of=self.document,
                            chunk_index=self.chunk_index,
                            cut_type=chunk_data["cut_type"],
                            contains=[],
                            metadata={"index_fields": ["text"]},
                        )
                    except Exception as exc:
                        logger.error(
                            "CognevraChunker: failed to yield chunk %d for document %s: %s",
                            self.chunk_index,
                            self.document.id,
                            exc,
                        )
                        raise

                    self.chunk_index += 1

        finally:
            await adapter.close()
