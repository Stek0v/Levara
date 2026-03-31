#!/usr/bin/env python3
"""NornicDB Fair Re-benchmark — WITH indexes, scaling 500→10K nodes."""
import sys, time, random, string, json, subprocess
sys.stdout.reconfigure(line_buffering=True)

from neo4j import GraphDatabase

URI = "bolt://localhost:7687"
SCALES = [500, 1000, 5000]
EDGES_PER_NODE = 5

def random_name():
    return "ent_" + "".join(random.choices(string.ascii_lowercase, k=8))

def generate_data(n, epn):
    nodes = [{"id": f"n-{i}", "name": random_name(), "type": "Entity", "desc": f"Node {i}"} for i in range(n)]
    edges = []
    for i in range(n):
        for t in random.sample(range(n), min(epn, n-1)):
            if t != i:
                edges.append({"src": f"n-{i}", "tgt": f"n-{t}"})
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

print("=" * 70)
print("NornicDB FAIR Re-benchmark (WITH indexes)")
print("=" * 70)

all_results = {}

for N in SCALES:
    print(f"\n{'─'*70}")
    print(f"Scale: {N} nodes, ~{N*EDGES_PER_NODE} edges")
    print(f"{'─'*70}")

    d = GraphDatabase.driver(URI, auth=None)
    result_entry = {"nodes": N}

    # 1. Clear
    print("[1] Clearing...")
    with d.session() as s:
        s.run("MATCH (n) DETACH DELETE n")
        time.sleep(1)
        cnt = s.run("MATCH (n) RETURN count(n) AS c").single()["c"]
        print(f"    After clear: {cnt}")

    # 2. CREATE INDEXES (CRITICAL FIX)
    print("[2] Creating indexes...")
    with d.session() as s:
        s.run("CREATE INDEX node_id IF NOT EXISTS FOR (n:__Node__) ON (n.id)")
        s.run("CREATE INDEX node_name IF NOT EXISTS FOR (n:__Node__) ON (n.name)")
    time.sleep(3)  # let indexes build
    print("    Indexes created: node_id, node_name")

    # 3. Generate data
    print(f"[3] Generating {N} nodes...")
    nodes, edges = generate_data(N, EDGES_PER_NODE)

    # 4. Insert nodes
    print(f"[4] Inserting {N} nodes...")
    t0 = time.perf_counter()
    for i in range(0, N, 200):
        batch = nodes[i:i+200]
        with d.session() as s:
            s.run("UNWIND $b AS n CREATE (:__Node__ {id: n.id, name: n.name, type: n.type, description: n.desc})", b=batch)
    t_nodes = time.perf_counter() - t0
    result_entry["insert_nodes_sec"] = round(N / t_nodes)
    print(f"    {N} nodes in {t_nodes:.2f}s ({N/t_nodes:.0f} nodes/sec)")

    # 5. Insert edges (batch=50)
    print(f"[5] Inserting {len(edges)} edges...")
    t0 = time.perf_counter()
    crashes = 0
    for i in range(0, len(edges), 50):
        batch = edges[i:i+50]
        try:
            with d.session() as s:
                s.run(
                    "UNWIND $batch AS e MATCH (src:__Node__ {id: e.src}), (tgt:__Node__ {id: e.tgt}) CREATE (src)-[:RELATES_TO]->(tgt)",
                    batch=batch
                )
        except Exception as ex:
            crashes += 1
            print(f"    CRASH at batch {i}: {ex}")
            # Reconnect
            try:
                d.close()
            except:
                pass
            d = GraphDatabase.driver(URI, auth=None)
            time.sleep(2)
    t_edges = time.perf_counter() - t0
    result_entry["insert_edges_sec"] = round(len(edges) / t_edges) if t_edges > 0 else 0
    result_entry["edge_crashes"] = crashes
    print(f"    {len(edges)} edges in {t_edges:.2f}s ({len(edges)/t_edges:.0f} edges/sec), crashes={crashes}")

    # Verify count
    with d.session() as s:
        cnt = s.run("MATCH (n:__Node__) RETURN count(n) AS c").single()["c"]
        print(f"    Verified: {cnt} nodes in DB")

    # 6. Traversals
    print("[6] Traversal benchmarks...")

    # 1-hop
    def query_1hop(j):
        name = nodes[j % N]["name"]
        with d.session() as s:
            recs = list(s.run("MATCH (n:__Node__)-[r]-(m:__Node__) WHERE n.name = $name RETURN n.name, type(r), m.name LIMIT 50", name=name))
        return len(recs)
    result_entry["1hop"] = bench("1-hop", query_1hop, min(100, N))

    # 2-hop
    def query_2hop(j):
        name = nodes[j % N]["name"]
        with d.session() as s:
            recs = list(s.run("MATCH (n:__Node__)-[r1]-(m)-[r2]-(k:__Node__) WHERE n.name = $name RETURN n.name, m.name, k.name LIMIT 100", name=name))
        return len(recs)
    result_entry["2hop"] = bench("2-hop", query_2hop, min(50, N))

    # 3-hop (with LIMIT to prevent explosion)
    def query_3hop(j):
        name = nodes[j % N]["name"]
        with d.session() as s:
            recs = list(s.run("MATCH (n:__Node__)-[*1..3]-(m) WHERE n.name = $name RETURN DISTINCT m.name LIMIT 200", name=name))
        return len(recs)
    result_entry["3hop"] = bench("3-hop", query_3hop, min(20, N))

    # 5-hop (careful — may OOM)
    if N <= 1000:
        def query_5hop(j):
            name = nodes[j % N]["name"]
            with d.session() as s:
                recs = list(s.run("MATCH (n:__Node__)-[*1..5]-(m) WHERE n.name = $name RETURN DISTINCT m.name LIMIT 100", name=name))
            return len(recs)
        try:
            result_entry["5hop"] = bench("5-hop", query_5hop, 5)
        except Exception as ex:
            result_entry["5hop"] = {"status": "FAILED", "error": str(ex)}
            print(f"    5-hop FAILED: {ex}")
    else:
        result_entry["5hop"] = {"status": "SKIPPED", "reason": f"N={N} too large for 5-hop"}
        print(f"    5-hop: SKIPPED (N={N})")

    # Shortest path
    def query_sp(j):
        n1 = nodes[j % N]["name"]
        n2 = nodes[(j + 50) % N]["name"]
        with d.session() as s:
            recs = list(s.run(
                "MATCH p=shortestPath((a:__Node__)-[*]-(b:__Node__)) WHERE a.name = $n1 AND b.name = $n2 RETURN length(p) AS pl",
                n1=n1, n2=n2
            ))
        return recs[0]["pl"] if recs else -1
    try:
        result_entry["shortest_path"] = bench("shortest_path", query_sp, min(30, N))
    except Exception as ex:
        result_entry["shortest_path"] = {"status": "FAILED", "error": str(ex)}
        print(f"    shortest_path FAILED: {ex}")

    # RAM
    try:
        out = subprocess.run(["docker", "stats", "nornicdb-bench", "--no-stream", "--format", "{{.MemUsage}}"],
                             capture_output=True, text=True, timeout=10)
        result_entry["ram"] = out.stdout.strip()
        print(f"    RAM: {result_entry['ram']}")
    except:
        pass

    all_results[f"N={N}"] = result_entry
    d.close()

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

with open("results_nornicdb_fair.json", "w") as f:
    json.dump(all_results, f, indent=2)
print(f"\nSaved to results_nornicdb_fair.json")
