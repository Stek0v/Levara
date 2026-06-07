"""MTEB SciDocs reranking corpus fixture for Levara rerank tests.

Loads pairs of (query, positive_docs[], negative_docs[]) from the
MTEB `mteb/scidocs-reranking` dataset (cached locally as JSONL) and
exposes helpers used by chaos/soak/perf tests:

  - `load_scidocs`   : parse the JSONL file into raw rows
  - `flatten_docs`   : deduped doc records with stable IDs + labels
  - `make_queries`   : query -> list of relevant_ids (positive docs only)
  - `seed_collection`: ingest docs into a Levara instance via /add + /cognify

Dataset path is overridable via the `MTEB_SCIDOCS_PATH` env var. The file
itself is ~9MB and is intentionally not committed; regenerate with:

    python -c "from datasets import load_dataset; \\
import json; ds=load_dataset('mteb/scidocs-reranking', split='test'); \\
open('deploy/rerank/eval/mteb_scidocs_reranking.jsonl','w').writelines( \\
  json.dumps({'query':r['query'],'positive':r['positive'],'negative':r['negative']})+'\\n' for r in ds)"
"""
from __future__ import annotations

import hashlib
import json
import os
import time
from pathlib import Path
from typing import Iterable, Optional

DEFAULT_PATH = "deploy/rerank/eval/mteb_scidocs_reranking.jsonl"
ENV_VAR = "MTEB_SCIDOCS_PATH"

REGEN_HINT = (
    "Dataset not found. Regenerate from HuggingFace:\n"
    "  python -c \"from datasets import load_dataset; import json; "
    "ds=load_dataset('mteb/scidocs-reranking', split='test'); "
    "open('deploy/rerank/eval/mteb_scidocs_reranking.jsonl','w').writelines("
    "json.dumps({'query':r['query'],'positive':r['positive'],'negative':r['negative']})+'\\n' for r in ds)\""
)


def _resolve_path(path: Optional[str]) -> Path:
    p = os.environ.get(ENV_VAR) or path or DEFAULT_PATH
    return Path(p)


def _doc_id(text: str) -> str:
    return "sci-" + hashlib.sha1(text.encode("utf-8")).hexdigest()[:8]


def load_scidocs(
    path: str = DEFAULT_PATH, limit: Optional[int] = None
) -> list[dict]:
    """Load raw rows from the MTEB SciDocs reranking JSONL file.

    Each row: {"query": str, "positive": [str, ...], "negative": [str, ...]}
    """
    fp = _resolve_path(path)
    if not fp.exists():
        raise FileNotFoundError(f"{fp}: {REGEN_HINT}")

    rows: list[dict] = []
    with fp.open("r", encoding="utf-8") as f:
        for i, line in enumerate(f):
            line = line.strip()
            if not line:
                continue
            obj = json.loads(line)
            rows.append(
                {
                    "query": obj.get("query", ""),
                    "positive": list(obj.get("positive", []) or []),
                    "negative": list(obj.get("negative", []) or []),
                }
            )
            if limit is not None and len(rows) >= limit:
                break
    return rows


def flatten_docs(
    rows: Iterable[dict], max_total: Optional[int] = None
) -> list[dict]:
    """Return deduped doc records across all rows.

    Each record: {"id": "sci-<hash8>", "text": str,
                  "source_query": str, "label": "pos"|"neg"}

    Dedup is by doc text. If the same text appears as positive in one row
    and negative in another, the first occurrence wins (stable order:
    positives are emitted before negatives within each row, and rows are
    consumed in input order).
    """
    seen: dict[str, dict] = {}
    for row in rows:
        q = row.get("query", "")
        for text in row.get("positive", []) or []:
            if not text:
                continue
            did = _doc_id(text)
            if did not in seen:
                seen[did] = {
                    "id": did,
                    "text": text,
                    "source_query": q,
                    "label": "pos",
                }
            if max_total is not None and len(seen) >= max_total:
                return list(seen.values())
        for text in row.get("negative", []) or []:
            if not text:
                continue
            did = _doc_id(text)
            if did not in seen:
                seen[did] = {
                    "id": did,
                    "text": text,
                    "source_query": q,
                    "label": "neg",
                }
            if max_total is not None and len(seen) >= max_total:
                return list(seen.values())
    return list(seen.values())


def make_queries(
    rows: Iterable[dict], n: Optional[int] = None
) -> list[dict]:
    """Return queries with their relevant (positive) doc IDs.

    Each item: {"query": str, "relevant_ids": [sci-..., ...]}

    Rows with empty positives are skipped (nothing to score against).
    """
    out: list[dict] = []
    for row in rows:
        positives = [t for t in (row.get("positive") or []) if t]
        if not positives:
            continue
        out.append(
            {
                "query": row.get("query", ""),
                "relevant_ids": [_doc_id(t) for t in positives],
            }
        )
        if n is not None and len(out) >= n:
            break
    return out


def seed_collection(
    base_url: str,
    collection: str,
    docs: list[dict],
    batch_size: int = 64,
    dataset_name: Optional[str] = None,
    token: Optional[str] = None,
    cognify_timeout_s: float = 600.0,
    poll_interval_s: float = 2.0,
) -> dict:
    """Ingest `docs` into a Levara instance and wait for cognify to finish.

    Uses the canonical /api/v1/add + /api/v1/cognify route pair (verified in
    `internal/http/api.go`). No direct vector-insert HTTP endpoint exists,
    so this is the only correct path regardless of whether an `embed_fn` is
    available.

    Returns: {"added": int, "run_id": str, "status": str}
    """
    import requests  # local import to keep module import-cheap

    base = base_url.rstrip("/")
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = f"Bearer {token}"
    ds_name = dataset_name or f"scidocs-{collection}"

    texts: list[str] = []
    added = 0
    for i in range(0, len(docs), batch_size):
        batch = docs[i : i + batch_size]
        for d in batch:
            payload = {
                "data": d["text"],
                "dataset_name": ds_name,
                "tags": ["scidocs", d.get("label", "")],
            }
            r = requests.post(
                f"{base}/api/v1/add", headers=headers, json=payload, timeout=30
            )
            r.raise_for_status()
            texts.append(d["text"])
            added += 1

    # Trigger cognify on the inline texts directly (bypasses dataset lookup).
    cog = requests.post(
        f"{base}/api/v1/cognify",
        headers=headers,
        json={"texts": texts, "collection": collection},
        timeout=60,
    )
    cog.raise_for_status()
    body = cog.json()
    run_id = body.get("pipeline_run_id") or body.get("run_id") or ""
    status = body.get("status", "")

    if not run_id or status == "already_processed":
        return {"added": added, "run_id": run_id, "status": status or "ok"}

    deadline = time.monotonic() + cognify_timeout_s
    final_status = "unknown"
    while time.monotonic() < deadline:
        sr = requests.get(
            f"{base}/api/v1/cognify/{run_id}/status",
            headers=headers,
            timeout=15,
        )
        if sr.status_code == 200:
            sb = sr.json()
            final_status = sb.get("status", final_status)
            if final_status in ("completed", "failed", "done", "error"):
                break
        time.sleep(poll_interval_s)

    return {"added": added, "run_id": run_id, "status": final_status}
