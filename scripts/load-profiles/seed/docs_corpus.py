"""Technical-docs corpus seeder for P2 (ambiguous queries).

Pulls a fixed set of Kubernetes and PostgreSQL doc pages whose
content overlaps semantically — multiple pages describe related
concepts in different framings (pods/replicasets/deployments,
WAL/checkpoint/recovery, transaction isolation across multiple
docs). That overlap is what makes the corpus useful for
calibration: every ambiguous query has several plausible right
answers, which is exactly the regime where rerank earns its keep.

Mirrors are GitHub raw URLs against pinned commits to keep the
corpus reproducible across runs and across hosts.
"""

from __future__ import annotations

import hashlib
import json
import os
import re
import urllib.request

# (slug, url, expected_min_chars). Pinned to specific commits.
PAGES: list[tuple[str, str, int]] = [
    (
        "k8s_pods",
        "https://raw.githubusercontent.com/kubernetes/website/main/content/en/docs/concepts/workloads/pods/_index.md",
        4_000,
    ),
    (
        "k8s_deployments",
        "https://raw.githubusercontent.com/kubernetes/website/main/content/en/docs/concepts/workloads/controllers/deployment.md",
        15_000,
    ),
    (
        "k8s_replicasets",
        "https://raw.githubusercontent.com/kubernetes/website/main/content/en/docs/concepts/workloads/controllers/replicaset.md",
        6_000,
    ),
    (
        "k8s_services",
        "https://raw.githubusercontent.com/kubernetes/website/main/content/en/docs/concepts/services-networking/service.md",
        20_000,
    ),
    (
        "k8s_taints",
        "https://raw.githubusercontent.com/kubernetes/website/main/content/en/docs/concepts/scheduling-eviction/taint-and-toleration.md",
        8_000,
    ),
    (
        "k8s_scheduler",
        "https://raw.githubusercontent.com/kubernetes/website/main/content/en/docs/concepts/scheduling-eviction/kube-scheduler.md",
        4_000,
    ),
    (
        "k8s_configmap",
        "https://raw.githubusercontent.com/kubernetes/website/main/content/en/docs/concepts/configuration/configmap.md",
        6_000,
    ),
    (
        "k8s_secrets",
        "https://raw.githubusercontent.com/kubernetes/website/main/content/en/docs/concepts/configuration/secret.md",
        10_000,
    ),
    (
        "pg_mvcc",
        "https://raw.githubusercontent.com/postgres/postgres/master/doc/src/sgml/mvcc.sgml",
        8_000,
    ),
    (
        "pg_wal",
        "https://raw.githubusercontent.com/postgres/postgres/master/doc/src/sgml/wal.sgml",
        8_000,
    ),
    (
        "pg_backup",
        "https://raw.githubusercontent.com/postgres/postgres/master/doc/src/sgml/backup.sgml",
        20_000,
    ),
    (
        "pg_isolation",
        "https://raw.githubusercontent.com/postgres/postgres/master/doc/src/sgml/ref/set_transaction.sgml",
        2_000,
    ),
]

# Strip markdown front-matter, HTML/SGML tags, and Hugo shortcodes —
# none of those help retrieval and they would distort BM25.
FRONT_MATTER = re.compile(r"^---\s*\n.*?\n---\s*\n", re.DOTALL)
HUGO_SHORTCODE = re.compile(r"{{[<%].*?[%>]}}", re.DOTALL)
TAG_RE = re.compile(r"<[^>]+>")
WS_RE = re.compile(r"[ \t]+")
HEADING_SPLIT = re.compile(r"\n(?=#{1,3}\s)|\n(?=<sect[12])")


def cache_dir() -> str:
    d = os.environ.get(
        "LOADPROFILE_CACHE",
        os.path.expanduser("~/.cache/levara-load-profiles/docs"),
    )
    os.makedirs(d, exist_ok=True)
    return d


def _download(slug: str, url: str, min_chars: int) -> str:
    path = os.path.join(cache_dir(), f"{slug}.txt")
    if os.path.exists(path) and os.path.getsize(path) >= min_chars:
        return path
    req = urllib.request.Request(url, headers={"User-Agent": "levara-loadprofile/1.0"})
    with urllib.request.urlopen(req, timeout=60) as resp:
        text = resp.read().decode("utf-8", "replace")
    if len(text) < min_chars:
        raise RuntimeError(
            f"docs page {slug} short download: {len(text)} < {min_chars}"
        )
    with open(path, "w", encoding="utf-8") as f:
        f.write(text)
    return path


def _clean(raw: str) -> str:
    text = FRONT_MATTER.sub("", raw)
    text = HUGO_SHORTCODE.sub("", text)
    text = TAG_RE.sub("", text)
    text = WS_RE.sub(" ", text)
    return text


def _chunks_from_page(path: str, slug: str) -> list[dict[str, str]]:
    with open(path, encoding="utf-8") as f:
        raw = f.read()
    text = _clean(raw)
    parts = [p.strip() for p in HEADING_SPLIT.split(text) if p.strip()]
    out: list[dict[str, str]] = []
    for idx, part in enumerate(parts):
        if len(part) < 120:
            continue  # skip stub sections — they only add noise
        while part:
            chunk = part[:1400]
            part = part[1400:]
            if len(chunk) < 120:
                break
            out.append(
                {
                    "id": f"docs:{slug}:{idx:03d}:{len(out):03d}",
                    "text": chunk,
                    "metadata": json.dumps(
                        {"source": "docs", "page": slug, "section_idx": idx}
                    ),
                }
            )
    return out


def load_corpus(max_chunks_per_page: int = 80) -> list[dict[str, str]]:
    out: list[dict[str, str]] = []
    for slug, url, min_chars in PAGES:
        path = _download(slug, url, min_chars)
        ch = _chunks_from_page(path, slug)
        out.extend(ch[:max_chunks_per_page])
    return out


def corpus_fingerprint() -> str:
    h = hashlib.sha256()
    for slug, url, min_chars in PAGES:
        h.update(f"{slug}:{url}:{min_chars}\n".encode())
    return h.hexdigest()[:12]


# Ambiguous-heavy query set. Each query has >=2 plausible target
# pages; the rerank pass is what should disambiguate. Also keeps a
# few sharp factual queries and out-of-corpus tails so the gap
# distribution covers all regimes.
QUERIES: list[dict[str, str]] = [
    # ambiguous — overlap across pages
    {"id": "q01", "text": "how do I control where my workload is scheduled", "kind": "ambiguous"},
    {"id": "q02", "text": "how do I run multiple replicas of a workload", "kind": "ambiguous"},
    {"id": "q03", "text": "how do I expose my workload over the network", "kind": "ambiguous"},
    {"id": "q04", "text": "how do I pass configuration into a workload", "kind": "ambiguous"},
    {"id": "q05", "text": "what guarantees does the database give about concurrent transactions", "kind": "ambiguous"},
    {"id": "q06", "text": "how does postgres recover after a crash", "kind": "ambiguous"},
    {"id": "q07", "text": "how do I take a consistent backup of postgres", "kind": "ambiguous"},
    {"id": "q08", "text": "what happens when two writers update the same row", "kind": "ambiguous"},
    # factual / sharp
    {"id": "q09", "text": "what is a NodeAffinity rule", "kind": "factual"},
    {"id": "q10", "text": "kubectl rollout status deployment", "kind": "factual"},
    {"id": "q11", "text": "headless service ClusterIP None", "kind": "factual"},
    {"id": "q12", "text": "default repeatable read isolation level", "kind": "factual"},
    {"id": "q13", "text": "what does a checkpoint do in postgres", "kind": "factual"},
    {"id": "q14", "text": "Secret type kubernetes.io/dockerconfigjson", "kind": "factual"},
    # paraphrase / vocabulary mismatch
    {"id": "q15", "text": "make sure two pods don't land on the same machine", "kind": "paraphrase"},
    {"id": "q16", "text": "keep N copies of my app running at all times", "kind": "paraphrase"},
    {"id": "q17", "text": "let my service be reached from outside the cluster", "kind": "paraphrase"},
    {"id": "q18", "text": "make the database see a snapshot of when my transaction started", "kind": "paraphrase"},
    {"id": "q19", "text": "what file does postgres write before it touches the data files", "kind": "paraphrase"},
    {"id": "q20", "text": "store a string of credentials so my pod can read it", "kind": "paraphrase"},
    # out-of-corpus
    {"id": "q21", "text": "victorian poetry meter analysis", "kind": "ooc"},
    {"id": "q22", "text": "guitar chord voicing technique", "kind": "ooc"},
    {"id": "q23", "text": "lasagne recipe with bechamel", "kind": "ooc"},
    {"id": "q24", "text": "tide tables for the english channel", "kind": "ooc"},
]
