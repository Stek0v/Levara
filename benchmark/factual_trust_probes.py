#!/usr/bin/env python3
"""Public-MCP trust diagnostics. Writes only to an explicitly approved isolated server.

Run --self-check offline first. Exit 2 means a quality gap or an inconclusive/error
probe, never a successful certification. Receipts prove structure, not fact truth.
"""
from __future__ import annotations

import argparse
import asyncio
from datetime import datetime, timezone
import hashlib
import ipaddress
import json
from pathlib import Path
import subprocess
import sys
import time
from urllib.parse import urlsplit
import uuid


ROOT = Path(__file__).resolve().parents[1]
TRUSTED = {"verified", "receipt-validated"}
PROVENANCE = {"verification_status", "source_task_id", "source_receipt_ids"}
PLANNED = [
    "save_forged_evidence", "supersede_without_evidence",
    "promotion_failed_command", "completion_missing_exit_code",
    "provenance_recall", "provenance_list", "provenance_wake_up",
    "commit_unverified_control", "commit_valid_receipt_control",
]


class ProbeError(RuntimeError):
    """Missing evidence or an invalid experiment; cannot count as a pass."""


class ToolRejected(ProbeError):
    pass


def isolated_url(value):
    parsed = urlsplit(value)
    try:
        local = parsed.hostname == "localhost" or ipaddress.ip_address(parsed.hostname).is_loopback
    except (ValueError, TypeError):
        local = False
    if (parsed.scheme != "http" or not local or not parsed.port
            or parsed.port in {8080, 8081} or parsed.username or parsed.password
            or parsed.path not in {"", "/"} or parsed.query or parsed.fragment):
        raise ValueError("--url must be an explicit HTTP loopback port, excluding 8080/8081")
    return value.rstrip("/")


def decode_result(envelope):
    if not isinstance(envelope, dict):
        raise ProbeError("JSON-RPC response is not an object")
    if "error" in envelope:
        raise ToolRejected(json.dumps(envelope["error"], ensure_ascii=False))
    result = envelope.get("result")
    if not isinstance(result, dict):
        raise ProbeError("JSON-RPC result is missing or malformed")
    if result.get("isError"):
        raise ToolRejected(json.dumps(result, ensure_ascii=False))
    if isinstance(result.get("structuredContent"), dict):
        return result["structuredContent"]
    content = result.get("content", [])
    if len(content) != 1 or content[0].get("type") != "text":
        raise ProbeError("Expected one JSON text result or structuredContent")
    try:
        value = json.loads(content[0]["text"])
    except (ValueError, KeyError) as exc:
        raise ProbeError("Tool result does not contain JSON") from exc
    if not isinstance(value, dict):
        raise ProbeError("Tool JSON result is not an object")
    return value


def trust_outcome(row, expected="unverified"):
    if row is None:
        return "fail", "missing_memory"
    if "verification_status" not in row:
        return "fail", "provenance_omission"
    status = row["verification_status"]
    if expected == "unverified":
        if status in TRUSTED:
            return "fail", "trust_escalation"
        return ("pass", "no_trust_escalation") if status == "unverified" else ("fail", "unknown_verification_status")
    return ("pass", "receipt_validated") if status == expected else ("fail", "wrong_verification_status")


def provenance_outcome(row):
    if row is None:
        return "fail", "missing_memory"
    if row.get("verification_status") in TRUSTED:
        return "fail", "trust_falsification"
    if not PROVENANCE.issubset(row):
        return "fail", "provenance_omission"
    if (row["verification_status"] != "unverified" or row["source_task_id"] != ""
            or row["source_receipt_ids"] != []):
        return "fail", "provenance_changed"
    return "pass", "provenance_preserved"


def promotion_outcome(completion, row, require_completion):
    if completion.get("ok") is not True:
        if require_completion:
            return "error", "positive_completion_control_failed"
        validation = completion.get("validation")
        if isinstance(validation, dict) and validation.get("valid") is False:
            receipt_blocked = "command" in validation.get("missing_receipts", []) or "command" in validation.get("failed_receipts", [])
            unrelated = any(validation.get(field) for field in ("active_blockers", "active_leases", "incomplete_steps", "stale_receipts", "audit_required"))
            if receipt_blocked and not unrelated:
                return "pass", "invalid_receipt_cannot_complete"
        return "error", "unexplained_completion_failure"
    if not require_completion:
        return "fail", "missing_exit_certified_completion"
    if row is None and completion.get("promoted_memories") == 0 and completion.get("rejected_memories") == 1:
        return "pass", "unsupported_candidate_rejected"
    if row is None:
        return "error", "promotion_result_not_observable"
    return trust_outcome(row)


def self_check():
    unverified = {"verification_status": "unverified", "source_task_id": "", "source_receipt_ids": []}
    for forged in TRUSTED:
        assert trust_outcome({**unverified, "verification_status": forged})[0] == "fail"
        assert provenance_outcome({**unverified, "verification_status": forged}) == ("fail", "trust_falsification")
    assert trust_outcome(unverified)[0] == "pass"
    for unknown in (None, "", "unknown"):
        assert trust_outcome({"verification_status": unknown})[0] == "fail"
    assert trust_outcome({}) == ("fail", "provenance_omission")
    assert provenance_outcome(unverified)[0] == "pass"
    assert provenance_outcome({"verification_status": "unverified"})[0] == "fail"
    assert trust_outcome(unverified, "receipt-validated")[0] == "fail"
    assert promotion_outcome({"ok": True}, {"verification_status": "verified"}, True)[0] == "fail"
    assert promotion_outcome({"ok": True}, None, False)[0] == "fail"
    assert promotion_outcome({"ok": False, "validation": {"valid": False, "failed_receipts": ["command"]}}, None, False)[0] == "pass"
    assert promotion_outcome({"ok": False, "validation": {"valid": False}}, None, False)[0] == "error"
    assert promotion_outcome({"ok": False, "validation": {"valid": False, "failed_receipts": ["command"], "active_blockers": ["other"]}}, None, False)[0] == "error"
    assert promotion_outcome({"ok": False}, None, True)[0] == "error"
    assert Suite.evidence_rejection(ToolRejected("Error: source evidence is missing, inaccessible, failed, or stale"))
    for message in ("database is closed", "connection refused", "context deadline exceeded"):
        assert not Suite.evidence_rejection(ToolRejected(message))
    async def check_rejected_write():
        for persisted, control, expected in (
            ({"key": "forged-source-marker"}, unverified, "rejected_write_persisted"),
            (None, {**unverified, "value": "wrong"}, "rejection_positive_control_failed"),
            (None, {**unverified, "value": "Synthetic evidence-free positive control.", "verification_status": "verified"}, "rejection_positive_control_failed"),
            (None, {**unverified, "value": "Synthetic evidence-free positive control."}, "invalid_evidence_rejected"),
        ):
            suite = object.__new__(Suite)
            suite.run_id = "self-check"
            async def save(*args, **kwargs):
                if "source_task_id" in kwargs:
                    raise ToolRejected("Error: source evidence is missing, inaccessible, failed, or stale")
            async def row(*args, required=True):
                return control if required else persisted
            suite.save, suite.row = save, row
            status, reason, _ = await suite.forged()
            assert reason == expected
            assert status == ("pass" if expected == "invalid_evidence_rejected" else "fail")
    asyncio.run(check_rejected_write())
    for envelope in [{"error": {"message": "failure"}}, {"result": {"isError": True}}]:
        try:
            decode_result(envelope)
        except ToolRejected:
            pass
        else:
            raise AssertionError("MCP error silently accepted")
    assert decode_result({"result": {"structuredContent": {"ok": True}}}) == {"ok": True}
    for url in ["http://localhost:8080", "http://127.0.0.1:8081", "http://example.com:8900", "http://127.0.0.1", "http://user@127.0.0.1:8900"]:
        try:
            isolated_url(url)
        except ValueError:
            pass
        else:
            raise AssertionError("Unsafe endpoint accepted")
    assert isolated_url("http://127.0.0.1:18900") == "http://127.0.0.1:18900"
    print("factual trust scorer, strict MCP decoding, and endpoint guards: PASS")


class Suite:
    def __init__(self, args):
        self.args = args
        self.run_id = uuid.uuid4().hex
        self.results = []
        self.events = []
        self.secrets = [uuid.uuid4().hex + "Trust_2026!"]
        self.output = Path(args.output_dir)
        self.output.mkdir(parents=True, exist_ok=True)
        self.evidence = self.output / "trust-evidence.jsonl"
        if self.evidence.exists() or (self.output / "trust-report.json").exists():
            raise ProbeError("Output contains an earlier trust run; use a fresh --output-dir")
        self.evidence.touch(mode=0o600)
        self.client = None

    def scrub(self, value):
        if isinstance(value, dict):
            return {key: "[REDACTED]" if key.lower() in {"password", "token", "access_token", "authorization", "session_id", "_session_id"}
                    else self.scrub(item) for key, item in value.items()}
        if isinstance(value, list):
            return [self.scrub(item) for item in value]
        if isinstance(value, str):
            for secret in self.secrets:
                if secret:
                    value = value.replace(secret, "[REDACTED]")
        return value

    def record(self, event):
        event = self.scrub({"sequence": len(self.events) + 1, "at": datetime.now(timezone.utc).isoformat(), **event})
        self.events.append(event)
        with self.evidence.open("a") as stream:
            stream.write(json.dumps(event, ensure_ascii=False) + "\n")

    async def call(self, name, args):
        start = time.monotonic()
        try:
            envelope = await asyncio.wait_for(self.client._rpc("tools/call", {"name": name, "arguments": args}), 60)
        except Exception as exc:
            self.record({"tool": name, "arguments": args, "transport_error": str(exc), "latency_ms": (time.monotonic() - start) * 1000})
            raise
        self.record({"tool": name, "arguments": args, "response": envelope, "latency_ms": (time.monotonic() - start) * 1000})
        return decode_result(envelope)

    def collection(self, suffix):
        return f"trust_{self.run_id}_{suffix}"

    async def save(self, collection, key, value, **extra):
        result = await self.call("save_memory", {"collection": collection, "key": key, "value": value, "room": "trust-test", "hall": "fact", **extra})
        if result.get("ok") is not True:
            raise ProbeError("save_memory did not acknowledge persistence")

    async def row(self, collection, key, tool="recall_memory", required=True):
        args = {"collection": collection}
        field = {"recall_memory": "results", "list_memories": "memories", "wake_up": "pinned"}[tool]
        if tool == "recall_memory":
            args["query"] = key
        elif tool == "wake_up":
            args["max_tokens"] = 2000
        for attempt in range(5 if required and tool == "recall_memory" else 1):
            result = await self.call(tool, args)
            rows = result.get(field)
            if not isinstance(rows, list):
                raise ProbeError(f"{tool} did not return a {field} array")
            matches = [item for item in rows if item.get("key") == key]
            if len(matches) > 1:
                raise ProbeError(f"{tool} returned duplicate active keys")
            if matches:
                return matches[0]
            if attempt < 4 and required and tool == "recall_memory":
                await asyncio.sleep(0.3)
        if required:
            raise ProbeError(f"{tool} cannot observe exact memory {key}")
        return None

    @staticmethod
    def evidence_rejection(exc):
        message = str(exc).lower()
        return any(word in message for word in ("invalid evidence", "verification status", "verification_status", "source receipt", "source_receipt", "exit_code", "exit code", "source evidence is missing, inaccessible, failed, or stale"))

    async def forged(self):
        collection, key = self.collection("forged"), "forged-source-marker"
        try:
            await self.save(collection, key, "Synthetic claim without any source evidence.",
                            source_task_id=str(uuid.uuid4()), source_receipt_ids=[str(uuid.uuid4())], verification_status="receipt-validated")
        except ToolRejected as exc:
            if self.evidence_rejection(exc):
                if await self.row(collection, key, "list_memories", required=False) is not None:
                    return "fail", "rejected_write_persisted", {"rejection": str(exc)}
                # A healthy write on the same scope must still work after rejection.
                await self.save(collection, key, "Synthetic evidence-free positive control.")
                control = await self.row(collection, key, "list_memories")
                if provenance_outcome(control)[0] != "pass" or control.get("value") != "Synthetic evidence-free positive control.":
                    return "fail", "rejection_positive_control_failed", {"memory": control}
                return "pass", "invalid_evidence_rejected", {"rejection": str(exc), "absent_after_rejection": True, "positive_control": control}
            raise
        row = await self.row(collection, key)
        return *trust_outcome(row), {"memory": row}

    async def supersede(self):
        collection, key = self.collection("supersede"), "superseded-unverified-marker"
        await self.save(collection, key, "Synthetic old value.", verification_status="unverified")
        old = await self.row(collection, key, "list_memories")
        await self.call("supersede_memory", {"old_memory_id": old["id"], "new_value": "Synthetic replacement without evidence.", "reason": "Isolated negative trust control."})
        row = await self.row(collection, key)
        if row.get("id") == old["id"] or row.get("value") != "Synthetic replacement without evidence.":
            raise ProbeError("Supersession was not observable")
        return *trust_outcome(row), {"memory": row}

    async def task(self, collection):
        return await self.call("task_open", {
            "collection": collection, "room": "trust-test", "objective": "Isolated receipt trust diagnostic",
            "idempotency_key": str(uuid.uuid4()), "risk_level": "low",
            "authority": {"scope": "Synthetic isolated test data only"},
            "definition_of_done": [{"criterion_id": "command", "description": "Command has explicit observed exit zero", "required": True}],
        })

    async def receipt(self, task, exit_code, omit_exit=False):
        observed = subprocess.run([sys.executable, "-c", f"raise SystemExit({exit_code})"], capture_output=True, text=True, check=False)
        self.record({"local_command": [sys.executable, "-c", f"raise SystemExit({exit_code})"], "exit_code": observed.returncode})
        args = {"task_id": task["task_id"], "base_version": task["version"], "idempotency_key": str(uuid.uuid4()),
                "receipt_type": "command", "status": "pass", "criterion_ids": ["command"],
                "workspace_revision": "synthetic-rev1", "observation": f"Negative-control caller status pass; observed process exit {observed.returncode}."}
        if not omit_exit:
            args["exit_code"] = observed.returncode
        result = await self.call("task_receipt", args)
        task["version"] = result["version"]
        return result["receipt_id"]

    async def promotion(self, missing_exit=False):
        collection = self.collection("missingexit" if missing_exit else "failedcommand")
        key = "unsupported-candidate-marker"
        task = await self.task(collection)
        try:
            bad_receipt = await self.receipt(task, 0 if missing_exit else 7, omit_exit=missing_exit)
        except ToolRejected as exc:
            if self.evidence_rejection(exc):
                return "pass", "invalid_command_receipt_rejected", {"rejection": str(exc)}
            raise
        if not missing_exit:
            await self.receipt(task, 0)
        checkpoint = await self.call("task_checkpoint", {"task_id": task["task_id"], "base_version": task["version"],
            "idempotency_key": str(uuid.uuid4()), "summary": "Probe deliberately cites only the invalid command receipt.",
            "workspace_revision": "synthetic-rev1", "memory_candidates": [{"key": key, "value": "Synthetic claim unsupported by a valid cited receipt.",
                "room": "trust-test", "hall": "fact", "evidence_receipt_ids": [bad_receipt]}]})
        completion = await self.call("task_complete", {"task_id": task["task_id"], "expected_version": checkpoint["version"]})
        listed = await self.row(collection, key, "list_memories", required=False)
        row = await self.row(collection, key) if listed else None
        return *promotion_outcome(completion, row, not missing_exit), {"task_id": task["task_id"], "cited_receipt_id": bad_receipt, "completion": completion, "memory": row}

    async def provenance(self, tool):
        collection, key = self.collection(tool), "pinned-unverified-marker"
        value = "Synthetic unverified value; pinning must not imply evidence."
        await self.save(collection, key, value, verification_status="unverified", pin=True, pin_priority=10)
        row = await self.row(collection, key, tool)
        if row.get("value") != value:
            return "fail", "value_changed", {"memory": row}
        return *provenance_outcome(row), {"memory": row}

    async def commit_control(self, validated=False):
        collection, key = self.collection("commitvalid" if validated else "commitunverified"), "prepared-candidate-marker"
        candidate = {"candidate_id": "candidate", "key": key, "value": "Synthetic structurally checked candidate.",
                     "room": "trust-test", "hall": "fact", "verification_status": "verified"}
        if validated:
            task = await self.task(collection)
            receipt_id = await self.receipt(task, 0)
            candidate.update(source_task_id=task["task_id"], source_receipt_ids=[receipt_id])
        preview = await self.call("memory_commit_preview", {"collection": collection, "idempotency_key": str(uuid.uuid4()), "candidates": [candidate]})
        items = preview.get("items", [])
        if len(items) != 1 or items[0].get("action") != "add":
            raise ProbeError(f"Positive commit control not accepted: {items}")
        applied = await self.call("memory_commit_apply", {"commit_id": preview["commit_id"], "plan_digest": preview["plan_digest"], "accepted_candidate_ids": ["candidate"]})
        if applied.get("added") != 1:
            raise ProbeError("Prepared commit did not add exactly one memory")
        row = await self.row(collection, key)
        expected = "receipt-validated" if validated else "unverified"
        outcome = trust_outcome(row, expected)
        if validated and (row.get("source_task_id") != candidate["source_task_id"] or row.get("source_receipt_ids") != candidate["source_receipt_ids"]):
            outcome = ("fail", "source_evidence_changed")
        return *outcome, {"preview": preview, "applied": applied, "memory": row}

    async def run(self):
        # Existing project dependencies only; offline scorer needs no HTTP packages.
        setup_error = None
        try:
            sys.path.insert(0, str(ROOT / "tests"))
            from conftest_mcp import MCPTestClient
            import aiohttp

            async def reject_redirect(session, context, params):
                raise ProbeError("HTTP redirects are forbidden for isolated write probes")

            trace = aiohttp.TraceConfig()
            trace.on_request_redirect.append(reject_redirect)
            self.client = MCPTestClient(self.args.url)
            self.client._http = aiohttp.ClientSession(timeout=aiohttp.ClientTimeout(total=60), trace_configs=[trace])
            token = await self.client.login_or_register(f"trust-{self.run_id}@bench.local", self.secrets[0])
            self.secrets.append(token)
            initialized = await self.client.connect()
            self.record({"initialize": initialized})
            if initialized.get("error") or not isinstance(initialized.get("result"), dict):
                raise ProbeError("MCP initialize failed")
            listed = await self.client._rpc("tools/list")
            self.record({"tools_list": listed})
            if listed.get("error"):
                raise ProbeError("MCP tools/list failed")
            available = {item["name"] for item in listed.get("result", {}).get("tools", [])}
            self.record({"available_tools": sorted(available)})
            callbacks = [self.forged, self.supersede, self.promotion, lambda: self.promotion(True),
                         lambda: self.provenance("recall_memory"), lambda: self.provenance("list_memories"),
                         lambda: self.provenance("wake_up"), self.commit_control, lambda: self.commit_control(True)]
            for name, callback in zip(PLANNED, callbacks):
                first_event = len(self.events) + 1
                try:
                    status, reason, details = await callback()
                except Exception as exc:
                    status, reason, details = "error", type(exc).__name__, {"error": self.scrub(str(exc))}
                self.results.append({"probe": name, "status": status, "reason": reason,
                                     "evidence_sequence": [first_event, len(self.events)], **details})
                print(f"{name}: {status} ({reason})", flush=True)
        except Exception as exc:
            setup_error = self.scrub(f"{type(exc).__name__}: {exc}")
        finally:
            if self.client:
                await self.client.close()
        counts = {state: sum(item["status"] == state for item in self.results) for state in ("pass", "fail", "error")}
        report = {"schema_version": 1, "run_id": self.run_id, "url": self.args.url,
                  "script_sha256": hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
                  "planned": len(PLANNED), "planned_probes": PLANNED, "executed": len(self.results),
                  **counts, "not_executed": len(PLANNED) - len(self.results), "setup_error": setup_error,
                  "results": self.results, "evidence_file": self.evidence.name,
                  "limitations": ["Synthetic public-MCP trust diagnostics, not world-fact verification.",
                      "Receipts are caller-submitted; validation does not prove semantic support or actual execution.",
                      "No SQL writes/tampering; generated isolated users and data retained for audit.",
                      "Provenance omission and explicit trust escalation are distinct failure classes."]}
        (self.output / "trust-report.json").write_text(json.dumps(self.scrub(report), ensure_ascii=False, indent=2) + "\n")
        print(json.dumps({key: report[key] for key in ("planned", "executed", "pass", "fail", "error", "not_executed", "setup_error")}, ensure_ascii=False))
        return 0 if counts["pass"] == len(PLANNED) and not setup_error else 2


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--url", help="Required explicit isolated HTTP loopback server, never 8080/8081")
    parser.add_argument("--allow-isolated-writes", action="store_true")
    parser.add_argument("--output-dir")
    parser.add_argument("--self-check", action="store_true")
    args = parser.parse_args()
    if args.self_check:
        self_check()
        return 0
    if not args.url or not args.output_dir or not args.allow_isolated_writes:
        parser.error("Live probes require --url, --output-dir, and --allow-isolated-writes")
    try:
        args.url = isolated_url(args.url)
        return asyncio.run(Suite(args).run())
    except (ValueError, ProbeError) as exc:
        parser.error(str(exc))


if __name__ == "__main__":
    raise SystemExit(main())
