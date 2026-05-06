"""Tests for Go file I/O acceleration RPCs."""

import sys
from unittest.mock import AsyncMock, MagicMock

import pytest

from cognee.infrastructure.databases.vector.levara.LevaraAdapter import LevaraAdapter

pb = sys.modules["cognee.infrastructure.databases.vector.levara.generated.levara_pb2"]


def _make_adapter():
    adapter = LevaraAdapter(url="localhost:50051", api_key=None, embedding_engine=MagicMock())
    adapter._stub = MagicMock()
    return adapter


class TestHashFiles:
    @pytest.mark.asyncio
    async def test_basic_hash(self):
        adapter = _make_adapter()
        adapter._stub.HashFiles = AsyncMock(return_value=pb.HashFilesResp(results=[
            pb.FileHash(file_path="/tmp/a.txt", sha256="abc123", file_size=100, mime_type="text/plain"),
        ]))

        result = await adapter.hash_files(["/tmp/a.txt"])
        assert len(result) == 1
        assert result[0]["sha256"] == "abc123"
        assert result[0]["file_size"] == 100
        assert result[0]["mime_type"] == "text/plain"

    @pytest.mark.asyncio
    async def test_max_concurrent_forwarded(self):
        adapter = _make_adapter()
        adapter._stub.HashFiles = AsyncMock(return_value=pb.HashFilesResp(results=[]))

        await adapter.hash_files(["/tmp/a.txt"], max_concurrent=16)
        req = adapter._stub.HashFiles.call_args[0][0]
        assert req.max_concurrent == 16

    @pytest.mark.asyncio
    async def test_error_file_included(self):
        adapter = _make_adapter()
        adapter._stub.HashFiles = AsyncMock(return_value=pb.HashFilesResp(results=[
            pb.FileHash(file_path="/missing.txt", error="open: no such file"),
        ]))

        result = await adapter.hash_files(["/missing.txt"])
        assert result[0]["error"] == "open: no such file"
        assert result[0]["sha256"] == ""

    @pytest.mark.asyncio
    async def test_multiple_files(self):
        adapter = _make_adapter()
        adapter._stub.HashFiles = AsyncMock(return_value=pb.HashFilesResp(results=[
            pb.FileHash(file_path="/a.txt", sha256="aaa", file_size=10, mime_type="text/plain"),
            pb.FileHash(file_path="/b.pdf", sha256="bbb", file_size=2000, mime_type="application/pdf"),
        ]))

        result = await adapter.hash_files(["/a.txt", "/b.pdf"])
        assert len(result) == 2
        assert result[0]["sha256"] != result[1]["sha256"]

    @pytest.mark.asyncio
    async def test_empty_list(self):
        adapter = _make_adapter()
        adapter._stub.HashFiles = AsyncMock(return_value=pb.HashFilesResp(results=[]))

        result = await adapter.hash_files([])
        assert result == []


class TestListDirectory:
    @pytest.mark.asyncio
    async def test_basic_listing(self):
        adapter = _make_adapter()
        adapter._stub.ListDirectory = AsyncMock(return_value=pb.ListDirectoryResp(
            file_paths=["/data/a.txt", "/data/b.txt"], total=2,
        ))

        result = await adapter.list_directory("/data")
        assert len(result) == 2
        assert "/data/a.txt" in result

    @pytest.mark.asyncio
    async def test_recursive_flag(self):
        adapter = _make_adapter()
        adapter._stub.ListDirectory = AsyncMock(return_value=pb.ListDirectoryResp(file_paths=[], total=0))

        await adapter.list_directory("/data", recursive=False)
        req = adapter._stub.ListDirectory.call_args[0][0]
        assert req.recursive is False

    @pytest.mark.asyncio
    async def test_extension_filter(self):
        adapter = _make_adapter()
        adapter._stub.ListDirectory = AsyncMock(return_value=pb.ListDirectoryResp(
            file_paths=["/data/a.txt", "/data/sub/b.txt"], total=2,
        ))

        await adapter.list_directory("/data", extensions=[".txt"])
        req = adapter._stub.ListDirectory.call_args[0][0]
        assert ".txt" in req.extensions

    @pytest.mark.asyncio
    async def test_empty_directory(self):
        adapter = _make_adapter()
        adapter._stub.ListDirectory = AsyncMock(return_value=pb.ListDirectoryResp(file_paths=[], total=0))

        result = await adapter.list_directory("/empty")
        assert result == []

    @pytest.mark.asyncio
    async def test_no_extensions_sends_empty_list(self):
        adapter = _make_adapter()
        adapter._stub.ListDirectory = AsyncMock(return_value=pb.ListDirectoryResp(file_paths=[], total=0))

        await adapter.list_directory("/data")
        req = adapter._stub.ListDirectory.call_args[0][0]
        assert len(req.extensions) == 0
