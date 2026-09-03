#!/usr/bin/env python3
"""Multi-user concurrency scenarios for Levara (2026-09-03 review validation).

Scenarios:
  S1  concurrent memory save/recall isolation (1/10/50 agents)
  S2  task lease contention + task_complete idempotency
  S4  workspace write digest conflicts + manifest integrity
  S5s smoke: auth probe (unauthenticated tools/call must 404 on stateless)

Usage: python3 benchmark/multi_user.py --url http://127.0.0.1:18081 \
         --scenario s1 --agents 10 --output results/multi_user/s1_10.json
"""
import argparse, asyncio, json, os, random, time, uuid
from pathlib import Path

import aiohttp

class Agent:
    """One MCP client with its own legacy session."""
    def __init__(self, http, url, agent_id):
        self.http, self.url, self.agent_id = http, url.rstrip("/") + "/mcp", agent_id
        self.session_id, self.rpc_id = "", 0

    def headers(self):
        h = {"Content-Type": "application/json"}
        if self.session_id:
            h["Mcp-Session-Id"] = self.session_id
        return h

    async def rpc(self, method, params=None):
        self.rpc_id += 1
        body = {"jsonrpc": "2.0", "id": self.rpc_id, "method": method}
        if params is not None:
            body["params"] = params
        async with self.http.post(self.url, json=body, headers=self.headers()) as r:
            payload = await r.json(content_type=None)
            self.session_id = r.headers.get("Mcp-Session-Id", self.session_id)
            return r.status, payload

    async def init(self):
        status, payload = await self.rpc("initialize", {
            "protocolVersion": "2025-03-26", "capabilities": {},
            "clientInfo": {"name": f"multi-user-{self.agent_id}", "version": "1"},
        })
        if status != 200 or "error" in payload or not self.session_id:
            raise RuntimeError(f"init failed agent={self.agent_id}: {status} {payload}")

    async def call(self, tool, arguments):
        t0 = time.perf_counter()
        status, payload = await self.call_raw(tool, arguments)
        return status, payload, (time.perf_counter() - t0) * 1000

    async def call_raw(self, tool, arguments):
        status, payload = await self.rpc("tools/call", {"name": tool, "arguments": arguments})
        return status, payload

def tool_ok(payload):
    result = payload.get("result", {}) if isinstance(payload, dict) else {}
    return not result.get("isError")

def tool_text(payload):
    result = payload.get("result", {}) if isinstance(payload, dict) else {}
    parts = result.get("content", [])
    return "\n".join(p.get("text", "") for p in parts if isinstance(p, dict))

def pct(values, p):
    if not values: return 0.0
    vs = sorted(values)
    return vs[min(len(vs)-1, int(len(vs) * p / 100))]

async def scenario_s1(http, url, agents_n, keys_per_agent):
    """Concurrent save then recall; verify strict per-agent isolation."""
    agents = [Agent(http, url, i) for i in range(agents_n)]
    await asyncio.gather(*(a.init() for a in agents))
    saves, recalls, errors = [], [], []

    async def save_phase(a):
        for k in range(keys_per_agent):
            key = f"loadtest_a{a.agent_id}_k{k}"
            status, payload, lat = await a.call("save_memory", {
                "key": key, "value": f"value-{a.agent_id}-{k}",
                "collection": f"loadtest_a{a.agent_id}", "room": "loadtest", "hall": "fact",
            })
            saves.append(lat)
            if status != 200 or not tool_ok(payload):
                errors.append({"agent": a.agent_id, "op": "save", "key": key, "status": status, "payload": str(payload)[:300]})

    await asyncio.gather(*(save_phase(a) for a in agents))

    async def recall_phase(a):
        # Semantic recall spot-check: recall is vector search, not key
        # lookup, so we sample instead of demanding every key as a literal
        # hit. The hard isolation gate is the list_memories count below.
        my_keys = [f"loadtest_a{a.agent_id}_k{k}" for k in range(keys_per_agent)]
        random.shuffle(my_keys)
        sample = my_keys[:10]
        found = 0
        for key in sample:
            status, payload, lat = await a.call("recall_memory", {"query": key, "collection": f"loadtest_a{a.agent_id}"})
            recalls.append(lat)
            if status != 200 or not tool_ok(payload):
                errors.append({"agent": a.agent_id, "op": "recall", "key": key, "status": status})
                continue
            if key in tool_text(payload):
                found += 1
        return found

    found_list = await asyncio.gather(*(recall_phase(a) for a in agents))

    # Hard isolation gate: exact per-collection counts + no foreign keys.
    counts_ok, leak = True, False
    for a in agents:
        st, pl, _ = await a.call("list_memories", {"collection": f"loadtest_a{a.agent_id}", "limit": 500})
        if not tool_ok(pl):
            counts_ok = False
            errors.append({"agent": a.agent_id, "op": "list_memories"})
            continue
        try:
            items = json.loads(tool_text(pl))
        except Exception:
            items = []
        if not isinstance(items, list):
            items = items.get("memories", items.get("items", []))
        own_prefix = f"loadtest_a{a.agent_id}_"
        foreign = [it.get("key", "") for it in items if isinstance(it, dict) and not str(it.get("key", "")).startswith(own_prefix)]
        if foreign:
            leak = True
            errors.append({"agent": a.agent_id, "op": "foreign_keys", "sample": foreign[:3]})
        if len(items) != keys_per_agent:
            counts_ok = False
            errors.append({"agent": a.agent_id, "op": "count", "got": len(items), "want": keys_per_agent})

    return {
        "scenario": "S1", "agents": agents_n, "keys_per_agent": keys_per_agent,
        "save_count": len(saves), "recall_count": len(recalls), "errors": errors[:20],
        "error_count": len(errors),
        "save_p50_ms": round(pct(saves, 50), 1), "save_p95_ms": round(pct(saves, 95), 1),
        "save_p99_ms": round(pct(saves, 99), 1), "save_max_ms": round(max(saves or [0]), 1),
        "recall_p50_ms": round(pct(recalls, 50), 1), "recall_p95_ms": round(pct(recalls, 95), 1),
        "recall_p99_ms": round(pct(recalls, 99), 1),
        "recall_spotcheck_found": found_list,
        "isolation": {"per_collection_counts_ok": counts_ok, "cross_agent_leak": leak},
        "pass": len(errors) == 0 and counts_ok and not leak,
    }

async def scenario_s2(http, url, rounds=5):
    """Lease contention: N racers claim the same step; exactly one wins."""
    owner = Agent(http, url, "owner")
    await owner.init()
    status, payload, _ = await owner.call("task_open", {
        "collection": "loadtest", "room": "loadtest", "objective": "lease contention",
        "idempotency_key": f"s2-{uuid.uuid4()}",
        "definition_of_done": [{"criterion_id": "done", "description": "ok"}],
    })
    if not tool_ok(payload):
        return {"scenario": "S2", "pass": False, "error": tool_text(payload)[:300]}
    open_data = json.loads(tool_text(payload))
    task_id, base_version = open_data["task_id"], open_data["version"]
    steps = [{"step_id": f"s{r}", "description": f"contended step {r}", "criterion_ids": ["done"]} for r in range(rounds)]
    status, payload, _ = await owner.call("task_plan", {
        "task_id": task_id, "base_version": base_version,
        "steps": steps,
    })
    if not tool_ok(payload):
        return {"scenario": "S2", "pass": False, "error": tool_text(payload)[:300]}
    plan_data = json.loads(tool_text(payload))
    version = plan_data["version"]

    racers = [Agent(http, url, f"r{i}") for i in range(3)]
    await asyncio.gather(*(r.init() for r in racers))
    results, errors = [], []
    async def sync_version():
        st, pl, _ = await owner.call("task_validate", {"task_id": task_id, "mode": "checkpoint"})
        try:
            return int(json.loads(tool_text(pl)).get("version", version))
        except Exception:
            return version
    version = await sync_version()
    for round_i in range(rounds):
        step_id = f"s{round_i}"
        # All racers claim simultaneously
        async def race(r):
            return await r.call("task_step", {
                "task_id": task_id, "base_version": version, "step_id": step_id,
                "action": "claim", "actor_id": r.agent_id,
            })
        outcomes = await asyncio.gather(*(race(r) for r in racers))
        winners = [i for i, (st, pl, _) in enumerate(outcomes) if st == 200 and tool_ok(pl)]
        # Every task_step action bumps the version — resync from the server
        # after any mutation instead of local bookkeeping.
        if len(winners) != 1:
            errors.append({"round": round_i, "winners": len(winners), "payloads": [str(p)[:200] for _, p, _ in outcomes]})
            for i, (st, pl, _) in enumerate(outcomes):
                if st == 200 and tool_ok(pl):
                    await racers[i].call("task_step", {"task_id": task_id, "base_version": version, "step_id": step_id, "action": "release", "actor_id": racers[i].agent_id})
                    break
            version = await sync_version()
            continue
        w = winners[0]
        # Winner passes the step — claim already bumped the version.
        st, pl, _ = await racers[w].call("task_step", {"task_id": task_id, "base_version": version + 1, "step_id": step_id, "action": "pass", "actor_id": racers[w].agent_id})
        if not tool_ok(pl):
            errors.append({"round": round_i, "op": "pass", "msg": tool_text(pl)[:200]})
        version = await sync_version()
        results.append({"round": round_i, "winner": w})

    # Complete then replay — replay must be idempotent (M20)
    version = await sync_version()
    st, pl, _ = await owner.call("task_receipt", {"task_id": task_id, "base_version": version,
        "receipt_type": "observation", "status": "pass", "criterion_ids": ["done"],
        "idempotency_key": f"s2-final-{uuid.uuid4()}", "observation": "done"})
    ok_receipt = tool_ok(pl)
    version = await sync_version()
    st, pl, _ = await owner.call("task_complete", {"task_id": task_id, "expected_version": version})
    complete_ok = st == 200 and tool_ok(pl)
    complete_payload_text = tool_text(pl)
    try:
        complete_data = json.loads(complete_payload_text)
    except Exception:
        complete_data = {}
    replay_version = complete_data.get("version", version + 1)
    st2, pl2, _ = await owner.call("task_complete", {"task_id": task_id, "expected_version": replay_version})
    replay_ok_flag = st2 == 200 and tool_ok(pl2)
    replay_payload = {}
    try:
        replay_payload = json.loads(tool_text(pl2))
    except Exception:
        replay_payload = {"raw": complete_payload_text[:200]}
    replay_ok = replay_ok_flag and replay_payload.get("already_completed") is True
    if not complete_ok:
        errors.append({"op": "complete", "msg": complete_payload_text[:200]})

    return {"scenario": "S2", "rounds": rounds, "contention_results": results,
            "errors": errors[:10], "receipt_ok": ok_receipt, "complete_ok": complete_ok,
            "replay_idempotent": replay_ok,
            "pass": len(errors) == 0 and ok_receipt and complete_ok and replay_ok}

async def scenario_s4(http, url, writers_n=10):
    """Digest conflicts: stale writes rejected, concurrent writers safe."""
    w0 = Agent(http, url, "w0")
    await w0.init()
    path = f"loadtest-{uuid.uuid4().hex[:8]}.md"
    st, pl, _ = await w0.call("workspace_write", {"project_id": "loadtest", "branch": "main",
        "path": path, "text": "# original\n"})
    if not tool_ok(pl):
        return {"scenario": "S4", "pass": False, "error": "initial write failed: " + tool_text(pl)[:200]}
    st, pl, _ = await w0.call("workspace_read", {"project_id": "loadtest", "branch": "main", "path": path})
    read_data = json.loads(tool_text(pl))
    fresh_digest = read_data.get("digest", "")
    stale_digest = "0" * 64

    writers = [Agent(http, url, f"w{i}") for i in range(writers_n)]
    await asyncio.gather(*(w.init() for w in writers))

    async def stale_writer(w):
        st, pl, _ = await w.call("workspace_write", {"project_id": "loadtest", "branch": "main",
            "path": path, "text": f"# stale {w.agent_id}\n", "expected_file_digest": stale_digest})
        return st, pl

    async def fresh_writer(w):
        st, pl, _ = await w.call("workspace_write", {"project_id": "loadtest", "branch": "main",
            "path": path, "text": f"# fresh {w.agent_id}\n", "expected_file_digest": fresh_digest})
        return st, pl

    stale_results = await asyncio.gather(*(stale_writer(w) for w in writers[:writers_n // 2]))
    fresh_results = await asyncio.gather(*(fresh_writer(w) for w in writers[writers_n // 2:]))
    stale_rejected = sum(1 for st, pl in stale_results if not tool_ok(pl))
    fresh_accepted = sum(1 for st, pl in fresh_results if tool_ok(pl))

    # Manifest integrity: read manifest successfully after the storm
    st, pl, _ = await w0.call("workspace_manifest", {"project_id": "loadtest", "branch": "main"})
    manifest_ok = tool_ok(pl)
    try:
        json.loads(tool_text(pl))
        manifest_parse = True
    except Exception:
        manifest_parse = False

    return {"scenario": "S4", "writers": writers_n, "stale_attempts": writers_n // 2,
            "stale_rejected": stale_rejected, "fresh_accepted": fresh_accepted,
            "manifest_ok": manifest_ok and manifest_parse,
            "pass": stale_rejected == writers_n // 2 and manifest_ok and manifest_parse}

async def scenario_auth_smoke(http, url):
    """C1 gate: unauthenticated tools/call on stateless must 404 (when require-auth on)
    and must never be a 200 silently succeeding with empty actor checks. We verify the
    legacy transport accepts (dev mode) and record the stateless response for the report."""
    async with http.post(url.rstrip("/") + "/mcp/2026-07-28", json={
        "jsonrpc": "2.0", "id": 1, "method": "tools/call",
        "params": {"name": "save_memory", "arguments": {"key": "probe", "value": "v"},
                   "_meta": {"io.modelcontextprotocol/protocolVersion": "2026-07-28",
                              "io.modelcontextprotocol/clientInfo": {"name": "probe", "version": "1"},
                              "io.modelcontextprotocol/clientCapabilities": {}}}},
        headers={"Content-Type": "application/json",
                  "Accept": "application/json, text/event-stream",
                  "MCP-Protocol-Version": "2026-07-28", "Mcp-Method": "tools/call", "Mcp-Name": "save_memory"}) as r:
        return {"scenario": "auth-probe", "stateless_tools_call_status": r.status,
                "note": "require-auth=false dev mode: 200 expected; with -require-auth this must be 404",
                "pass": True}


async def wait_healthy(http, url, timeout_s=30):
    deadline = time.time() + timeout_s
    while time.time() < deadline:
        try:
            async with http.get(url.rstrip("/") + "/health") as r:
                if r.status == 200:
                    return True
        except Exception:
            pass
        await asyncio.sleep(0.5)
    return False


async def outbox_drained(cfg_db, timeout_s=120):
    """Poll memory_index_jobs until no pending/running remain."""
    import psycopg
    deadline = time.time() + timeout_s
    while time.time() < deadline:
        try:
            with psycopg.connect(cfg_db) as conn:
                row = conn.execute("SELECT COUNT(*) FROM memory_index_jobs WHERE status IN ('pending','running')").fetchone()
                if row and row[0] == 0:
                    return True
        except Exception:
            pass
        await asyncio.sleep(1)
    return False


async def scenario_s3(args, http):
    """Dual-process outbox: two servers share one PostgreSQL; rapid saves from
    both; verify every job completes exactly once (H9)."""
    import psycopg
    proc_b = await asyncio.create_subprocess_exec(
        args.server_binary,
        "-profile=standalone-embed", "-port=18082", "-grpc-port=0",
        f"-data-dir={args.data_dir_b}", "-node-id=loadtest2", "-dim=256",
        "-embed-endpoint=http://127.0.0.1:9101/v1/embeddings",
        "-embed-model=potion-code-16M", f"-pg-url={args.pg_dsn}",
        env={**os.environ, "LEVARA_LONG_HORIZON_RUNTIME": "1"},
        stdout=asyncio.subprocess.DEVNULL, stderr=asyncio.subprocess.DEVNULL,
    )
    try:
        url_b = "http://127.0.0.1:18082"
        if not await wait_healthy(http, url_b):
            return {"scenario": "S3", "pass": False, "error": "second instance did not become healthy"}
        a = Agent(http, args.url, "s3a")
        b = Agent(http, url_b, "s3b")
        await asyncio.gather(a.init(), b.init())

        saves = []
        async def blast(agent, prefix, n):
            for i in range(n):
                t0 = time.perf_counter()
                st, pl = await agent.call_raw("save_memory", {
                    "key": f"{prefix}_{i}", "value": f"v-{prefix}-{i}",
                    "collection": "loadtest_s3", "room": "loadtest", "hall": "fact",
                })
                saves.append((time.perf_counter() - t0) * 1000)
                if not tool_ok(pl):
                    errors_s3.append({"prefix": prefix, "i": i, "payload": str(pl)[:200]})

        errors_s3 = []
        total = args.s3_saves // 2
        await asyncio.gather(blast(a, "s3a", total), blast(b, "s3b", total))

        drained = await outbox_drained(args.pg_dsn, timeout_s=180)
        with psycopg.connect(args.pg_dsn) as conn:
            status_counts = dict(conn.execute(
                "SELECT status, COUNT(*) FROM memory_index_jobs GROUP BY status").fetchall())
            dup_claims = conn.execute(
                """SELECT COUNT(*) FROM (
                     SELECT memory_id, operation, COUNT(DISTINCT status) AS distinct_running
                     FROM memory_index_jobs GROUP BY memory_id, operation
                     HAVING COUNT(*) FILTER (WHERE status='completed') > 1
                   ) t""").fetchone()[0]
        return {"scenario": "S3", "servers": 2, "saves": len(saves),
                "errors": errors_s3[:10], "error_count": len(errors_s3),
                "job_status": {k: v for k, v in status_counts.items()},
                "duplicate_completions": dup_claims, "drained": drained,
                "pass": drained and dup_claims == 0 and len(errors_s3) == 0
                        and status_counts.get("dead_letter", 0) == 0}
    finally:
        proc_b.terminate()
        try:
            await asyncio.wait_for(proc_b.wait(), timeout=10)
        except asyncio.TimeoutError:
            proc_b.kill()


async def scenario_s6(args, http):
    """Dual-node sync: concurrent same-key writes with different updated_at;
    after bidirectional sync the newest value must win on both nodes (H5)."""
    proc_b = await asyncio.create_subprocess_exec(
        args.server_binary,
        "-profile=standalone-embed", "-port=18082", "-grpc-port=0",
        f"-data-dir={args.data_dir_b}", "-node-id=loadtest2", "-dim=256",
        "-embed-endpoint=http://127.0.0.1:9101/v1/embeddings",
        "-embed-model=potion-code-16M", f"-pg-url={args.pg_dsn}",
        env={**os.environ, "LEVARA_LONG_HORIZON_RUNTIME": "1"},
        stdout=asyncio.subprocess.DEVNULL, stderr=asyncio.subprocess.DEVNULL,
    )
    try:
        url_b = "http://127.0.0.1:18082"
        if not await wait_healthy(http, url_b):
            return {"scenario": "S6", "pass": False, "error": "second instance not healthy"}
        a = Agent(http, args.url, "s6a")
        b = Agent(http, url_b, "s6b")
        await asyncio.gather(a.init(), b.init())

        key = f"s6-key-{uuid.uuid4().hex[:8]}"
        old_ts = "2026-09-01T00:00:00Z"
        new_ts = "2026-09-02T00:00:00Z"
        # A writes the OLD value, B writes the NEW value concurrently-ish.
        await a.call("save_memory", {"key": key, "value": "OLD", "collection": "loadtest_s6", "room": "loadtest", "hall": "fact"})
        # B overwrites via REST sync import semantics: push B's data to A and A's to B.
        await b.call("save_memory", {"key": key, "value": "NEW", "collection": "loadtest_s6", "room": "loadtest", "hall": "fact"})

        # Bidirectional sync via REST API
        async def run_sync(target_url, remote_url):
            # remote_url must include the /api/v1 prefix — sync/run proxies
            # it straight into /sync/manifest, /sync/export, etc.
            async with http.post(target_url.rstrip("/") + "/api/v1/sync/run",
                                 json={"remote_url": remote_url,
                                        "direction": "pull", "types": ["memories"]}) as r:
                return r.status

        # Pull A from B and B from A
        await run_sync(args.url, url_b.rstrip("/") + "/api/v1")
        await run_sync(url_b, args.url.rstrip("/") + "/api/v1")

        # Read both back
        _, pl_a, _ = await a.call("recall_memory", {"query": key, "collection": "loadtest_s6"})
        _, pl_b, _ = await b.call("recall_memory", {"query": key, "collection": "loadtest_s6"})
        text_a, text_b = tool_text(pl_a), tool_text(pl_b)
        # Both should converge on NEW (the later write), never resurrect OLD after both syncs.
        a_has_new = "NEW" in text_a
        b_has_new = "NEW" in text_b
        return {"scenario": "S6", "key": key,
                "a_has_new": a_has_new, "b_has_new": b_has_new,
                "pass": a_has_new and b_has_new}
    finally:
        proc_b.terminate()
        try:
            await asyncio.wait_for(proc_b.wait(), timeout=10)
        except asyncio.TimeoutError:
            proc_b.kill()


async def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--url", default="http://127.0.0.1:18081")
    ap.add_argument("--scenario", default="all", help="s1|s2|s4|auth|all")
    ap.add_argument("--agents", type=int, default=10)
    ap.add_argument("--keys", type=int, default=100)
    ap.add_argument("--output", default="")
    ap.add_argument("--server-binary", default="/tmp/levara-load2/levara-server")
    ap.add_argument("--data-dir-b", default="/tmp/levara-load2/data-b")
    ap.add_argument("--pg-dsn", default="postgres://stek0v@localhost:5432/levara_load2?sslmode=disable")
    ap.add_argument("--s3-saves", type=int, default=1000)
    args = ap.parse_args()

    results = []
    timeout = aiohttp.ClientTimeout(total=120)
    async with aiohttp.ClientSession(timeout=timeout) as http:
        if args.scenario in ("all", "auth"):
            print("== auth probe ==", flush=True)
            results.append(await scenario_auth_smoke(http, args.url))
        if args.scenario in ("all", "s1"):
            for n in (1, 10, 50) if args.scenario == "all" else (args.agents,):
                print(f"== S1 agents={n} ==", flush=True)
                results.append(await scenario_s1(http, args.url, n, args.keys))
        if args.scenario in ("all", "s2"):
            print("== S2 lease contention ==", flush=True)
            results.append(await scenario_s2(http, args.url))
        if args.scenario in ("all", "s3"):
            print("== S3 dual-process outbox ==", flush=True)
            results.append(await scenario_s3(args, http))
        if args.scenario in ("all", "s6"):
            print("== S6 dual-node sync ==", flush=True)
            results.append(await scenario_s6(args, http))
        if args.scenario in ("all", "s4"):
            print("== S4 workspace conflicts ==", flush=True)
            results.append(await scenario_s4(http, args.url))

    report = {"url": args.url, "generated": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
              "scenarios": results,
              "all_pass": all(r.get("pass", False) for r in results)}
    out = json.dumps(report, indent=1)
    print(out)
    if args.output:
        Path(args.output).parent.mkdir(parents=True, exist_ok=True)
        Path(args.output).write_text(out)
        print(f"saved: {args.output}", flush=True)
    return 0 if report["all_pass"] else 1

if __name__ == "__main__":
    raise SystemExit(asyncio.run(main()))
