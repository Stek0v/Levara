"""E2E tests for ADD pipeline — all user paths from docs.cognee.ai.

Tests: IngestData, ExtractText (tabula: PDF/DOCX/PPTX/XLSX/HTML),
dedup, incremental, Cyrillic, mixed batch, markdown export.
Requires: Cognevra gRPC:50051, embed-server:9001 (for some tests).
"""
import grpc
import json
import os
import sys
import time
from pathlib import Path

import pytest

pb = sys.modules.get("cognee.infrastructure.databases.vector.cognevra.generated.cognevra_pb2")
pb_grpc = sys.modules.get("cognee.infrastructure.databases.vector.cognevra.generated.cognevra_pb2_grpc")
if pb is None or pb_grpc is None:
    pytest.skip("Proto stubs not loaded", allow_module_level=True)

GRPC = "localhost:50051"
TEST_DATA = Path(__file__).parent.parent / "cognee" / "cognee" / "tests" / "test_data"
BOOK = Path(__file__).parent.parent / "Edvards_Dzanet_Uragan_r4_P61XH.txt"


def _stub():
    ch = grpc.insecure_channel(GRPC)
    try:
        grpc.channel_ready_future(ch).result(timeout=3)
    except grpc.FutureTimeoutError:
        pytest.skip("Cognevra not running")
    return pb_grpc.CognevraServiceStub(ch), ch


# ── ADD: Text ──

class TestAddText:
    def test_01_add_text_string(self):
        stub, ch = _stub()
        resp = stub.IngestData(pb.IngestDataReq(
            items=[pb.IngestItem(text="Simple test text for ingestion", dataset_name="e2e_add")],
            storage_path="/tmp/e2e_add_test",
        ))
        assert len(resp.results) == 1
        r = resp.results[0]
        assert r.content_hash != ""
        assert r.file_size == 30
        assert r.mime_type == "text/plain"
        assert r.id != ""
        ch.close()

    def test_13_unicode_cyrillic(self):
        stub, ch = _stub()
        text = "Телепат Эмбер читает мысли других людей в городе-улье"
        resp = stub.IngestData(pb.IngestDataReq(
            items=[pb.IngestItem(text=text, dataset_name="e2e_cyrillic")],
            storage_path="/tmp/e2e_cyrillic",
        ))
        assert resp.results[0].file_size == len(text.encode("utf-8"))
        assert resp.results[0].content_hash != ""
        ch.close()

    def test_12_large_text_ingest(self):
        if not BOOK.exists():
            pytest.skip("Book file not found")
        stub, ch = _stub()
        book = BOOK.read_text(encoding="utf-8")
        resp = stub.IngestData(pb.IngestDataReq(
            items=[pb.IngestItem(text=book, dataset_name="e2e_book")],
            storage_path="/tmp/e2e_book",
        ))
        assert resp.results[0].file_size > 1_000_000  # >1MB
        print(f"\n  Book: {resp.results[0].file_size} bytes, hash={resp.results[0].content_hash[:16]}")
        ch.close()


# ── ADD: File Extraction ──

class TestAddFiles:
    def test_02_add_pdf_file(self):
        pdf_path = TEST_DATA / "artificial-intelligence.pdf"
        if not pdf_path.exists():
            pytest.skip("PDF test file not found")
        stub, ch = _stub()
        resp = stub.ExtractText(pb.ExtractTextReq(
            file_data=pdf_path.read_bytes(), filename="ai.pdf", include_markdown=True,
        ))
        assert resp.format == "pdf"
        assert resp.pages >= 1
        assert len(resp.text) > 100
        assert "artificial intelligence" in resp.text.lower() or "AI" in resp.text
        assert len(resp.markdown) > 0
        print(f"\n  PDF: {resp.pages} pages, {len(resp.text)} chars, {resp.extract_ms}ms")
        ch.close()

    def test_03_add_docx_file(self):
        docx_path = TEST_DATA / "example.docx"
        if not docx_path.exists():
            pytest.skip("DOCX test file not found")
        stub, ch = _stub()
        resp = stub.ExtractText(pb.ExtractTextReq(
            file_data=docx_path.read_bytes(), filename="example.docx",
        ))
        assert resp.format == "docx"
        assert len(resp.text) > 0
        print(f"\n  DOCX: {len(resp.text)} chars, {resp.extract_ms}ms")
        ch.close()

    def test_04_add_pptx_file(self):
        pptx_path = TEST_DATA / "example.pptx"
        if not pptx_path.exists():
            pytest.skip("PPTX test file not found")
        stub, ch = _stub()
        resp = stub.ExtractText(pb.ExtractTextReq(
            file_data=pptx_path.read_bytes(), filename="example.pptx",
        ))
        assert resp.format == "pptx"
        assert len(resp.text) > 0
        print(f"\n  PPTX: {len(resp.text)} chars, {resp.extract_ms}ms")
        ch.close()

    def test_05_add_xlsx_file(self):
        xlsx_path = TEST_DATA / "example.xlsx"
        if not xlsx_path.exists():
            pytest.skip("XLSX test file not found")
        stub, ch = _stub()
        resp = stub.ExtractText(pb.ExtractTextReq(
            file_data=xlsx_path.read_bytes(), filename="example.xlsx",
        ))
        assert resp.format == "xlsx"
        assert len(resp.text) > 0
        print(f"\n  XLSX: {len(resp.text)} chars, {resp.extract_ms}ms")
        ch.close()

    def test_06_add_html_content(self):
        stub, ch = _stub()
        html = b"<html><body><h1>Title</h1><p>Paragraph about AI</p><table><tr><td>A</td><td>B</td></tr></table></body></html>"
        resp = stub.ExtractText(pb.ExtractTextReq(file_data=html, filename="test.html"))
        assert resp.format == "html"
        assert "Title" in resp.text
        assert "Paragraph" in resp.text
        ch.close()

    def test_11_markdown_extraction(self):
        pdf_path = TEST_DATA / "artificial-intelligence.pdf"
        if not pdf_path.exists():
            pytest.skip("PDF not found")
        stub, ch = _stub()
        resp = stub.ExtractText(pb.ExtractTextReq(
            file_data=pdf_path.read_bytes(), filename="ai.pdf", include_markdown=True,
        ))
        assert len(resp.markdown) > 0
        # Markdown should have heading markers
        assert "#" in resp.markdown or "**" in resp.markdown or resp.markdown != resp.text
        print(f"\n  Markdown: {len(resp.markdown)} chars")
        ch.close()


# ── ADD: Dedup + Batch ──

class TestAddDedup:
    def test_07_add_mixed_batch(self):
        stub, ch = _stub()
        items = [
            pb.IngestItem(text="First document about quantum computing", dataset_name="e2e_batch"),
            pb.IngestItem(text="Second document about NLP", dataset_name="e2e_batch"),
            pb.IngestItem(text="Third document about HNSW search", dataset_name="e2e_batch"),
        ]
        resp = stub.IngestData(pb.IngestDataReq(items=items, storage_path="/tmp/e2e_batch"))
        assert len(resp.results) == 3
        assert all(r.content_hash != "" for r in resp.results)
        # All unique — no duplicates
        hashes = {r.content_hash for r in resp.results}
        assert len(hashes) == 3
        ch.close()

    def test_08_dedup_same_content(self):
        stub, ch = _stub()
        text = "Duplicate content for dedup test"
        items = [
            pb.IngestItem(text=text, dataset_name="e2e_dedup"),
            pb.IngestItem(text=text, dataset_name="e2e_dedup"),
        ]
        resp = stub.IngestData(pb.IngestDataReq(items=items, storage_path="/tmp/e2e_dedup"))
        assert len(resp.results) == 2
        dups = sum(1 for r in resp.results if r.already_exists)
        assert dups == 1, f"Expected 1 duplicate, got {dups}"
        ch.close()

    def test_09_dedup_different_content(self):
        stub, ch = _stub()
        items = [
            pb.IngestItem(text="Content A unique", dataset_name="e2e_nodup"),
            pb.IngestItem(text="Content B unique", dataset_name="e2e_nodup"),
        ]
        resp = stub.IngestData(pb.IngestDataReq(items=items, storage_path="/tmp/e2e_nodup"))
        dups = sum(1 for r in resp.results if r.already_exists)
        assert dups == 0

    def test_10_incremental_add(self):
        stub, ch = _stub()
        # First batch: 3 unique items
        batch1 = [pb.IngestItem(text=f"Incremental doc {i}", dataset_name="e2e_incr") for i in range(3)]
        r1 = stub.IngestData(pb.IngestDataReq(items=batch1, storage_path="/tmp/e2e_incr"))
        assert len(r1.results) == 3

        # Second batch: 2 new + 1 duplicate (same as doc 0)
        batch2 = [
            pb.IngestItem(text="Incremental doc 0", dataset_name="e2e_incr"),  # dup
            pb.IngestItem(text="Incremental doc 3", dataset_name="e2e_incr"),  # new
            pb.IngestItem(text="Incremental doc 4", dataset_name="e2e_incr"),  # new
        ]
        r2 = stub.IngestData(pb.IngestDataReq(items=batch2, storage_path="/tmp/e2e_incr"))
        dups2 = sum(1 for r in r2.results if r.already_exists)
        print(f"\n  Incremental: {len(r2.results)} results, {dups2} within-batch dups")
        assert len(r2.results) == 3  # all 3 items processed
        ch.close()

    def test_14_empty_input(self):
        stub, ch = _stub()
        # Empty batch — should return empty results (not crash)
        resp = stub.IngestData(pb.IngestDataReq(items=[], storage_path="/tmp/e2e_empty"))
        assert len(resp.results) == 0
        ch.close()
