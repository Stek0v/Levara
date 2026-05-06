"""Tests for Go chunker integration via gRPC."""

import sys
import uuid
from unittest.mock import AsyncMock, MagicMock

import pytest

# Import adapter and protobuf from conftest-registered modules
from cognee.infrastructure.databases.vector.levara.LevaraAdapter import LevaraAdapter

pb = sys.modules["cognee.infrastructure.databases.vector.levara.generated.levara_pb2"]


def _make_adapter():
    adapter = LevaraAdapter(url="localhost:50051", api_key=None, embedding_engine=MagicMock())
    adapter._stub = MagicMock()
    return adapter


class TestChunkTextGRPC:

    @pytest.mark.asyncio
    async def test_chunk_text_calls_grpc(self):
        """chunk_text() calls ChunkText RPC with correct params."""
        adapter = _make_adapter()
        adapter._stub.ChunkText = AsyncMock(return_value=pb.ChunkTextResp(chunks=[
            pb.TextChunk(id="uuid1", text="hello world", chapter=1, chunk_index=0, cut_type="paragraph"),
        ]))

        result = await adapter.chunk_text("hello world", max_chunk_size=600, document_id="doc-123")

        adapter._stub.ChunkText.assert_called_once()
        req = adapter._stub.ChunkText.call_args[0][0]
        assert req.document_id == "doc-123"
        assert req.max_chunk_chars == 2400  # 600 * 4.0 (default char_per_token)
        assert len(result) == 1
        assert result[0]["text"] == "hello world"

    @pytest.mark.asyncio
    async def test_cut_type_mapping(self):
        """Go cut_types are mapped to Cognee conventions."""
        adapter = _make_adapter()
        adapter._stub.ChunkText = AsyncMock(return_value=pb.ChunkTextResp(chunks=[
            pb.TextChunk(id="1", text="a", chunk_index=0, cut_type="paragraph"),
            pb.TextChunk(id="2", text="b", chunk_index=1, cut_type="sentence"),
            pb.TextChunk(id="3", text="c", chunk_index=2, cut_type=""),
        ]))

        result = await adapter.chunk_text("a b c", max_chunk_size=600)

        assert result[0]["cut_type"] == "paragraph_end"
        assert result[1]["cut_type"] == "sentence_cut"
        assert result[2]["cut_type"] == "default"

    @pytest.mark.asyncio
    async def test_char_per_token_estimation(self):
        """char_per_token controls max_chunk_chars sent to Go."""
        adapter = _make_adapter()
        adapter._stub.ChunkText = AsyncMock(return_value=pb.ChunkTextResp(chunks=[]))

        await adapter.chunk_text("text", max_chunk_size=100, char_per_token=5.0)

        req = adapter._stub.ChunkText.call_args[0][0]
        assert req.max_chunk_chars == 500  # 100 * 5.0

    @pytest.mark.asyncio
    async def test_document_id_passed_through(self):
        """document_id is forwarded to Go for UUID5 generation."""
        adapter = _make_adapter()
        adapter._stub.ChunkText = AsyncMock(return_value=pb.ChunkTextResp(chunks=[
            pb.TextChunk(id="test-uuid", text="chunk", chunk_index=0, cut_type="paragraph"),
        ]))

        result = await adapter.chunk_text("chunk", document_id="my-doc-uuid")

        req = adapter._stub.ChunkText.call_args[0][0]
        assert req.document_id == "my-doc-uuid"
        assert result[0]["id"] == "test-uuid"  # Go-generated UUID

    @pytest.mark.asyncio
    async def test_multiple_chunks_ordered(self):
        """Multiple chunks maintain correct order."""
        adapter = _make_adapter()
        adapter._stub.ChunkText = AsyncMock(return_value=pb.ChunkTextResp(chunks=[
            pb.TextChunk(id="a", text="first", chunk_index=0, cut_type="paragraph"),
            pb.TextChunk(id="b", text="second", chunk_index=1, cut_type="paragraph"),
            pb.TextChunk(id="c", text="third", chunk_index=2, cut_type="paragraph"),
        ]))

        result = await adapter.chunk_text("first second third", max_chunk_size=600)

        assert len(result) == 3
        assert [c["chunk_index"] for c in result] == [0, 1, 2]
        assert [c["text"] for c in result] == ["first", "second", "third"]

    @pytest.mark.asyncio
    async def test_empty_text_returns_empty(self):
        """Empty text returns no chunks."""
        adapter = _make_adapter()
        adapter._stub.ChunkText = AsyncMock(return_value=pb.ChunkTextResp(chunks=[]))

        result = await adapter.chunk_text("", max_chunk_size=600)

        assert result == []

    @pytest.mark.asyncio
    async def test_chunk_result_includes_chapter_field(self):
        """Result dicts include the chapter field from the proto."""
        adapter = _make_adapter()
        adapter._stub.ChunkText = AsyncMock(return_value=pb.ChunkTextResp(chunks=[
            pb.TextChunk(id="x", text="intro", chapter=3, chunk_index=0, cut_type="paragraph"),
        ]))

        result = await adapter.chunk_text("intro", max_chunk_size=600)

        assert result[0]["chapter"] == 3

    @pytest.mark.asyncio
    async def test_chunk_result_includes_chunk_size_estimate(self):
        """Result dicts include chunk_size (word-count estimate)."""
        adapter = _make_adapter()
        adapter._stub.ChunkText = AsyncMock(return_value=pb.ChunkTextResp(chunks=[
            pb.TextChunk(id="y", text="one two three", chunk_index=0, cut_type="sentence"),
        ]))

        result = await adapter.chunk_text("one two three", max_chunk_size=600)

        # chunk_size is len(text.split()) word-count estimate
        assert result[0]["chunk_size"] == 3

    @pytest.mark.asyncio
    async def test_default_strategy_is_merged(self):
        """chunk_text sends strategy='merged' by default."""
        adapter = _make_adapter()
        adapter._stub.ChunkText = AsyncMock(return_value=pb.ChunkTextResp(chunks=[]))

        await adapter.chunk_text("some text")

        req = adapter._stub.ChunkText.call_args[0][0]
        assert req.strategy == "merged"

    @pytest.mark.asyncio
    async def test_custom_strategy_forwarded(self):
        """A non-default strategy is forwarded verbatim to Go."""
        adapter = _make_adapter()
        adapter._stub.ChunkText = AsyncMock(return_value=pb.ChunkTextResp(chunks=[]))

        await adapter.chunk_text("some text", strategy="paragraph")

        req = adapter._stub.ChunkText.call_args[0][0]
        assert req.strategy == "paragraph"

    @pytest.mark.asyncio
    async def test_unknown_cut_type_maps_to_default(self):
        """An unrecognised cut_type string maps to 'default'."""
        adapter = _make_adapter()
        adapter._stub.ChunkText = AsyncMock(return_value=pb.ChunkTextResp(chunks=[
            pb.TextChunk(id="z", text="x", chunk_index=0, cut_type="chapter_break"),
        ]))

        result = await adapter.chunk_text("x", max_chunk_size=600)

        assert result[0]["cut_type"] == "default"


class TestUUID5Determinism:

    def test_python_uuid5_matches_go(self):
        """Python uuid5 produces same value as Go uuid.NewSHA1."""
        # This value was verified in Go test output
        expected = "ff501e71-5b83-59e4-b2b7-77c20fcc0ab3"
        got = str(uuid.uuid5(uuid.NAMESPACE_OID, "test-doc-0"))
        assert got == expected, f"Python uuid5 mismatch: {got} != {expected}"

    def test_uuid5_deterministic(self):
        """Same input always produces same UUID5."""
        id1 = str(uuid.uuid5(uuid.NAMESPACE_OID, "doc-42-7"))
        id2 = str(uuid.uuid5(uuid.NAMESPACE_OID, "doc-42-7"))
        assert id1 == id2

    def test_uuid5_unique_across_documents(self):
        """Different document_id produces different UUIDs."""
        id1 = str(uuid.uuid5(uuid.NAMESPACE_OID, "doc-A-0"))
        id2 = str(uuid.uuid5(uuid.NAMESPACE_OID, "doc-B-0"))
        assert id1 != id2

    def test_uuid5_unique_across_chunks(self):
        """Different chunk_index produces different UUIDs."""
        id1 = str(uuid.uuid5(uuid.NAMESPACE_OID, "doc-A-0"))
        id2 = str(uuid.uuid5(uuid.NAMESPACE_OID, "doc-A-1"))
        assert id1 != id2

    def test_uuid5_namespace_oid_is_correct(self):
        """NAMESPACE_OID is the correct namespace matching Go's uuid.NameSpaceOID."""
        # Go uses uuid.NameSpaceOID which corresponds to Python's uuid.NAMESPACE_OID
        # Verify the namespace UUID value is as expected
        assert str(uuid.NAMESPACE_OID) == "6ba7b812-9dad-11d1-80b4-00c04fd430c8"

    def test_uuid5_key_format_doc_chunk(self):
        """The key format 'docID-chunkIndex' is stable across calls."""
        doc_id = "my-document-uuid"
        chunk_index = 5
        key = f"{doc_id}-{chunk_index}"

        id1 = str(uuid.uuid5(uuid.NAMESPACE_OID, key))
        id2 = str(uuid.uuid5(uuid.NAMESPACE_OID, key))
        assert id1 == id2

    def test_uuid5_output_is_valid_uuid(self):
        """UUID5 output can be parsed as a valid UUID."""
        result = str(uuid.uuid5(uuid.NAMESPACE_OID, "any-input-string"))
        # Should not raise
        parsed = uuid.UUID(result)
        # UUID5 has version 5
        assert parsed.version == 5

    def test_uuid5_version_is_5(self):
        """Generated UUID has version 5 (SHA-1 based)."""
        result = uuid.uuid5(uuid.NAMESPACE_OID, "test-doc-0")
        assert result.version == 5
