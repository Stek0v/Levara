#!/usr/bin/env python3
"""Constant-arrival-rate load generator for Levara MCP.

Designed for release gates, not microbenchmarks: it initializes independent
MCP sessions, schedules calls at a fixed rate, records scheduling delay and
end-to-end latency, and emits a machine-readable JSON report.
"""

import argparse
import asyncio
import json
import math
import resource
import time
from pathlib import Path

import aiohttp


def percentile(values, p):
    if not values:
        return 0.0
    values = sorted(values)
    rank = (len(values) - 1) * p / 100
    lo = math.floor(rank)
    hi = math.ceil(rank)
    if lo == hi:
        return values[lo]
    return values[lo] + (values[hi] - values[lo]) * (rank - lo)


class Client:
    def __init__(self, http, url, token=""):
        self.http, self.url, self.token = http, url.rstrip("/") + "/mcp", token
        self.session_id, self.rpc_id = "", 0

    def headers(self):
        result = {"Content-Type": "application/json"}
        if self.session_id:
            result["Mcp-Session-Id"] = self.session_id
        if self.token:
            result["Authorization"] = "Bearer " + self.token
        return result

    async def rpc(self, method, params=None):
        self.rpc_id += 1
        body = {"jsonrpc": "2.0", "id": self.rpc_id, "method": method}
        if params is not None:
            body["params"] = params
        async with self.http.post(self.url, json=body, headers=self.headers()) as response:
            payload = await response.json(content_type=None)
            self.session_id = response.headers.get("Mcp-Session-Id", self.session_id)
            return response.status, payload

    async def initialize(self):
        status, payload = await self.rpc("initialize", {
            "protocolVersion": "2025-03-26", "capabilities": {},
            "clientInfo": {"name": "levara-mcp-load", "version": "1"},
        })
        if status != 200 or "error" in payload or not self.session_id:
            raise RuntimeError(f"MCP initialize failed: {status} {payload}")

    async def call(self, tool, arguments):
        return await self.rpc("tools/call", {"name": tool, "arguments": arguments})


async def run(args):
    timeout = aiohttp.ClientTimeout(total=args.timeout)
    connector = aiohttp.TCPConnector(limit=max(args.workers * 2, 100), ttl_dns_cache=300)
    latencies, schedule_lag, errors = [], [], []
    scheduled = completed = 0
    queue = asyncio.Queue(maxsize=max(args.workers * 20, int(args.rate * 2)))
    started = time.perf_counter()

    async with aiohttp.ClientSession(timeout=timeout, connector=connector) as http:
        clients = [Client(http, args.url, args.token) for _ in range(args.workers)]
        await asyncio.gather(*(client.initialize() for client in clients))

        async def worker(client):
            nonlocal completed
            while True:
                item = await queue.get()
                if item is None:
                    queue.task_done()
                    return
                due, sequence = item
                began = time.perf_counter()
                schedule_lag.append((began - due) * 1000)
                try:
                    arguments = json.loads(args.arguments)
                    if args.unique_key_prefix:
                        arguments["key"] = f"{args.unique_key_prefix}_{sequence}"
                    status, payload = await client.call(args.tool, arguments)
                    latency = (time.perf_counter() - began) * 1000
                    latencies.append(latency)
                    result = payload.get("result", {}) if isinstance(payload, dict) else {}
                    if status != 200 or "error" in payload or result.get("isError"):
                        errors.append({"status": status, "payload": str(payload)[:500]})
                except Exception as exc:
                    latencies.append((time.perf_counter() - began) * 1000)
                    errors.append({"exception": type(exc).__name__, "message": str(exc)[:500]})
                completed += 1
                queue.task_done()

        tasks = [asyncio.create_task(worker(client)) for client in clients]
        epoch = time.perf_counter()
        total = int(args.rate * args.duration)
        for sequence in range(total):
            due = epoch + sequence / args.rate
            delay = due - time.perf_counter()
            if delay > 0:
                await asyncio.sleep(delay)
            await queue.put((due, sequence))
            scheduled += 1
        await queue.join()
        for _ in tasks:
            await queue.put(None)
        await asyncio.gather(*tasks)

    elapsed = time.perf_counter() - started
    report = {
        "url": args.url, "tool": args.tool, "target_rps": args.rate,
        "duration_target_s": args.duration, "elapsed_s": round(elapsed, 3),
        "scheduled": scheduled, "completed": completed,
        "achieved_rps": round(completed / elapsed, 3),
        "error_count": len(errors),
        "error_rate": round(len(errors) / max(completed, 1), 8),
        "latency_ms": {p: round(percentile(latencies, int(p[1:])), 3) for p in ("p50", "p95", "p99")},
        "schedule_lag_ms": {p: round(percentile(schedule_lag, int(p[1:])), 3) for p in ("p50", "p95", "p99")},
        "max_rss_kb_generator": resource.getrusage(resource.RUSAGE_SELF).ru_maxrss,
        "errors_sample": errors[:20],
    }
    print(json.dumps(report, ensure_ascii=False, indent=2))
    if args.output:
        Path(args.output).parent.mkdir(parents=True, exist_ok=True)
        Path(args.output).write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    return 1 if report["error_rate"] > args.max_error_rate else 0


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--url", default="http://127.0.0.1:8080")
    parser.add_argument("--rate", type=float, default=500)
    parser.add_argument("--duration", type=int, default=600)
    parser.add_argument("--workers", type=int, default=64)
    parser.add_argument("--tool", default="heartbeat")
    parser.add_argument("--arguments", default="{}")
    parser.add_argument("--unique-key-prefix", default="")
    parser.add_argument("--token", default="")
    parser.add_argument("--timeout", type=float, default=10)
    parser.add_argument("--max-error-rate", type=float, default=0.001)
    parser.add_argument("--output")
    args = parser.parse_args()
    raise SystemExit(asyncio.run(run(args)))


if __name__ == "__main__":
    main()
