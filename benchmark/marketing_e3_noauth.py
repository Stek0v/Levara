#!/usr/bin/env python3
"""Marketing evidence campaign, phase A — E3: no-auth document matrix.

Personal preset as shipped: SQLite metadata DB, -require-auth=false, fixed
binary (F1 no-auth publication fix). Mirrors the E2 (authenticated bracket)
methodology: per-cycle upload -> cognify(datasets[]) -> control-fact search,
client-observed latencies, per-IP rate-limit pacing.

Prerequisites (verify before running):
  - embedding sidecar on 127.0.0.1:9101 (GET /health answers model info)
  - isolated server on :18099, e.g.:
      DB_PROVIDER=sqlite EMBEDDING_MODEL=potion-code-16M levarа-server \\
        -profile standalone-embed -port 18099 \\
        -embed-endpoint http://127.0.0.1:9101/v1/embeddings -dim 256 \\
        -data-dir <fresh tmp dir>
  - pandoc (docx generation) and cupsfilter (pdf generation) on PATH

Usage: python3 benchmark/marketing_e3_noauth.py <out.json>
Output schema: benchmark/results/marketing_e3_noauth_docs_latest.json
"""

from __future__ import annotations

import json
import random
import subprocess
import sys
import time
import urllib.error
import urllib.request
import uuid
from pathlib import Path

BASE = "http://127.0.0.1:18099"
OUT = Path(sys.argv[1] if len(sys.argv) > 1 else "/tmp/marketing_e3_noauth.json")
WORK = Path("/tmp/e3_docs")
RATE_PER_MIN = 75  # server per-IP bucket is 100/min; stay under like E2

random.seed(20260916)
WORK.mkdir(parents=True, exist_ok=True)

SENTENCES = [
    "The ingestion pipeline normalizes uploaded documents before extraction.",
    "Chunk boundaries follow semantic paragraphs rather than fixed windows.",
    "Every chunk carries provenance binding it to the source revision.",
    "Vector collections are scoped per dataset to keep tenants isolated.",
    "The lexical index is rebuilt incrementally as chunks are appended.",
    "Publication is the final stage that makes derivatives searchable.",
    "Source version binding prevents stale content from being indexed twice.",
    "Embedding batches are sized to balance latency and throughput.",
    "Search merges vector recall with lexical scoring before rerank.",
    "The knowledge graph extracts entities only when graph mode is enabled.",
    "Retention policies operate on datasets, never on individual chunks.",
    "Background indexing drains through a transactional outbox.",
    "Uploads stream to local storage and register metadata atomically.",
    "Rate limits are enforced per identity with an anonymous IP fallback.",
    "Cognify runs are observable through polling and server-sent events.",
]

_req_times: list[float] = []


def pace() -> None:
    while True:
        now = time.time()
        while _req_times and now - _req_times[0] > 60.0:
            _req_times.pop(0)
        if len(_req_times) < RATE_PER_MIN:
            _req_times.append(now)
            return
        time.sleep(max(0.0, 60.0 - (now - _req_times[0])) + 0.05)


def http(method: str, path: str, body: bytes | None = None, headers: dict | None = None, timeout: int = 120):
    pace()
    req = urllib.request.Request(BASE + path, data=body, method=method)
    for k, v in (headers or {}).items():
        req.add_header(k, v)
    t0 = time.perf_counter()
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            return resp.status, resp.read(), (time.perf_counter() - t0) * 1000.0
    except urllib.error.HTTPError as e:
        return e.code, e.read(), (time.perf_counter() - t0) * 1000.0


def multipart(file_field: str, filename: str, payload: bytes, fields: dict) -> tuple[bytes, str]:
    bnd = "----e3" + uuid.uuid4().hex
    parts = []
    for k, v in fields.items():
        parts.append(f"--{bnd}\r\nContent-Disposition: form-data; name=\"{k}\"\r\n\r\n{v}\r\n".encode())
    parts.append(
        f"--{bnd}\r\nContent-Disposition: form-data; name=\"{file_field}\"; filename=\"{filename}\"\r\n"
        f"Content-Type: application/octet-stream\r\n\r\n".encode() + payload + b"\r\n"
    )
    parts.append(f"--{bnd}--\r\n".encode())
    return b"".join(parts), "multipart/form-data; boundary=" + bnd


def prose(target: int, fact: str) -> tuple[str, int]:
    paras, total = [], 0
    first = f"Overview. The control fact for this document is {fact}. " + " ".join(random.sample(SENTENCES, 4))
    paras.append(first)
    total += len(first)
    while total < target:
        n = random.randint(4, 7)
        p = " ".join(random.choice(SENTENCES) for _ in range(n))
        paras.append(p)
        total += len(p) + 2
    return "\n\n".join(paras), len(paras)


def gen_doc(kind: str, target: int, tag: str) -> tuple[Path, int]:
    fact = f"ZXQ-{random.getrandbits(24):06X}-{tag[-4:].upper()}"
    stem = WORK / f"{kind}_{target}_{tag}"
    if kind == "md":
        body, paras = prose(target, fact)
        text = f"# Document {tag}\n\n" + body + "\n"
        stem.with_suffix(".md").write_text(text)
        return stem.with_suffix(".md"), paras
    if kind == "html":
        body, paras = prose(target, fact)
        text = "<html><head><title>" + tag + "</title></head><body>\n" + "\n".join(
            f"<p>{p}</p>" for p in body.split("\n\n")) + "\n</body></html>\n"
        stem.with_suffix(".html").write_text(text)
        return stem.with_suffix(".html"), paras
    if kind == "csv":
        rows, total = ["id,section,payload"], 0
        head = f"0,control,The control fact for this document is {fact}."
        rows.append(head)
        total += len(head)
        i = 1
        while total < target:
            payload = " ".join(random.choice(SENTENCES) for _ in range(3))
            row = f"{i},s{i},{payload}"
            rows.append(row)
            total += len(row)
            i += 1
        text = "\n".join(rows) + "\n"
        stem.with_suffix(".csv").write_text(text)
        return stem.with_suffix(".csv"), i - 1
    if kind == "docx":
        body, paras = prose(int(target * 0.92), fact)
        md = stem.with_suffix(".md")
        md.write_text(f"# Document {tag}\n\n" + body + "\n")
        out = stem.with_suffix(".docx")
        subprocess.run(["pandoc", str(md), "-o", str(out)], check=True, capture_output=True)
        return out, paras
    if kind == "pdf":
        body, paras = prose(int(target * 0.92), fact)
        txt = stem.with_suffix(".txt")
        txt.write_text(f"Document {tag}\n\n" + body + "\n")
        out = stem.with_suffix(".pdf")
        with open(out, "wb") as fh:
            subprocess.run(["cupsfilter", str(txt)], check=True, stdout=fh, stderr=subprocess.DEVNULL)
        return out, paras
    raise ValueError(kind)


def cycle(kind: str, target: int, rep: int) -> dict:
    tag = uuid.uuid4().hex[:8]
    coll = f"perfe3-{tag}"
    path, paras = gen_doc(kind, target, tag)
    payload = path.read_bytes()
    fact = None
    m = path.read_text(errors="ignore") if kind in ("md", "html", "csv") else path.with_suffix(".md").read_text(errors="ignore") if kind == "docx" else path.with_suffix(".txt").read_text(errors="ignore")
    for word in m.split():
        if word.startswith("ZXQ-"):
            fact = word.rstrip(".,")
            break
    row = {"kind": kind, "size_target": target, "file_bytes": len(payload), "paragraphs": paras}
    t0 = time.perf_counter()
    body, ctype = multipart("data", path.name, payload, {"datasetName": coll})
    st, resp, _ = http("POST", "/api/v1/add", body, {"Content-Type": ctype}, timeout=180)
    row["upload_ms"] = round((time.perf_counter() - t0) * 1000.0, 1)
    row["upload_status"] = st
    if st != 200:
        row["cognify_status"] = "SKIPPED"
        row["provenance_ok"] = False
        return row
    dataset_id = json.loads(resp)["dataset_id"]
    tc = time.perf_counter()
    st, resp, _ = http("POST", "/api/v1/cognify", json.dumps({"datasets": [dataset_id], "mode": "rag", "collection": coll}).encode(), {"Content-Type": "application/json"})
    if st != 200:
        row["cognify_status"] = f"START_{st}"
        row["provenance_ok"] = False
        return row
    run_id = json.loads(resp)["pipeline_run_id"]
    status = "RUNNING"
    while True:
        st, resp, _ = http("GET", f"/api/v1/cognify/{run_id}/status")
        try:
            status = json.loads(resp).get("status", "?")
        except Exception:
            status = "?"
        if status in ("COMPLETED", "FAILED", "ERROR"):
            break
        if time.perf_counter() - tc > 300:
            status = "TIMEOUT"
            break
        time.sleep(1.0)
    row["cognify_s"] = round(time.perf_counter() - tc, 1)
    row["cognify_status"] = status
    found, prov = False, {}
    for attempt in range(4):
        st, resp, _ = http("POST", "/api/v1/search/text", json.dumps({"query_text": fact, "query_type": "CHUNKS", "top_k": 3, "collection": coll}).encode(), {"Content-Type": "application/json"})
        try:
            hits = json.loads(resp)
        except Exception:
            hits = []
        if isinstance(hits, list):
            for h in hits:
                text = (h.get("metadata", {}) or {}).get("text", "") or h.get("text", "")
                if fact in text:
                    found = True
                    meta = h.get("metadata", {}) or {}
                    prov = {"collection": meta.get("collection"), "dataset_id": meta.get("dataset_id"),
                            "document_id": meta.get("document_id"), "generation": meta.get("generation"),
                            "content_revision": meta.get("content_revision")}
                    break
        if found:
            break
        time.sleep(1.0)
    row["fact_found_after_s"] = round(time.perf_counter() - t0, 1)
    row["fact_found"] = found
    row["provenance_ok"] = bool(found and prov.get("dataset_id") == dataset_id and prov.get("document_id") and prov.get("generation") and prov.get("collection") == coll)
    row["provenance_fields"] = prov
    return row


def main() -> int:
    matrix = []
    for kind in ("md", "html", "csv", "pdf", "docx"):
        for size in (10_000, 100_000):
            for rep in range(3):
                matrix.append((kind, size, rep))
    matrix += [("md", 1_000_000, 0), ("pdf", 1_000_000, 0), ("md", 5_000_000, 0)]
    st, resp, _ = http("GET", "/health")
    if st != 200:
        print("server not healthy", st)
        return 1
    rows = []
    for i, (kind, size, rep) in enumerate(matrix, 1):
        row = cycle(kind, size, rep)
        rows.append(row)
        print(f"[{i:02d}/33] {kind} {size} rep{rep}: upload={row['upload_status']} {row.get('upload_ms')}ms "
              f"cognify={row.get('cognify_status')} {row.get('cognify_s')}s found={row.get('fact_found')} "
              f"prov={row.get('provenance_ok')}", flush=True)
    doc = {
        "meta": {
            "phase": "A/E3 (documents, no-auth personal preset)",
            "date": time.strftime("%Y-%m-%dT%H:%M:%S%z"),
            "config": {
                "db": "SQLite (DB_PROVIDER=sqlite)",
                "auth": "disabled (-require-auth=false, anonymous per-IP bucket)",
                "embed": "potion-code-16M dim=256 sidecar :9101",
                "transport": "loopback HTTP, client-observed latency",
                "note": "no-auth bracket of finding F1 after fix; document matrix T2.1-T2.3 methodology; client pacing 75 req/min",
            },
            "matrix": "10KB/100KB x md/html/csv/pdf/docx x3 + 1MB x md/pdf + 5MB x md",
        },
        "t3_matrix": rows,
    }
    OUT.write_text(json.dumps(doc, indent=1))
    ok = sum(1 for r in rows if r.get("cognify_status") == "COMPLETED" and r.get("provenance_ok"))
    print(f"done: {ok}/{len(rows)} completed with valid provenance -> {OUT}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
