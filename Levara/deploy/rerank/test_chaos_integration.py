"""End-to-end chaos integration test.

Verifies that Levara's chunk-search handler's rerank-outcome distribution,
as exposed by `levara_rerank_invocations_total{outcome=...}`, matches the
chaos sidecar's injected error rate within a 3sigma band.

Heavy and slow — skipped unless `LEVARA_INTEGRATION=1` is set.

Topology:

    pytest -> chaos_sidecar (127.0.0.1:9101)
           -> levara-server (127.0.0.1:<free port>)

The Levara server is launched with RERANK_ENDPOINT pointed at the chaos
sidecar; we drive it with 100 sequential /api/v1/search/text calls and
scrape /metrics at the end.
"""
from __future__ import annotations

import os
import re
import socket
import subprocess
import sys
import time
from contextlib import closing
from pathlib import Path
from typing import Dict

import pytest


INTEGRATION = os.environ.get("LEVARA_INTEGRATION") == "1"
pytestmark = pytest.mark.skipif(
    not INTEGRATION,
    reason="set LEVARA_INTEGRATION=1 to run the chaos integration test",
)

REPO_ROOT = Path(__file__).resolve().parents[2]  # .../Levara
SIDECAR_PORT = 9101
CHAOS_SEED = "1337"
CHAOS_5XX_PROB = "0.20"
CHAOS_LATENCY_MS_MAX = "500"
RERANK_BUDGET_MS = "5000"
N_REQUESTS = 100
P_ERROR = 0.20
# Allowed deviation per side of expected fraction. n=100, p=0.2 → σ≈0.04,
# 3σ ≈ 0.12. Task asks for ±0.10 — slightly tighter than 3σ but stable
# with a fixed CHAOS_SEED.
TOLERANCE = 0.10


def _free_port() -> int:
    with closing(socket.socket(socket.AF_INET, socket.SOCK_STREAM)) as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def _wait_http_ready(url: str, timeout_s: float = 60.0) -> None:
    import urllib.request
    import urllib.error

    deadline = time.time() + timeout_s
    last_err: Exception | None = None
    while time.time() < deadline:
        try:
            with urllib.request.urlopen(url, timeout=2) as r:
                if 200 <= r.status < 500:
                    return
        except (urllib.error.URLError, ConnectionError, OSError) as e:
            last_err = e
        time.sleep(0.5)
    raise RuntimeError(f"service at {url} did not become ready: {last_err}")


def _resolve_server_cmd() -> list[str]:
    """Pick the cheapest server invocation available."""
    for candidate in ("levara-server", "cognevra-server", "server"):
        p = REPO_ROOT / candidate
        if p.is_file() and os.access(p, os.X_OK):
            return [str(p)]
    # Fall back to `go run` from source — slow but always works.
    return ["go", "run", "./cmd/server"]


@pytest.fixture(scope="module")
def chaos_sidecar():
    """Boot the chaos sidecar via uvicorn on a fixed loopback port."""
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
    """Boot levara-server pointed at the chaos sidecar."""
    port = _free_port()
    env = {
        **os.environ,
        "RERANK_ENDPOINT": chaos_sidecar,
        "RERANK_MODEL": "chaos",
        "RERANK_BUDGET_MS": RERANK_BUDGET_MS,
        "RERANK_TIMEOUT_MS": "10000",
        "HTTP_PORT": str(port),
        "PORT": str(port),
        # Keep the process self-contained when possible.
        "DB_PROVIDER": os.environ.get("DB_PROVIDER", "sqlite"),
    }
    proc = subprocess.Popen(
        _resolve_server_cmd(),
        cwd=str(REPO_ROOT),
        env=env,
    )
    try:
        _wait_http_ready(f"http://127.0.0.1:{port}/metrics", timeout_s=120.0)
        yield f"http://127.0.0.1:{port}"
    finally:
        proc.terminate()
        try:
            proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait()


def _post_json(url: str, body: dict, timeout: float = 30.0) -> tuple[int, dict | None]:
    import json
    import urllib.request
    import urllib.error

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


def _scrape_outcomes(metrics_url: str) -> Dict[str, float]:
    import urllib.request

    with urllib.request.urlopen(metrics_url, timeout=10) as r:
        text = r.read().decode("utf-8", errors="replace")
    pat = re.compile(
        r'^levara_rerank_invocations_total\{outcome="([^"]+)"\}\s+([0-9.eE+-]+)',
        re.MULTILINE,
    )
    out: Dict[str, float] = {}
    for label, val in pat.findall(text):
        out[label] = float(val)
    return out


def test_chaos_outcome_distribution(levara_server):
    base = levara_server

    # 1. Create a collection.
    status, _ = _post_json(
        f"{base}/api/v1/collections",
        {"name": "chaos", "vector_size": 4, "distance": "Cosine"},
    )
    assert status in (200, 201, 409), f"collection create: {status}"

    # 2. Seed ~10 records via /add. Each piece becomes chunk(s) with text
    #    metadata, which is what the rerank path requires.
    seeds = [
        f"chaos doc {i}: alpha beta gamma delta epsilon zeta eta theta"
        for i in range(10)
    ]
    for text in seeds:
        status, _ = _post_json(
            f"{base}/api/v1/add",
            {"data": text, "dataset_name": "chaos", "tags": ["chaos"]},
        )
        assert status in (200, 201, 202), f"add: {status}"

    # Give the indexer a moment to settle.
    time.sleep(2.0)

    before = _scrape_outcomes(f"{base}/metrics")
    before_total = sum(before.values())

    # 3. Drive 100 sequential searches.
    for i in range(N_REQUESTS):
        status, _ = _post_json(
            f"{base}/api/v1/search/text",
            {
                "query_text": f"alpha gamma {i}",
                "query_type": "CHUNKS",
                "collection": "chaos",
                "top_k": 5,
                "rerank": True,
            },
        )
        # The handler must degrade gracefully on rerank failure; HTTP 200
        # is the contract regardless of the upstream chaos.
        assert status == 200, f"search #{i}: {status}"

    after = _scrape_outcomes(f"{base}/metrics")
    delta = {k: after.get(k, 0.0) - before.get(k, 0.0) for k in set(after) | set(before)}
    total = sum(delta.values())

    # 4. Distribution assertions.
    assert total == N_REQUESTS, (
        f"expected exactly {N_REQUESTS} rerank outcomes, got {total}: {delta}"
    )

    ok = delta.get("ok", 0.0) / total
    err = delta.get("error", 0.0) / total

    assert abs(err - P_ERROR) <= TOLERANCE, (
        f"error fraction {err:.3f} outside [{P_ERROR - TOLERANCE:.3f}, "
        f"{P_ERROR + TOLERANCE:.3f}]; outcomes={delta}"
    )
    p_ok = 1.0 - P_ERROR
    assert abs(ok - p_ok) <= TOLERANCE, (
        f"ok fraction {ok:.3f} outside [{p_ok - TOLERANCE:.3f}, "
        f"{p_ok + TOLERANCE:.3f}]; outcomes={delta}"
    )
