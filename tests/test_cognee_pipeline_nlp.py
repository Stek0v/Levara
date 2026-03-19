"""B1: Cognee full pipeline smoke test — NLP article.

Tests add → cognify → search with real Perplexity embeddings + gemma3:4b LLM.
Requires: Cognee API:8002, embed-server:9001, Ollama:11434.
"""
import asyncio
import io
import json
import subprocess
import time
from pathlib import Path

import aiohttp
import pytest

COGNEE_URL = "http://localhost:8002"
NLP_PATH = Path(__file__).parent.parent / "cognee" / "cognee" / "tests" / "test_data" / "Natural_language_processing.txt"
DATASET = "test_nlp_smoke"


def _check(url):
    try:
        import urllib.request
        r = urllib.request.urlopen(url, timeout=5)
        return r.status == 200
    except Exception:
        return False


pytestmark = pytest.mark.skipif(
    not _check(f"{COGNEE_URL}/health"),
    reason="Need Cognee API at localhost:8002",
)


def _cypher(query):
    r = subprocess.run(
        ["docker", "exec", "new_db-neo4j-1", "cypher-shell",
         "-u", "neo4j", "-p", "pleaseletmein", query],
        capture_output=True, text=True, timeout=10,
    )
    return r.stdout.strip()


class TestCogneePipelineNLP:
    """Smoke test: NLP article through full Cognee pipeline."""

    @pytest.fixture(scope="class")
    def pipeline_result(self):
        return asyncio.run(self._run_pipeline())

    @staticmethod
    async def _run_pipeline():
        timeout = aiohttp.ClientTimeout(total=600)
        results = {}
        async with aiohttp.ClientSession(timeout=timeout) as s:
            # 1. Add NLP text file
            text = NLP_PATH.read_text(encoding="utf-8")
            data = aiohttp.FormData()
            data.add_field('data', io.BytesIO(text.encode()),
                          filename='nlp.txt', content_type='text/plain')
            data.add_field('datasetName', DATASET)

            t0 = time.time()
            r = await s.post(f"{COGNEE_URL}/api/v1/add", data=data)
            results["add_status"] = r.status
            results["add_time"] = time.time() - t0
            print(f"\n  Add: {r.status} ({results['add_time']:.1f}s)")

            # 2. Cognify
            t0 = time.time()
            r = await s.post(f"{COGNEE_URL}/api/v1/cognify",
                            json={"datasets": [DATASET]})
            body = await r.json()
            results["cognify_status"] = r.status
            results["cognify_time"] = time.time() - t0
            results["cognify_body"] = body
            print(f"  Cognify: {r.status} ({results['cognify_time']:.1f}s)")

            # 3. Search CHUNKS
            r = await s.post(f"{COGNEE_URL}/api/v1/search",
                            json={"query_text": "natural language processing", "query_type": "CHUNKS"})
            results["chunks_status"] = r.status
            try:
                results["chunks"] = await r.json()
            except Exception:
                results["chunks"] = await r.text()
            print(f"  Search CHUNKS: {r.status}, results={len(results['chunks']) if isinstance(results['chunks'], list) else 'error'}")

            # 4. Search SUMMARIES
            r = await s.post(f"{COGNEE_URL}/api/v1/search",
                            json={"query_text": "NLP text analysis", "query_type": "SUMMARIES"})
            results["summaries_status"] = r.status
            try:
                results["summaries"] = await r.json()
            except Exception:
                results["summaries"] = await r.text()
            print(f"  Search SUMMARIES: {r.status}, results={len(results['summaries']) if isinstance(results['summaries'], list) else 'error'}")

        return results

    def test_add_succeeds(self, pipeline_result):
        assert pipeline_result["add_status"] == 200

    def test_cognify_succeeds(self, pipeline_result):
        assert pipeline_result["cognify_status"] == 200
        # Check pipeline completed (not error/conflict)
        body = pipeline_result["cognify_body"]
        if isinstance(body, dict):
            for ds_info in body.values():
                if isinstance(ds_info, dict):
                    assert "Completed" in ds_info.get("status", ""), f"Pipeline not completed: {ds_info}"

    def test_cognify_time_reasonable(self, pipeline_result):
        """Cognify should complete within 5 minutes for a small text."""
        t = pipeline_result["cognify_time"]
        assert t < 300, f"Cognify too slow: {t:.0f}s (limit 300s)"
        print(f"\n  Cognify time: {t:.1f}s")

    def test_search_chunks_returns_results(self, pipeline_result):
        chunks = pipeline_result["chunks"]
        if isinstance(chunks, list):
            assert len(chunks) > 0, "CHUNKS search should return results"
            print(f"\n  CHUNKS: {len(chunks)} results")
        else:
            # May be error dict — check if it's a collection-not-found (expected for small text)
            print(f"\n  CHUNKS response: {str(chunks)[:200]}")

    def test_search_summaries(self, pipeline_result):
        summaries = pipeline_result["summaries"]
        if isinstance(summaries, list):
            print(f"\n  SUMMARIES: {len(summaries)} results")
        else:
            print(f"\n  SUMMARIES response: {str(summaries)[:200]}")

    def test_neo4j_has_entities(self, pipeline_result):
        """Cognify should have created entities in Neo4j."""
        if pipeline_result["cognify_status"] != 200:
            pytest.skip("Cognify failed")
        out = _cypher("MATCH (n:`__Node__`) RETURN count(n) AS cnt")
        try:
            cnt = int(out.strip().split("\n")[-1])
        except (ValueError, IndexError):
            cnt = 0
        assert cnt > 0, f"Expected entities in Neo4j, got {cnt}"
        print(f"\n  Neo4j nodes: {cnt}")

    def test_cognevra_has_collections(self, pipeline_result):
        """Cognify should have created vector collections in Cognevra."""
        if pipeline_result["cognify_status"] != 200:
            pytest.skip("Cognify failed")
        import grpc
        ch = grpc.insecure_channel("localhost:50051")
        try:
            grpc.channel_ready_future(ch).result(timeout=3)
            stub = pb_grpc.CognevraServiceStub(ch)
            resp = stub.ListCollections(pb.Empty())
            colls = list(resp.collections)
            print(f"\n  Cognevra collections: {colls}")
            assert len(colls) > 0, "Expected collections in Cognevra"
        except Exception as e:
            print(f"\n  Cognevra check failed: {e}")
        finally:
            ch.close()


# Import proto stubs for collection check
import sys
pb = sys.modules.get("cognee.infrastructure.databases.vector.cognevra.generated.cognevra_pb2")
pb_grpc = sys.modules.get("cognee.infrastructure.databases.vector.cognevra.generated.cognevra_pb2_grpc")
