#!/usr/bin/env python3
"""Golden eval for Levara agent memory behavior.

The suite is intentionally LLM-free.  CI should use --fake for deterministic
fixtures.  Local canary may use --base-url to generate real MCP audit events
and compare the /memory-behavior read-model output.
"""

from __future__ import annotations

import argparse
import json
import time
import uuid
from dataclasses import dataclass, field, asdict
from datetime import datetime, timezone
from pathlib import Path
from typing import Any
from urllib import request, error


REPO_ROOT = Path(__file__).resolve().parents[2]
RESULTS_DIR = REPO_ROOT / "benchmark" / "results"


@dataclass
class Event:
    tool: str
    outcome: str = "ok"
    key: str = ""
    result_count: int = 0
    zero_result: bool = False
    request_bytes: int = 0
    response_bytes: int = 0
    error_message: str = ""


@dataclass
class Scenario:
    name: str
    events: list[Event]
    expected: dict[str, Any] = field(default_factory=dict)


def fake_scenarios() -> list[Scenario]:
    return [
        Scenario(
            name="good_recall_then_save_new_decision",
            events=[
                Event("recall_memory", result_count=1, request_bytes=80, response_bytes=400),
                Event("save_memory", key="decision_arch", request_bytes=260, response_bytes=180),
            ],
            expected={"blind_saves": 0, "repeat_saves": 0},
        ),
        Scenario(
            name="bad_repeated_save_without_recall",
            events=[
                Event("save_memory", key="dup_key", request_bytes=220, response_bytes=200),
                Event("save_memory", key="dup_key", request_bytes=230, response_bytes=220),
            ],
            expected={"blind_saves": 2, "repeat_saves": 1},
        ),
        Scenario(
            name="zero_result_reformulation_success",
            events=[
                Event("recall_memory", result_count=0, zero_result=True, request_bytes=120, response_bytes=120),
                Event("search", result_count=2, zero_result=False, request_bytes=140, response_bytes=520),
            ],
            expected={"zero_result_recovered": True},
        ),
        Scenario(
            name="wrong_hall_rejected",
            events=[
                Event("save_memory", key="bad_hall", outcome="client_error", request_bytes=220, response_bytes=160, error_message="invalid hall 'todo'"),
            ],
            expected={"unknown_hall_error": True},
        ),
        Scenario(
            name="noisy_context_many_wakeup_hits",
            events=[
                Event("wake_up", result_count=20, request_bytes=80, response_bytes=12_000),
                Event("recall_memory", result_count=1, request_bytes=100, response_bytes=900),
            ],
            expected={"context_bytes_high": True},
        ),
    ]


def analyze_scenarios(scenarios: list[Scenario]) -> dict[str, Any]:
    total_saves = 0
    saves_after_consult = 0
    repeat_saves = 0
    blind_saves = 0
    zero_results = 0
    recovered_zero_results = 0
    memory_ops = 0
    context_bytes = 0
    unknown_hall_errors = 0
    seen_keys: set[str] = set()

    for scenario in scenarios:
        consulted = False
        saw_zero = False
        recovered = False
        for event in scenario.events:
            context_bytes += event.request_bytes + event.response_bytes
            if event.tool in {"recall_memory", "search", "list_memories", "wake_up"}:
                memory_ops += 1
                consulted = True
                if event.zero_result or event.result_count == 0:
                    zero_results += 1
                    saw_zero = True
                elif saw_zero:
                    recovered = True
            if event.tool == "save_memory":
                memory_ops += 1
                total_saves += 1
                if consulted:
                    saves_after_consult += 1
                else:
                    blind_saves += 1
                if event.key and event.key in seen_keys:
                    repeat_saves += 1
                if event.key:
                    seen_keys.add(event.key)
                if event.outcome != "ok" and "hall" in event.error_message.lower():
                    unknown_hall_errors += 1
        if recovered:
            recovered_zero_results += 1

    recall_before_save_rate = saves_after_consult / total_saves if total_saves else 1.0
    repeat_save_rate = repeat_saves / total_saves if total_saves else 0.0
    zero_result_recovery_rate = recovered_zero_results / zero_results if zero_results else 1.0
    behavior_score = max(
        0.0,
        100.0
        - (blind_saves * 6.0)
        - (repeat_save_rate * 12.0)
        - (unknown_hall_errors * 4.0)
        - max(0.0, (context_bytes / max(len(scenarios), 1) - 8_000) / 1_000),
    )
    return {
        "recall_before_save_rate": recall_before_save_rate,
        "repeat_save_rate": repeat_save_rate,
        "zero_result_recovery_rate": zero_result_recovery_rate,
        "behavior_score": round(behavior_score, 2),
        "context_bytes": context_bytes,
        "context_bytes_per_trajectory": context_bytes / max(len(scenarios), 1),
        "memory_ops": memory_ops,
        "memory_ops_per_trajectory": memory_ops / max(len(scenarios), 1),
        "blind_saves": blind_saves,
        "repeat_saves": repeat_saves,
        "unknown_hall_error_count": unknown_hall_errors,
    }


def post_json(url: str, payload: dict[str, Any], headers: dict[str, str] | None = None) -> dict[str, Any]:
    raw = json.dumps(payload).encode("utf-8")
    req = request.Request(url, data=raw, method="POST", headers={"Content-Type": "application/json", **(headers or {})})
    with request.urlopen(req, timeout=30) as resp:
        return json.loads(resp.read().decode("utf-8"))


def get_json(url: str, headers: dict[str, str] | None = None) -> dict[str, Any]:
    req = request.Request(url, method="GET", headers=headers or {})
    with request.urlopen(req, timeout=30) as resp:
        return json.loads(resp.read().decode("utf-8"))


def mcp_call(base_url: str, tool: str, arguments: dict[str, Any], headers: dict[str, str] | None = None) -> dict[str, Any]:
    body = {
        "jsonrpc": "2.0",
        "id": int(time.monotonic() * 1_000_000),
        "method": "tools/call",
        "params": {"name": tool, "arguments": arguments},
    }
    return post_json(f"{base_url.rstrip('/')}/mcp", body, headers=headers)


def run_local(base_url: str, label: str, token: str | None) -> dict[str, Any]:
    headers = {"Authorization": f"Bearer {token}"} if token else {}
    collection = f"memory-behavior-eval-{uuid.uuid4().hex[:8]}"
    mcp_call(base_url, "set_context", {"collection": collection}, headers)
    mcp_call(base_url, "recall_memory", {"query": "architecture decision that should not exist", "collection": collection}, headers)
    mcp_call(base_url, "save_memory", {"collection": collection, "room": "eval", "hall": "decision", "key": "decision_eval", "value": "2026-07-13 memory behavior eval decision fixture"}, headers)
    mcp_call(base_url, "save_memory", {"collection": collection, "room": "eval", "hall": "decision", "key": "decision_eval", "value": "2026-07-13 memory behavior eval updated decision fixture"}, headers)
    bad = mcp_call(base_url, "save_memory", {"collection": collection, "room": "eval", "hall": "todo", "key": "bad_hall", "value": "invalid hall fixture"}, headers)

    # Give the async audit projection a short window to flush.
    time.sleep(0.2)
    behavior = get_json(f"{base_url.rstrip('/')}/api/v1/memory-behavior?hours=1&collection={collection}", headers)
    summary = behavior.get("summary", {})
    return {
        "label": label,
        "mode": "local",
        "collection": collection,
        "generated_at": now_iso(),
        "metrics": {
            "recall_before_save_rate": summary.get("recall_before_save_rate", 0.0),
            "repeat_save_rate": summary.get("repeat_save_rate", 0.0),
            "zero_result_recovery_rate": 0.0,
            "behavior_score": local_behavior_score(summary),
            "context_bytes": summary.get("context_bytes_per_trajectory", 0.0) * summary.get("total_trajectories", 0),
            "context_bytes_per_trajectory": summary.get("context_bytes_per_trajectory", 0.0),
            "memory_ops": summary.get("memory_ops", 0),
            "memory_ops_per_trajectory": summary.get("memory_ops_per_trajectory", 0.0),
            "blind_saves": sum(p.get("blind_saves", 0) for p in summary.get("problem_trajectories", [])),
            "repeat_saves": sum(p.get("repeat_saves", 0) for p in summary.get("problem_trajectories", [])),
            "unknown_hall_error_count": summary.get("unknown_hall_error_count", 0),
        },
        "raw": {"memory_behavior": behavior, "bad_hall_response": bad},
    }


def local_behavior_score(summary: dict[str, Any]) -> float:
    repeat_rate = float(summary.get("repeat_save_rate", 0.0) or 0.0)
    recall_rate = float(summary.get("recall_before_save_rate", 0.0) or 0.0)
    zero_rate = float(summary.get("zero_result_rate", 0.0) or 0.0)
    unknown_hall = int(summary.get("unknown_hall_error_count", 0) or 0)
    score = 100.0 - (max(0.0, 0.7 - recall_rate) * 50.0) - (repeat_rate * 25.0) - (zero_rate * 20.0) - (unknown_hall * 10.0)
    return round(max(0.0, score), 2)


def now_iso() -> str:
    return datetime.now(timezone.utc).isoformat()


def write_report(report: dict[str, Any], output: Path | None) -> Path:
    RESULTS_DIR.mkdir(parents=True, exist_ok=True)
    if output is None:
        stamp = datetime.now(timezone.utc).strftime("%Y%m%d_%H%M%S")
        label = report["label"].replace("/", "-")
        output = RESULTS_DIR / f"memory_behavior_eval_{label}_{stamp}.json"
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    latest = RESULTS_DIR / "memory_behavior_eval_latest.json"
    latest.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    return output


def build_fake_report(label: str) -> dict[str, Any]:
    scenarios = fake_scenarios()
    return {
        "label": label,
        "mode": "fake",
        "generated_at": now_iso(),
        "scenarios": [asdict(s) for s in scenarios],
        "metrics": analyze_scenarios(scenarios),
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--fake", action="store_true", help="run deterministic in-memory fixtures")
    parser.add_argument("--base-url", default="", help="Levara base URL for local canary, e.g. http://localhost:8081")
    parser.add_argument("--token", default="", help="optional Bearer token for auth-on Levara")
    parser.add_argument("--label", default="local", help="result label")
    parser.add_argument("--output", type=Path, default=None, help="output JSON path")
    parser.add_argument("--behavior-score-min", type=float, default=60.0, help="regression gate")
    args = parser.parse_args()

    try:
        if args.fake or not args.base_url:
            report = build_fake_report(args.label)
        else:
            report = run_local(args.base_url, args.label, args.token or None)
    except error.URLError as exc:
        print(f"memory behavior eval failed: {exc}", flush=True)
        return 2

    score = float(report["metrics"].get("behavior_score", 0.0))
    report["gates"] = {"behavior_score_min": args.behavior_score_min, "passed": score >= args.behavior_score_min}
    report["passed"] = bool(report["gates"]["passed"])
    path = write_report(report, args.output)
    print(json.dumps({"output": str(path), "passed": report["passed"], "metrics": report["metrics"]}, indent=2, sort_keys=True))
    return 0 if report["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
