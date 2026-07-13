#!/usr/bin/env python3
"""Compare Levara project-assistance modes on the local WB workspace.

Modes:
  filesystem  - local lexical baseline over project files.
  memory      - current Levara memory profile, recall_memory only.
  search_*    - Levara project collection search after RAG-mode cognify.
  workspace   - MCP workspace_index/workspace_search when explicitly enabled.

The script is intentionally read-only for the target project. It may create a
new Levara collection when --index is passed.
"""

from __future__ import annotations

import argparse
import json
import math
import os
import re
import subprocess
import time
import urllib.error
import urllib.request
from dataclasses import dataclass
from pathlib import Path
from typing import Any


EXCLUDE_DIRS = {
    ".git",
    ".evidence",
    ".venv",
    "node_modules",
    "dist",
    "build",
    "output",
    "tmp",
    "__pycache__",
    ".pytest_cache",
}
INCLUDE_SUFFIXES = {".md", ".ts", ".py", ".sql", ".yaml", ".yml", ".json", ".toml"}
MAX_FILE_BYTES = 120_000


GOLDEN = [
    {
        "id": "token_source",
        "query": "Where does wb-mcp-server read WB_API_TOKEN and CLI --token?",
        "expected": ["wb-mcp-server/src/config.ts", "wb-mcp-server/src/index.ts", "wb-mcp-server/README.md"],
        "slice": "mcp-auth",
    },
    {
        "id": "authorization_header",
        "query": "Where is the Wildberries Authorization header set without Bearer?",
        "expected": ["wb-mcp-server/src/wb-client.ts", "wb-mcp-server/CLAUDE.md"],
        "slice": "mcp-auth",
    },
    {
        "id": "write_tools",
        "query": "Which WB MCP tools can mutate real seller data?",
        "expected": ["wb-mcp-server/README.md", "wb-mcp-server/README.ru.md", "wb-mcp-server/docs/wb-api-read-coverage.md"],
        "slice": "safety",
    },
    {
        "id": "readonly_profile",
        "query": "Why does the WB MCP need a read-only profile for analytics?",
        "expected": ["wb-mcp-server/docs/wb-api-read-coverage.md"],
        "slice": "safety",
    },
    {
        "id": "account_limits",
        "query": "Why should equivalent tokens for the same seller not be alternated?",
        "expected": ["wb-analytics-platform/README.md", "wb-analytics-platform/docs/ingestion-control.md"],
        "slice": "ingestion-control",
    },
    {
        "id": "rate_limit_429",
        "query": "How does ingestion handle WB HTTP 429 and persistent cooldown?",
        "expected": ["wb-analytics-platform/docs/ingestion-control.md", "wb-analytics-platform/ingestion/wb_analytics/history.py"],
        "slice": "ingestion-control",
    },
    {
        "id": "finance_rrdid",
        "query": "How does finance realization pagination resume with rrdId until HTTP 204?",
        "expected": ["wb-analytics-platform/README.md", "wb-analytics-platform/docs/pnl-data-contract.md", "wb-analytics-platform/ingestion/wb_analytics/collectors.py"],
        "slice": "finance",
    },
    {
        "id": "raw_s3_contract",
        "query": "Where is the contract that raw WB responses are archived before normalization?",
        "expected": ["wb-analytics-platform/README.md", "wb-analytics-platform/docs/mass-ingestion-contract.md", "wb-analytics-platform/ingestion/wb_analytics/storage.py"],
        "slice": "storage",
    },
    {
        "id": "pnl_tables",
        "query": "Which ClickHouse tables and SQL define the daily P&L mart?",
        "expected": ["wb-analytics-platform/README.md", "wb-analytics-platform/docker/clickhouse/init/002_finance_pnl.sql", "wb-analytics-platform/docs/pnl-data-contract.md"],
        "slice": "finance",
    },
    {
        "id": "evidence_reports",
        "query": "Where are Evidence dashboards and ClickHouse report queries defined?",
        "expected": ["wb-analytics-platform/docs/evidence-report-design.md", "wb-analytics-platform/evidence/pages/index.md", "wb-analytics-platform/evidence/sources/clickhouse/kpi_summary.sql"],
        "slice": "bi",
    },
    {
        "id": "mcp_large_exports",
        "query": "Why should MCP not stream full multi-page WB exports through model context?",
        "expected": ["wb-analytics-platform/docs/mass-ingestion-contract.md", "wb-mcp-server/docs/wb-api-read-coverage.md"],
        "slice": "mcp-architecture",
    },
    {
        "id": "async_warehouse_report",
        "query": "Where is the warehouse remains async report implemented and documented?",
        "expected": ["wb-analytics-platform/README.md", "wb-analytics-platform/ingestion/wb_analytics/collectors.py", "wb-mcp-server/src/tools/analytics.ts"],
        "slice": "analytics",
    },
]


def percentile(values: list[float], p: float) -> float:
    if not values:
        return 0.0
    data = sorted(values)
    rank = (len(data) - 1) * p / 100.0
    lo = math.floor(rank)
    hi = math.ceil(rank)
    if lo == hi:
        return data[lo]
    return data[lo] + (data[hi] - data[lo]) * (rank - lo)


def tokenize(text: str) -> list[str]:
    return [t for t in re.findall(r"[A-Za-zА-Яа-я0-9_]+", text.lower()) if len(t) > 2]


@dataclass
class CorpusFile:
    rel: str
    path: Path
    text: str


def collect_corpus(root: Path) -> list[CorpusFile]:
    files: list[CorpusFile] = []
    for path in sorted(root.rglob("*")):
        if not path.is_file():
            continue
        rel_parts = path.relative_to(root).parts
        if any(part in EXCLUDE_DIRS for part in rel_parts):
            continue
        if path.suffix not in INCLUDE_SUFFIXES:
            continue
        try:
            if path.stat().st_size > MAX_FILE_BYTES:
                continue
            text = path.read_text(encoding="utf-8", errors="replace")
        except OSError:
            continue
        rel = str(path.relative_to(root))
        files.append(CorpusFile(rel=rel, path=path, text=text))
    return files


def match_expected(results: list[str], expected: list[str], k: int) -> tuple[bool, int | None]:
    for idx, result in enumerate(results[:k], start=1):
        for exp in expected:
            if exp in result:
                return True, idx
    return False, None


def recall_at(rows: list[dict[str, Any]], k: int) -> float:
    hits = 0
    for r in rows:
        ok, _ = match_expected(r.get("result_refs", []), r["expected"], k)
        if ok:
            hits += 1
    return round(hits / max(len(rows), 1), 4)


def metric_summary(rows: list[dict[str, Any]], k: int = 3) -> dict[str, Any]:
    lat = [r["latency_ms"] for r in rows if r.get("latency_ms") is not None]
    bytes_ = [r.get("context_bytes", 0) for r in rows]
    hits = 0
    rr = 0.0
    for r in rows:
        ok, rank = match_expected(r.get("result_refs", []), r["expected"], k)
        if ok:
            hits += 1
            rr += 1.0 / float(rank or 1)
    n = max(len(rows), 1)
    return {
        "cases": len(rows),
        f"recall@{k}": round(hits / n, 4),
        "recall@5": recall_at(rows, 5),
        "mrr": round(rr / n, 4),
        "latency_ms": {
            "p50": round(percentile(lat, 50), 3),
            "p95": round(percentile(lat, 95), 3),
            "p99": round(percentile(lat, 99), 3),
        },
        "context_bytes_avg": round(sum(bytes_) / max(len(bytes_), 1), 1),
    }


def filesystem_search(corpus: list[CorpusFile], query: str, limit: int) -> tuple[list[str], float, int]:
    start = time.perf_counter()
    terms = tokenize(query)
    scored: list[tuple[int, str]] = []
    for item in corpus:
        low = item.text.lower()
        score = sum(low.count(term) for term in terms)
        if score:
            scored.append((score, item.rel))
    scored.sort(key=lambda x: (-x[0], x[1]))
    refs = [rel for _, rel in scored[:limit]]
    elapsed = (time.perf_counter() - start) * 1000
    return refs, elapsed, sum(len(r) for r in refs)


class MCP:
    def __init__(self, base_url: str):
        self.base_url = base_url.rstrip("/")
        self.mcp_url = self.base_url + "/mcp"
        self.session_id = ""
        self.rpc_id = 0

    def rpc(self, method: str, params: dict[str, Any] | None = None) -> tuple[dict[str, Any], float, int]:
        self.rpc_id += 1
        payload: dict[str, Any] = {"jsonrpc": "2.0", "id": self.rpc_id, "method": method}
        if params is not None:
            payload["params"] = params
        data = json.dumps(payload).encode()
        headers = {"Content-Type": "application/json"}
        if self.session_id:
            headers["Mcp-Session-Id"] = self.session_id
        req = urllib.request.Request(self.mcp_url, data=data, headers=headers, method="POST")
        start = time.perf_counter()
        with urllib.request.urlopen(req, timeout=60) as resp:
            body = resp.read()
            self.session_id = resp.headers.get("Mcp-Session-Id", self.session_id)
        elapsed = (time.perf_counter() - start) * 1000
        return json.loads(body.decode() or "{}"), elapsed, len(body)

    def initialize(self) -> dict[str, Any]:
        body, _, _ = self.rpc(
            "initialize",
            {
                "protocolVersion": "2025-03-26",
                "capabilities": {},
                "clientInfo": {"name": "wb-project-eval", "version": "1"},
            },
        )
        try:
            self.rpc("notifications/initialized", {})
        except Exception:
            pass
        return body

    def call_tool(self, name: str, arguments: dict[str, Any]) -> tuple[dict[str, Any], float, int]:
        return self.rpc("tools/call", {"name": name, "arguments": arguments})


def tool_text(result: dict[str, Any]) -> str:
    body = result.get("result", result)
    content = body.get("content") or []
    if content and isinstance(content[0], dict):
        return str(content[0].get("text", ""))
    return json.dumps(body, ensure_ascii=False)


def extract_refs_from_text(text: str, corpus: list[CorpusFile]) -> list[str]:
    refs: list[str] = []
    for item in corpus:
        if item.rel in text:
            refs.append(item.rel)
    return refs


def mcp_memory_recall(client: MCP, query: str, limit: int, corpus: list[CorpusFile]) -> tuple[list[str], float, int]:
    body, lat, size = client.call_tool("recall_memory", {"query": query, "limit": limit})
    refs = extract_refs_from_text(tool_text(body), corpus)
    return refs, lat, size


def mcp_search(client: MCP, collection: str, query: str, limit: int, corpus: list[CorpusFile], search_type: str) -> tuple[list[str], float, int]:
    body, lat, size = client.call_tool(
        "search",
        {
            "search_query": query,
            "search_type": search_type,
            "collection": collection,
            "top_k": limit,
            "mode": "rag",
            "rerank": False,
        },
    )
    text = tool_text(body)
    refs = extract_refs_from_text(text, corpus)
    if not refs:
        # Fallback for result structures that include metadata paths under
        # different keys but still stringify cleanly.
        refs = extract_refs_from_text(json.dumps(body, ensure_ascii=False), corpus)
    return refs[:limit], lat, size


def mcp_workspace_index(
    client: MCP,
    project_id: str,
    branch: str,
    generation: str,
    collection: str,
    corpus: list[CorpusFile],
) -> dict[str, Any]:
    started = time.perf_counter()
    indexed = 0
    chunks = 0
    failures: list[dict[str, Any]] = []
    for item in corpus:
        body, lat, _ = client.call_tool(
            "workspace_index",
            {
                "project_id": project_id,
                "branch": branch,
                "generation": generation,
                "collection": collection,
                "path": item.rel,
                "text": item.text,
                "document_id": item.rel,
                "title": item.rel,
                "room": "wb",
                "tags": ["wb-eval", item.path.suffix.lstrip(".")],
                "chunk_strategy": "merged",
                "min_chunk_chars": 80,
                "max_chunk_chars": 1200,
                "activate_generation": True,
            },
        )
        result_body = body.get("result") or {}
        if "error" in body or result_body.get("isError"):
            failures.append(
                {
                    "path": item.rel,
                    "latency_ms": round(lat, 3),
                    "error": body.get("error") or tool_text(body)[:1000],
                }
            )
            continue
        indexed += 1
        structured = result_body.get("structuredContent") or {}
        result = structured.get("result") or structured or result_body.get("result") or {}
        try:
            chunks += int(result.get("chunks_created") or result.get("chunks") or 0)
        except (TypeError, ValueError):
            pass
    return {
        "project_id": project_id,
        "branch": branch,
        "generation": generation,
        "collection": collection,
        "files_attempted": len(corpus),
        "files_indexed": indexed,
        "chunks_created_observed": chunks,
        "failures": failures[:20],
        "failure_count": len(failures),
        "elapsed_ms": round((time.perf_counter() - started) * 1000, 3),
    }


def mcp_workspace_search(
    client: MCP,
    project_id: str,
    branch: str,
    generation: str,
    query: str,
    limit: int,
    corpus: list[CorpusFile],
    search_type: str,
) -> tuple[list[str], float, int]:
    body, lat, size = client.call_tool(
        "workspace_search",
        {
            "project_id": project_id,
            "branch": branch,
            "generation": generation,
            "search_query": query,
            "search_type": search_type,
            "top_k": limit,
            "mode": "rag",
            "rerank": False,
        },
    )
    text = tool_text(body)
    refs = extract_refs_from_text(text, corpus)
    if not refs:
        refs = extract_refs_from_text(json.dumps(body, ensure_ascii=False), corpus)
    return refs[:limit], lat, size


def http_json(method: str, url: str, payload: dict[str, Any] | None = None, timeout: float = 60) -> tuple[dict[str, Any], float]:
    data = json.dumps(payload).encode() if payload is not None else None
    headers = {"Content-Type": "application/json"}
    req = urllib.request.Request(url, data=data, headers=headers, method=method)
    start = time.perf_counter()
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        body = resp.read()
    elapsed = (time.perf_counter() - start) * 1000
    return json.loads(body.decode() or "{}"), elapsed


def index_collection(base_url: str, collection: str, corpus: list[CorpusFile]) -> dict[str, Any]:
    texts = [
        f"FILE: {item.rel}\nPROJECT: WB\nCONTENT:\n{item.text}"
        for item in corpus
    ]
    payload = {"collection": collection, "mode": "rag", "skip_graph": True, "texts": texts}
    body, submit_ms = http_json("POST", base_url.rstrip("/") + "/api/v1/cognify", payload, timeout=120)
    run_id = body.get("pipeline_run_id") or body.get("run_id")
    if not run_id:
        return {"submitted": body, "submit_ms": submit_ms, "status": "unknown"}
    status_url = base_url.rstrip("/") + f"/api/v1/cognify/{run_id}/status"
    deadline = time.time() + 360
    last: dict[str, Any] = {}
    while time.time() < deadline:
        try:
            last, _ = http_json("GET", status_url, None, timeout=30)
        except (urllib.error.URLError, TimeoutError):
            time.sleep(1)
            continue
        state = str(last.get("status") or last.get("state") or "").upper()
        if state in {"COMPLETED", "FAILED", "CANCELED", "CANCELLED"}:
            break
        time.sleep(1)
    return {"run_id": run_id, "submit_ms": submit_ms, "final": last}


def run_mode_rows(
    mode: str,
    cases: list[dict[str, Any]],
    corpus: list[CorpusFile],
    func,
) -> list[dict[str, Any]]:
    rows: list[dict[str, Any]] = []
    for case in cases:
        refs, lat, size = func(case["query"])
        ok, rank = match_expected(refs, case["expected"], 3)
        rows.append(
            {
                "mode": mode,
                "id": case["id"],
                "slice": case["slice"],
                "query": case["query"],
                "expected": case["expected"],
                "result_refs": refs,
                "hit@3": ok,
                "rank": rank,
                "latency_ms": round(lat, 3),
                "context_bytes": size,
            }
        )
    return rows


def main() -> int:
    p = argparse.ArgumentParser()
    p.add_argument("--project", default="/Users/stek0v/Documents/WB")
    p.add_argument("--url", default="http://127.0.0.1:8081")
    p.add_argument("--collection", default=f"wb_eval_{int(time.time())}")
    p.add_argument("--index", action="store_true", help="Create a Levara RAG collection for search mode.")
    p.add_argument("--skip-workspace", action="store_true")
    p.add_argument("--run-workspace", action="store_true", help="Index/search through MCP workspace tools when the active profile exposes them.")
    p.add_argument("--workspace-project", default="wb_eval")
    p.add_argument("--workspace-branch", default="main")
    p.add_argument("--workspace-generation", default="")
    p.add_argument("--workspace-collection", default="")
    p.add_argument("--output", default="benchmark/results/wb_project_eval_latest.json")
    args = p.parse_args()

    root = Path(args.project).expanduser().resolve()
    if not root.exists():
        raise SystemExit(f"project not found: {root}")

    corpus = collect_corpus(root)
    client = MCP(args.url)
    init = client.initialize()
    toolset = ((init.get("result") or {}).get("toolset") or {})
    tools_body, _, tools_bytes = client.rpc("tools/list", {})
    active_tools = [t.get("name") for t in (tools_body.get("result") or {}).get("tools", [])]

    report: dict[str, Any] = {
        "project": str(root),
        "collection": args.collection,
        "corpus": {
            "files": len(corpus),
            "bytes": sum(len(c.text.encode("utf-8")) for c in corpus),
            "excluded_dirs": sorted(EXCLUDE_DIRS),
            "suffixes": sorted(INCLUDE_SUFFIXES),
        },
        "mcp": {"toolset": toolset, "tools": len(active_tools), "tools_list_bytes": tools_bytes},
        "golden_cases": GOLDEN,
        "modes": {},
        "rows": [],
    }

    fs_rows = run_mode_rows(
        "filesystem",
        GOLDEN,
        corpus,
        lambda q: filesystem_search(corpus, q, 5),
    )
    report["rows"].extend(fs_rows)
    report["modes"]["filesystem"] = metric_summary(fs_rows)

    mem_rows = run_mode_rows(
        "memory",
        GOLDEN,
        corpus,
        lambda q: mcp_memory_recall(client, q, 5, corpus),
    )
    report["rows"].extend(mem_rows)
    report["modes"]["memory"] = metric_summary(mem_rows)

    if args.index:
        report["index"] = index_collection(args.url, args.collection, corpus)
        for search_type in ("HYBRID", "CHUNKS", "CHUNKS_LEXICAL"):
            mode = "search_" + search_type.lower()
            search_rows = run_mode_rows(
                mode,
                GOLDEN,
                corpus,
                lambda q, st=search_type: mcp_search(client, args.collection, q, 5, corpus, st),
            )
            report["rows"].extend(search_rows)
            report["modes"][mode] = metric_summary(search_rows)
    else:
        report["modes"]["search"] = {"status": "not_run", "reason": "pass --index to create/search a Levara collection"}

    workspace_generation = args.workspace_generation or args.collection + "_ws_gen"
    workspace_collection = args.workspace_collection or args.collection + "_workspace"

    if args.skip_workspace:
        report["modes"]["workspace"] = {"status": "not_run", "reason": "--skip-workspace"}
    elif "workspace_search" not in active_tools:
        report["modes"]["workspace"] = {
            "status": "not_available",
            "reason": "active MCP profile does not expose workspace_search",
            "active_toolset": toolset,
        }
    elif args.run_workspace:
        report["workspace_index"] = mcp_workspace_index(
            client,
            args.workspace_project,
            args.workspace_branch,
            workspace_generation,
            workspace_collection,
            corpus,
        )
        for search_type in ("HYBRID", "CHUNKS_LEXICAL"):
            mode = "workspace_" + search_type.lower()
            workspace_rows = run_mode_rows(
                mode,
                GOLDEN,
                corpus,
                lambda q, st=search_type: mcp_workspace_search(
                    client,
                    args.workspace_project,
                    args.workspace_branch,
                    workspace_generation,
                    q,
                    5,
                    corpus,
                    st,
                ),
            )
            report["rows"].extend(workspace_rows)
            report["modes"][mode] = metric_summary(workspace_rows)
        report["modes"]["workspace"] = {
            "status": "run",
            "project_id": args.workspace_project,
            "branch": args.workspace_branch,
            "generation": workspace_generation,
            "collection": workspace_collection,
        }
    else:
        report["modes"]["workspace"] = {
            "status": "available_not_run",
            "reason": "workspace profile is active; pass --run-workspace to create a test workspace index",
        }

    out = Path(args.output)
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    print(json.dumps({k: report[k] for k in ("project", "collection", "corpus", "mcp", "modes")}, ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
