"""Soak / stability test for the Levara rerank path.

Drives a long, mixed (search-heavy + insert) workload against a real
Levara binary fronted by the chaos rerank sidecar (low 5xx probability —
soak is about leaks, not chaos) and asserts that the process is not
leaking:

  * RSS growth                 ≤ SOAK_RSS_GROWTH_PCT (default 20%) over baseline
  * Open file descriptor count ≤ SOAK_FD_GROWTH      (default 50)   over baseline
  * Goroutine count            ≤ 50% over baseline
  * Sum of outcome-counter deltas == number of search iterations
                                (no silent drops in the rerank path)

Gated on **`LEVARA_SOAK=1`** — deliberately a different gate than the
chaos integration test (LEVARA_INTEGRATION) because this one is heavier
(seeds ~200 docs, runs hundreds of iterations).

Topology mirrors `test_chaos_integration.py`:

    pytest → chaos_sidecar (127.0.0.1:9101)
           → levara-server  (127.0.0.1:<free port>)

Resource sampling choice: **psutil** when present (cross-platform,
exposes `memory_info().rss` and `num_fds()` directly). If psutil cannot
be imported, we fall back to per-platform shims:

  * RSS: `/proc/<pid>/status` (Linux) → `VmRSS` line in kB; on darwin,
    `ps -o rss= -p <pid>` (kB).
  * FDs: `lsof -p <pid> | wc -l` minus 1 (header). Slow but portable.

The fallback is documented inline; on CI we expect psutil.

Knobs (all env):

    SOAK_ITERATIONS        total workload iterations           (500)
    SOAK_WARMUP            iterations before baseline snapshot (50)
    SOAK_QPS               target requests/second              (5)
    SOAK_RSS_GROWTH_PCT    max %% RSS growth allowed            (20)
    SOAK_FD_GROWTH         max absolute FD growth allowed      (50)
    SOAK_GOROUTINE_PCT     max %% goroutine growth allowed      (50)
    SOAK_INSERT_FRACTION   fraction of iters that are inserts  (0.20)
    SOAK_SEED_DOCS         docs to seed from SciDocs           (200)
    SOAK_SAMPLE_EVERY      iters between resource snapshots    (50)

At default settings: 500 iters at 5 QPS = 100s of workload, plus
seed/cognify (~60-90s) and warmup/teardown. Total wall time under
5 minutes on a developer laptop.
"""
from __future__ import annotations

import json
import os
import re
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.request
from contextlib import closing
from pathlib import Path
from typing import Dict, List, Optional, Tuple

import pytest

# Re-use the corpus fixture from the eval directory.
sys.path.insert(0, str(Path(__file__).parent / "eval"))
from corpus_fixture import (  # noqa: E402
    flatten_docs,
    load_scidocs,
    make_queries,
    seed_collection,
)


SOAK = os.environ.get("LEVARA_SOAK") == "1"
pytestmark = pytest.mark.skipif(
    not SOAK,
    reason="set LEVARA_SOAK=1 to run the soak test (heavy: seeds ~200 docs, runs hundreds of iterations)",
)


# --- knobs --------------------------------------------------------------

REPO_ROOT = Path(__file__).resolve().parents[2]  # .../Levara
SIDECAR_PORT = 9101
COLLECTION = "soak"

ITERATIONS = int(os.environ.get("SOAK_ITERATIONS", "500"))
WARMUP = int(os.environ.get("SOAK_WARMUP", "50"))
QPS = float(os.environ.get("SOAK_QPS", "5"))
RSS_PCT = float(os.environ.get("SOAK_RSS_GROWTH_PCT", "20"))
FD_GROWTH = int(os.environ.get("SOAK_FD_GROWTH", "50"))
GOROUTINE_PCT = float(os.environ.get("SOAK_GOROUTINE_PCT", "50"))
INSERT_FRACTION = float(os.environ.get("SOAK_INSERT_FRACTION", "0.20"))
SEED_DOCS = int(os.environ.get("SOAK_SEED_DOCS", "200"))
SAMPLE_EVERY = int(os.environ.get("SOAK_SAMPLE_EVERY", "50"))

CHAOS_SEED = "1337"
CHAOS_LATENCY_MS_MAX = "300"
CHAOS_5XX_PROB = "0.05"  # soak is not chaos: keep failures rare


# --- helpers ------------------------------------------------------------


def _free_port() -> int:
    with closing(socket.socket(socket.AF_INET, socket.SOCK_STREAM)) as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def _wait_http_ready(url: str, timeout_s: float = 60.0) -> None:
    deadline = time.time() + timeout_s
    last_err: Optional[Exception] = None
    while time.time() < deadline:
        try:
            with urllib.request.urlopen(url, timeout=2) as r:
                if 200 <= r.status < 500:
                    return
        except (urllib.error.URLError, ConnectionError, OSError) as e:
            last_err = e
        time.sleep(0.5)
    raise RuntimeError(f"service at {url} did not become ready: {last_err}")


def _resolve_server_cmd() -> List[str]:
    for candidate in ("levara-server", "cognevra-server", "server"):
        p = REPO_ROOT / candidate
        if p.is_file() and os.access(p, os.X_OK):
            return [str(p)]
    return ["go", "run", "./cmd/server"]


def _post_json(
    url: str, body: dict, timeout: float = 30.0
) -> Tuple[int, Optional[dict]]:
    req = urllib.request.Request(
        url,
        data=json.dumps(body).encode("utf-8"),
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            raw = r.read()
            try:
                return r.status, json.loads(raw)
            except json.JSONDecodeError:
                return r.status, None
    except urllib.error.HTTPError as e:
        return e.code, None
    except (urllib.error.URLError, ConnectionError, OSError):
        return 0, None


# --- resource sampling -------------------------------------------------

try:
    import psutil  # type: ignore

    _HAS_PSUTIL = True
except ImportError:  # pragma: no cover — CI is expected to have psutil
    _HAS_PSUTIL = False


def _rss_bytes(pid: int) -> int:
    """Resident set size in bytes."""
    if _HAS_PSUTIL:
        return int(psutil.Process(pid).memory_info().rss)
    # Fallback. Linux: /proc/<pid>/status VmRSS kB. Darwin: ps -o rss=.
    if sys.platform.startswith("linux"):
        with open(f"/proc/{pid}/status", "r") as f:
            for line in f:
                if line.startswith("VmRSS:"):
                    kb = int(line.split()[1])
                    return kb * 1024
        return 0
    out = subprocess.run(
        ["ps", "-o", "rss=", "-p", str(pid)], capture_output=True, text=True
    )
    try:
        return int(out.stdout.strip()) * 1024
    except (ValueError, AttributeError):
        return 0


def _fd_count(pid: int) -> int:
    """Number of open file descriptors for `pid`."""
    if _HAS_PSUTIL:
        try:
            return int(psutil.Process(pid).num_fds())
        except (AttributeError, psutil.AccessDenied):  # type: ignore[attr-defined]
            pass
    # Fallback to lsof (slow but portable). The -F n flag prints one
    # field per line which is easier to count than the default table.
    try:
        out = subprocess.run(
            ["lsof", "-p", str(pid)],
            capture_output=True,
            text=True,
            timeout=10,
        )
        # Subtract 1 for the header row.
        lines = [ln for ln in out.stdout.splitlines() if ln.strip()]
        return max(0, len(lines) - 1)
    except (FileNotFoundError, subprocess.TimeoutExpired):
        return 0


_GOROUTINE_RE = re.compile(r"^go_goroutines\s+([0-9.eE+-]+)", re.MULTILINE)
_OUTCOME_RE = re.compile(
    r'^levara_rerank_invocations_total\{outcome="([^"]+)"\}\s+([0-9.eE+-]+)',
    re.MULTILINE,
)


def _scrape_metrics(metrics_url: str) -> str:
    with urllib.request.urlopen(metrics_url, timeout=10) as r:
        return r.read().decode("utf-8", errors="replace")


def _goroutines(metrics_text: str) -> int:
    m = _GOROUTINE_RE.search(metrics_text)
    if not m:
        return 0
    return int(float(m.group(1)))


def _outcomes(metrics_text: str) -> Dict[str, float]:
    out: Dict[str, float] = {}
    for label, val in _OUTCOME_RE.findall(metrics_text):
        out[label] = float(val)
    return out


# --- fixtures -----------------------------------------------------------


@pytest.fixture(scope="module")
def chaos_sidecar():
    here = Path(__file__).parent
    env = {
        **os.environ,
        "CHAOS_SEED": CHAOS_SEED,
        "CHAOS_LATENCY_MS_MAX": CHAOS_LATENCY_MS_MAX,
        "CHAOS_5XX_PROB": CHAOS_5XX_PROB,
        "CHAOS_PORT": str(SIDECAR_PORT),
    }
    proc = subprocess.Popen(
        [
            sys.executable,
            "-m",
            "uvicorn",
            "chaos_sidecar:app",
            "--host",
            "127.0.0.1",
            "--port",
            str(SIDECAR_PORT),
            "--log-level",
            "warning",
        ],
        cwd=str(here),
        env=env,
    )
    try:
        _wait_http_ready(f"http://127.0.0.1:{SIDECAR_PORT}/health", timeout_s=20.0)
        yield f"http://127.0.0.1:{SIDECAR_PORT}/rerank"
    finally:
        proc.terminate()
        try:
            proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait()


@pytest.fixture(scope="module")
def levara_server(chaos_sidecar):
    port = _free_port()
    env = {
        **os.environ,
        "RERANK_ENDPOINT": chaos_sidecar,
        "RERANK_MODEL": "chaos",
        "RERANK_BUDGET_MS": "5000",
        "RERANK_TIMEOUT_MS": "10000",
        "HTTP_PORT": str(port),
        "PORT": str(port),
        "DB_PROVIDER": os.environ.get("DB_PROVIDER", "sqlite"),
    }
    proc = subprocess.Popen(
        _resolve_server_cmd(),
        cwd=str(REPO_ROOT),
        env=env,
    )
    try:
        _wait_http_ready(f"http://127.0.0.1:{port}/metrics", timeout_s=120.0)
        yield {"base": f"http://127.0.0.1:{port}", "pid": proc.pid}
    finally:
        proc.terminate()
        try:
            proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait()


# --- the test ----------------------------------------------------------


def _snapshot(pid: int, metrics_url: str) -> Dict[str, int]:
    text = _scrape_metrics(metrics_url)
    return {
        "rss": _rss_bytes(pid),
        "fds": _fd_count(pid),
        "goroutines": _goroutines(text),
    }


def _fmt_snapshot(s: Dict[str, int]) -> str:
    return (
        f"rss={s['rss'] / (1024 * 1024):.1f}MiB "
        f"fds={s['fds']} "
        f"goroutines={s['goroutines']}"
    )


def test_soak_no_leak(levara_server):
    base = levara_server["base"]
    pid = levara_server["pid"]
    metrics_url = f"{base}/metrics"

    # 1. Bootstrap collection.
    status, _ = _post_json(
        f"{base}/api/v1/collections",
        {"name": COLLECTION, "vector_size": 4, "distance": "Cosine"},
    )
    assert status in (200, 201, 409), f"collection create: {status}"

    # 2. Seed corpus from MTEB SciDocs (first SEED_DOCS docs after flatten).
    rows = load_scidocs(limit=max(50, SEED_DOCS // 2))
    docs = flatten_docs(rows, max_total=SEED_DOCS)
    queries = make_queries(rows, n=64)
    assert docs, "expected at least one doc from SciDocs fixture"
    assert queries, "expected at least one query from SciDocs fixture"

    seed_collection(base_url=base, collection=COLLECTION, docs=docs)

    # Give indexer a moment after cognify completes.
    time.sleep(2.0)

    # 3. Workload primitive: either a search or an insert, deterministic
    #    by iteration index so reruns of a soak with a leak suspicion are
    #    reproducible.
    insert_every = max(1, int(round(1.0 / INSERT_FRACTION))) if INSERT_FRACTION > 0 else 0
    sleep_per_iter = 1.0 / QPS if QPS > 0 else 0.0

    def _do_search(i: int) -> int:
        q = queries[i % len(queries)]["query"]
        status, _ = _post_json(
            f"{base}/api/v1/search/text",
            {
                "query_text": q,
                "query_type": "CHUNKS",
                "collection": COLLECTION,
                "top_k": 10,
                "rerank": True,
            },
            timeout=30.0,
        )
        return status

    def _do_insert(i: int) -> int:
        text = f"soak insert iter={i} alpha beta gamma {i * 7 % 9973}"
        status, _ = _post_json(
            f"{base}/api/v1/add",
            {"data": text, "dataset_name": f"scidocs-{COLLECTION}", "tags": ["soak"]},
            timeout=30.0,
        )
        return status

    # 4. Warmup so JIT-like effects (lazy alloc, pprof handler registration,
    #    HNSW first-touch) don't pollute baseline.
    search_iters = 0
    for i in range(WARMUP):
        if insert_every and i % insert_every == 0:
            st = _do_insert(i)
            assert st in (200, 201, 202), f"warmup insert #{i}: {st}"
        else:
            st = _do_search(i)
            assert st == 200, f"warmup search #{i}: {st}"
            search_iters += 1
        if sleep_per_iter:
            time.sleep(sleep_per_iter)

    # Settle indexer/runreg before snapshot.
    time.sleep(2.0)

    baseline = _snapshot(pid, metrics_url)
    baseline_outcomes = _outcomes(_scrape_metrics(metrics_url))
    baseline_outcomes_total = sum(baseline_outcomes.values())

    timeline: List[Tuple[int, Dict[str, int]]] = [(WARMUP, baseline)]
    peak = dict(baseline)
    failed_iters: List[Tuple[int, str, int]] = []

    # 5. Main soak loop.
    remaining = max(0, ITERATIONS - WARMUP)
    start_t = time.monotonic()
    for j in range(remaining):
        i = WARMUP + j
        if insert_every and i % insert_every == 0:
            st = _do_insert(i)
            kind = "insert"
            ok = st in (200, 201, 202)
        else:
            st = _do_search(i)
            kind = "search"
            ok = st == 200
            if ok:
                search_iters += 1
        if not ok:
            failed_iters.append((i, kind, st))

        if (j + 1) % SAMPLE_EVERY == 0:
            snap = _snapshot(pid, metrics_url)
            timeline.append((i + 1, snap))
            for k, v in snap.items():
                if v > peak[k]:
                    peak[k] = v
            print(
                f"[soak] iter={i + 1} elapsed={time.monotonic() - start_t:.1f}s "
                f"{_fmt_snapshot(snap)} "
                f"(Δrss={(snap['rss'] - baseline['rss']) / (1024 * 1024):+.1f}MiB "
                f"Δfds={snap['fds'] - baseline['fds']:+d} "
                f"Δgo={snap['goroutines'] - baseline['goroutines']:+d})",
                flush=True,
            )

        if sleep_per_iter:
            time.sleep(sleep_per_iter)

    # Final snapshot (in case ITERATIONS isn't a multiple of SAMPLE_EVERY).
    final = _snapshot(pid, metrics_url)
    timeline.append((ITERATIONS, final))
    for k, v in final.items():
        if v > peak[k]:
            peak[k] = v

    final_outcomes = _outcomes(_scrape_metrics(metrics_url))
    outcome_delta_total = sum(final_outcomes.values()) - baseline_outcomes_total

    # 6. Asserts. Build a full report up front so on failure we dump it.
    rss_growth_pct = (
        (peak["rss"] - baseline["rss"]) / baseline["rss"] * 100.0
        if baseline["rss"]
        else 0.0
    )
    fd_growth = peak["fds"] - baseline["fds"]
    goroutine_growth_pct = (
        (peak["goroutines"] - baseline["goroutines"]) / baseline["goroutines"] * 100.0
        if baseline["goroutines"]
        else 0.0
    )

    # Number of successful searches AFTER baseline (the rerank-bearing ops).
    # We compare against outcome counter delta scraped from /metrics.
    expected_outcome_count = search_iters - sum(
        1 for entry in timeline if entry[0] == WARMUP
    ) * 0  # no-op; warmup searches counted in baseline_outcomes already
    # Cleaner: search_iters includes warmup searches, which baseline already
    # captured. So delta should equal (search_iters - warmup_searches).
    warmup_searches = sum(
        1 for i in range(WARMUP) if not (insert_every and i % insert_every == 0)
    )
    expected_outcome_count = search_iters - warmup_searches

    def _dump_timeline() -> str:
        rows_out = [
            f"  iter={it:5d} {_fmt_snapshot(s)} "
            f"Δrss={(s['rss'] - baseline['rss']) / (1024 * 1024):+.1f}MiB "
            f"Δfds={s['fds'] - baseline['fds']:+d} "
            f"Δgo={s['goroutines'] - baseline['goroutines']:+d}"
            for it, s in timeline
        ]
        return (
            "soak timeline (baseline = first row):\n"
            + "\n".join(rows_out)
            + f"\n peak {_fmt_snapshot(peak)}"
            + f"\n baseline {_fmt_snapshot(baseline)}"
            + f"\n outcome_delta_total={outcome_delta_total} expected={expected_outcome_count}"
            + f"\n failed_iters={failed_iters[:20]}"
            f"{' ...' if len(failed_iters) > 20 else ''}"
        )

    # No silent drops first — if this fails, the leak numbers are noise.
    if int(outcome_delta_total) != int(expected_outcome_count):
        print(_dump_timeline(), flush=True)
    assert int(outcome_delta_total) == int(expected_outcome_count), (
        f"rerank outcome counter desync: scraped Δ={outcome_delta_total} "
        f"but ran {expected_outcome_count} reranked searches post-baseline. "
        f"Indicates silent drops in /api/v1/search/text rerank path."
    )

    # Each individual iteration must have completed (no HTTP failures).
    if failed_iters:
        print(_dump_timeline(), flush=True)
    assert not failed_iters, (
        f"{len(failed_iters)} workload iteration(s) failed; first={failed_iters[:5]}"
    )

    # Resource leak gates.
    leak_msgs = []
    if rss_growth_pct > RSS_PCT:
        leak_msgs.append(
            f"RSS grew {rss_growth_pct:.1f}% (>{RSS_PCT:.0f}% threshold): "
            f"baseline={baseline['rss'] / 1024 / 1024:.1f}MiB "
            f"peak={peak['rss'] / 1024 / 1024:.1f}MiB"
        )
    if fd_growth > FD_GROWTH:
        leak_msgs.append(
            f"FD count grew by {fd_growth} (>{FD_GROWTH} threshold): "
            f"baseline={baseline['fds']} peak={peak['fds']}"
        )
    if goroutine_growth_pct > GOROUTINE_PCT:
        leak_msgs.append(
            f"goroutines grew {goroutine_growth_pct:.1f}% "
            f"(>{GOROUTINE_PCT:.0f}% threshold): "
            f"baseline={baseline['goroutines']} peak={peak['goroutines']}"
        )

    if leak_msgs:
        print(_dump_timeline(), flush=True)
    assert not leak_msgs, "; ".join(leak_msgs)
