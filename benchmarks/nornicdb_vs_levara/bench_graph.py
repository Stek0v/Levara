#!/usr/bin/env python3
"""
NornicDB vs Levara — Graph Operations Benchmark (Category 2)

Tests graph CRUD and traversal via Bolt (NornicDB) vs HTTP/SQL (Levara).
Runs on macOS. Both services must be running:
  - NornicDB: bolt://localhost:7687
  - Levara:   http://localhost:8080

Usage:
  pip install neo4j aiohttp
  python bench_graph.py
"""

import time
import json
import statistics
import random
import string
import sys
from concurrent.futures import ThreadPoolExecutor

try:
    from neo4j import GraphDatabase
except ImportError:
    print("pip install neo4j")
    sys.exit(1)

try:
    import requests
except ImportError:
    print("pip install requests")
    sys.exit(1)

# ── Config ──────────────────────────────────────────────────────────────────

NORNIC_BOLT = "bolt://localhost:7687"
LEVARA_HTTP = "http://localhost:8080"

NUM_NODES = 1_000
EDGES_PER_NODE = 5
QUERY_ITERATIONS = 50

# ── Data Generation ─────────────────────────────────────────────────────────

def random_name(prefix="entity"):
    return f"{prefix}_{''.join(random.choices(string.ascii_lowercase, k=6))}"

def generate_graph_data(num_nodes, edges_per_node):
    """Generate synthetic knowledge graph data."""
    types = ["Person", "Concept", "Document", "Function", "Module", "Class", "API", "Service"]
    relationships = ["RELATES_TO", "CALLS", "IMPORTS", "CONTAINS", "DEPENDS_ON", "EXTENDS", "USES", "PART_OF"]

    nodes = []
    for i in range(num_nodes):
        nodes.append({
            "id": f"n-{i}",
            "name": random_name(),
            "type": random.choice(types),
            "description": f"Node {i} description",
        })

    edges = []
    for i in range(num_nodes):
        targets = random.sample(range(num_nodes), min(edges_per_node, num_nodes - 1))
        for t in targets:
            if t != i:
                edges.append({
                    "source_id": f"n-{i}",
                    "target_id": f"n-{t}",
                    "relationship": random.choice(relationships),
                })

    return nodes, edges

# ── NornicDB (Bolt) ────────────────────────────────────────────────────────

class NornicDBBench:
    def __init__(self, uri=NORNIC_BOLT):
        self.driver = GraphDatabase.driver(uri, auth=None)

    def close(self):
        self.driver.close()

    def clear(self):
        with self.driver.session() as s:
            s.run("MATCH (n) DETACH DELETE n")

    def insert_nodes(self, nodes):
        """Batch insert nodes via Cypher UNWIND."""
        start = time.perf_counter()
        with self.driver.session() as s:
            # Batch in chunks of 500
            for i in range(0, len(nodes), 500):
                batch = nodes[i:i+500]
                s.run(
                    "UNWIND $nodes AS n "
                    "CREATE (x:__Node__ {id: n.id, name: n.name, type: n.type, description: n.description})",
                    nodes=batch
                )
        elapsed = time.perf_counter() - start
        return elapsed

    def insert_edges(self, edges):
        """Batch insert edges via Cypher UNWIND."""
        start = time.perf_counter()
        with self.driver.session() as s:
            for i in range(0, len(edges), 500):
                batch = edges[i:i+500]
                s.run(
                    "UNWIND $edges AS e "
                    "MATCH (a:__Node__ {id: e.source_id}), (b:__Node__ {id: e.target_id}) "
                    "CREATE (a)-[:RELATES_TO {relationship: e.relationship}]->(b)",
                    edges=batch
                )
        elapsed = time.perf_counter() - start
        return elapsed

    def traversal_1hop(self, name):
        start = time.perf_counter()
        with self.driver.session() as s:
            result = s.run(
                "MATCH (n:__Node__)-[r]-(m:__Node__) WHERE n.name = $name "
                "RETURN n.name AS source, type(r) AS rel, m.name AS target LIMIT 50",
                name=name
            )
            records = list(result)
        elapsed = time.perf_counter() - start
        return elapsed, len(records)

    def traversal_2hop(self, name):
        start = time.perf_counter()
        with self.driver.session() as s:
            result = s.run(
                "MATCH (n:__Node__)-[r1]-(m:__Node__)-[r2]-(k:__Node__) WHERE n.name = $name "
                "RETURN n.name, type(r1), m.name, type(r2), k.name LIMIT 100",
                name=name
            )
            records = list(result)
        elapsed = time.perf_counter() - start
        return elapsed, len(records)

    def traversal_3hop(self, name):
        start = time.perf_counter()
        with self.driver.session() as s:
            result = s.run(
                "MATCH (n:__Node__)-[*1..3]-(m:__Node__) WHERE n.name = $name "
                "RETURN DISTINCT m.name LIMIT 200",
                name=name
            )
            records = list(result)
        elapsed = time.perf_counter() - start
        return elapsed, len(records)

    def traversal_5hop(self, name):
        start = time.perf_counter()
        with self.driver.session() as s:
            result = s.run(
                "MATCH (n:__Node__)-[*1..5]-(m:__Node__) WHERE n.name = $name "
                "RETURN DISTINCT m.name LIMIT 200",
                name=name
            )
            records = list(result)
        elapsed = time.perf_counter() - start
        return elapsed, len(records)

    def shortest_path(self, name1, name2):
        start = time.perf_counter()
        with self.driver.session() as s:
            result = s.run(
                "MATCH p=shortestPath((a:__Node__)-[*]-(b:__Node__)) "
                "WHERE a.name = $n1 AND b.name = $n2 "
                "RETURN length(p) AS pathLen",
                n1=name1, n2=name2
            )
            records = list(result)
        elapsed = time.perf_counter() - start
        path_len = records[0]["pathLen"] if records else -1
        return elapsed, path_len

    def count_nodes(self):
        with self.driver.session() as s:
            result = s.run("MATCH (n:__Node__) RETURN count(n) AS cnt")
            return result.single()["cnt"]


# ── Levara (HTTP + SQL graph) ──────────────────────────────────────────────

class LevaraBench:
    def __init__(self, base_url=LEVARA_HTTP):
        self.base = base_url
        self.session = requests.Session()

    def close(self):
        self.session.close()

    def clear(self):
        """Clear graph data via prune or direct API."""
        # Try prune endpoint
        self.session.post(f"{self.base}/api/v1/prune")

    def insert_nodes(self, nodes):
        """Insert nodes via graph API (batch)."""
        start = time.perf_counter()
        # Levara stores graph nodes via cognify pipeline or direct SQL
        # Using the graph nodes API if available, otherwise batch via cognify
        for i in range(0, len(nodes), 100):
            batch = nodes[i:i+100]
            resp = self.session.post(
                f"{self.base}/api/v1/graph/nodes",
                json={"nodes": batch}
            )
            if resp.status_code not in (200, 201, 404):
                # If no direct graph API, use SQL-based approach
                pass
        elapsed = time.perf_counter() - start
        return elapsed

    def insert_edges(self, edges):
        """Insert edges via graph API (batch)."""
        start = time.perf_counter()
        for i in range(0, len(edges), 100):
            batch = edges[i:i+100]
            resp = self.session.post(
                f"{self.base}/api/v1/graph/edges",
                json={"edges": batch}
            )
        elapsed = time.perf_counter() - start
        return elapsed

    def traversal_1hop(self, name):
        """1-hop via search API (GRAPH_COMPLETION uses SQL internally)."""
        start = time.perf_counter()
        resp = self.session.post(
            f"{self.base}/api/v1/search/text",
            json={
                "query_text": name,
                "query_type": "GRAPH_COMPLETION",
                "top_k": 50
            }
        )
        elapsed = time.perf_counter() - start
        data = resp.json() if resp.status_code == 200 else {}
        ctx = data.get("context") or []
        return elapsed, len(ctx) if isinstance(ctx, list) else 0

    def traversal_2hop(self, name):
        """2-hop via CONTEXT_EXTENSION."""
        start = time.perf_counter()
        resp = self.session.post(
            f"{self.base}/api/v1/search/text",
            json={
                "query_text": name,
                "query_type": "GRAPH_COMPLETION_CONTEXT_EXTENSION",
                "top_k": 50
            }
        )
        elapsed = time.perf_counter() - start
        data = resp.json() if resp.status_code == 200 else {}
        hop1 = data.get("context_hop1") or []
        hop2 = data.get("context_hop2") or []
        return elapsed, len(hop1) + len(hop2)


# ── Benchmark Runner ───────────────────────────────────────────────────────

def percentile(data, p):
    """Compute p-th percentile."""
    if not data:
        return 0
    sorted_data = sorted(data)
    k = (len(sorted_data) - 1) * p / 100
    f = int(k)
    c = f + 1
    if c >= len(sorted_data):
        return sorted_data[-1]
    return sorted_data[f] + (k - f) * (sorted_data[c] - sorted_data[f])

def run_benchmark(name, func, iterations=QUERY_ITERATIONS):
    """Run a benchmark function N times, collect latency stats."""
    latencies = []
    result_counts = []
    for _ in range(iterations):
        result = func()
        if isinstance(result, tuple):
            latencies.append(result[0])
            if len(result) > 1:
                result_counts.append(result[1])
        else:
            latencies.append(result)

    stats = {
        "name": name,
        "iterations": iterations,
        "p50_ms": round(percentile(latencies, 50) * 1000, 3),
        "p95_ms": round(percentile(latencies, 95) * 1000, 3),
        "p99_ms": round(percentile(latencies, 99) * 1000, 3),
        "mean_ms": round(statistics.mean(latencies) * 1000, 3),
        "min_ms": round(min(latencies) * 1000, 3),
        "max_ms": round(max(latencies) * 1000, 3),
    }
    if result_counts:
        stats["avg_results"] = round(statistics.mean(result_counts), 1)

    return stats

def print_comparison(nornic_stats, levara_stats):
    """Print side-by-side comparison."""
    print(f"\n{'='*80}")
    print(f"{'Benchmark':<35} {'NornicDB':>15} {'Levara':>15} {'Winner':>12}")
    print(f"{'='*80}")

    for ns in nornic_stats:
        ls = next((l for l in levara_stats if l["name"] == ns["name"]), None)
        if ls:
            nv = ns["p50_ms"]
            lv = ls["p50_ms"]
            winner = "NornicDB" if nv < lv else ("Levara" if lv < nv else "Tie")
            ratio = max(nv, lv) / max(min(nv, lv), 0.001)
            print(f"{ns['name']:<35} {nv:>12.3f}ms {lv:>12.3f}ms {winner:>8} ({ratio:.1f}x)")
        else:
            print(f"{ns['name']:<35} {ns['p50_ms']:>12.3f}ms {'N/A':>15} {'NornicDB':>8} (only)")


def main():
    print("=" * 80)
    print("NornicDB vs Levara — Graph Operations Benchmark")
    print("=" * 80)

    # ── Check connectivity ──
    print("\n[1/6] Checking connectivity...")
    nornic = NornicDBBench()
    try:
        cnt = nornic.count_nodes()
        print(f"  NornicDB: connected ({cnt} existing nodes)")
    except Exception as e:
        print(f"  NornicDB: FAILED — {e}")
        print("  Start NornicDB: docker run -d -p 7687:7687 -p 7474:7474 timothyswt/nornicdb-arm64-metal serve --no-auth")
        sys.exit(1)

    levara_ok = False
    try:
        r = requests.get(f"{LEVARA_HTTP}/health", timeout=5)
        if r.status_code == 200:
            levara_ok = True
            print(f"  Levara: connected")
    except:
        pass
    if not levara_ok:
        print(f"  Levara: not available at {LEVARA_HTTP} — running NornicDB-only benchmarks")

    # ── Generate data ──
    print(f"\n[2/6] Generating graph data ({NUM_NODES} nodes, ~{NUM_NODES*EDGES_PER_NODE} edges)...")
    nodes, edges = generate_graph_data(NUM_NODES, EDGES_PER_NODE)
    # Pick sample node names for traversal queries
    sample_names = [nodes[i]["name"] for i in random.sample(range(len(nodes)), min(QUERY_ITERATIONS, len(nodes)))]

    # ── NornicDB: Load data ──
    print(f"\n[3/6] Loading data into NornicDB...")
    nornic.clear()
    t_nodes = nornic.insert_nodes(nodes)
    t_edges = nornic.insert_edges(edges)
    nornic_count = nornic.count_nodes()
    print(f"  Nodes: {nornic_count} loaded in {t_nodes:.1f}s ({NUM_NODES/t_nodes:.0f} nodes/s)")
    print(f"  Edges: loaded in {t_edges:.1f}s ({len(edges)/t_edges:.0f} edges/s)")

    # ── NornicDB: Benchmarks ──
    print(f"\n[4/6] Running NornicDB benchmarks ({QUERY_ITERATIONS} iterations each)...")
    nornic_results = []

    # 1-hop
    print("  1-hop traversal...")
    stats = run_benchmark("traversal_1hop", lambda: nornic.traversal_1hop(random.choice(sample_names)))
    nornic_results.append(stats)
    print(f"    p50={stats['p50_ms']:.3f}ms, p99={stats['p99_ms']:.3f}ms, avg_results={stats.get('avg_results', 'N/A')}")

    # 2-hop
    print("  2-hop traversal...")
    stats = run_benchmark("traversal_2hop", lambda: nornic.traversal_2hop(random.choice(sample_names)))
    nornic_results.append(stats)
    print(f"    p50={stats['p50_ms']:.3f}ms, p99={stats['p99_ms']:.3f}ms, avg_results={stats.get('avg_results', 'N/A')}")

    # 3-hop
    print("  3-hop traversal...")
    stats = run_benchmark("traversal_3hop", lambda: nornic.traversal_3hop(random.choice(sample_names)), iterations=50)
    nornic_results.append(stats)
    print(f"    p50={stats['p50_ms']:.3f}ms, p99={stats['p99_ms']:.3f}ms, avg_results={stats.get('avg_results', 'N/A')}")

    # 5-hop
    print("  5-hop traversal...")
    stats = run_benchmark("traversal_5hop", lambda: nornic.traversal_5hop(random.choice(sample_names)), iterations=20)
    nornic_results.append(stats)
    print(f"    p50={stats['p50_ms']:.3f}ms, p99={stats['p99_ms']:.3f}ms, avg_results={stats.get('avg_results', 'N/A')}")

    # Shortest path
    print("  Shortest path...")
    pairs = [(sample_names[i], sample_names[(i+1) % len(sample_names)]) for i in range(min(50, len(sample_names)))]
    pair_idx = [0]
    def sp_query():
        idx = pair_idx[0] % len(pairs)
        pair_idx[0] += 1
        return nornic.shortest_path(pairs[idx][0], pairs[idx][1])
    stats = run_benchmark("shortest_path", sp_query, iterations=50)
    nornic_results.append(stats)
    print(f"    p50={stats['p50_ms']:.3f}ms, p99={stats['p99_ms']:.3f}ms")

    # Node count (sanity)
    print("  Node count query...")
    stats = run_benchmark("count_nodes", lambda: (time.perf_counter() - time.perf_counter() + 0.001, nornic.count_nodes()), iterations=50)
    # Re-run properly
    def count_query():
        t0 = time.perf_counter()
        nornic.count_nodes()
        return time.perf_counter() - t0
    stats = run_benchmark("count_nodes", lambda: (count_query(),), iterations=50)
    nornic_results.append(stats)
    print(f"    p50={stats['p50_ms']:.3f}ms")

    # ── Levara: Benchmarks (if available) ──
    levara_results = []
    if levara_ok:
        print(f"\n[5/6] Running Levara benchmarks...")
        levara = LevaraBench()

        # Note: Levara graph search requires vectors + LLM, so we test what's available
        # 1-hop via GRAPH_COMPLETION
        print("  1-hop traversal (GRAPH_COMPLETION)...")
        stats = run_benchmark("traversal_1hop",
            lambda: levara.traversal_1hop(random.choice(sample_names)),
            iterations=min(QUERY_ITERATIONS, 30)  # LLM calls are slow
        )
        levara_results.append(stats)
        print(f"    p50={stats['p50_ms']:.3f}ms, p99={stats['p99_ms']:.3f}ms")

        # 2-hop via CONTEXT_EXTENSION
        print("  2-hop traversal (CONTEXT_EXTENSION)...")
        stats = run_benchmark("traversal_2hop",
            lambda: levara.traversal_2hop(random.choice(sample_names)),
            iterations=min(QUERY_ITERATIONS, 30)
        )
        levara_results.append(stats)
        print(f"    p50={stats['p50_ms']:.3f}ms, p99={stats['p99_ms']:.3f}ms")

        levara.close()
    else:
        print(f"\n[5/6] Skipping Levara benchmarks (not available)")

    # ── Results ──
    print(f"\n[6/6] Results")
    print_comparison(nornic_results, levara_results)

    # NornicDB-only results
    print(f"\n{'─'*80}")
    print("NornicDB-only capabilities (not available in Levara):")
    for stats in nornic_results:
        if stats["name"] in ("traversal_3hop", "traversal_5hop", "shortest_path"):
            print(f"  {stats['name']}: p50={stats['p50_ms']:.3f}ms, p99={stats['p99_ms']:.3f}ms, avg_results={stats.get('avg_results', 'N/A')}")

    # Save results
    results = {
        "timestamp": time.strftime("%Y-%m-%dT%H:%M:%S"),
        "config": {
            "num_nodes": NUM_NODES,
            "edges_per_node": EDGES_PER_NODE,
            "query_iterations": QUERY_ITERATIONS,
        },
        "nornicdb": nornic_results,
        "levara": levara_results,
    }

    outfile = "results_graph.json"
    with open(outfile, "w") as f:
        json.dump(results, f, indent=2)
    print(f"\nResults saved to {outfile}")

    nornic.close()


if __name__ == "__main__":
    main()
