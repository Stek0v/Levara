"""Mixed code+prose corpus for P3.

Half of the chunks are real source code (Go, Python, JS) and half
are prose describing what that code does. The two halves share
vocabulary but in very different syntactic shapes — that's the
calibration signal we're after: vector embeddings collapse a lot of
code into similar regions, while lexical/cross-encoder rerank can
exploit token-level cues that the bi-encoder misses. The split
also lets the calibration analyzer slice score-gap distributions
by content kind (code vs prose) and see whether the same gate
threshold works for both.

Source: a small set of permissively-licensed open repos pinned to
specific commits. We pull whole files rather than diffs so the
content is stable across runs.
"""

from __future__ import annotations

import hashlib
import json
import os
import re
import urllib.request

# (slug, language, url, expected_min_chars).
SOURCES: list[tuple[str, str, str, int]] = [
    # Go — small handler files, easy to chunk.
    (
        "go_net_http_server",
        "go",
        "https://raw.githubusercontent.com/golang/go/release-branch.go1.22/src/net/http/server.go",
        100_000,
    ),
    (
        "go_sync_mutex",
        "go",
        "https://raw.githubusercontent.com/golang/go/release-branch.go1.22/src/sync/mutex.go",
        5_000,
    ),
    (
        "go_sync_waitgroup",
        "go",
        "https://raw.githubusercontent.com/golang/go/release-branch.go1.22/src/sync/waitgroup.go",
        3_000,
    ),
    # Python stdlib
    (
        "py_asyncio_locks",
        "py",
        "https://raw.githubusercontent.com/python/cpython/v3.12.0/Lib/asyncio/locks.py",
        10_000,
    ),
    (
        "py_threading",
        "py",
        "https://raw.githubusercontent.com/python/cpython/v3.12.0/Lib/threading.py",
        40_000,
    ),
    (
        "py_queue",
        "py",
        "https://raw.githubusercontent.com/python/cpython/v3.12.0/Lib/queue.py",
        8_000,
    ),
    # JS — a real-world utility lib
    (
        "js_lodash_debounce",
        "js",
        "https://raw.githubusercontent.com/lodash/lodash/4.17.21-npm/debounce.js",
        2_000,
    ),
    (
        "js_lodash_throttle",
        "js",
        "https://raw.githubusercontent.com/lodash/lodash/4.17.21-npm/throttle.js",
        1_500,
    ),
]

# Companion prose: short descriptions of each concept. Hand-written
# so vocabulary overlap with the code is partial (function names
# appear, but most of the prose is in natural-language paraphrase).
# This is what makes P3 useful — bi-encoder will match the code
# strongly when query is concept-shaped, and rerank should rescue
# prose targets only when concept actually matches.
PROSE: list[dict[str, str]] = [
    {
        "slug": "prose_http_handler",
        "text": (
            "An HTTP server in Go is built around the ServeMux router and the "
            "Handler interface. A handler implements ServeHTTP, which receives "
            "the response writer and the parsed request. The server reads the "
            "request line, headers, and body off the connection, dispatches to "
            "the registered handler, and writes the response back. Connection "
            "lifecycle, keep-alive, and TLS termination are managed by the "
            "Server struct."
        ),
    },
    {
        "slug": "prose_mutex",
        "text": (
            "A mutex is the simplest synchronization primitive: at most one "
            "goroutine can hold it at a time. Lock blocks until the mutex is "
            "free; Unlock releases it. Used to protect access to a shared "
            "variable from concurrent reads and writes. Incorrect use — "
            "double-unlock, lock-without-unlock — is a runtime error."
        ),
    },
    {
        "slug": "prose_waitgroup",
        "text": (
            "A WaitGroup lets a goroutine wait for a collection of other "
            "goroutines to finish. Add increments the counter before "
            "launching workers; each worker calls Done when it finishes; the "
            "coordinator calls Wait, which blocks until the counter hits "
            "zero."
        ),
    },
    {
        "slug": "prose_asyncio_lock",
        "text": (
            "An asyncio Lock is the asynchronous analogue of threading.Lock. "
            "Acquire is a coroutine that suspends until the lock is free; "
            "release wakes the next waiter. Locks are typically used as "
            "async context managers via async with."
        ),
    },
    {
        "slug": "prose_threading_module",
        "text": (
            "The threading module exposes Thread, Lock, RLock, Condition, "
            "Event, Semaphore, and Barrier. Threads share memory and the "
            "GIL serializes Python bytecode execution. Daemon threads are "
            "killed when the main thread exits."
        ),
    },
    {
        "slug": "prose_queue",
        "text": (
            "A thread-safe Queue supports put and get with optional "
            "blocking and timeouts. Variants include LifoQueue for stack "
            "semantics and PriorityQueue ordered by item value. Used as "
            "a hand-off between producer and consumer threads."
        ),
    },
    {
        "slug": "prose_debounce",
        "text": (
            "Debounce postpones invoking a function until after a quiet "
            "period has elapsed since the last call. Useful for resize and "
            "input handlers where rapid events should collapse into a "
            "single invocation when the burst ends."
        ),
    },
    {
        "slug": "prose_throttle",
        "text": (
            "Throttle limits how often a function can run regardless of "
            "how many times it's called: at most once per interval. Unlike "
            "debounce it does fire during a burst, just at a capped rate."
        ),
    },
]


COMMENT_TRIM = re.compile(r"^\s*//.*?$|^\s*#.*?$", re.MULTILINE)


def cache_dir() -> str:
    d = os.environ.get(
        "LOADPROFILE_CACHE",
        os.path.expanduser("~/.cache/levara-load-profiles/code"),
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
        raise RuntimeError(f"code source {slug} short: {len(text)} < {min_chars}")
    with open(path, "w", encoding="utf-8") as f:
        f.write(text)
    return path


def _code_chunks(path: str, slug: str, language: str) -> list[dict[str, str]]:
    """Split source by top-level definitions when we can spot them
    (`func `, `def `, `class `, `function `, `export function`); else
    fall back to fixed-size windows. Keep chunks ~800–1500 chars so
    most fit a single function body."""
    with open(path, encoding="utf-8") as f:
        raw = f.read()
    if language == "go":
        pattern = re.compile(r"\n(?=func\s|type\s\w+\s+struct|type\s\w+\s+interface)")
    elif language == "py":
        pattern = re.compile(r"\n(?=def\s|class\s|async\s+def\s)")
    else:  # js / generic
        pattern = re.compile(r"\n(?=function\s|export\s+(?:function|const|default))")
    parts = [p for p in pattern.split(raw) if p.strip()]
    chunks: list[dict[str, str]] = []
    for idx, part in enumerate(parts):
        if len(part) < 200:
            continue
        # Cap long bodies; split into ~1200-char windows.
        for off in range(0, len(part), 1200):
            sub = part[off : off + 1400]
            if len(sub) < 200:
                break
            chunks.append(
                {
                    "id": f"code:{slug}:{idx:03d}:{off:05d}",
                    "text": sub,
                    "metadata": json.dumps(
                        {
                            "source": "code",
                            "kind": "code",
                            "lang": language,
                            "file": slug,
                        }
                    ),
                }
            )
    return chunks


def load_corpus(max_chunks_per_source: int = 60) -> list[dict[str, str]]:
    out: list[dict[str, str]] = []
    for slug, lang, url, min_chars in SOURCES:
        path = _download(slug, url, min_chars)
        chunks = _code_chunks(path, slug, lang)
        out.extend(chunks[:max_chunks_per_source])
    for p in PROSE:
        out.append(
            {
                "id": f"prose:{p['slug']}",
                "text": p["text"],
                "metadata": json.dumps(
                    {"source": "prose", "kind": "prose", "slug": p["slug"]}
                ),
            }
        )
    return out


def corpus_fingerprint() -> str:
    h = hashlib.sha256()
    for slug, lang, url, min_chars in SOURCES:
        h.update(f"{slug}:{lang}:{url}:{min_chars}\n".encode())
    for p in PROSE:
        h.update(f"prose:{p['slug']}:{len(p['text'])}\n".encode())
    return h.hexdigest()[:12]


def seed_via_cognify(
    target,
    collection: str,
    *,
    chunks: list[dict],
    poll_seconds: float = 2.0,
    timeout_seconds: float = 1800.0,
) -> dict:
    """Seed `collection` by submitting the chunk corpus to /api/v1/cognify
    with skip_graph=true (RAG mode — chunk → embed → HNSW+BM25 insert,
    no LLM entity extraction).

    Delegates to runner.add_texts which is the canonical, known-good
    cognify payload shape used by p4/p5 (texts:[...], collection,
    skip_graph=true, runInBackground=true) and polls the run via
    /api/v1/cognify/{run_id}/status. The original implementation sent
    {dataset, text} which the server rejects with `no texts to cognify`.
    """
    import sys
    from pathlib import Path

    load_profiles_root = Path(__file__).resolve().parents[1]
    if str(load_profiles_root) not in sys.path:
        sys.path.insert(0, str(load_profiles_root))
    import runner

    return runner.add_texts(
        target,
        collection,
        chunks,
        poll_timeout_s=timeout_seconds,
        poll_interval_s=poll_seconds,
    )


# Mixed query set with explicit `target_kind` tag so the analyzer
# can slice gap distributions by whether the right answer is code
# or prose. The same conceptual query is duplicated in two shapes
# (token-precise vs concept-paraphrase) so we can see how vector
# vs rerank handle each.
QUERIES: list[dict[str, str]] = [
    # token-precise — should win on code chunks via lexical overlap
    {"id": "q01", "text": "func (mu *Mutex) Lock()", "kind": "code-precise", "target_kind": "code"},
    {"id": "q02", "text": "func (wg *WaitGroup) Wait()", "kind": "code-precise", "target_kind": "code"},
    {"id": "q03", "text": "async def acquire(self)", "kind": "code-precise", "target_kind": "code"},
    {"id": "q04", "text": "function debounce(func, wait, options)", "kind": "code-precise", "target_kind": "code"},
    {"id": "q05", "text": "ServeMux Handle pattern", "kind": "code-precise", "target_kind": "code"},
    {"id": "q06", "text": "class Queue put_nowait", "kind": "code-precise", "target_kind": "code"},
    # concept-paraphrase — prose should win if rerank does its job
    {"id": "q07", "text": "what happens when two threads grab the same lock at once", "kind": "concept-paraphrase", "target_kind": "prose"},
    {"id": "q08", "text": "wait for a group of background tasks to all complete", "kind": "concept-paraphrase", "target_kind": "prose"},
    {"id": "q09", "text": "non-blocking version of a mutex for coroutines", "kind": "concept-paraphrase", "target_kind": "prose"},
    {"id": "q10", "text": "collapse a burst of events into one call at the end", "kind": "concept-paraphrase", "target_kind": "prose"},
    {"id": "q11", "text": "how an HTTP server routes incoming requests to handlers", "kind": "concept-paraphrase", "target_kind": "prose"},
    {"id": "q12", "text": "give consumer threads work without races", "kind": "concept-paraphrase", "target_kind": "prose"},
    # mixed — both prose and code could plausibly answer
    {"id": "q13", "text": "throttle vs debounce", "kind": "mixed", "target_kind": "either"},
    {"id": "q14", "text": "GIL and threading in python", "kind": "mixed", "target_kind": "either"},
    {"id": "q15", "text": "daemon threads in python", "kind": "mixed", "target_kind": "either"},
    {"id": "q16", "text": "sync.WaitGroup add done wait", "kind": "mixed", "target_kind": "either"},
    {"id": "q17", "text": "PriorityQueue vs LifoQueue", "kind": "mixed", "target_kind": "either"},
    {"id": "q18", "text": "context cancellation in http handlers", "kind": "mixed", "target_kind": "either"},
    # adversarial — query that looks code-shaped but isn't in corpus
    {"id": "q19", "text": "fn render_widget(props: &Props) -> Element", "kind": "adversarial", "target_kind": "ooc"},
    {"id": "q20", "text": "trait IteratorExt for Vec<T>", "kind": "adversarial", "target_kind": "ooc"},
    # out-of-corpus
    {"id": "q21", "text": "ottoman empire decline causes", "kind": "ooc", "target_kind": "ooc"},
    {"id": "q22", "text": "knee surgery recovery timeline", "kind": "ooc", "target_kind": "ooc"},
    {"id": "q23", "text": "espresso grind size for moka pot", "kind": "ooc", "target_kind": "ooc"},
    {"id": "q24", "text": "rust borrow checker lifetime elision", "kind": "ooc", "target_kind": "ooc"},
]
