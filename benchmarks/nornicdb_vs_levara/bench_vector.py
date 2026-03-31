#!/usr/bin/env python3
"""
NornicDB vs Levara — Vector Operations Benchmark (Category 1)

Tests vector insert, search, concurrent QPS, and recall via HTTP APIs.
Both services must be running:
  - NornicDB: http://localhost:7474
  - Levara:   http://localhost:8080

Usage:
  pip install requests numpy
  python bench_vector.py
"""

import time
import json
import math
import random
import statistics
import sys
from concurrent.futures import ThreadPoolExecutor, as_completed

try:
    import requests
except ImportError:
    print("pip install requests")
    sys.exit(1)

try:
    import numpy as np
except ImportError:
    np = None
    print("Warning: numpy not available, using pure Python vectors")

# ── Config ──────────────────────────────────────────────────────────────────

NORNIC_HTTP = "http://localhost:7474"
LEVARA_HTTP = "http://localhost:8080"

DIMENSIONS = [384, 768]
DATASET_SIZES = [1000, 5000, 10000]
TOP_K_VALUES = [5, 10, 50]
CONCURRENCY_LEVELS = [1, 4, 8, 16]
RECALL_QUERIES = 200
RECALL_K = 10

# ── Vector Generation ──────────────────────────────────────────────────────

def random_vectors(n, dim):
    """Generate n random unit vectors."""
    if np:
        vecs = np.random.randn(n, dim).astype(np.float32)
        norms = np.linalg.norm(vecs, axis=1, keepdims=True)
        vecs = vecs / norms
        return [v.tolist() for v in vecs]
    else:
        vecs = []
        for _ in range(n):
            v = [random.gauss(0, 1) for _ in range(dim)]
            norm = math.sqrt(sum(x*x for x in v))
            vecs.append([x/norm for x in v])
        return vecs

def cosine_similarity(a, b):
    """Compute cosine similarity between two vectors."""
    dot = sum(x*y for x, y in zip(a, b))
    na = math.sqrt(sum(x*x for x in a))
    nb = math.sqrt(sum(x*x for x in b))
    if na == 0 or nb == 0:
        return 0
    return dot / (na * nb)

def brute_force_topk(query, vectors, ids, k):
    """Brute force exact top-K by cosine similarity."""
    scored = [(cosine_similarity(query, v), id_) for v, id_ in zip(vectors, ids)]
    scored.sort(key=lambda x: -x[0])
    return [id_ for _, id_ in scored[:k]]


# ── NornicDB Vector Client ─────────────────────────────────────────────────

class NornicVectorClient:
    def __init__(self, base_url=NORNIC_HTTP):
        self.base = base_url
        self.session = requests.Session()

    def close(self):
        self.session.close()

    def create_nodes_with_vectors(self, ids, vectors):
        """Insert vectors as node properties via Cypher."""
        start = time.perf_counter()
        # Use the tx/commit endpoint for Cypher
        for i in range(0, len(ids), 100):
            batch_ids = ids[i:i+100]
            batch_vecs = vectors[i:i+100]
            statements = []
            for id_, vec in zip(batch_ids, batch_vecs):
                statements.append({
                    "statement": "CREATE (n:Vector {id: $id, embedding: $vec})",
                    "parameters": {"id": id_, "vec": vec}
                })
            self.session.post(
                f"{self.base}/db/nornicdb/tx/commit",
                json={"statements": statements}
            )
        elapsed = time.perf_counter() - start
        return elapsed

    def vector_search(self, query_vec, top_k=10):
        """Search using NornicDB's vector search endpoint."""
        start = time.perf_counter()
        resp = self.session.post(
            f"{self.base}/nornicdb/search",
            json={
                "vector": query_vec,
                "top_k": top_k,
                "label": "Vector"
            }
        )
        elapsed = time.perf_counter() - start
        results = resp.json() if resp.status_code == 200 else []
        return elapsed, results

    def clear(self):
        self.session.post(
            f"{self.base}/db/nornicdb/tx/commit",
            json={"statements": [{"statement": "MATCH (n:Vector) DETACH DELETE n"}]}
        )


# ── Levara Vector Client ──────────────────────────────────────────────────

class LevaraVectorClient:
    def __init__(self, base_url=LEVARA_HTTP, collection="bench"):
        self.base = base_url
        self.collection = collection
        self.session = requests.Session()

    def close(self):
        self.session.close()

    def create_collection(self):
        """Create a benchmark collection."""
        self.session.post(
            f"{self.base}/api/v1/collections",
            json={"name": self.collection, "dimension": 0}  # auto-detect
        )

    def insert_vectors(self, ids, vectors):
        """Batch insert via Levara HTTP API."""
        start = time.perf_counter()
        for i in range(0, len(ids), 100):
            batch = [
                {"id": id_, "vector": vec, "metadata": {"name": id_}}
                for id_, vec in zip(ids[i:i+100], vectors[i:i+100])
            ]
            resp = self.session.post(
                f"{self.base}/api/v1/batch_insert",
                json={"records": batch, "collection": self.collection}
            )
        elapsed = time.perf_counter() - start
        return elapsed

    def search(self, query_vec, top_k=10):
        """Vector search via Levara HTTP API."""
        start = time.perf_counter()
        resp = self.session.post(
            f"{self.base}/api/v1/search",
            json={"vector": query_vec, "k": top_k, "collection": self.collection}
        )
        elapsed = time.perf_counter() - start
        results = resp.json().get("results", []) if resp.status_code == 200 else []
        return elapsed, results

    def clear(self):
        self.session.delete(f"{self.base}/api/v1/collections/{self.collection}")


# ── Benchmark Utilities ────────────────────────────────────────────────────

def percentile(data, p):
    if not data:
        return 0
    s = sorted(data)
    k = (len(s) - 1) * p / 100
    f = int(k)
    c = min(f + 1, len(s) - 1)
    return s[f] + (k - f) * (s[c] - s[f])

def bench_search_latency(search_fn, queries, top_k, iterations=100):
    """Benchmark search latency."""
    latencies = []
    for i in range(iterations):
        q = queries[i % len(queries)]
        elapsed, _ = search_fn(q, top_k)
        latencies.append(elapsed)
    return {
        "p50_ms": round(percentile(latencies, 50) * 1000, 3),
        "p95_ms": round(percentile(latencies, 95) * 1000, 3),
        "p99_ms": round(percentile(latencies, 99) * 1000, 3),
        "mean_ms": round(statistics.mean(latencies) * 1000, 3),
        "qps": round(len(latencies) / sum(latencies), 1) if sum(latencies) > 0 else 0,
    }

def bench_concurrent_qps(search_fn, queries, top_k, concurrency, duration_sec=5):
    """Benchmark concurrent QPS."""
    completed = [0]
    latencies = []
    stop_time = time.perf_counter() + duration_sec

    def worker():
        local_latencies = []
        while time.perf_counter() < stop_time:
            q = random.choice(queries)
            elapsed, _ = search_fn(q, top_k)
            local_latencies.append(elapsed)
            completed[0] += 1
        return local_latencies

    start = time.perf_counter()
    with ThreadPoolExecutor(max_workers=concurrency) as pool:
        futures = [pool.submit(worker) for _ in range(concurrency)]
        for f in as_completed(futures):
            latencies.extend(f.result())
    total_time = time.perf_counter() - start

    return {
        "concurrency": concurrency,
        "total_queries": len(latencies),
        "qps": round(len(latencies) / total_time, 1),
        "p50_ms": round(percentile(latencies, 50) * 1000, 3),
        "p99_ms": round(percentile(latencies, 99) * 1000, 3),
    }

def bench_recall(search_fn, vectors, ids, queries, k=RECALL_K):
    """Benchmark recall@K accuracy."""
    hits = 0
    total = 0
    for q in queries:
        gt_ids = set(brute_force_topk(q, vectors, ids, k))
        _, results = search_fn(q, k)
        # Extract IDs from results
        result_ids = set()
        if isinstance(results, list):
            for r in results:
                if isinstance(r, dict):
                    result_ids.add(r.get("id", r.get("ID", "")))
                elif isinstance(r, str):
                    result_ids.add(r)
        hits += len(gt_ids & result_ids)
        total += k
    return round(hits / total, 4) if total > 0 else 0


# ── Main ───────────────────────────────────────────────────────────────────

def main():
    print("=" * 80)
    print("NornicDB vs Levara — Vector Operations Benchmark")
    print("=" * 80)

    # Check connectivity
    nornic_ok = False
    levara_ok = False

    try:
        r = requests.get(f"{NORNIC_HTTP}/health", timeout=5)
        if r.status_code == 200:
            nornic_ok = True
            print(f"  NornicDB: connected at {NORNIC_HTTP}")
    except:
        pass

    try:
        r = requests.get(f"{LEVARA_HTTP}/health", timeout=5)
        if r.status_code == 200:
            levara_ok = True
            print(f"  Levara: connected at {LEVARA_HTTP}")
    except:
        pass

    if not nornic_ok:
        print(f"  NornicDB not available — skipping")
    if not levara_ok:
        print(f"  Levara not available — skipping")

    if not nornic_ok and not levara_ok:
        print("No services available. Exiting.")
        sys.exit(1)

    all_results = {
        "timestamp": time.strftime("%Y-%m-%dT%H:%M:%S"),
        "benchmarks": []
    }

    for dim in DIMENSIONS:
        for n in DATASET_SIZES:
            print(f"\n{'─'*80}")
            print(f"Dataset: {n} vectors, dim={dim}")
            print(f"{'─'*80}")

            # Generate data
            print(f"  Generating {n} vectors...")
            vectors = random_vectors(n, dim)
            ids = [f"v-{i}" for i in range(n)]
            queries = random_vectors(min(RECALL_QUERIES, 200), dim)

            bench_config = {"dim": dim, "n": n}
            bench_entry = {"config": bench_config, "nornicdb": {}, "levara": {}}

            # ── NornicDB ──
            if nornic_ok:
                nc = NornicVectorClient()

                # Insert
                print(f"  NornicDB: inserting {n} vectors...")
                nc.clear()
                t_insert = nc.create_nodes_with_vectors(ids, vectors)
                rps = n / t_insert if t_insert > 0 else 0
                bench_entry["nornicdb"]["insert"] = {
                    "time_sec": round(t_insert, 2),
                    "records_per_sec": round(rps, 1)
                }
                print(f"    Insert: {t_insert:.2f}s ({rps:.0f} rps)")

                # Search latency
                for k in TOP_K_VALUES:
                    print(f"  NornicDB: search top-{k}...")
                    stats = bench_search_latency(nc.vector_search, queries, k, iterations=100)
                    bench_entry["nornicdb"][f"search_top{k}"] = stats
                    print(f"    p50={stats['p50_ms']:.3f}ms, p99={stats['p99_ms']:.3f}ms, QPS={stats['qps']}")

                # Concurrent QPS
                for c in CONCURRENCY_LEVELS:
                    print(f"  NornicDB: concurrent QPS (workers={c})...")
                    stats = bench_concurrent_qps(nc.vector_search, queries, 10, c, duration_sec=3)
                    bench_entry["nornicdb"][f"concurrent_{c}"] = stats
                    print(f"    QPS={stats['qps']}, p50={stats['p50_ms']:.3f}ms, p99={stats['p99_ms']:.3f}ms")

                # Recall (only for small datasets — brute force is expensive)
                if n <= 5000:
                    print(f"  NornicDB: recall@{RECALL_K}...")
                    recall = bench_recall(nc.vector_search, vectors, ids, queries[:50], RECALL_K)
                    bench_entry["nornicdb"]["recall"] = recall
                    print(f"    Recall@{RECALL_K} = {recall:.4f}")

                nc.close()

            # ── Levara ──
            if levara_ok:
                lc = LevaraVectorClient(collection=f"bench_{dim}_{n}")

                # Insert
                print(f"  Levara: inserting {n} vectors...")
                lc.clear()
                lc.create_collection()
                t_insert = lc.insert_vectors(ids, vectors)
                rps = n / t_insert if t_insert > 0 else 0
                bench_entry["levara"]["insert"] = {
                    "time_sec": round(t_insert, 2),
                    "records_per_sec": round(rps, 1)
                }
                print(f"    Insert: {t_insert:.2f}s ({rps:.0f} rps)")

                # Search latency
                for k in TOP_K_VALUES:
                    print(f"  Levara: search top-{k}...")
                    stats = bench_search_latency(lc.search, queries, k, iterations=100)
                    bench_entry["levara"][f"search_top{k}"] = stats
                    print(f"    p50={stats['p50_ms']:.3f}ms, p99={stats['p99_ms']:.3f}ms, QPS={stats['qps']}")

                # Concurrent QPS
                for c in CONCURRENCY_LEVELS:
                    print(f"  Levara: concurrent QPS (workers={c})...")
                    stats = bench_concurrent_qps(lc.search, queries, 10, c, duration_sec=3)
                    bench_entry["levara"][f"concurrent_{c}"] = stats
                    print(f"    QPS={stats['qps']}, p50={stats['p50_ms']:.3f}ms, p99={stats['p99_ms']:.3f}ms")

                # Recall
                if n <= 5000:
                    print(f"  Levara: recall@{RECALL_K}...")
                    recall = bench_recall(lc.search, vectors, ids, queries[:50], RECALL_K)
                    bench_entry["levara"]["recall"] = recall
                    print(f"    Recall@{RECALL_K} = {recall:.4f}")

                lc.close()

            all_results["benchmarks"].append(bench_entry)

    # Save results
    outfile = "results_vector.json"
    with open(outfile, "w") as f:
        json.dump(all_results, f, indent=2)
    print(f"\nResults saved to {outfile}")


if __name__ == "__main__":
    main()
