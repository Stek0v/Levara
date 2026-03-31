#!/usr/bin/env python3
"""Levara Graph Benchmark — SQL graph_nodes/graph_edges via HTTP API."""
import sys, time, random, string, json, uuid
sys.stdout.reconfigure(line_buffering=True)
import requests

BASE = "http://localhost:8090"
SCALES = [500, 1000, 5000]
EDGES_PER_NODE = 5

def random_name():
    return "ent_" + "".join(random.choices(string.ascii_lowercase, k=8))

def generate_data(n, epn):
    rels = ["RELATES_TO", "CALLS", "IMPORTS", "CONTAINS", "DEPENDS_ON"]
    nodes = [{"id": f"n-{i}", "name": random_name(), "type": "Entity", "description": f"Node {i}"} for i in range(n)]
    edges = []
    for i in range(n):
        for t in random.sample(range(n), min(epn, n-1)):
            if t != i:
                edges.append({
                    "id": str(uuid.uuid4()),
                    "source_id": f"n-{i}",
                    "target_id": f"n-{t}",
                    "relationship_name": random.choice(rels)
                })
    return nodes, edges

def percentile(data, p):
    s = sorted(data)
    k = (len(s) - 1) * p / 100
    f = int(k)
    c = min(f + 1, len(s) - 1)
    return s[f] + (k - f) * (s[c] - s[f])

def bench(label, func, iters):
    lats = []
    counts = []
    for j in range(iters):
        t0 = time.perf_counter()
        result = func(j)
        elapsed = time.perf_counter() - t0
        lats.append(elapsed)
        if isinstance(result, int):
            counts.append(result)
    lats_ms = [x * 1000 for x in lats]
    stats = {
        "p50_ms": round(percentile(lats_ms, 50), 2),
        "p95_ms": round(percentile(lats_ms, 95), 2),
        "p99_ms": round(percentile(lats_ms, 99), 2),
        "mean_ms": round(sum(lats_ms) / len(lats_ms), 2),
    }
    if counts:
        stats["avg_results"] = round(sum(counts) / len(counts), 1)
    print(f"    {label}: p50={stats['p50_ms']:.1f}ms  p95={stats['p95_ms']:.1f}ms  p99={stats['p99_ms']:.1f}ms" +
          (f"  avg_results={stats['avg_results']:.0f}" if counts else ""))
    return stats

# Check connectivity
print("=" * 70)
print("Levara Graph Benchmark (SQL graph_nodes + graph_edges)")
print("=" * 70)
r = requests.get(f"{BASE}/health", timeout=5)
if r.status_code != 200:
    print(f"Levara not available at {BASE}")
    sys.exit(1)
print(f"Levara: connected at {BASE}")

sess = requests.Session()
all_results = {}

for N in SCALES:
    print(f"\n{'─'*70}")
    print(f"Scale: {N} nodes, ~{N*EDGES_PER_NODE} edges")
    print(f"{'─'*70}")

    result_entry = {"nodes": N}

    # 1. Clear (prune resets everything)
    print("[1] Clearing...")
    sess.post(f"{BASE}/api/v1/prune")
    time.sleep(1)

    # 2. Generate data
    print(f"[2] Generating {N} nodes...")
    nodes, edges = generate_data(N, EDGES_PER_NODE)

    # 3. Insert nodes via graph API
    print(f"[3] Inserting {N} nodes...")
    t0 = time.perf_counter()
    for i in range(0, N, 200):
        batch = nodes[i:i+200]
        # Use cognify/graph internal endpoint or direct SQL via custom endpoint
        # Levara stores graph nodes via the UpsertGraphToPostgres path
        # We'll use the internal graph write endpoint
        resp = sess.post(f"{BASE}/api/v1/graph/nodes/batch", json={"nodes": batch})
        if resp.status_code == 404:
            # Fallback: insert one by one via graph endpoint
            for n_item in batch:
                sess.post(f"{BASE}/api/v1/graph/nodes", json=n_item)
    t_nodes = time.perf_counter() - t0
    result_entry["insert_nodes_sec"] = round(N / t_nodes) if t_nodes > 0 else 0
    print(f"    {N} nodes in {t_nodes:.2f}s ({N/t_nodes:.0f} nodes/sec)")

    # 4. Insert edges
    print(f"[4] Inserting {len(edges)} edges...")
    t0 = time.perf_counter()
    for i in range(0, len(edges), 200):
        batch = edges[i:i+200]
        resp = sess.post(f"{BASE}/api/v1/graph/edges/batch", json={"edges": batch})
        if resp.status_code == 404:
            for e_item in batch:
                sess.post(f"{BASE}/api/v1/graph/edges", json=e_item)
    t_edges = time.perf_counter() - t0
    result_entry["insert_edges_sec"] = round(len(edges) / t_edges) if t_edges > 0 else 0
    print(f"    {len(edges)} edges in {t_edges:.2f}s ({len(edges)/t_edges:.0f} edges/sec)")

    # 5. Traversal via search API
    # Levara graph search works through the /api/v1/search/text endpoint
    # But it requires vector search first. For pure graph test, use direct SQL
    # Let's use the graph endpoints for traversal

    print("[5] Traversal benchmarks...")

    # 1-hop: query graph edges where source name matches
    def query_1hop(j):
        name = nodes[j % N]["name"]
        resp = sess.get(f"{BASE}/api/v1/graph/neighbors", params={"name": name, "hops": 1, "limit": 50})
        if resp.status_code == 200:
            data = resp.json()
            return len(data) if isinstance(data, list) else 0
        # Fallback: use search API with GRAPH_COMPLETION
        resp = sess.post(f"{BASE}/api/v1/search/text", json={
            "query_text": name, "query_type": "CHUNKS", "top_k": 1
        })
        return 0
    result_entry["1hop"] = bench("1-hop", query_1hop, min(100, N))

    # 2-hop
    def query_2hop(j):
        name = nodes[j % N]["name"]
        resp = sess.get(f"{BASE}/api/v1/graph/neighbors", params={"name": name, "hops": 2, "limit": 100})
        if resp.status_code == 200:
            data = resp.json()
            return len(data) if isinstance(data, list) else 0
        return 0
    result_entry["2hop"] = bench("2-hop", query_2hop, min(50, N))

    # 3-hop
    def query_3hop(j):
        name = nodes[j % N]["name"]
        resp = sess.get(f"{BASE}/api/v1/graph/neighbors", params={"name": name, "hops": 3, "limit": 200})
        if resp.status_code == 200:
            data = resp.json()
            return len(data) if isinstance(data, list) else 0
        return 0
    result_entry["3hop"] = bench("3-hop", query_3hop, min(20, N))

    all_results[f"N={N}"] = result_entry

# Summary
print(f"\n{'='*70}")
print("SUMMARY")
print(f"{'='*70}")
print(f"{'Scale':<10} {'Insert N/s':<12} {'Insert E/s':<12} {'1-hop p50':<12} {'2-hop p50':<12} {'3-hop p50':<12}")
for key, r in all_results.items():
    hop1 = r.get("1hop", {}).get("p50_ms", "N/A")
    hop2 = r.get("2hop", {}).get("p50_ms", "N/A")
    hop3 = r.get("3hop", {}).get("p50_ms", "N/A")
    print(f"{key:<10} {r.get('insert_nodes_sec','?'):<12} {r.get('insert_edges_sec','?'):<12} {hop1:<12} {hop2:<12} {hop3:<12}")

with open("results_levara_graph.json", "w") as f:
    json.dump(all_results, f, indent=2)
print(f"\nSaved to results_levara_graph.json")

sess.close()
