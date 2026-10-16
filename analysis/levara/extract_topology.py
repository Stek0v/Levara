#!/usr/bin/env python3
"""Topology extraction for the Levara modernization run (code-modernization plugin).

Sources of truth (auditable, re-runnable):
  - `go list -json ./...`  — the Go build system's own package + import graph
    (fixed argument list; no shell, no user-controlled args)
  - directory walk restricted to the repo root — module sizes for webui/tests
  - domain rules + dispatch/data edges + persona flows declared below as data,
    derived from analysis/levara/ASSESSMENT.md (2026-10-15).

Every filesystem path is built by rooted(), which accepts only plain relative
segments (no "..", no separators), joins them onto the repository root, and
re-verifies the realpath stays inside it. No path from external process output
is ever joined or opened directly.

Edge kinds: call (package import), dispatch (router/registry-reached),
read / write (datastore access heuristics — documented in observations).

Writes analysis/levara/topology.json and prints a human summary (<=200 lines).
"""
import json, subprocess, os
from collections import defaultdict

HERE = os.path.dirname(os.path.abspath(__file__))          # <repo>/analysis/levara
ANALYSIS = os.path.dirname(HERE)                           # <repo>/analysis
ROOT = os.path.dirname(ANALYSIS)                           # <repo>
ROOT_REAL = os.path.realpath(ROOT)
OUT = os.path.join(HERE, "topology.json")
MODULE = "github.com/stek0v/levara"
SKIP_DIRS = {"node_modules", ".next", "__pycache__", ".git", ".venv-ab"}

DOMAIN_RULES = [  # ordered: first match wins (package import path suffix)
    ("clients",    ["cmd/", "webui", "tests"]),
    ("api",        ["internal/http", "pkg/mcp", "pkg/profile", "pkg/router"]),
    ("grpc-api",   ["internal/grpc", "proto"]),
    ("memory",     ["pkg/memoryhall", "pkg/memoryindex", "pkg/consolidate"]),
    ("search",     ["pkg/bm25", "pkg/aggregator", "pkg/graphrank", "pkg/rerank",
                    "pkg/vsa", "pkg/vsamemory"]),
    ("ai-ml",      ["pkg/embed", "pkg/embcontract", "pkg/llm", "pkg/llmcache",
                    "pkg/llmproxy", "pkg/classify", "pkg/chunker", "pkg/extract",
                    "pkg/structuredextract", "pkg/docdetect"]),
    ("knowledge-graph", ["pkg/graph", "pkg/graphdb", "pkg/graphstore",
                    "pkg/orchestrator", "pkg/ontology", "pkg/taxonomy",
                    "pkg/temporal", "pkg/community"]),
    ("ingestion",  ["pkg/ingest", "pkg/git", "pkg/chatimport", "pkg/fetch",
                    "pkg/audio", "pkg/fileio"]),
    ("workspace",  ["pkg/workspace"]),
    ("storage",    ["internal/store", "pkg/storage", "pkg/vectorstore",
                    "pkg/sqlcompat", "pkg/backup"]),
    ("enterprise", ["pkg/access", "pkg/auth", "pkg/agenthosts", "pkg/audit",
                    "internal/contract"]),
    ("platform",   ["internal/cluster", "internal/metrics",
                    "internal/trajectory", "pkg/governor", "pkg/runreg",
                    "pkg/observe"]),
]

# datastore membership: package import path suffix -> datastore leaf id
STORE_RULES = [
    ("internal/store",        ["ds:wal"]),
    ("pkg/storage",           ["ds:sqlite", "ds:postgres"]),   # sqlcompat duality
    ("pkg/graphdb",           ["ds:neo4j"]),
    ("pkg/vectorstore",       ["ds:hnsw"]),
]
DATASTORES = [
    ("ds:wal",      "WAL + collections (internal/store)"),
    ("ds:sqlite",   "SQLite (ncruces)"),
    ("ds:postgres", "PostgreSQL (pgx)"),
    ("ds:neo4j",    "Neo4j graph"),
    ("ds:hnsw",     "HNSW vector index"),
    ("ds:embedsrv", "Embed server :9101 (external Python/MPS)"),
    ("ds:ollama",   "Ollama LLM runtime (external)"),
]
# packages that talk to EXTERNAL services (modeled as datastore leaves)
EXTERNAL_CALLS = [
    ("pkg/embed", "ds:embedsrv"),
    ("pkg/llm",   "ds:ollama"),
]

# read vs write heuristics by domain (documented in observations)
WRITE_DOMAINS = {"api", "memory", "ingestion", "workspace", "enterprise", "grpc-api"}
READ_DOMAINS  = {"search", "knowledge-graph", "ai-ml", "storage", "platform", "clients"}

# dispatcher-reached edges (router/registry — not visible in the import graph)
DISPATCH_EDGES = [
    ("pkg/mcp",  "internal/http", "MCP tool registry -> handlers"),
    ("cmd/server", "internal/grpc", "gRPC service registration"),
]

# entry points (deployment-level: launchd/compose are the real invokers)
ENTRY_HINTS = ["cmd/server", "cmd/cli", "cmd/backup", "cmd/benchmark",
               "cmd/loadtest", "cmd/reconcile", "cmd/audit", "cmd/contract",
               "cmd/agent-hosts", "cmd/qwen3rerank"]

OBSERVATIONS = [
    "internal/http is the god-hub: nearly every domain imports or is dispatched "
    "through it; its own methods carry the hottest fan-outs (mcpHandler.Q 564).",
    "cmd/server/main.go hand-wires every subsystem (betweenness 133, highest in "
    "app code) — single point of wiring failure; runreg exists but is not the sole path.",
    "Recall path depends on the EXTERNAL embed server :9101 — prod telemetry shows "
    "p99/p50 = ~1000x variance when it is slow/down (documented incident class).",
    "Data writes fan out to BOTH SQLite and PostgreSQL through pkg/sqlcompat — "
    "any storage refactor must keep the dual-DB contract tests green.",
    "gRPC v1 (internal/grpc/service.go) coexists with v2 (service_v2.go); v1 "
    "ListCollections/HasCollection are import-graph dead — candidate retirement.",
    "Symbol-level dead candidates inside internal/http/mcp*.go are suppressed "
    "from deadEnds: the MCP dispatch table reaches handlers dynamically; "
    "verify against ToolDescriptors before deleting (see ASSESSMENT.md #3).",
    "Datastore read/write edges are domain-role heuristics (WRITE_DOMAINS/"
    "READ_DOMAINS), not per-call-site facts; treat as orientation, not proof.",
]

FLOWS = [
    {
        "name": "Agent saves and later recalls a memory",
        "persona": "AI assistant using Levara over MCP (Claude, Codex, ZCode)",
        "description": "An agent stores a durable decision in one session and "
                       "retrieves it in a later session.",
        "steps": [
            {"label": "Agent calls save_memory tool", "nodes": ["pkg/mcp", "internal/http"]},
            {"label": "Memory written to hall + WAL", "nodes": ["pkg/memoryhall", "internal/store"]},
            {"label": "Text embedded (external server)", "nodes": ["pkg/embed"]},
            {"label": "Vector stored in collection", "nodes": ["pkg/vectorstore", "ds:hnsw"]},
            {"label": "Later: recall_memory query", "nodes": ["pkg/mcp", "internal/http"]},
            {"label": "Hybrid search + rerank", "nodes": ["pkg/bm25", "pkg/rerank", "pkg/vectorstore"]},
            {"label": "Ranked memories returned", "nodes": ["pkg/memoryindex"]},
        ],
    },
    {
        "name": "Engineer ingests a repository and explores the knowledge graph",
        "persona": "Knowledge worker / onboarding engineer",
        "description": "Someone points Levara at a git repo, lets it extract "
                       "entities and edges, then queries who-did-what over time.",
        "steps": [
            {"label": "Ingest documents from git", "nodes": ["pkg/ingest", "pkg/git"]},
            {"label": "Chunk + classify", "nodes": ["pkg/chunker", "pkg/classify"]},
            {"label": "LLM extracts entities/edges", "nodes": ["pkg/llm", "pkg/structuredextract"]},
            {"label": "Graph persisted with temporal validity", "nodes": ["pkg/orchestrator", "pkg/temporal", "pkg/graphdb"]},
            {"label": "User queries entities / communities", "nodes": ["internal/http", "pkg/community"]},
        ],
    },
    {
        "name": "Operator backs up and restores production",
        "persona": "On-call operator (Mac/Pi instances)",
        "description": "Nightly or pre-migration snapshot of all collections "
                       "with isolated restore verification.",
        "steps": [
            {"label": "Run levara-backup verified", "nodes": ["cmd/backup", "pkg/backup"]},
            {"label": "Snapshot WAL + collections", "nodes": ["internal/store", "pkg/storage"]},
            {"label": "Restore into isolated copy", "nodes": ["pkg/backup", "pkg/storage"]},
            {"label": "Verification passes, archive kept", "nodes": ["cmd/backup"]},
        ],
    },
]


def rooted(*segments):
    """Join plain relative segments onto the repo root, refusing traversal.

    Accepts only single path components (no '..', '.', separators); verifies
    the resolved realpath stays inside the repository root.
    """
    for seg in segments:
        if seg in ("", ".", "..") or "/" in seg or "\\" in seg or "\0" in seg:
            raise ValueError("unsafe path segment: %r" % (seg,))
    path = os.path.realpath(os.path.join(ROOT_REAL, *segments))
    if not path.startswith(ROOT_REAL + os.sep):
        raise ValueError("path escapes repo root")
    return path


def count_lines(path):
    n = 0
    with open(path, errors="ignore") as fh:
        for _ in fh:
            n += 1
    return n


def walk_loc(subdir, exts):
    """Non-test source LOC under <repo>/<subdir>, skipping vendored/build dirs."""
    total = 0
    for cur, dirs, files in os.walk(rooted(subdir)):
        dirs[:] = [d for d in dirs if d not in SKIP_DIRS]
        for f in files:
            if f.endswith(exts):
                try:
                    total += count_lines(os.path.join(cur, f))
                except OSError:
                    pass
    return total


def go_list():
    """Fixed-argument, no-shell invocation of the Go toolchain."""
    proc = subprocess.run(["go", "list", "-json", "./..."],
                          capture_output=True, text=True, check=True, cwd=ROOT)
    return proc.stdout


def domain_of(rel):  # rel: package path relative to module root
    for dom, prefixes in DOMAIN_RULES:
        for p in prefixes:
            if p.endswith("/"):
                if rel.startswith(p):
                    return dom
            elif rel == p or rel.startswith(p + "/"):
                return dom
    return "platform"


def safe_rel(imp):
    """Validate a module-internal import path; return repo-relative segments.

    Go import paths cannot contain '..' segments, but this never trusts that:
    each segment is re-checked before it may be used to build a path.
    """
    if not imp.startswith(MODULE + "/"):
        return None
    rel = imp[len(MODULE) + 1:]
    segs = rel.split("/")
    if any(s in ("", ".", "..") for s in segs):
        return None
    if segs[0] not in ("cmd", "internal", "pkg", "proto", "tests"):
        return None
    return segs


def main():
    raw = go_list()
    dec, idx, pkgs = json.JSONDecoder(), 0, []
    while idx < len(raw):
        while idx < len(raw) and raw[idx] in " \t\r\n":
            idx += 1
        if idx >= len(raw):
            break
        obj, idx = dec.raw_decode(raw, idx)
        pkgs.append(obj)

    nodes, edges, seen = {}, [], set()

    def edge(s, t, kind):
        if s != t and (s, t, kind) not in seen and s in nodes and t in nodes:
            seen.add((s, t, kind))
            edges.append({"source": s, "target": t, "kind": kind})

    in_degree = defaultdict(int)
    for p in pkgs:
        segs = safe_rel(p.get("ImportPath", ""))
        if segs is None:
            continue
        rel = "/".join(segs)
        loc = 0
        for f in p.get("GoFiles", []) or []:
            if f in ("", ".", "..") or "/" in f or "\\" in f:
                continue
            try:
                loc += count_lines(rooted(*segs, f))
            except (ValueError, OSError):
                pass
        nodes[rel] = {"id": rel, "name": segs[-1],
                      "kind": "module", "language": "go", "loc": loc,
                      "file": rel, "domain": domain_of(rel)}

    # webui + tests as client modules (directory walk, traversal-guarded)
    for mid, sub, name, lang, exts in [
            ("webui", "webui", "webui (Next.js)", "ts", (".ts", ".tsx")),
            ("tests", "tests", "tests (pytest e2e)", "py", (".py",))]:
        nodes[mid] = {"id": mid, "name": name, "kind": "module", "language": lang,
                      "loc": walk_loc(sub, exts), "file": sub, "domain": "clients"}

    for nid, meta in DATASTORES:
        nodes[nid] = {"id": nid, "name": meta, "kind": "datastore",
                      "domain": "data"}

    # --- edges ---
    for p in pkgs:  # call edges: internal imports
        src_segs = safe_rel(p.get("ImportPath", ""))
        if src_segs is None:
            continue
        src = "/".join(src_segs)
        if src not in nodes:
            continue
        for dep in p.get("Imports", []) or []:
            dep_segs = safe_rel(dep)
            if dep_segs is None:
                continue
            d = "/".join(dep_segs)
            if d in nodes:
                edge(src, d, "call")
                in_degree[d] += 1

    for src, dst, why in DISPATCH_EDGES:  # registry/router edges
        edge(src, dst, "dispatch")

    for p in pkgs:  # datastore read/write edges (domain-role heuristic)
        segs = safe_rel(p.get("ImportPath", ""))
        if segs is None:
            continue
        src = "/".join(segs)
        if src not in nodes:
            continue
        dom = nodes[src]["domain"]
        for prefix, stores in STORE_RULES:
            if src == prefix or src.startswith(prefix):
                for ds in stores:
                    edge(src, ds, "write" if dom in WRITE_DOMAINS else "read")
        for prefix, ds in EXTERNAL_CALLS:
            if src == prefix or src.startswith(prefix):
                edge(src, ds, "call")

    # --- entry points & dead ends ---
    entry = [nid for nid in ENTRY_HINTS if nid in nodes] + ["webui", "tests"]
    dead = sorted(nid for nid, m in nodes.items()
                  if m["kind"] == "module" and nid not in entry
                  and in_degree.get(nid, 0) == 0
                  and m["domain"] != "clients" and not nid.startswith("cmd/"))
    # suppression: anything the dispatch tables reach is NOT dead
    DISPATCH_REACHED = {"internal/http", "pkg/mcp", "internal/grpc"}
    dead = [d for d in dead if d not in DISPATCH_REACHED]

    # --- tree assembly ---
    dom_children = defaultdict(list)
    for nid, m in sorted(nodes.items()):
        dom_children[m["domain"]].append(m)
    order = [d for d, _ in DOMAIN_RULES] + ["data"]
    children = [{"id": "dom:%s" % d, "name": d.replace("-", " ").title(),
                 "kind": "domain",
                 "children": [m for m in dom_children.get(d, [])]}
                for d in order if dom_children.get(d)]

    topo = {"system": "Levara", "root": {"id": "sys", "name": "Levara",
            "kind": "system", "children": children},
            "edges": edges, "entryPoints": entry, "deadEnds": dead,
            "observations": OBSERVATIONS, "flows": FLOWS}
    json.dump(topo, open(OUT, "w"), indent=1)

    # --- human summary ---
    print("topology.json: %d leaves, %d edges, %d entry points, %d dead-end candidates"
          % (len(nodes), len(edges), len(entry), len(dead)))
    print("\n%-18s%8s%9s" % ("domain", "modules", "loc"))
    for d in order:
        mods = dom_children.get(d, [])
        if mods:
            print("%-18s%8d%9d" % (d, len(mods), sum(m.get("loc", 0) for m in mods)))
    print("\nTop modules by inbound call edges:")
    top = sorted(((n, c) for n, c in in_degree.items() if n in nodes),
                 key=lambda x: -x[1])[:12]
    for n, c in top:
        m = nodes[n]
        print("  %4d <- %s (%s, %d loc)" % (c, n, m["domain"], m["loc"]))
    print("\nDead-end candidate packages (dispatch suppressed):")
    for d in dead:
        print("  %s (%s)" % (d, nodes[d]["domain"]))
    if not dead:
        print("  (none)")
    print("\nwrote %s" % OUT)


if __name__ == "__main__":
    main()
