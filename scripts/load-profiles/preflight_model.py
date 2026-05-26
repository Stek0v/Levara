#!/usr/bin/env python3
"""Preflight gate for one model run on the Pi bench stack.

Usage: python preflight_model.py --model <short> --host 10.23.0.53
Exits 0 if every check passes, 1 otherwise. Prints structured JSON report.
"""
from __future__ import annotations

import argparse
import json
import sys
import urllib.request
from dataclasses import dataclass, field
from typing import Callable

from embed_bench.recipes import get_recipe


@dataclass
class Checks:
    model_short: str
    expected_dim: int
    expected_openai_name: str
    host: str = "10.23.0.53"
    sidecar_port: int = 9101
    bench_port: int = 8091
    rerank_port: int = 9100
    ollama_port: int = 11434
    ollama_model_required: str = "qwen3:0.6b"


@dataclass
class Result:
    ok: bool
    passed: list[str] = field(default_factory=list)
    failed: list[str] = field(default_factory=list)


def default_http(method: str, url: str, headers=None, body=None, timeout: float = 10.0) -> dict:
    payload = json.dumps(body).encode("utf-8") if body is not None else None
    req = urllib.request.Request(url, data=payload, headers=headers or {}, method=method)
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        data = resp.read()
        if not data:
            return {}
        return json.loads(data)


HttpFn = Callable[..., dict]


def run_checks(c: Checks, *, http: HttpFn = default_http) -> Result:
    passed: list[str] = []
    failed: list[str] = []

    def _try(label: str, fn):
        try:
            fn()
            passed.append(label)
        except Exception as e:
            failed.append(f"{label}: {e}")

    sidecar = f"http://{c.host}:{c.sidecar_port}"
    bench = f"http://{c.host}:{c.bench_port}"
    rerank = f"http://{c.host}:{c.rerank_port}"
    ollama = f"http://{c.host}:{c.ollama_port}"

    def check_sidecar_health():
        body = http("GET", f"{sidecar}/health")
        if body.get("model") != c.expected_openai_name:
            raise RuntimeError(f"model name mismatch: {body.get('model')!r} != {c.expected_openai_name!r}")
        if body.get("dim") != c.expected_dim:
            raise RuntimeError(f"sidecar_dim mismatch: {body.get('dim')} != {c.expected_dim}")

    def check_embed_ping():
        body = http("POST", f"{sidecar}/v1/embeddings",
                    headers={"Content-Type": "application/json"},
                    body={"input": ["ping"], "model": c.expected_openai_name})
        data = body.get("data", [])
        if not data:
            raise RuntimeError("empty data array")
        v = data[0].get("embedding", [])
        if len(v) != c.expected_dim:
            raise RuntimeError(f"embed_ping_dim {len(v)} != {c.expected_dim}")

    def check_bench_health():
        http("GET", f"{bench}/health")

    def check_ollama_model():
        body = http("GET", f"{ollama}/api/tags")
        names = {m.get("name") for m in body.get("models", [])}
        if c.ollama_model_required not in names:
            raise RuntimeError(f"ollama_model {c.ollama_model_required!r} not in {names}")

    def check_rerank_health():
        http("GET", f"{rerank}/health")

    def check_auth():
        import secrets
        email = f"preflight_{secrets.token_hex(4)}@local"
        passwd = secrets.token_hex(8)
        body = http("POST", f"{bench}/api/v1/auth/register",
                    headers={"Content-Type": "application/json"},
                    body={"email": email, "password": passwd})
        if not body.get("access_token"):
            raise RuntimeError("no access_token returned")

    _try("sidecar_health", check_sidecar_health)
    _try("embed_ping",     check_embed_ping)
    _try("bench_health",   check_bench_health)
    _try("ollama_model",   check_ollama_model)
    _try("rerank_health",  check_rerank_health)
    _try("auth_register",  check_auth)

    return Result(ok=not failed, passed=passed, failed=failed)


def main() -> int:
    p = argparse.ArgumentParser()
    p.add_argument("--model", required=True)
    p.add_argument("--host", default="10.23.0.53")
    args = p.parse_args()

    recipe = get_recipe(args.model)
    checks = Checks(
        model_short=recipe.short,
        expected_dim=recipe.dim,
        expected_openai_name=recipe.openai_name,
        host=args.host,
    )
    result = run_checks(checks)
    print(json.dumps({"ok": result.ok, "passed": result.passed, "failed": result.failed}, indent=2))
    return 0 if result.ok else 1


if __name__ == "__main__":
    sys.exit(main())
