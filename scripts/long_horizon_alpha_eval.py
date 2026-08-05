#!/usr/bin/env python3
"""Run reproducible acceptance scenarios against a live Levara MCP server."""

from __future__ import annotations

import argparse
import json
import threading
import time
import urllib.request
import uuid
from dataclasses import dataclass
from typing import Any, Callable


@dataclass
class MCP:
    url: str
    session_id: str = ""
    request_id: int = 0

    def request(self, method: str, params: dict[str, Any]) -> dict[str, Any]:
        self.request_id += 1
        payload = json.dumps({"jsonrpc": "2.0", "id": self.request_id, "method": method, "params": params}).encode()
        headers = {"Content-Type": "application/json", "Accept": "application/json, text/event-stream"}
        if self.session_id:
            headers["Mcp-Session-Id"] = self.session_id
        with urllib.request.urlopen(urllib.request.Request(self.url, data=payload, headers=headers), timeout=30) as response:
            if not self.session_id:
                self.session_id = response.headers.get("Mcp-Session-Id", "")
            result = json.loads(response.read())
        if "error" in result:
            raise AssertionError(result["error"])
        return result["result"]

    def initialize(self) -> None:
        result = self.request("initialize", {"protocolVersion": "2025-03-26", "capabilities": {}, "clientInfo": {"name": "long-horizon-alpha", "version": "1"}})
        assert result["toolset"]["name"] == "long-horizon"

    def call(self, name: str, arguments: dict[str, Any]) -> dict[str, Any]:
        result = self.request("tools/call", {"name": name, "arguments": arguments})
        text = result.get("content", [{}])[0].get("text", "{}")
        if result.get("isError"):
            raise AssertionError(f"{name}: {text}")
        decoded = json.loads(text)
        return decoded


def task_open(mcp: MCP, run: str, name: str, risk: str = "low", collection: str = "alpha-runtime") -> dict[str, Any]:
    return mcp.call("task_open", {
        "collection": collection,
        "room": "acceptance",
        "objective": f"Alpha scenario {name} completes with current evidence",
        "idempotency_key": f"{run}:{name}:open",
        "risk_level": risk,
        "definition_of_done": [{"criterion_id": "verified", "description": "Scenario invariant verified"}],
        "actor_id": "alpha-runner",
    })


def receipt(mcp: MCP, task: dict[str, Any], run: str, name: str, revision: str = "", receipt_type: str = "observation") -> dict[str, Any]:
    args: dict[str, Any] = {
        "task_id": task["task_id"], "base_version": task["version"],
        "idempotency_key": f"{run}:{name}:receipt", "receipt_type": receipt_type,
        "status": "pass", "criterion_ids": ["verified"], "observation": f"{name} verified",
        "actor_id": "alpha-runner",
    }
    if revision:
        args["workspace_revision"] = revision
    if receipt_type == "command":
        args["exit_code"] = 0
    result = mcp.call("task_receipt", args)
    task["version"] = result["version"]
    return result


def complete(mcp: MCP, task: dict[str, Any]) -> dict[str, Any]:
    validation = mcp.call("task_validate", {"task_id": task["task_id"], "mode": "completion"})
    assert validation["valid"], validation
    result = mcp.call("task_complete", {"task_id": task["task_id"], "expected_version": task["version"], "actor_id": "alpha-runner"})
    assert result["status"] == "completed", result
    return result


def scenario_basic(mcp: MCP, run: str) -> dict[str, Any]:
    task = task_open(mcp, run, "basic")
    receipt(mcp, task, run, "basic")
    return complete(mcp, task)


def scenario_stale_receipt(mcp: MCP, run: str) -> dict[str, Any]:
    task = task_open(mcp, run, "stale")
    receipt(mcp, task, run, "stale-r1", "rev-1", "command")
    checkpoint = mcp.call("task_checkpoint", {
        "task_id": task["task_id"], "base_version": task["version"], "idempotency_key": f"{run}:stale:checkpoint",
        "summary": "workspace changed after verification", "workspace_revision": "rev-2", "next_action": "rerun verification",
    })
    task["version"] = checkpoint["version"]
    invalid = mcp.call("task_validate", {"task_id": task["task_id"]})
    assert not invalid["valid"] and invalid["stale_receipts"] == ["verified"], invalid
    receipt(mcp, task, run, "stale-r2", "rev-2", "command")
    return complete(mcp, task)


def scenario_dependencies(mcp: MCP, run: str) -> dict[str, Any]:
    task = task_open(mcp, run, "dependencies")
    plan = mcp.call("task_plan", {"task_id": task["task_id"], "base_version": task["version"], "actor_id": "alpha-runner", "steps": [
        {"step_id": "first", "description": "first", "required": True},
        {"step_id": "second", "description": "second", "required": True, "dependencies": ["first"], "criterion_ids": ["verified"]},
    ]})
    task["version"] = plan["version"]
    blocked = mcp.request("tools/call", {"name": "task_step", "arguments": {"task_id": task["task_id"], "step_id": "second", "action": "claim", "base_version": task["version"], "actor_id": "alpha-runner"}})
    assert blocked.get("isError") or blocked.get("content", [{}])[0].get("text", "").startswith("Error")
    for step in ("first", "second"):
        claimed = mcp.call("task_step", {"task_id": task["task_id"], "step_id": step, "action": "claim", "base_version": task["version"], "actor_id": "alpha-runner"})
        task["version"] = claimed["version"]
        passed = mcp.call("task_step", {"task_id": task["task_id"], "step_id": step, "action": "pass", "base_version": task["version"], "actor_id": "alpha-runner"})
        task["version"] = passed["version"]
    receipt(mcp, task, run, "dependencies")
    return complete(mcp, task)


def scenario_idempotency(mcp: MCP, run: str) -> dict[str, Any]:
    task = task_open(mcp, run, "idempotency")
    replay = task_open(mcp, run, "idempotency")
    assert replay["task_id"] == task["task_id"] and replay["version"] == task["version"]
    first = receipt(mcp, task, run, "idempotency")
    replay_receipt = mcp.call("task_receipt", {"task_id": task["task_id"], "base_version": first["version"] - 1, "idempotency_key": f"{run}:idempotency:receipt", "receipt_type": "observation", "status": "pass", "criterion_ids": ["verified"]})
    assert replay_receipt["receipt_id"] == first["receipt_id"] and replay_receipt["idempotent_replay"]
    return complete(mcp, task)


def scenario_monitor_noop(mcp: MCP, run: str) -> dict[str, Any]:
    task = task_open(mcp, run, "monitor")
    first = mcp.call("task_bootstrap", {"task_id": task["task_id"], "max_tokens": 600})
    second = mcp.call("task_bootstrap", {"task_id": task["task_id"], "max_tokens": 600})
    assert first["version"] == second["version"] == task["version"]
    receipt(mcp, task, run, "monitor")
    return complete(mcp, task)


def scenario_collection_isolation(mcp: MCP, run: str) -> dict[str, Any]:
    marker_a, marker_b = f"{run}-only-a", f"{run}-only-b"
    mcp.call("save_memory", {"collection": f"{run}-a", "room": "acceptance", "hall": "fact", "key": marker_a, "value": marker_a})
    mcp.call("save_memory", {"collection": f"{run}-b", "room": "acceptance", "hall": "fact", "key": marker_b, "value": marker_b})
    task = task_open(mcp, run, "isolation", collection=f"{run}-a")
    bootstrap = mcp.call("task_bootstrap", {"task_id": task["task_id"], "max_tokens": 600})
    values = json.dumps(bootstrap["memories"])
    assert marker_b not in values and bootstrap["scope_status"] == "exact"
    receipt(mcp, task, run, "isolation")
    return complete(mcp, task)


def scenario_medium_policy(mcp: MCP, run: str) -> dict[str, Any]:
    task = task_open(mcp, run, "medium", "medium")
    receipt(mcp, task, run, "medium")
    validation = mcp.call("task_validate", {"task_id": task["task_id"]})
    assert validation["valid"] and not validation["audit_required"]
    return complete(mcp, task)


def scenario_high_policy(mcp: MCP, run: str) -> dict[str, Any]:
    task = task_open(mcp, run, "high", "high")
    receipt(mcp, task, run, "high-command", "rev-high", "command")
    validation = mcp.call("task_validate", {"task_id": task["task_id"]})
    assert not validation["valid"] and validation["audit_required"]
    receipt(mcp, task, run, "high-review", "rev-high", "reviewer")
    return complete(mcp, task)


def scenario_concurrency(mcp: MCP, run: str) -> dict[str, Any]:
    task = task_open(mcp, run, "concurrency")
    plan = mcp.call("task_plan", {"task_id": task["task_id"], "base_version": task["version"], "steps": [{"step_id": "single", "description": "single", "criterion_ids": ["verified"]}]})
    task["version"] = plan["version"]
    barrier = threading.Barrier(2)
    outcomes: list[tuple[str, bool, dict[str, Any]]] = []
    lock = threading.Lock()

    def claim(actor: str) -> None:
        client = MCP(mcp.url)
        client.initialize()
        barrier.wait()
        raw = client.request("tools/call", {"name": "task_step", "arguments": {"task_id": task["task_id"], "step_id": "single", "action": "claim", "base_version": task["version"], "actor_id": actor}})
        succeeded = not raw.get("isError", False)
        with lock:
            outcomes.append((actor, succeeded, raw))

    threads = [threading.Thread(target=claim, args=(actor,)) for actor in ("agent-a", "agent-b")]
    for thread in threads:
        thread.start()
    for thread in threads:
        thread.join()
    winners = [item for item in outcomes if item[1]]
    assert len(winners) == 1, outcomes
    winner, _, raw = winners[0]
    claimed = json.loads(raw["content"][0]["text"])
    task["version"] = claimed["version"]
    passed = mcp.call("task_step", {"task_id": task["task_id"], "step_id": "single", "action": "pass", "base_version": task["version"], "actor_id": winner})
    task["version"] = passed["version"]
    receipt(mcp, task, run, "concurrency")
    result = complete(mcp, task)
    result["claim_winner"] = winner
    return result


def scenario_memory_promotion(mcp: MCP, run: str) -> dict[str, Any]:
    task = task_open(mcp, run, "promotion")
    evidence = receipt(mcp, task, run, "promotion")
    checkpoint = mcp.call("task_checkpoint", {
        "task_id": task["task_id"], "base_version": task["version"], "idempotency_key": f"{run}:promotion:checkpoint",
        "summary": "promotion candidates captured", "memory_candidates": [
            {"key": f"{run}-decision", "value": "Verified alpha decision", "room": "acceptance", "hall": "decision", "evidence_receipt_ids": [evidence["receipt_id"]]},
            {"key": f"{run}-discovery", "value": "Verified alpha discovery", "room": "acceptance", "hall": "discovery", "evidence_receipt_ids": [evidence["receipt_id"]]},
            {"key": f"{run}-speculation", "value": "Unverified guess", "room": "acceptance", "hall": "discovery"},
        ],
    })
    task["version"] = checkpoint["version"]
    result = complete(mcp, task)
    assert result["promoted_memories"] == 2 and result["rejected_memories"] == 1, result
    recalled = mcp.call("recall_memory", {"collection": "alpha-runtime", "room": "acceptance", "query": run})
    values = json.dumps(recalled["results"])
    assert f"{run}-decision" in values and f"{run}-discovery" in values and f"{run}-speculation" not in values
    return result


SCENARIOS: list[tuple[str, Callable[[MCP, str], dict[str, Any]]]] = [
    ("basic", scenario_basic),
    ("stale_receipt", scenario_stale_receipt),
    ("dependencies", scenario_dependencies),
    ("idempotency", scenario_idempotency),
    ("monitor_noop", scenario_monitor_noop),
    ("collection_isolation", scenario_collection_isolation),
    ("medium_policy", scenario_medium_policy),
    ("high_policy", scenario_high_policy),
    ("concurrency", scenario_concurrency),
    ("memory_promotion", scenario_memory_promotion),
]


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--url", default="http://127.0.0.1:8081/mcp")
    parser.add_argument("--prepare-crash", action="store_true")
    parser.add_argument("--resume-crash", metavar="TASK_ID")
    parser.add_argument("--bootstrap-eval", action="store_true")
    args = parser.parse_args()
    run = f"alpha-{int(time.time())}-{uuid.uuid4().hex[:6]}"
    mcp = MCP(args.url)
    mcp.initialize()
    if args.prepare_crash:
        task = task_open(mcp, run, "crash-recovery")
        receipt(mcp, task, run, "crash-recovery", "rev-crash", "command")
        checkpoint = mcp.call("task_checkpoint", {
            "task_id": task["task_id"], "base_version": task["version"],
            "idempotency_key": f"{run}:crash:checkpoint", "summary": "durable before restart",
            "workspace_revision": "rev-crash", "next_action": "complete after restart",
        })
        print(json.dumps({"task_id": task["task_id"], "version": checkpoint["version"], "checkpoint_id": checkpoint["checkpoint_id"]}, sort_keys=True))
        return 0
    if args.resume_crash:
        bootstrap = mcp.call("task_bootstrap", {"task_id": args.resume_crash, "max_tokens": 600})
        assert bootstrap["last_checkpoint"] and bootstrap["last_checkpoint"][0]["summary"] == "durable before restart", bootstrap
        result = mcp.call("task_complete", {"task_id": args.resume_crash, "expected_version": bootstrap["version"], "actor_id": "alpha-recovery"})
        assert result["status"] == "completed", result
        print(json.dumps({"task_id": args.resume_crash, "status": result["status"], "checkpoint_recovered": True}, sort_keys=True))
        return 0
    if args.bootstrap_eval:
        collection = f"{run}-bootstrap"
        relevant_prefix = f"{run}-relevant"
        for index in range(10):
            mcp.call("save_memory", {
                "collection": collection, "room": "acceptance", "hall": ("decision", "discovery", "fact")[index % 3],
                "key": f"{relevant_prefix}-{index}", "value": f"Relevant annotated memory {index}",
            })
            mcp.call("save_memory", {
                "collection": collection, "room": "unrelated", "hall": "fact",
                "key": f"{run}-irrelevant-{index}", "value": f"Irrelevant annotated memory {index}",
            })
        task = task_open(mcp, run, "bootstrap-relevance", collection=collection)
        bootstrap = mcp.call("task_bootstrap", {"task_id": task["task_id"], "max_tokens": 600})
        returned = bootstrap["memories"]
        relevant = sum(str(item.get("key", "")).startswith(relevant_prefix) for item in returned)
        precision = relevant / len(returned) if returned else 0.0
        assert precision >= 0.9 and bootstrap["tokens_used"] <= 600, bootstrap
        receipt(mcp, task, run, "bootstrap-relevance")
        complete(mcp, task)
        print(json.dumps({
            "task_id": task["task_id"], "returned_memories": len(returned), "relevant_memories": relevant,
            "precision": precision, "tokens_used": bootstrap["tokens_used"], "token_budget": 600,
        }, sort_keys=True))
        return 0
    started = time.time()
    results: list[dict[str, Any]] = []
    for name, scenario in SCENARIOS:
        before = time.time()
        try:
            detail = scenario(mcp, run)
            results.append({"name": name, "status": "pass", "duration_ms": round((time.time() - before) * 1000), "task_id": detail.get("task_id")})
        except Exception as exc:  # keep the report complete
            results.append({"name": name, "status": "fail", "duration_ms": round((time.time() - before) * 1000), "error": str(exc)})
            break
    report = {
        "run_id": run,
        "server": args.url,
        "started_at_epoch": int(started),
        "duration_ms": round((time.time() - started) * 1000),
        "passed": sum(item["status"] == "pass" for item in results),
        "failed": sum(item["status"] == "fail" for item in results),
        "scenarios": results,
    }
    print(json.dumps(report, indent=2, sort_keys=True))
    return 0 if report["failed"] == 0 and report["passed"] == len(SCENARIOS) else 1


if __name__ == "__main__":
    raise SystemExit(main())
