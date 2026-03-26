"""
Levara MCP Stress Tests — 5 tests, ~1 hour.
Run: pytest tests/test_mcp_stress.py -m stress -v

Heavy load: concurrent sessions, sustained QPS, large payloads.
"""
import asyncio
import time
import uuid

import pytest
from conftest_mcp import MCPTestClient, percentile

pytestmark = [pytest.mark.stress, pytest.mark.asyncio]


class TestLatency:
    """S8.1 — Latency under sustained load."""

    async def test_search_latency_distribution(self, mcp, results):
        """S8.1 — 200 sequential searches, measure p50/p95/p99."""
        latencies = []
        for i in range(200):
            _, lat = await mcp.call_tool_timed("search", {
                "search_query": f"test query {i % 20}",
                "search_type": "CHUNKS", "top_k": 5
            })
            latencies.append(lat)

        p50 = percentile(latencies, 50)
        p95 = percentile(latencies, 95)
        p99 = percentile(latencies, 99)

        results.record("test_search_latency", "stress/latency", latency_ms=p50,
                       passed=p99 < 500,
                       meta={"p50": p50, "p95": p95, "p99": p99, "n": len(latencies)})

        assert p99 < 500, f"p99={p99:.1f}ms exceeds 500ms"

    async def test_memory_latency(self, mcp, results):
        """S8.1b — 100 save + recall cycles."""
        save_lats = []
        recall_lats = []
        coll = f"stress_mem_{uuid.uuid4().hex[:6]}"

        for i in range(100):
            _, lat = await mcp.call_tool_timed("save_memory", {
                "key": f"stress_key_{i}", "value": f"value_{i}",
                "type": "project", "collection": coll
            })
            save_lats.append(lat)

        for i in range(100):
            _, lat = await mcp.call_tool_timed("recall_memory", {
                "query": f"stress_key_{i % 50}", "collection": coll
            })
            recall_lats.append(lat)

        save_p50 = percentile(save_lats, 50)
        recall_p50 = percentile(recall_lats, 50)

        results.record("test_memory_save_latency", "stress/memory", latency_ms=save_p50,
                       passed=True, meta={"p95": percentile(save_lats, 95)})
        results.record("test_memory_recall_latency", "stress/memory", latency_ms=recall_p50,
                       passed=True, meta={"p95": percentile(recall_lats, 95)})


class TestConcurrency:
    """S8.2 — Concurrent MCP sessions."""

    async def test_concurrent_searches(self, mcp_url, results):
        """S8.2 — 10 concurrent search requests."""
        concurrency = 10
        iterations = 20

        async def worker(worker_id):
            client = MCPTestClient(mcp_url)
            await client.connect()
            lats = []
            for i in range(iterations):
                _, lat = await client.call_tool_timed("search", {
                    "search_query": f"concurrent test {worker_id}-{i}",
                    "search_type": "CHUNKS", "top_k": 5
                })
                lats.append(lat)
            await client.close()
            return lats

        t0 = time.perf_counter()
        tasks = [worker(i) for i in range(concurrency)]
        all_lats_nested = await asyncio.gather(*tasks, return_exceptions=True)
        wall_time = (time.perf_counter() - t0) * 1000

        # Flatten latencies, skip exceptions
        all_lats = []
        errors = 0
        for r in all_lats_nested:
            if isinstance(r, Exception):
                errors += 1
            else:
                all_lats.extend(r)

        total_queries = len(all_lats)
        qps = total_queries / (wall_time / 1000) if wall_time > 0 else 0
        p99 = percentile(all_lats, 99)

        results.record("test_concurrent_searches", "stress/concurrency",
                       latency_ms=percentile(all_lats, 50), passed=errors == 0,
                       meta={"concurrency": concurrency, "total": total_queries,
                             "qps": round(qps, 1), "p99": p99, "errors": errors,
                             "wall_ms": round(wall_time)})

        assert errors == 0, f"{errors} workers failed"
        assert qps > 10, f"QPS={qps:.1f} too low"

    async def test_concurrent_sessions(self, mcp_url, results):
        """S8.2b — 20 parallel sessions, each does init + ping + tools/list."""
        async def session_worker(i):
            client = MCPTestClient(mcp_url)
            t0 = time.perf_counter()
            await client.connect()
            tools = await client.tools_list()
            await client.ping()
            await client.close()
            return (time.perf_counter() - t0) * 1000, len(tools)

        tasks = [session_worker(i) for i in range(20)]
        results_list = await asyncio.gather(*tasks, return_exceptions=True)

        successes = [r for r in results_list if not isinstance(r, Exception)]
        failures = [r for r in results_list if isinstance(r, Exception)]

        lats = [r[0] for r in successes]
        tool_counts = [r[1] for r in successes]

        results.record("test_concurrent_sessions", "stress/sessions",
                       latency_ms=percentile(lats, 50) if lats else 0,
                       passed=len(failures) == 0,
                       meta={"sessions": 20, "successes": len(successes),
                             "failures": len(failures),
                             "p50_ms": percentile(lats, 50) if lats else 0,
                             "all_have_16_tools": all(c >= 16 for c in tool_counts)})

        assert len(failures) == 0, f"{len(failures)} sessions failed: {failures[:3]}"
        assert all(c >= 16 for c in tool_counts), "Some sessions didn't get all tools"


class TestLargePayload:
    """S8.4 — Large data operations."""

    async def test_large_cognify(self, mcp, results):
        """S2.7 — Cognify with 50KB text."""
        # Generate ~50KB text
        big_text = "This is a test paragraph about software architecture. " * 500
        coll = f"stress_big_{uuid.uuid4().hex[:6]}"

        t0 = time.perf_counter()
        result = await mcp.call_tool("cognify", {"data": big_text, "collection": coll})
        elapsed = (time.perf_counter() - t0) * 1000

        results.record("test_large_cognify", "stress/payload",
                       latency_ms=elapsed, passed=not mcp.tool_error(result),
                       meta={"size_bytes": len(big_text)})

        assert not mcp.tool_error(result)

    async def test_many_memories(self, mcp, results):
        """S8.3 — Save 500 memories, then list."""
        coll = f"stress_many_{uuid.uuid4().hex[:6]}"

        t0 = time.perf_counter()
        for i in range(500):
            await mcp.call_tool("save_memory", {
                "key": f"bulk_{i:04d}", "value": f"Memory value number {i}",
                "type": "project", "collection": coll
            })
        save_time = (time.perf_counter() - t0) * 1000

        # List (should return max 100)
        t0 = time.perf_counter()
        result = await mcp.call_tool("list_memories", {
            "type": "project", "collection": coll
        })
        list_time = (time.perf_counter() - t0) * 1000

        results.record("test_many_memories", "stress/bulk",
                       latency_ms=save_time, passed=not mcp.tool_error(result),
                       meta={"count": 500, "save_ms": round(save_time),
                             "list_ms": round(list_time),
                             "save_per_sec": round(500 / (save_time / 1000))})
