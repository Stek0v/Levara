"""Parallel data ingestion coordinator using Levara Go RPCs + Python loaders.

Orchestrates the ingestion pipeline:
  Phase 1 (Go): ListDirectory → get file paths
  Phase 2 (Go): HashFiles → get SHA256 + MIME type + file size
  Phase 3 (Python parallel): Load files → text via asyncio.gather
  Phase 4 (Go): ChunkText → split into semantic chunks
  Phase 5 (Go): BatchInsert → store in vector DB

Each phase runs in parallel where possible.
"""

import asyncio
import logging
from typing import Any, Callable, Optional

logger = logging.getLogger(__name__)


async def parallel_ingest(
    *,
    file_paths: list[str],
    load_file: Callable,        # async fn(path) → str (text content)
    hash_files: Callable,       # async fn(paths) → list[dict] (Go HashFiles RPC)
    chunk_text: Callable,       # async fn(text, max_chunk_size, doc_id) → list[dict] (Go ChunkText RPC)
    insert_vectors: Callable,   # async fn(collection, chunks, vectors) → int (Go BatchInsert)
    embed_texts: Optional[Callable] = None,  # async fn(texts) → list[list[float]]
    collection_name: str = "default",
    max_chunk_size: int = 600,
    max_concurrent_loads: int = 10,
    char_per_token: float = 4.0,
) -> dict:
    """Run full ingestion pipeline with Go acceleration.

    Returns dict with: files_processed, chunks_created, vectors_inserted, errors
    """
    errors = []

    # Phase 1: Hash files in parallel (Go goroutines)
    logger.info(f"Phase 1: Hashing {len(file_paths)} files...")
    file_hashes = await hash_files(file_paths)

    # Filter out errored files
    valid_files = []
    for fh in file_hashes:
        if fh.get("error"):
            errors.append(f"hash error: {fh['file_path']}: {fh['error']}")
        else:
            valid_files.append(fh)

    # Phase 2: Load files in parallel (Python asyncio)
    logger.info(f"Phase 2: Loading {len(valid_files)} files...")
    sem = asyncio.Semaphore(max_concurrent_loads)

    async def _load_one(file_info):
        async with sem:
            try:
                text = await load_file(file_info["file_path"])
                return {"file_path": file_info["file_path"], "text": text,
                        "sha256": file_info["sha256"], "mime_type": file_info["mime_type"],
                        "file_size": file_info["file_size"]}
            except Exception as e:
                return {"file_path": file_info["file_path"], "error": str(e)}

    load_results = await asyncio.gather(*[_load_one(f) for f in valid_files])

    loaded_files = []
    for lr in load_results:
        if "error" in lr:
            errors.append(f"load error: {lr['file_path']}: {lr['error']}")
        else:
            loaded_files.append(lr)

    # Phase 3: Chunk all texts (Go ChunkText RPC)
    logger.info(f"Phase 3: Chunking {len(loaded_files)} files...")
    all_chunks = []
    for lf in loaded_files:
        chunks = await chunk_text(
            lf["text"],
            max_chunk_size=max_chunk_size,
            document_id=lf["sha256"],  # deterministic UUID from content hash
            char_per_token=char_per_token,
        )
        for c in chunks:
            c["file_path"] = lf["file_path"]
            c["sha256"] = lf["sha256"]
        all_chunks.extend(chunks)

    # Phase 4: Embed + Insert (if embed function provided)
    vectors_inserted = 0
    if embed_texts and all_chunks:
        logger.info(f"Phase 4: Embedding + inserting {len(all_chunks)} chunks...")
        texts = [c["text"] for c in all_chunks]
        vectors = await embed_texts(texts)
        vectors_inserted = await insert_vectors(collection_name, all_chunks, vectors)

    result = {
        "files_processed": len(loaded_files),
        "files_errored": len(errors),
        "chunks_created": len(all_chunks),
        "vectors_inserted": vectors_inserted,
        "errors": errors,
    }
    logger.info(f"Ingestion complete: {result['files_processed']} files, {result['chunks_created']} chunks")
    return result
