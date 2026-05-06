"""Stress tests: edge cases, error handling, boundary conditions.

Tests that the system handles bad input gracefully without crashes.
Requires: Levara gRPC:50051 only (no embed-server needed for most).
"""
import grpc
import json
import sys
import zipfile
import io

import pytest

pb = sys.modules.get("cognee.infrastructure.databases.vector.levara.generated.levara_pb2")
pb_grpc = sys.modules.get("cognee.infrastructure.databases.vector.levara.generated.levara_pb2_grpc")
if pb is None or pb_grpc is None:
    pytest.skip("Proto stubs not loaded", allow_module_level=True)

GRPC = "localhost:50051"


def _stub():
    ch = grpc.insecure_channel(GRPC)
    try:
        grpc.channel_ready_future(ch).result(timeout=3)
    except grpc.FutureTimeoutError:
        pytest.skip("Levara not running")
    return pb_grpc.LevaraServiceStub(ch), ch


class TestVectorEdgeCases:
    """Vector insert/search boundary conditions."""

    def test_01_empty_vector(self):
        stub, ch = _stub()
        # Insert with empty vector — should fail or be handled
        try:
            stub.Insert(pb.InsertReq(collection="edge_test", id="e1", vector=[], metadata_json="{}"))
        except grpc.RpcError:
            pass  # Expected error
        ch.close()

    def test_02_wrong_dimension(self):
        stub, ch = _stub()
        # Server is dim=1024, try inserting dim=3
        try:
            stub.Insert(pb.InsertReq(collection="edge_dim", id="e2", vector=[0.1, 0.2, 0.3], metadata_json="{}"))
        except grpc.RpcError:
            pass  # Expected dimension mismatch
        ch.close()

    def test_03_duplicate_ids(self):
        stub, ch = _stub()
        vec = [0.01] * 1024
        stub.Insert(pb.InsertReq(collection="edge_dup", id="same-id", vector=vec, metadata_json='{"v": 1}'))
        stub.Insert(pb.InsertReq(collection="edge_dup", id="same-id", vector=vec, metadata_json='{"v": 2}'))
        # Should not crash — either upsert or error
        ch.close()

    def test_04_unicode_metadata(self):
        stub, ch = _stub()
        vec = [0.01] * 1024
        meta = json.dumps({"name": "Эмбер", "desc": "テスト", "arabic": "اختبار"}, ensure_ascii=False)
        resp = stub.Insert(pb.InsertReq(collection="edge_unicode", id="u1", vector=vec, metadata_json=meta))
        assert resp.ok
        # Retrieve and verify
        get_resp = stub.GetByID(pb.GetByIDReq(collection="edge_unicode", ids=["u1"]))
        assert len(get_resp.records) == 1
        retrieved = get_resp.records[0].metadata_json
        assert "Эмбер" in retrieved
        ch.close()

    def test_05_huge_metadata(self):
        stub, ch = _stub()
        vec = [0.01] * 1024
        # 100KB metadata
        big_meta = json.dumps({"data": "x" * 100_000})
        try:
            stub.Insert(pb.InsertReq(collection="edge_bigmeta", id="big1", vector=vec, metadata_json=big_meta))
        except grpc.RpcError:
            pass  # May reject large payloads
        ch.close()

    def test_06_null_fields(self):
        stub, ch = _stub()
        # Empty collection name
        try:
            stub.Insert(pb.InsertReq(collection="", id="n1", vector=[0.01] * 1024, metadata_json="{}"))
        except grpc.RpcError:
            pass  # Expected
        ch.close()

    def test_07_special_chars_collection(self):
        stub, ch = _stub()
        vec = [0.01] * 1024
        # Collection with special characters
        for name in ["test-dash", "test_underscore", "test123"]:
            try:
                stub.Insert(pb.InsertReq(collection=name, id="s1", vector=vec, metadata_json="{}"))
            except grpc.RpcError:
                pass  # Some names may be rejected
        ch.close()


class TestSearchEdgeCases:
    """Search boundary conditions."""

    def test_12_search_empty_collection(self):
        stub, ch = _stub()
        vec = [0.01] * 1024
        try:
            resp = stub.Search(pb.SearchReq(collection="nonexistent_collection", vector=vec, top_k=5))
            # If succeeds, should return empty results
        except grpc.RpcError as e:
            # NOT_FOUND is acceptable for nonexistent collection
            assert "not found" in str(e.details()).lower() or e.code() == grpc.StatusCode.NOT_FOUND
        ch.close()

    def test_10_bm25_single_char_query(self):
        stub, ch = _stub()
        # Add data first
        stub.BM25Index(pb.BM25IndexReq(collection="edge_bm25", items=[
            pb.IndexItem(id="b1", text="quantum computing qubits"),
        ]))
        resp = stub.BM25Search(pb.BM25SearchReq(collection="edge_bm25", query="a", top_k=5))
        # Should not crash on single char
        ch.close()

    def test_08_concurrent_delete_search(self):
        """Delete while searching — should not crash."""
        import concurrent.futures
        stub, ch = _stub()
        vec = [0.02] * 1024
        # Insert some data
        for i in range(10):
            stub.Insert(pb.InsertReq(collection="edge_concurrent", id=f"c{i}", vector=vec, metadata_json="{}"))

        def search():
            stub.Search(pb.SearchReq(collection="edge_concurrent", vector=vec, top_k=5))

        def delete():
            stub.Delete(pb.DeleteReq(collection="edge_concurrent", ids=["c0", "c1"]))

        with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:
            futures = [pool.submit(search) for _ in range(3)] + [pool.submit(delete)]
            for f in concurrent.futures.as_completed(futures):
                try:
                    f.result()
                except grpc.RpcError:
                    pass  # OK — concurrent access may cause transient errors
        ch.close()


class TestTemporalEdgeCases:
    def test_11_temporal_no_dates(self):
        stub, ch = _stub()
        resp = stub.TemporalSearch(pb.TemporalSearchReq(text="No dates in this text at all"))
        assert resp.total_extracted == 0
        ch.close()

    def test_12_temporal_russian_dates(self):
        stub, ch = _stub()
        resp = stub.TemporalSearch(pb.TemporalSearchReq(text="5 января 2024 произошёл инцидент"))
        assert resp.total_extracted >= 1
        has_jan = any(e.date.startswith("2024-01") for e in resp.events)
        assert has_jan, f"Expected January 2024, got {[e.date for e in resp.events]}"
        ch.close()


class TestExtractEdgeCases:
    def test_13_extract_corrupt_pdf(self):
        stub, ch = _stub()
        try:
            resp = stub.ExtractText(pb.ExtractTextReq(file_data=b"not a real pdf", filename="bad.pdf"))
            # May succeed with empty text or error — should not crash
        except grpc.RpcError as e:
            assert "extract" in str(e.details()).lower() or "pdf" in str(e.details()).lower()
        ch.close()

    def test_14_extract_empty_docx(self):
        stub, ch = _stub()
        # Minimal valid DOCX (ZIP with empty document.xml)
        buf = io.BytesIO()
        with zipfile.ZipFile(buf, 'w') as zf:
            zf.writestr('[Content_Types].xml', '<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"></Types>')
            zf.writestr('word/document.xml', '<?xml version="1.0"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body></w:body></w:document>')
        try:
            resp = stub.ExtractText(pb.ExtractTextReq(file_data=buf.getvalue(), filename="empty.docx"))
            # Should return empty or minimal text, not crash
        except grpc.RpcError:
            pass
        ch.close()

    def test_15_extract_text_passthrough(self):
        stub, ch = _stub()
        text = "Plain text should pass through unchanged"
        resp = stub.ExtractText(pb.ExtractTextReq(file_data=text.encode(), filename="test.txt"))
        assert resp.text == text
        assert resp.format == "txt"
        ch.close()


class TestCacheEdgeCases:
    def test_llm_cache_nonexistent(self):
        stub, ch = _stub()
        resp = stub.LLMCacheGet(pb.LLMCacheGetReq(model="x", prompt="y", system_prompt="z", temperature=0))
        assert not resp.hit
        ch.close()

    def test_llm_cache_empty_fields(self):
        stub, ch = _stub()
        resp = stub.LLMCacheGet(pb.LLMCacheGetReq(model="", prompt="", system_prompt="", temperature=0))
        assert not resp.hit  # Should not crash on empty fields
        ch.close()
