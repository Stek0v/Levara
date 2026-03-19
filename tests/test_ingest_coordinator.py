"""Tests for parallel ingestion coordinator."""
import asyncio
from unittest.mock import AsyncMock
import pytest

import sys
sys.path.insert(0, 'cognee/cognee/tasks/ingestion')
from parallel_ingest import parallel_ingest


class TestParallelIngest:

    @pytest.mark.asyncio
    async def test_full_pipeline(self):
        hash_files = AsyncMock(return_value=[
            {"file_path": "/a.txt", "sha256": "abc", "mime_type": "text/plain", "file_size": 100},
            {"file_path": "/b.txt", "sha256": "def", "mime_type": "text/plain", "file_size": 200},
        ])
        load_file = AsyncMock(side_effect=["Hello world", "Goodbye world"])
        chunk_text = AsyncMock(side_effect=[
            [{"id": "c1", "text": "Hello world", "chunk_index": 0}],
            [{"id": "c2", "text": "Goodbye world", "chunk_index": 0}],
        ])

        result = await parallel_ingest(
            file_paths=["/a.txt", "/b.txt"],
            hash_files=hash_files,
            load_file=load_file,
            chunk_text=chunk_text,
            insert_vectors=AsyncMock(return_value=0),
        )

        assert result["files_processed"] == 2
        assert result["chunks_created"] == 2
        assert result["files_errored"] == 0

    @pytest.mark.asyncio
    async def test_hash_error_skips_file(self):
        hash_files = AsyncMock(return_value=[
            {"file_path": "/a.txt", "sha256": "abc", "mime_type": "text/plain", "file_size": 100},
            {"file_path": "/bad.txt", "error": "permission denied"},
        ])
        load_file = AsyncMock(return_value="content")
        chunk_text = AsyncMock(return_value=[{"id": "c1", "text": "content", "chunk_index": 0}])

        result = await parallel_ingest(
            file_paths=["/a.txt", "/bad.txt"],
            hash_files=hash_files,
            load_file=load_file,
            chunk_text=chunk_text,
            insert_vectors=AsyncMock(return_value=0),
        )

        assert result["files_processed"] == 1
        assert result["files_errored"] == 1
        assert "permission denied" in result["errors"][0]

    @pytest.mark.asyncio
    async def test_load_error_skips_file(self):
        hash_files = AsyncMock(return_value=[
            {"file_path": "/a.txt", "sha256": "abc", "mime_type": "text/plain", "file_size": 100},
        ])
        load_file = AsyncMock(side_effect=RuntimeError("corrupt PDF"))
        chunk_text = AsyncMock(return_value=[])

        result = await parallel_ingest(
            file_paths=["/a.txt"],
            hash_files=hash_files,
            load_file=load_file,
            chunk_text=chunk_text,
            insert_vectors=AsyncMock(return_value=0),
        )

        assert result["files_processed"] == 0
        assert "corrupt PDF" in result["errors"][0]

    @pytest.mark.asyncio
    async def test_parallel_loading(self):
        """Files are loaded concurrently, not sequentially."""
        import time
        call_times = []

        async def slow_load(path):
            call_times.append(time.monotonic())
            await asyncio.sleep(0.05)
            return "content"

        hash_files = AsyncMock(return_value=[
            {"file_path": f"/{i}.txt", "sha256": f"h{i}", "mime_type": "text/plain", "file_size": 10}
            for i in range(5)
        ])
        chunk_text = AsyncMock(return_value=[{"id": "c", "text": "content", "chunk_index": 0}])

        t0 = time.monotonic()
        result = await parallel_ingest(
            file_paths=[f"/{i}.txt" for i in range(5)],
            hash_files=hash_files,
            load_file=slow_load,
            chunk_text=chunk_text,
            insert_vectors=AsyncMock(return_value=0),
        )
        elapsed = time.monotonic() - t0

        assert result["files_processed"] == 5
        # 5 files × 50ms sequential = 250ms. Parallel should be ~50-70ms.
        assert elapsed < 0.15, f"Loading took {elapsed:.3f}s — not parallel!"

    @pytest.mark.asyncio
    async def test_embed_and_insert(self):
        hash_files = AsyncMock(return_value=[
            {"file_path": "/a.txt", "sha256": "abc", "mime_type": "text/plain", "file_size": 100},
        ])
        load_file = AsyncMock(return_value="Hello")
        chunk_text = AsyncMock(return_value=[{"id": "c1", "text": "Hello", "chunk_index": 0}])
        embed_texts = AsyncMock(return_value=[[0.1] * 1024])
        insert_vectors = AsyncMock(return_value=1)

        result = await parallel_ingest(
            file_paths=["/a.txt"],
            hash_files=hash_files,
            load_file=load_file,
            chunk_text=chunk_text,
            insert_vectors=insert_vectors,
            embed_texts=embed_texts,
            collection_name="test_col",
        )

        assert result["vectors_inserted"] == 1
        insert_vectors.assert_called_once()

    @pytest.mark.asyncio
    async def test_no_embed_skips_insert(self):
        hash_files = AsyncMock(return_value=[
            {"file_path": "/a.txt", "sha256": "abc", "mime_type": "text/plain", "file_size": 100},
        ])
        load_file = AsyncMock(return_value="Hello")
        chunk_text = AsyncMock(return_value=[{"id": "c1", "text": "Hello", "chunk_index": 0}])
        insert_vectors = AsyncMock(return_value=0)

        result = await parallel_ingest(
            file_paths=["/a.txt"],
            hash_files=hash_files,
            load_file=load_file,
            chunk_text=chunk_text,
            insert_vectors=insert_vectors,
            # No embed_texts → skip phase 4
        )

        assert result["vectors_inserted"] == 0
        insert_vectors.assert_not_called()

    @pytest.mark.asyncio
    async def test_empty_file_list(self):
        hash_files = AsyncMock(return_value=[])

        result = await parallel_ingest(
            file_paths=[],
            hash_files=hash_files,
            load_file=AsyncMock(),
            chunk_text=AsyncMock(return_value=[]),
            insert_vectors=AsyncMock(return_value=0),
        )

        assert result["files_processed"] == 0
        assert result["chunks_created"] == 0
