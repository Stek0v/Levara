#!/usr/bin/env python3
"""One-command ingestion for an existing project into Levara.

The script intentionally stays dependency-free. It can:

1. scan an existing repository/project while excluding generated/heavy folders;
2. create or extend a local AGENTS.md with a Levara memory contract;
3. submit the corpus to /api/v1/cognify for classic collection search;
4. optionally index the same corpus into the workspace plane for
   workspace_search/workspace_read flows.

Default target is the local launchd instance used on this Mac:
http://127.0.0.1:8081/api/v1. Override with --url or LEVARA_URL.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import sys
import time
import urllib.error
import urllib.request
from dataclasses import dataclass
from datetime import datetime
from pathlib import Path
from typing import Any


DEFAULT_EXCLUDE_DIRS = {
    ".git",
    ".hg",
    ".svn",
    ".evidence",
    ".venv",
    "venv",
    "env",
    ".env",
    "node_modules",
    "vendor",
    "dist",
    "build",
    "out",
    "output",
    "target",
    "tmp",
    "temp",
    "__pycache__",
    ".pytest_cache",
    ".mypy_cache",
    ".ruff_cache",
    ".next",
    ".nuxt",
    ".turbo",
    ".cache",
    "coverage",
}

DEFAULT_SUFFIXES = {
    ".c",
    ".cc",
    ".cfg",
    ".conf",
    ".cpp",
    ".cs",
    ".css",
    ".go",
    ".graphql",
    ".h",
    ".hpp",
    ".html",
    ".java",
    ".js",
    ".json",
    ".jsx",
    ".kt",
    ".lua",
    ".md",
    ".mdx",
    ".php",
    ".proto",
    ".py",
    ".rb",
    ".rs",
    ".scss",
    ".sh",
    ".sql",
    ".svelte",
    ".swift",
    ".toml",
    ".ts",
    ".tsx",
    ".txt",
    ".vue",
    ".xml",
    ".yaml",
    ".yml",
}


@dataclass
class CorpusFile:
    rel: str
    path: Path
    text: str


def slugify(value: str) -> str:
    value = value.strip().lower()
    value = re.sub(r"[^a-z0-9а-яё._-]+", "-", value, flags=re.IGNORECASE)
    value = re.sub(r"-+", "-", value).strip("-._")
    return value or "project"


def repo_or_dir_name(root: Path) -> str:
    try:
        git_head = root / ".git" / "HEAD"
        if git_head.exists():
            return root.name
    except OSError:
        pass
    return root.name


def detect_rooms(files: list[CorpusFile], fallback: str) -> list[str]:
    top: dict[str, int] = {}
    for item in files:
        first = item.rel.split("/", 1)[0]
        if first and first != item.rel:
            top[first] = top.get(first, 0) + 1
    rooms = [slugify(name) for name, _ in sorted(top.items(), key=lambda kv: (-kv[1], kv[0]))[:8]]
    return rooms or [fallback]


def collect_corpus(root: Path, suffixes: set[str], exclude_dirs: set[str], max_file_bytes: int) -> list[CorpusFile]:
    files: list[CorpusFile] = []
    for path in sorted(root.rglob("*")):
        if not path.is_file():
            continue
        try:
            rel_parts = path.relative_to(root).parts
        except ValueError:
            continue
        if any(part in exclude_dirs for part in rel_parts):
            continue
        if path.suffix.lower() not in suffixes:
            continue
        try:
            if path.stat().st_size > max_file_bytes:
                continue
            text = path.read_text(encoding="utf-8", errors="replace")
        except OSError:
            continue
        if text.strip():
            files.append(CorpusFile(rel=str(path.relative_to(root)), path=path, text=text))
    return files


def agents_contract(collection: str, default_room: str, rooms: list[str]) -> str:
    room_lines = "\n".join(f"  - `{room}` — project subsystem/topic" for room in rooms)
    return f"""## Levara memory

- Collection: `{collection}`
- Default room: `{default_room}`
- Room taxonomy:
{room_lines}
- Hall vocabulary:
  - `fact` — stable objective project/service property
  - `event` — dated milestone or external event; always use absolute dates
  - `decision` — choice plus reason/tradeoff
  - `preference` — durable user/team preference
  - `advice` — reusable recommendation
  - `discovery` — root cause, gotcha, validation result, or non-obvious finding
- Pin policy:
  - priority 10: global user/team preference
  - priority 8: critical infra or launch/runtime fact
  - priority 5: active major project decision
- Do not save: code snippets, file paths, git history, temporary TODO state,
  speculation, or anything likely to go stale after refactors.
"""


def ensure_agents(root: Path, collection: str, default_room: str, rooms: list[str], dry_run: bool) -> str:
    path = root / "AGENTS.md"
    block = agents_contract(collection, default_room, rooms)
    if path.exists():
        current = path.read_text(encoding="utf-8", errors="replace")
        if "Levara memory" in current and "Collection:" in current and "Room taxonomy:" in current:
            return "exists"
        new_text = current.rstrip() + "\n\n" + block
        status = "appended"
    else:
        new_text = "# AGENTS.md\n\n" + block
        status = "created"
    if not dry_run:
        path.write_text(new_text, encoding="utf-8")
    return status + ("_dry_run" if dry_run else "")


def request_json(method: str, url: str, payload: dict[str, Any] | None, token: str, timeout: float) -> dict[str, Any]:
    data = json.dumps(payload).encode() if payload is not None else None
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = "Bearer " + token
    req = urllib.request.Request(url, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            body = resp.read()
            if resp.status >= 400:
                raise RuntimeError(f"{method} {url} failed with HTTP {resp.status}: {body[:1000]!r}")
            return json.loads(body.decode() or "{}")
    except urllib.error.HTTPError as e:
        body = e.read().decode(errors="replace")
        raise RuntimeError(f"{method} {url} failed with HTTP {e.code}: {body[:1000]}") from e


def poll_cognify(base_url: str, run_id: str, token: str, timeout_s: int) -> dict[str, Any]:
    deadline = time.time() + timeout_s
    last: dict[str, Any] = {}
    while time.time() < deadline:
        last = request_json("GET", f"{base_url}/cognify/{run_id}/status", None, token, 30)
        status = str(last.get("status") or last.get("state") or "").upper()
        stage = last.get("stage") or ""
        print(f"[cognify] status={status or 'UNKNOWN'} stage={stage}", file=sys.stderr)
        if status in {"COMPLETED", "FAILED", "CANCELED", "CANCELLED"}:
            return last
        time.sleep(2)
    raise TimeoutError(f"cognify run {run_id} did not finish in {timeout_s}s; last={last!r}")


def submit_cognify(base_url: str, token: str, collection: str, files: list[CorpusFile], mode: str, timeout_s: int) -> dict[str, Any]:
    texts = [f"FILE: {item.rel}\nPROJECT: {collection}\nCONTENT:\n{item.text}" for item in files]
    payload: dict[str, Any] = {"collection": collection, "texts": texts, "mode": mode}
    if mode == "rag":
        payload["skip_graph"] = True
    started = time.perf_counter()
    body = request_json("POST", f"{base_url}/cognify", payload, token, 120)
    run_id = body.get("pipeline_run_id") or body.get("run_id")
    if not run_id:
        return {"submitted": body, "status": "unknown", "elapsed_ms": round((time.perf_counter() - started) * 1000, 3)}
    final = poll_cognify(base_url, str(run_id), token, timeout_s)
    final["run_id"] = run_id
    final["elapsed_ms_total"] = round((time.perf_counter() - started) * 1000, 3)
    return final


def workspace_index(
    base_url: str,
    token: str,
    project_id: str,
    branch: str,
    generation: str,
    collection: str,
    files: list[CorpusFile],
    chunk_strategy: str,
) -> dict[str, Any]:
    started = time.perf_counter()
    failures: list[dict[str, Any]] = []
    chunks = 0
    for idx, item in enumerate(files, start=1):
        payload = {
            "project_id": project_id,
            "branch": branch,
            "generation": generation,
            "collection": collection,
            "path": item.rel,
            "text": item.text,
            "file_digest": hashlib.sha256(item.text.encode()).hexdigest(),
            "document_id": item.rel,
            "title": item.rel,
            "room": project_id,
            "tags": ["project-ingest", item.path.suffix.lower().lstrip(".")],
            "chunk_strategy": chunk_strategy,
            "min_chunk_chars": 80,
            "max_chunk_chars": 1200,
            "activate_generation": True,
        }
        try:
            body = request_json("POST", f"{base_url}/workspace/index", payload, token, 120)
            result = ((body.get("result") or {}).get("result") or body.get("result") or {})
            chunks += int(result.get("chunks_created") or result.get("ChunksCreated") or 0)
        except Exception as exc:  # keep indexing other files
            failures.append({"path": item.rel, "error": str(exc)[:1000]})
        if idx % 50 == 0:
            print(f"[workspace] indexed {idx}/{len(files)} files", file=sys.stderr)
    return {
        "project_id": project_id,
        "branch": branch,
        "generation": generation,
        "collection": collection,
        "files_attempted": len(files),
        "files_failed": len(failures),
        "chunks_created_observed": chunks,
        "failures": failures[:25],
        "elapsed_ms": round((time.perf_counter() - started) * 1000, 3),
    }


def main() -> int:
    parser = argparse.ArgumentParser(description="Ingest an existing project into Levara in one command.")
    parser.add_argument("project", help="Existing project/repository path.")
    parser.add_argument("--url", default=os.environ.get("LEVARA_URL", "http://127.0.0.1:8081/api/v1"))
    parser.add_argument("--token", default=os.environ.get("LEVARA_TOKEN", ""))
    parser.add_argument("--collection", default="")
    parser.add_argument("--project-id", default="")
    parser.add_argument("--branch", default="main")
    parser.add_argument("--generation", default="")
    parser.add_argument(
        "--mode",
        choices=["rag", "full", "graph", "auto"],
        default="rag",
        help="cognify mode. Default rag is fast project search; full enables heavier graph/LLM extraction.",
    )
    parser.add_argument("--pipeline", choices=["classic", "workspace", "all"], default="all")
    parser.add_argument("--chunk-strategy", choices=["merged", "paragraph", "sentence", "sliding"], default="merged")
    parser.add_argument("--max-file-bytes", type=int, default=200_000)
    parser.add_argument("--limit", type=int, default=0, help="Limit collected files for smoke tests; 0 means no limit.")
    parser.add_argument("--include", default=",".join(sorted(DEFAULT_SUFFIXES)), help="Comma-separated file suffixes.")
    parser.add_argument("--exclude-dir", action="append", default=[], help="Extra directory name to exclude; can be repeated.")
    parser.add_argument("--no-agents", action="store_true", help="Do not create/append AGENTS.md memory contract.")
    parser.add_argument("--dry-run", action="store_true")
    parser.add_argument("--timeout-seconds", type=int, default=1800)
    parser.add_argument("--output", default="")
    args = parser.parse_args()

    root = Path(args.project).expanduser().resolve()
    if not root.exists() or not root.is_dir():
        raise SystemExit(f"project directory not found: {root}")

    collection = slugify(args.collection or repo_or_dir_name(root))
    project_id = slugify(args.project_id or collection)
    generation = slugify(args.generation or ("ingest-" + datetime.now().strftime("%Y%m%d-%H%M%S")))
    workspace_collection = f"kb_{project_id}_{slugify(args.branch)}_{generation}"
    suffixes = {s if s.startswith(".") else "." + s for s in args.include.split(",") if s.strip()}
    exclude_dirs = set(DEFAULT_EXCLUDE_DIRS) | set(args.exclude_dir)
    corpus = collect_corpus(root, suffixes, exclude_dirs, args.max_file_bytes)
    if args.limit > 0:
        corpus = corpus[: args.limit]
    rooms = detect_rooms(corpus, project_id)

    report: dict[str, Any] = {
        "project": str(root),
        "url": args.url,
        "collection": collection,
        "project_id": project_id,
        "branch": args.branch,
        "generation": generation,
        "pipeline": args.pipeline,
        "mode": args.mode,
        "corpus": {
            "files": len(corpus),
            "bytes": sum(len(item.text.encode()) for item in corpus),
            "suffixes": sorted(suffixes),
            "excluded_dirs": sorted(exclude_dirs),
            "max_file_bytes": args.max_file_bytes,
            "limit": args.limit,
        },
        "agents_md": "skipped",
        "classic": {"status": "not_run"},
        "workspace": {"status": "not_run"},
    }

    if not corpus:
        raise SystemExit("no files collected; adjust --include/--exclude-dir/--max-file-bytes")

    if not args.no_agents:
        report["agents_md"] = ensure_agents(root, collection, project_id, rooms, args.dry_run)

    if args.dry_run:
        report["sample_files"] = [item.rel for item in corpus[:30]]
        if args.output:
            out = Path(args.output).expanduser()
            out.parent.mkdir(parents=True, exist_ok=True)
            out.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        print(json.dumps(report, ensure_ascii=False, indent=2))
        return 0

    if args.pipeline in {"classic", "all"}:
        try:
            report["classic"] = submit_cognify(args.url.rstrip("/"), args.token, collection, corpus, args.mode, args.timeout_seconds)
        except Exception as exc:
            report["classic"] = {"status": "error", "error": str(exc)}

    if args.pipeline in {"workspace", "all"}:
        try:
            report["workspace"] = workspace_index(
                args.url.rstrip("/"),
                args.token,
                project_id,
                args.branch,
                generation,
                workspace_collection,
                corpus,
                args.chunk_strategy,
            )
        except Exception as exc:
            report["workspace"] = {"status": "error", "error": str(exc)}

    if args.output:
        out = Path(args.output).expanduser()
        out.parent.mkdir(parents=True, exist_ok=True)
        out.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")

    print(json.dumps(report, ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
