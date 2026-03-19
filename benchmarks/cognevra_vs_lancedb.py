"""
Benchmark: Cognee+LanceDB vs Cognee+Cognevra

Metrics: insert latency (p50/p95/p99), search latency, throughput (ops/sec), recall@10.

Usage:
    # Against LanceDB (default Cognee)
    python benchmarks/cognevra_vs_lancedb.py --provider=lancedb

    # Against Cognevra (start server first: cd Cognevra && make run)
    python benchmarks/cognevra_vs_lancedb.py --provider=cognevra --cognevra-url=http://localhost:8080

    # Both in sequence and compare
    python benchmarks/cognevra_vs_lancedb.py --provider=both

Prerequisites:
    pip install cognee numpy tqdm

For Cognevra, start the server with matching vector dimension:
    cd Cognevra && ./cognevra -bootstrap=true -dim=384
"""

import argparse
import asyncio
import json
import os
import statistics
import tempfile
import time
import uuid
from pathlib import Path
from typing import List, Optional

import numpy as np

try:
    from tqdm import tqdm
    HAS_TQDM = True
except ImportError:
    HAS_TQDM = False
    def tqdm(iterable, **kwargs):  # noqa: N802
        return iterable


# ─── synthetic dataset ────────────────────────────────────────────────────────

def generate_synthetic_vectors(n: int, dim: int) -> List[List[float]]:
    """Generate n random unit vectors of dimension dim."""
    rng = np.random.default_rng(42)
    vecs = rng.standard_normal((n, dim)).astype(np.float32)
    norms = np.linalg.norm(vecs, axis=1, keepdims=True)
    return (vecs / norms).tolist()


def ground_truth_top_k(
    query_vectors: List[List[float]],
    corpus_vectors: List[List[float]],
    k: int,
) -> List[List[int]]:
    """Brute-force exact top-k for recall computation (cosine similarity)."""
    Q = np.array(query_vectors, dtype=np.float32)
    C = np.array(corpus_vectors, dtype=np.float32)
    scores = Q @ C.T  # (n_queries, n_corpus)
    return np.argsort(-scores, axis=1)[:, :k].tolist()


# ─── mock embedding engine ────────────────────────────────────────────────────

class MockEmbeddingEngine:
    """Returns pre-computed vectors without calling an LLM."""

    def __init__(self, vectors: List[List[float]], dim: int):
        self._vectors = vectors
        self._dim = dim
        self._idx = 0

    async def embed_text(self, texts: List[str]) -> List[List[float]]:
        batch = []
        for _ in texts:
            batch.append(self._vectors[self._idx % len(self._vectors)])
            self._idx += 1
        return batch

    def get_vector_size(self) -> int:
        return self._dim

    def get_batch_size(self) -> int:
        return 32


# ─── DataPoint stub ──────────────────────────────────────────────────────────

def _make_data_point(text: str, vec_id: Optional[str] = None):
    """Build a minimal Cognee DataPoint for benchmarking."""
    from cognee.infrastructure.engine import DataPoint

    class BenchPoint(DataPoint):
        text: str
        metadata: dict = {"index_fields": ["text"]}
        belongs_to_set: List[str] = []

    dp = BenchPoint(text=text)
    return dp


# ─── timing utilities ────────────────────────────────────────────────────────

def percentile(data: List[float], p: int) -> float:
    return statistics.quantiles(sorted(data), n=100)[p - 1]


def print_stats(label: str, latencies_ms: List[float], throughput: float):
    p50 = percentile(latencies_ms, 50)
    p95 = percentile(latencies_ms, 95)
    p99 = percentile(latencies_ms, 99)
    print(f"  {label}:")
    print(f"    p50={p50:.1f}ms  p95={p95:.1f}ms  p99={p99:.1f}ms  throughput={throughput:.1f} ops/s")


# ─── benchmark runner ────────────────────────────────────────────────────────

async def run_benchmark(
    provider: str,
    n_docs: int,
    n_queries: int,
    dim: int,
    cognevra_url: str,
    output_dir: Path,
) -> dict:
    print(f"\n{'='*60}")
    print(f"Provider: {provider.upper()}  |  docs={n_docs}  queries={n_queries}  dim={dim}")
    print(f"{'='*60}")

    corpus_vectors = generate_synthetic_vectors(n_docs, dim)
    query_vectors = generate_synthetic_vectors(n_queries, dim)
    gt_top10 = ground_truth_top_k(query_vectors, corpus_vectors, k=10)

    embedding_engine = MockEmbeddingEngine(corpus_vectors, dim)

    # ── build adapter ────────────────────────────────────────────────────────
    if provider == "lancedb":
        from cognee.infrastructure.databases.vector.lancedb.LanceDBAdapter import LanceDBAdapter

        tmpdir = tempfile.mkdtemp(prefix="bench_lancedb_")
        adapter = LanceDBAdapter(
            url=tmpdir,
            api_key=None,
            embedding_engine=embedding_engine,
        )
    elif provider == "cognevra":
        from cognee.infrastructure.databases.vector.cognevra.CognevraAdapter import CognevraAdapter

        adapter = CognevraAdapter(
            url=cognevra_url,
            api_key=None,
            embedding_engine=embedding_engine,
        )
    else:
        raise ValueError(f"Unknown provider: {provider}")

    # ── insert phase ─────────────────────────────────────────────────────────
    print(f"[1/3] Inserting {n_docs} vectors...")
    insert_latencies = []
    for i in tqdm(range(n_docs), desc="insert"):
        dp = _make_data_point(f"doc_{i}")
        t0 = time.perf_counter()
        try:
            await adapter.create_data_points("bench_collection", [dp])
        except Exception as exc:
            print(f"  Insert error at doc {i}: {exc}")
            break
        insert_latencies.append((time.perf_counter() - t0) * 1000)

    insert_throughput = len(insert_latencies) / (sum(insert_latencies) / 1000 + 1e-9)
    print_stats("Insert", insert_latencies, insert_throughput)

    # ── search phase ─────────────────────────────────────────────────────────
    print(f"\n[2/3] Searching {n_queries} queries (k=10)...")
    search_latencies = []
    result_ids: List[List[str]] = []

    # Re-create embedding engine for queries
    query_engine = MockEmbeddingEngine(query_vectors, dim)
    adapter.embedding_engine = query_engine

    for i in tqdm(range(n_queries), desc="search"):
        t0 = time.perf_counter()
        try:
            results = await adapter.search(
                collection_name="bench_collection",
                query_vector=query_vectors[i],
                limit=10,
            )
        except Exception as exc:
            print(f"  Search error at query {i}: {exc}")
            results = []
        search_latencies.append((time.perf_counter() - t0) * 1000)
        result_ids.append([str(r.id) for r in results])

    search_throughput = len(search_latencies) / (sum(search_latencies) / 1000 + 1e-9)
    print_stats("Search", search_latencies, search_throughput)

    # ── recall@10 ────────────────────────────────────────────────────────────
    print("\n[3/3] Computing recall@10...")
    # For recall we need to know which IDs correspond to which corpus indices.
    # Since we inserted them in order, this is deterministic if the adapter caches IDs.
    # We use the adapter's in-memory cache to map corpus index → id.
    id_to_idx = {}
    if hasattr(adapter, "_id_cache"):
        cached_ids = list(adapter._id_cache.keys())
        for idx, key in enumerate(cached_ids[:n_docs]):
            raw_id = key.split(":", 1)[-1] if ":" in key else key
            id_to_idx[raw_id] = idx

    recalls = []
    for q_idx, (pred_ids, true_idxs) in enumerate(zip(result_ids, gt_top10)):
        true_set = set(true_idxs)
        pred_set = {id_to_idx.get(pid) for pid in pred_ids if pid in id_to_idx}
        hits = len(true_set & pred_set)
        recalls.append(hits / min(10, len(true_set)))

    avg_recall = sum(recalls) / len(recalls) if recalls else 0.0
    print(f"  recall@10 = {avg_recall:.4f} ({avg_recall*100:.1f}%)")

    result = {
        "provider": provider,
        "n_docs": n_docs,
        "n_queries": n_queries,
        "dim": dim,
        "insert": {
            "p50_ms": percentile(insert_latencies, 50) if insert_latencies else 0,
            "p95_ms": percentile(insert_latencies, 95) if insert_latencies else 0,
            "p99_ms": percentile(insert_latencies, 99) if insert_latencies else 0,
            "throughput_ops_s": insert_throughput,
        },
        "search": {
            "p50_ms": percentile(search_latencies, 50) if search_latencies else 0,
            "p95_ms": percentile(search_latencies, 95) if search_latencies else 0,
            "p99_ms": percentile(search_latencies, 99) if search_latencies else 0,
            "throughput_ops_s": search_throughput,
        },
        "recall_at_10": avg_recall,
    }

    out_file = output_dir / f"result_{provider}.json"
    out_file.write_text(json.dumps(result, indent=2))
    print(f"\nResults saved to {out_file}")

    return result


def print_comparison(results: List[dict]):
    if len(results) < 2:
        return

    print(f"\n{'='*60}")
    print("COMPARISON SUMMARY")
    print(f"{'='*60}")
    headers = ["Metric", results[0]["provider"].upper(), results[1]["provider"].upper(), "Winner"]
    rows = [
        ("Insert p50 (ms)", results[0]["insert"]["p50_ms"], results[1]["insert"]["p50_ms"], "lower"),
        ("Insert p99 (ms)", results[0]["insert"]["p99_ms"], results[1]["insert"]["p99_ms"], "lower"),
        ("Insert throughput", results[0]["insert"]["throughput_ops_s"], results[1]["insert"]["throughput_ops_s"], "higher"),
        ("Search p50 (ms)", results[0]["search"]["p50_ms"], results[1]["search"]["p50_ms"], "lower"),
        ("Search p99 (ms)", results[0]["search"]["p99_ms"], results[1]["search"]["p99_ms"], "lower"),
        ("Search throughput", results[0]["search"]["throughput_ops_s"], results[1]["search"]["throughput_ops_s"], "higher"),
        ("Recall@10", results[0]["recall_at_10"], results[1]["recall_at_10"], "higher"),
    ]

    print(f"{'Metric':<25} {headers[1]:<15} {headers[2]:<15} {'Winner':<10}")
    print("-" * 65)
    for metric, v0, v1, better in rows:
        if better == "lower":
            winner = results[0]["provider"] if v0 < v1 else results[1]["provider"]
        else:
            winner = results[0]["provider"] if v0 > v1 else results[1]["provider"]
        print(f"{metric:<25} {v0:<15.2f} {v1:<15.2f} {winner}")

    recall_diff = abs(results[0]["recall_at_10"] - results[1]["recall_at_10"])
    dod_recall = recall_diff <= 0.05
    print(f"\nDoD check — recall@10 difference ≤ 5%: {'✓ PASS' if dod_recall else '✗ FAIL'} (diff={recall_diff:.4f})")


async def main():
    parser = argparse.ArgumentParser(description="Cognevra vs LanceDB benchmark")
    parser.add_argument("--provider", choices=["lancedb", "cognevra", "both"], default="both")
    parser.add_argument("--n-docs", type=int, default=10_000)
    parser.add_argument("--n-queries", type=int, default=1_000)
    parser.add_argument("--dim", type=int, default=384, help="Vector dimension (match embedding model)")
    parser.add_argument("--cognevra-url", default="http://localhost:8080")
    parser.add_argument("--output-dir", default="benchmarks/results")
    args = parser.parse_args()

    output_dir = Path(args.output_dir)
    output_dir.mkdir(parents=True, exist_ok=True)

    providers = ["lancedb", "cognevra"] if args.provider == "both" else [args.provider]
    results = []
    for provider in providers:
        result = await run_benchmark(
            provider=provider,
            n_docs=args.n_docs,
            n_queries=args.n_queries,
            dim=args.dim,
            cognevra_url=args.cognevra_url,
            output_dir=output_dir,
        )
        results.append(result)

    if len(results) == 2:
        print_comparison(results)


if __name__ == "__main__":
    asyncio.run(main())
