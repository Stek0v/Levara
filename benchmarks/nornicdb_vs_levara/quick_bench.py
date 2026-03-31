#!/usr/bin/env python3
"""Quick NornicDB graph benchmark — 1K nodes, 5K edges."""
import time, random, string, json, subprocess
from neo4j import GraphDatabase

URI = "bolt://localhost:7687"
N = 500
EDGES = 5

print("=== NornicDB Graph Benchmark ===")
d = GraphDatabase.driver(URI, auth=None)

# Clear
print("[1] Clearing...")
with d.session() as s:
    s.run("MATCH (n) DETACH DELETE n")
    cnt = s.run("MATCH (n) RETURN count(n) AS c").single()["c"]
    print(f"    After clear: {cnt}")

# Generate
print(f"[2] Generating {N} nodes...")
nodes = []
for i in range(N):
    name = "ent_" + "".join(random.choices(string.ascii_lowercase, k=6))
    nodes.append({"id": f"n-{i}", "name": name, "type": "Entity", "desc": f"Node {i}"})

edges = []
for i in range(N):
    for t in random.sample(range(N), EDGES):
        if t != i:
            edges.append({"src": f"n-{i}", "tgt": f"n-{t}"})

# Insert nodes
print(f"[3] Inserting {N} nodes...")
t0 = time.time()
for i in range(0, N, 200):
    batch = nodes[i:i+200]
    with d.session() as s:
        s.run(
            "UNWIND $b AS n CREATE (:__Node__ {id: n.id, name: n.name, type: n.type, description: n.desc})",
            b=batch
        )
t_n = time.time() - t0
print(f"    {N} nodes in {t_n:.2f}s ({N/t_n:.0f} nodes/sec)")

# Insert edges
print(f"[4] Inserting {len(edges)} edges...")
t0 = time.time()
for i in range(0, len(edges), 50):
    batch = edges[i:i+50]
    with d.session() as s:
        s.run(
            "UNWIND $batch AS e MATCH (src:__Node__ {id: e.src}), (tgt:__Node__ {id: e.tgt}) CREATE (src)-[:RELATES_TO]->(tgt)",
            batch=batch
        )
t_e = time.time() - t0
print(f"    {len(edges)} edges in {t_e:.2f}s ({len(edges)/t_e:.0f} edges/sec)")

# Traversals
results = {}

for label, query, iters in [
    ("1-hop", "MATCH (n:__Node__)-[r]-(m:__Node__) WHERE n.name = $name RETURN n.name, type(r), m.name LIMIT 50", 50),
    ("2-hop", "MATCH (n:__Node__)-[r1]-(m)-[r2]-(k:__Node__) WHERE n.name = $name RETURN n.name, m.name, k.name LIMIT 100", 50),
    ("3-hop", "MATCH (n:__Node__)-[*1..3]-(m) WHERE n.name = $name RETURN DISTINCT m.name LIMIT 200", 30),
    ("5-hop", "MATCH (n:__Node__)-[*1..5]-(m) WHERE n.name = $name RETURN DISTINCT m.name LIMIT 200", 10),
]:
    print(f"[5] {label} traversal ({iters} queries)...")
    lats = []
    counts = []
    for j in range(iters):
        name = nodes[j % N]["name"]
        t0 = time.time()
        with d.session() as s:
            recs = list(s.run(query, name=name))
        lats.append(time.time() - t0)
        counts.append(len(recs))
    lats.sort()
    p50 = lats[len(lats)//2] * 1000
    p99 = lats[min(int(len(lats)*0.99), len(lats)-1)] * 1000
    avg_cnt = sum(counts) / len(counts) if counts else 0
    print(f"    p50={p50:.1f}ms  p99={p99:.1f}ms  avg_results={avg_cnt:.0f}")
    results[label] = {"p50_ms": round(p50, 2), "p99_ms": round(p99, 2), "avg_results": round(avg_cnt, 1)}

# Shortest path
print("[6] Shortest path (30 queries)...")
lats = []
plens = []
for j in range(30):
    n1 = nodes[j]["name"]
    n2 = nodes[(j+50) % N]["name"]
    t0 = time.time()
    with d.session() as s:
        recs = list(s.run(
            "MATCH p=shortestPath((a:__Node__)-[*]-(b:__Node__)) WHERE a.name = $n1 AND b.name = $n2 RETURN length(p) AS pl",
            n1=n1, n2=n2
        ))
    lats.append(time.time() - t0)
    if recs:
        plens.append(recs[0]["pl"])
lats.sort()
p50 = lats[len(lats)//2] * 1000
p99 = lats[-1] * 1000
avg_pl = sum(plens)/len(plens) if plens else -1
print(f"    p50={p50:.1f}ms  p99={p99:.1f}ms  avg_path_len={avg_pl:.1f}")
results["shortest_path"] = {"p50_ms": round(p50, 2), "p99_ms": round(p99, 2), "avg_path_len": round(avg_pl, 1)}

# Resource
try:
    out = subprocess.run(["docker", "stats", "nornicdb-bench", "--no-stream", "--format", "{{.MemUsage}}"], capture_output=True, text=True, timeout=10)
    mem = out.stdout.strip()
    print(f"\n[7] RAM: {mem}")
    results["ram"] = mem
except:
    pass

# Summary
print(f"\n{'='*60}")
print(f"{'Test':<20} {'p50 (ms)':>10} {'p99 (ms)':>10} {'Results':>10}")
print(f"{'='*60}")
for k, v in results.items():
    if k != "ram":
        print(f"{k:<20} {v['p50_ms']:>10.1f} {v['p99_ms']:>10.1f} {v.get('avg_results', v.get('avg_path_len', '')):>10}")

print(f"\nInsert: {N/t_n:.0f} nodes/sec, {len(edges)/t_e:.0f} edges/sec")
if "ram" in results:
    print(f"RAM: {results['ram']}")

with open("results_graph_quick.json", "w") as f:
    json.dump({"nodes": N, "edges": len(edges), "insert_nodes_sec": round(N/t_n), "insert_edges_sec": round(len(edges)/t_e), "results": results}, f, indent=2)
print("\nSaved to results_graph_quick.json")

d.close()
