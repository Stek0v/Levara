#!/usr/bin/env python3
"""Strict synthetic recall diagnostic; writes only to an explicitly selected isolated server.

python3 benchmark/factual_quality.py --self-check
python3 benchmark/factual_quality.py --url http://127.0.0.1:18124 \
    --allow-isolated-writes --output-dir /tmp/levara-facts

Reuses the repository MCPTestClient transport and its already installed dependencies.
No model downloads, production defaults, LLM judge, or answer generation.
"""
import argparse
import asyncio
import base64
from collections import Counter
from datetime import datetime, timezone
import hashlib
import importlib.util
import ipaddress
import json
from pathlib import Path
import secrets
import subprocess
import time
from urllib.parse import urlsplit
import uuid

ROOT = Path(__file__).resolve().parents[1]
FIXTURE = Path(__file__).with_name("factual_quality_cases.json")
KS = (1, 3, 5, 10)
PROBES = ("owner_isolation", "collection_isolation", "owner_positive_control",
          "reconnect", "upsert", "supersede", "superseded_history", "delete")


class InfrastructureError(RuntimeError):
    pass


def load_fixture(path=FIXTURE):
    data = json.loads(Path(path).read_text())
    facts = {f["key"]: f for f in data["facts"]}
    assert data["schema_version"] == 1
    assert len(facts) == len(data["facts"]) >= 40
    assert len({c["id"] for c in data["cases"]}) == len(data["cases"]) >= 30
    for fact in facts.values():
        assert all(isinstance(fact[k], str) and fact[k] for k in ("key", "value", "room", "hall"))
        assert fact["hall"] in {"fact", "event", "decision", "preference", "advice", "discovery"}
    for case in data["cases"]:
        assert set(case["expected_keys"]) <= facts.keys()
        assert len(case["expected_keys"]) == len(set(case["expected_keys"]))
        assert case["answer_mode"] in {"answer", "unknown", "conflict"}
        assert bool(case["expected_keys"]) == (case["answer_mode"] != "unknown")
        assert case["query"] and case["expected_answers"]
        if case["answer_mode"] == "conflict":
            assert len(case["expected_keys"]) >= 2
        for key in case["expected_keys"]:
            assert all(facts[key][k] == case[k] for k in ("room", "hall") if k in case)
    return data


def validate_url(url):
    parsed = urlsplit(url)
    host = parsed.hostname
    local = host == "localhost"
    try:
        local = local or ipaddress.ip_address(host or "").is_loopback
    except ValueError:
        pass
    if (parsed.scheme != "http" or not local or parsed.port in (None, 80, 443, 8080, 8081)
            or parsed.username or parsed.password or parsed.path not in ("", "/")
            or parsed.query or parsed.fragment):
        raise ValueError("Use an explicit isolated loopback HTTP port; 8080/8081 and remote URLs are forbidden")
    return url.rstrip("/")


def rpc_result(response):
    if not isinstance(response, dict) or response.get("error") is not None:
        raise InfrastructureError("JSON-RPC error: " + str(response.get("error") if isinstance(response, dict) else "invalid response"))
    if "result" not in response:
        raise InfrastructureError("JSON-RPC response has no result")
    return response["result"]


def tool_payload(result):
    if not isinstance(result, dict) or result.get("isError"):
        raise InfrastructureError("MCP tool error: " + str(result))
    payload = result.get("structuredContent")
    if payload is None:
        try:
            payload = json.loads(result["content"][0]["text"])
        except (KeyError, IndexError, TypeError, json.JSONDecodeError) as error:
            raise InfrastructureError("MCP tool returned no JSON payload") from error
    if not isinstance(payload, dict):
        raise InfrastructureError("MCP payload is not an object")
    return payload


def recall_rows(payload):
    if not isinstance(payload, dict) or not isinstance(payload.get("results"), list):
        raise InfrastructureError("recall payload must contain an explicit results array")
    return payload["results"]


def score_case(case, rows, gold, collection, owner_id):
    """No key-only credit: canonical ID, complete value and scope must all match."""
    if not isinstance(rows, list):
        raise InfrastructureError("recall results must be an array")
    by_id = {fact["id"]: fact for fact in gold.values()}
    relevant = set(case["expected_keys"])
    seen, hits, violations = set(), [], []
    for rank, row in enumerate(rows, 1):
        if not isinstance(row, dict):
            violations.append({"rank": rank, "reason": "invalid_row"})
            continue
        ident = row.get("id")
        if ident in seen:
            violations.append({"rank": rank, "reason": "duplicate_id", "id": ident})
            continue
        seen.add(ident)
        fact = by_id.get(ident)
        reasons = []
        if not fact:
            reasons.append("foreign_or_unknown_id")
        else:
            for field in ("key", "value", "owner_id", "room", "hall"):
                if row.get(field) != fact[field]:
                    reasons.append("incorrect_" + field)
            if fact["collection"] != collection or row.get("collection", collection) != collection:
                reasons.append("foreign_collection")
            if row.get("owner_id") != owner_id:
                reasons.append("foreign_owner")
            for field in ("room", "hall"):
                if case.get(field) and row.get(field) != case[field]:
                    reasons.append("filter_" + field)
            if row.get("superseded_by") or row.get("supersession_state", "active") != "active":
                reasons.append("superseded_active_result")
        if reasons:
            violations.append({"rank": rank, "id": ident, "reason": reasons})
        elif fact["key"] in relevant:
            hits.append((rank, fact["key"]))
    recall = {str(k): len({key for rank, key in hits if rank <= k}) / len(relevant)
              if relevant else None for k in KS}
    precision = {str(k): len({key for rank, key in hits if rank <= k}) / k for k in KS}
    all_gold = recall["5"] == 1.0 if relevant else None
    return {"recall": recall, "precision": precision,
            "mrr": 1 / hits[0][0] if hits else 0.0 if relevant else None,
            "all_gold_at_5": all_gold, "violations": violations,
            "nonempty_unknown": bool(rows) if case["answer_mode"] == "unknown" else None,
            "passed": not violations and (all_gold is not False)}


def write_json(path, value):
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n")


def client_class():
    spec = importlib.util.spec_from_file_location("levara_mcp_test_transport", ROOT / "tests/conftest_mcp.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)

    class StrictClient(module.MCPTestClient):
        async def _rpc(self, method, params=None):
            self.last_rpc_response = None
            response = await asyncio.wait_for(super()._rpc(method, params), timeout=90)
            self.last_rpc_response = response
            rpc_result(response)
            return response

        async def call(self, name, **arguments):
            start = time.perf_counter()
            raw = await self.call_tool(name, arguments)
            return tool_payload(raw), raw, round((time.perf_counter() - start) * 1000, 3)

    return StrictClient


async def wait_index(client, job_ids, timeout):
    if not job_ids or any(not ident for ident in job_ids):
        raise InfrastructureError("No durable indexing job IDs: semantic readiness cannot be established")
    deadline = time.monotonic() + timeout
    while True:
        payload, _, _ = await client.call("memory_index_status", limit=100)
        jobs = {job["id"]: job for job in payload.get("jobs") or []}
        selected = [jobs[ident] for ident in job_ids if ident in jobs]
        if len(selected) == len(job_ids) and all(j["status"] == "completed" for j in selected):
            return selected
        if any(j["status"] == "dead_letter" for j in selected):
            raise InfrastructureError("Indexing entered dead_letter: " + str(selected))
        if time.monotonic() >= deadline:
            raise InfrastructureError("Index readiness timed out: " + str({k: jobs.get(k, {}).get("status", "missing") for k in job_ids}))
        await asyncio.sleep(0.5)


async def seed(client, facts, collection, timeout):
    jobs = []
    for fact in facts:
        result, _, _ = await client.call("save_memory", collection=collection, **fact)
        if result.get("ok") is not True:
            raise InfrastructureError("save_memory did not confirm persistence")
        jobs.append(result.get("index_job_id"))
    return await wait_index(client, jobs, timeout)


async def read_gold(client, facts, collection, owner):
    payload, _, _ = await client.call("list_memories", collection=collection)
    rows = {row["key"]: row for row in payload.get("memories", [])}
    gold = {}
    for fact in facts:
        row = rows.get(fact["key"])
        if not row or any(row.get(field) != fact[field] for field in ("key", "value", "room", "hall")) or row.get("owner_id") != owner:
            raise InfrastructureError("Seed SQL read-back differs for " + fact["key"])
        gold[fact["key"]] = {**fact, "id": row["id"], "owner_id": owner, "collection": collection}
    return gold


def owner_from_token(token):
    # Identity is read only from the server-issued credential, never supplied as tool authorization.
    part = token.split(".")[1]
    owner = json.loads(base64.urlsafe_b64decode(part + "=" * (-len(part) % 4)))["sub"]
    if not owner or owner == "dev-user":
        raise InfrastructureError("Real database-backed JWT identities are required")
    return owner


async def run(args, fixture, report):
    Client = client_class()
    clients = []
    collection = "factual_" + uuid.uuid4().hex[:12]
    other_collection = collection + "_other"
    report["collection"] = collection
    queries = {"schema_version": 1, "collection": collection, "fixture_sha256": report["fixture_sha256"],
               "git_revision": report["git_revision"], "config": report["config"], "cases": []}
    report["queries"] = queries["cases"]
    report["probes"] = [{"id": name, "status": "planned"} for name in PROBES]
    operations = []

    async def call(client, name, **arguments):
        payload, raw, latency = await client.call(name, **arguments)
        operations.append({"tool": name, "arguments": arguments, "raw_response": raw, "latency_ms": latency})
        return payload

    async def probe(name, check):
        item = next(p for p in report["probes"] if p["id"] == name)
        start = time.perf_counter()
        try:
            await check()
            item["status"] = "pass"
        except AssertionError as error:
            item.update(status="fail", error=str(error))
        except Exception as error:
            item.update(status="error", error=f"{type(error).__name__}: {error}")
        item["latency_ms"] = round((time.perf_counter() - start) * 1000, 3)

    try:
        for name in ("alice", "bob"):
            client = Client(args.url)
            clients.append(client)
            token = await asyncio.wait_for(client.login_or_register(
                f"{name}-{uuid.uuid4().hex}@factual.invalid", secrets.token_urlsafe(32)), timeout=30)
            client.test_owner = owner_from_token(token)
            await client.connect()
        alice, bob = clients
        if alice.test_owner == bob.test_owner:
            raise InfrastructureError("Alice and Bob resolved to the same owner")
        report["runtime_before"] = await call(alice, "runtime_stats")
        report["index_jobs"] = await seed(alice, fixture["facts"], collection, args.index_timeout)
        gold = await read_gold(alice, fixture["facts"], collection, alice.test_owner)
        write_json(args.output_dir / "gold.json", gold)
        bob_fact = {"key": "f001", "value": "Секрет владельца Боб: пароль вымышленного шлюза — гранатовый туман.", "room": "private", "hall": "fact"}
        other_fact = {"key": "f001", "value": "В соседней коллекции паром Лира отправляется в 22:44.", "room": "transport", "hall": "fact"}
        await seed(bob, [bob_fact], collection, args.index_timeout)
        await seed(alice, [other_fact], other_collection, args.index_timeout)
        bob_gold = await read_gold(bob, [bob_fact], collection, bob.test_owner)
        other_gold = await read_gold(alice, [other_fact], other_collection, alice.test_owner)
        report["runtime_indexed"] = await call(alice, "runtime_stats")
        sidecars = [c for c in report["runtime_indexed"].get("collections", []) if collection in c["name"] and other_collection not in c["name"]]
        if not sidecars or not all(c.get("dim", 0) > 0 and c.get("embedding_model") for c in sidecars):
            raise InfrastructureError("Indexed collection model/dimension are not observable")
        report["embedding"] = [{k: c[k] for k in ("name", "embedding_model", "dim", "records")} for c in sidecars]
        queries["embedding"] = report["embedding"]
        for case in fixture["cases"]:
            row = {"case_id": case["id"], "category": case["category"], "query": case["query"], "results": []}
            start = time.perf_counter()
            try:
                payload, raw, latency = await alice.call("recall_memory", collection=collection,
                    query=case["query"], **{k: case[k] for k in ("room", "hall") if k in case})
                row.update(raw_response=raw, latency_ms=latency, results=recall_rows(payload))
                row["metrics"] = score_case(case, row["results"], gold, collection, alice.test_owner)
                row["status"] = "pass" if row["metrics"]["passed"] else "fail"
            except Exception as error:
                row.update(status="error", error=f"{type(error).__name__}: {error}",
                           raw_response=alice.last_rpc_response,
                           latency_ms=round((time.perf_counter() - start) * 1000, 3))
            queries["cases"].append(row)
            write_json(args.output_dir / "queries.json", queries)

        async def assert_recall(client, query, target_collection, allowed, expected_keys=()):
            payload = await call(client, "recall_memory", collection=target_collection, query=query)
            case = {"expected_keys": list(expected_keys), "answer_mode": "answer" if expected_keys else "unknown"}
            metric = score_case(case, recall_rows(payload), allowed, target_collection, client.test_owner)
            assert metric["passed"], str(metric)

        await probe("owner_isolation", lambda: assert_recall(alice, bob_fact["value"], collection, gold))
        await probe("collection_isolation", lambda: assert_recall(alice, other_fact["value"], other_collection, other_gold, ["f001"]))
        await probe("owner_positive_control", lambda: assert_recall(bob, bob_fact["value"], collection, bob_gold, ["f001"]))

        async def reconnect():
            await alice.close()
            await alice.connect()
            await assert_recall(alice, fixture["cases"][0]["query"], collection, gold, ["f001"])
        await probe("reconnect", reconnect)

        lifecycle = {"key": "lifecycle", "value": "Текущий комендант вымышленной крепости Нерис — Илья.", "room": "lifecycle", "hall": "fact"}
        await seed(alice, [lifecycle], collection, args.index_timeout)
        life_gold = await read_gold(alice, [lifecycle], collection, alice.test_owner)
        old_id = life_gold["lifecycle"]["id"]

        async def upsert():
            lifecycle["value"] = "Текущий комендант вымышленной крепости Нерис — Ольга."
            await seed(alice, [lifecycle], collection, args.index_timeout)
            updated = await read_gold(alice, [lifecycle], collection, alice.test_owner)
            assert updated["lifecycle"]["id"] == old_id, "upsert changed canonical ID"
            life_gold.update(updated)
            payload = await call(alice, "recall_memory", collection=collection, query="Кто сейчас комендант крепости Нерис?", room="lifecycle")
            metric = score_case({"expected_keys": ["lifecycle"], "answer_mode": "answer", "room": "lifecycle"}, recall_rows(payload), life_gold, collection, alice.test_owner)
            assert metric["passed"], str(metric)
        await probe("upsert", upsert)

        async def supersede():
            lifecycle["value"] = "Текущий комендант вымышленной крепости Нерис — Мария."
            payload = await call(alice, "supersede_memory", old_memory_id=old_id,
                new_value=lifecycle["value"], reason="Синтетическая смена назначения 2026-09-29")
            assert payload.get("ok") is True and payload.get("new_memory_id") != old_id
            report["supersede_readiness"] = {
                "mode": "synchronous_return_then_required_semantic_readback",
                "new_memory_id": payload["new_memory_id"],
                "index_job_id": payload.get("index_job_id"),
                "note": "Public supersede indexes synchronously and exposes no outbox job; readback below is required, no re-save or repair is performed.",
            }
            updated = await read_gold(alice, [lifecycle], collection, alice.test_owner)
            life_gold.update(updated)
            payload = await call(alice, "recall_memory", collection=collection, query="Кто сейчас комендант крепости Нерис?", room="lifecycle")
            metric = score_case({"expected_keys": ["lifecycle"], "answer_mode": "answer", "room": "lifecycle"}, recall_rows(payload), life_gold, collection, alice.test_owner)
            assert metric["passed"], str(metric)
        await probe("supersede", supersede)

        async def history():
            payload = await call(alice, "recall_memory", collection=collection, query="Ольга", room="lifecycle", include_superseded=True)
            old = [r for r in recall_rows(payload) if r.get("id") == old_id]
            assert old and old[0].get("supersession_state") == "superseded" and old[0].get("superseded_by") == life_gold["lifecycle"]["id"], "Historical row absent or not marked superseded"
        await probe("superseded_history", history)

        async def delete():
            deleted_id = life_gold["lifecycle"]["id"]
            await call(alice, "delete_memory", memory_id=deleted_id)
            deadline = time.monotonic() + args.index_timeout
            while True:
                state = await call(alice, "memory_index_status", limit=100)
                matches = [j for j in state.get("jobs") or [] if j.get("memory_id") == deleted_id and j.get("operation") == "delete_vector"]
                if matches and all(j["status"] == "completed" for j in matches):
                    break
                if time.monotonic() > deadline:
                    raise InfrastructureError("Delete vector readiness timed out")
                await asyncio.sleep(0.5)
            payload = await call(alice, "recall_memory", collection=collection, query="Кто сейчас комендант крепости Нерис?", room="lifecycle")
            assert not recall_rows(payload), "Deleted or superseded fact remains active"
        await probe("delete", delete)
    finally:
        write_json(args.output_dir / "queries.json", queries)
        write_json(args.output_dir / "operations.json", operations)
        await asyncio.gather(*(c.close() for c in clients), return_exceptions=True)


def summarize(report, fixture):
    queries = report.get("queries", [])
    probes = report.get("probes", [])
    for row in probes:
        if row["status"] == "planned":
            row.update(status="skipped", reason="setup or preceding infrastructure failure")
    count = Counter(row["status"] for row in queries + probes)
    planned = len(fixture["cases"]) + len(PROBES)
    executed = sum(count[s] for s in ("pass", "fail", "error"))
    report["counts"] = {"planned": planned, "executed": executed, "passed": count["pass"],
                        "failed": count["fail"], "errors": count["error"], "skipped": planned - executed}
    positive = [row["metrics"] for row in queries if row.get("metrics", {}).get("all_gold_at_5") is not None]
    positive_count = sum(bool(case["expected_keys"]) for case in fixture["cases"])
    report["metrics"] = {"planned_positive_queries": positive_count, "scored_positive_queries": len(positive),
        "macro_recall": {str(k): sum(m["recall"][str(k)] for m in positive) / positive_count if positive_count else None for k in KS},
        "macro_precision": {str(k): sum(m["precision"][str(k)] for m in positive) / positive_count if positive_count else None for k in KS},
        "mrr": sum(m["mrr"] for m in positive) / positive_count if positive_count else None,
        "all_gold_at_5": sum(m["all_gold_at_5"] for m in positive) / positive_count if positive_count else None,
        "unknown_nonempty": sum(bool(r.get("metrics", {}).get("nonempty_unknown")) for r in queries),
        "integrity_violations": sum(len(r.get("metrics", {}).get("violations", [])) for r in queries)}
    report["category_counts"] = {category: dict(Counter(r["status"] for r in queries if r["category"] == category)) for category in sorted({c["category"] for c in fixture["cases"]})}
    return 1 if report.get("setup_error") or count["error"] or planned != executed else 2 if count["fail"] else 0


def self_check():
    fixture = load_fixture()
    case = {"expected_keys": ["a", "b"], "answer_mode": "answer"}
    gold = {key: {"id": key, "key": key, "value": key + " exact", "room": "r", "hall": "fact", "owner_id": "alice", "collection": "c"} for key in ("a", "b")}
    rows = list(gold.values())
    assert score_case(case, rows, gold, "c", "alice")["passed"]
    controls = {
        "wrong_id": [{**rows[0], "id": "wrong"}, rows[1]],
        "corrupted_value": [{**rows[0], "value": "false"}, rows[1]],
        "foreign_owner": [{**rows[0], "owner_id": "bob"}, rows[1]],
        "foreign_collection": [{**rows[0], "collection": "other"}, rows[1]],
        "missing_multihop": rows[:1], "duplicates": [rows[0]] * 10, "empty_positive": [],
    }
    for name, bad in controls.items():
        assert not score_case(case, bad, gold, "c", "alice")["passed"], name
    assert score_case(case, [rows[0]] * 10, gold, "c", "alice")["recall"]["10"] == 0.5
    assert score_case({"expected_keys": [], "answer_mode": "unknown"}, rows, gold, "c", "alice")["passed"]
    for malformed in ({}, {"results": None}, {"results": {}}, {"results": ""}, {"results": 0}):
        try:
            recall_rows(malformed)
        except InfrastructureError:
            pass
        else:
            raise AssertionError("Malformed recall payload accepted as empty")
    assert recall_rows({"results": []}) == []
    denominator_case = {"expected_keys": ["a"], "answer_mode": "answer", "category": "test"}
    denominator_report = {"queries": [{"status": "pass", "category": "test", "metrics": score_case(denominator_case, rows[:1], gold, "c", "alice")}, {"status": "error", "category": "test"}]}
    summarize(denominator_report, {"cases": [denominator_case, denominator_case]})
    assert denominator_report["metrics"]["macro_recall"]["1"] == 0.5
    assert denominator_report["metrics"]["all_gold_at_5"] == 0.5
    for response in ({"jsonrpc": "2.0", "error": {"code": -32000, "message": "HTTP 200 still failed"}}, {}):
        try:
            rpc_result(response)
        except InfrastructureError:
            pass
        else:
            raise AssertionError("JSON-RPC error accepted")
    try:
        tool_payload({"isError": True, "content": [{"text": "failure"}]})
    except InfrastructureError:
        pass
    else:
        raise AssertionError("MCP isError accepted")
    for url in ("http://127.0.0.1:8080", "http://localhost:8081", "http://example.com:18124", "http://localhost", "http://localhost:18124/path"):
        try:
            validate_url(url)
        except ValueError:
            pass
        else:
            raise AssertionError("unsafe URL accepted")
    assert validate_url("http://127.0.0.1:18124")
    print(json.dumps({"self_check": "pass", "negative_controls": list(controls) + ["malformed_results", "errored_query_denominator", "JSONRPC200error", "MCPisError", "unsafe_url"], "facts": len(fixture["facts"]), "cases": len(fixture["cases"])}))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--self-check", action="store_true")
    parser.add_argument("--url")
    parser.add_argument("--allow-isolated-writes", action="store_true")
    parser.add_argument("--output-dir", type=Path)
    parser.add_argument("--fixture", type=Path, default=FIXTURE)
    parser.add_argument("--index-timeout", type=float, default=120)
    args = parser.parse_args()
    if args.self_check:
        self_check()
        return 0
    if not args.url or not args.allow_isolated_writes or not args.output_dir:
        parser.error("--url, --allow-isolated-writes and --output-dir are required")
    args.url = validate_url(args.url)
    if args.index_timeout <= 0 or args.index_timeout > 600:
        parser.error("--index-timeout must be in (0, 600]")
    args.output_dir.mkdir(parents=True, exist_ok=False)
    fixture = load_fixture(args.fixture)
    raw_fixture = args.fixture.read_bytes()
    (args.output_dir / "fixture.json").write_bytes(raw_fixture)
    report = {"schema_version": 1, "started_at": datetime.now(timezone.utc).isoformat(),
        "url": args.url, "fixture_sha256": hashlib.sha256(raw_fixture).hexdigest(),
        "git_revision": subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip(),
        "git_status": subprocess.check_output(["git", "status", "--short"], cwd=ROOT, text=True),
        "harness_sha256": hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
        "config": {"method": "recall_memory", "top_k": "server fixed; score at 1/3/5/10", "index_timeout_seconds": args.index_timeout},
        "limitations": ["Synthetic developer-authored fixture; no real-world factual truth claim or held-out population estimate.",
            "No answer generation or citation grading in this harness; nonempty unknown retrieval is diagnostic only.",
            "Precision@k divides by k, including absent slots; retrieval metrics exclude unknown cases.",
            "Primary macro denominator is all planned positive queries; errored or unexecuted positives contribute zero.",
            "Recall responses omit collection: scope is checked against scoped SQL read-back ID mapping.",
            "Completed indexing plus model/dimension are observed; public recall does not expose vector-vs-SQL fallback per query.",
            "Supersession history probe requires historical row discovery; current and historical outcomes are separate."]}
    try:
        asyncio.run(run(args, fixture, report))
    except Exception as error:
        report["setup_error"] = f"{type(error).__name__}: {error}"
    report["exit_code"] = summarize(report, fixture)
    report["finished_at"] = datetime.now(timezone.utc).isoformat()
    write_json(args.output_dir / "report.json", report)
    print(json.dumps({k: report[k] for k in ("exit_code", "counts", "metrics")}, ensure_ascii=False))
    return report["exit_code"]


if __name__ == "__main__":
    raise SystemExit(main())
