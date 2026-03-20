"""Stress tests — concurrency, volume, and performance under load."""
import asyncio
import time
import pytest
import aiohttp
from conftest_http import BASE_URL, sample_vector, unique_id

pytestmark = pytest.mark.asyncio
DIM = 1024


async def test_concurrent_health():
    """100 parallel GET /health — all 200, p99 < 100ms."""
    n = 100
    latencies = []

    async def hit(session):
        t0 = time.monotonic()
        async with session.get(f"{BASE_URL}/health") as r:
            assert r.status == 200
        latencies.append(time.monotonic() - t0)

    async with aiohttp.ClientSession() as s:
        await asyncio.gather(*[hit(s) for _ in range(n)])

    assert len(latencies) == n
    latencies.sort()
    p99 = latencies[int(n * 0.99)]
    assert p99 < 0.1, f"p99 = {p99*1000:.1f}ms > 100ms"


async def test_concurrent_inserts():
    """50 parallel POST /insert — all succeed."""
    n = 50
    ids = [unique_id("stress_ins") for _ in range(n)]
    results = []

    async def insert(session, vid, vec):
        async with session.post(f"{BASE_URL}/insert", json={
            "id": vid, "vector": vec, "data": "{}"
        }) as r:
            results.append(r.status)

    async with aiohttp.ClientSession() as s:
        await asyncio.gather(*[insert(s, ids[i], sample_vector(DIM)) for i in range(n)])
        assert results.count(200) == n
        await s.post(f"{BASE_URL}/delete", json={"ids": ids})


async def test_concurrent_batch():
    """20 parallel batches of 50 = 1000 total records."""
    batches = 20
    per_batch = 50
    all_ids = []
    results = []

    async def batch_insert(session, batch_ids, batch_vecs):
        records = [{"id": i, "vector": v, "data": "{}"} for i, v in zip(batch_ids, batch_vecs)]
        async with session.post(f"{BASE_URL}/batch_insert", json={"records": records}) as r:
            data = await r.json()
            results.append(data.get("inserted", 0))

    async with aiohttp.ClientSession() as s:
        tasks = []
        for _ in range(batches):
            batch_ids = [unique_id("stress_batch") for _ in range(per_batch)]
            all_ids.extend(batch_ids)
            batch_vecs = [sample_vector(DIM) for _ in range(per_batch)]
            tasks.append(batch_insert(s, batch_ids, batch_vecs))
        await asyncio.gather(*tasks)

        total = sum(results)
        assert total == batches * per_batch, f"Inserted {total}/{batches * per_batch}"
        await s.post(f"{BASE_URL}/delete", json={"ids": all_ids})


async def test_concurrent_searches():
    """Insert 100 vectors, 100 parallel searches — measure QPS."""
    n = 100
    ids = [unique_id("stress_search") for _ in range(n)]
    vecs = [sample_vector(DIM) for _ in range(n)]

    async with aiohttp.ClientSession() as s:
        records = [{"id": i, "vector": v, "data": "{}"} for i, v in zip(ids, vecs)]
        await s.post(f"{BASE_URL}/batch_insert", json={"records": records})

        latencies = []

        async def search(session, vec):
            t0 = time.monotonic()
            async with session.post(f"{BASE_URL}/search", json={"vector": vec, "k": 5}) as r:
                assert r.status == 200
            latencies.append(time.monotonic() - t0)

        t_start = time.monotonic()
        await asyncio.gather(*[search(s, vecs[i % n]) for i in range(n)])
        elapsed = time.monotonic() - t_start

        qps = n / elapsed
        latencies.sort()
        p50 = latencies[n // 2] * 1000
        p99 = latencies[int(n * 0.99)] * 1000

        print(f"\nSearch: QPS={qps:.0f}, p50={p50:.1f}ms, p99={p99:.1f}ms")
        assert qps > 50, f"QPS too low: {qps:.0f}"

        await s.post(f"{BASE_URL}/delete", json={"ids": ids})


async def test_mixed_read_write():
    """80% search + 20% insert parallel — no 500 errors."""
    n = 100
    errors = []
    base_ids = [unique_id("stress_mix") for _ in range(20)]
    vecs = [sample_vector(DIM) for _ in range(20)]

    async with aiohttp.ClientSession() as s:
        # Seed some data
        records = [{"id": i, "vector": v, "data": "{}"} for i, v in zip(base_ids, vecs)]
        await s.post(f"{BASE_URL}/batch_insert", json={"records": records})

        async def do_search(session):
            async with session.post(f"{BASE_URL}/search", json={"vector": sample_vector(DIM), "k": 3}) as r:
                if r.status >= 500:
                    errors.append(("search", r.status))

        async def do_insert(session):
            vid = unique_id("stress_mix_w")
            base_ids.append(vid)
            async with session.post(f"{BASE_URL}/insert", json={
                "id": vid, "vector": sample_vector(DIM), "data": "{}"
            }) as r:
                if r.status >= 500:
                    errors.append(("insert", r.status))

        tasks = []
        for i in range(n):
            if i % 5 == 0:
                tasks.append(do_insert(s))
            else:
                tasks.append(do_search(s))

        await asyncio.gather(*tasks)
        assert len(errors) == 0, f"Server errors: {errors}"
        await s.post(f"{BASE_URL}/delete", json={"ids": base_ids})


async def test_large_batch():
    """Single batch of 100 records at 1024 dim — completes < 30s."""
    n = 100
    ids = [unique_id("stress_lg") for _ in range(n)]
    records = [{"id": i, "vector": sample_vector(DIM), "data": "{}"} for i in ids]

    async with aiohttp.ClientSession() as s:
        t0 = time.monotonic()
        async with s.post(f"{BASE_URL}/batch_insert", json={"records": records}) as r:
            elapsed = time.monotonic() - t0
            assert r.status == 200
            data = await r.json()
            assert data["inserted"] == n
            print(f"\nLarge batch: {n} records in {elapsed:.1f}s ({n/elapsed:.0f} rec/s)")
            assert elapsed < 30, f"Too slow: {elapsed:.1f}s"
        await s.post(f"{BASE_URL}/delete", json={"ids": ids})


async def test_concurrent_auth():
    """50 parallel register+login — all succeed."""
    n = 50
    results = []

    async def auth(session, i):
        email = f"stress_auth_{i}_{unique_id()}@test.com"
        await session.post(f"{BASE_URL}/auth/register", json={"email": email, "password": "stresspass"})
        async with session.post(f"{BASE_URL}/auth/login", json={"email": email, "password": "stresspass"}) as r:
            results.append(r.status)

    async with aiohttp.ClientSession() as s:
        await asyncio.gather(*[auth(s, i) for i in range(n)])

    assert results.count(200) == n, f"Auth failures: {n - results.count(200)}/{n}"


async def test_concurrent_cognify():
    """10 parallel cognify triggers — all return unique run IDs."""
    n = 10
    run_ids = []

    async def trigger(session):
        async with session.post(f"{BASE_URL}/cognify", json={"texts": [f"stress test {unique_id()}"]}) as r:
            data = await r.json()
            run_ids.append(data.get("pipeline_run_id", ""))

    async with aiohttp.ClientSession() as s:
        await asyncio.gather(*[trigger(s) for _ in range(n)])

    assert len(run_ids) == n
    assert len(set(run_ids)) == n, "Run IDs not unique"


async def test_concurrent_settings():
    """30 parallel PUT /settings — no crashes."""
    n = 30
    results = []

    async def put_settings(session, i):
        async with session.put(f"{BASE_URL}/settings", json={
            "llm_model": f"stress_model_{i}"
        }) as r:
            results.append(r.status)

    async with aiohttp.ClientSession() as s:
        await asyncio.gather(*[put_settings(s, i) for i in range(n)])

    assert all(s == 200 for s in results), f"Failures: {[s for s in results if s != 200]}"


async def test_dimension_mismatch():
    """Wrong dimension under load — all return 400/500, no crash."""
    n = 20
    results = []

    async def bad_insert(session):
        async with session.post(f"{BASE_URL}/insert", json={
            "id": unique_id("dim_err"), "vector": [0.1, 0.2, 0.3], "data": "{}"
        }) as r:
            results.append(r.status)

    async with aiohttp.ClientSession() as s:
        await asyncio.gather(*[bad_insert(s) for _ in range(n)])

    # All should be errors (dimension mismatch), not crashes
    assert all(s in (400, 500) for s in results)
    # Server still healthy
    async with aiohttp.ClientSession() as s:
        async with s.get(f"{BASE_URL}/health") as r:
            assert r.status == 200
