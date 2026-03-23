#!/usr/bin/env python3
"""Levara MCP Benchmark -- measures latency, quality, and resource usage on Raspberry Pi."""
import argparse
import asyncio
import json
import os
import subprocess
import sys
import time
from datetime import datetime

# Allow running from repo root or benchmark/ dir
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import aiohttp
from mcp_client import MCPClient
from scenarios import (
    scenario_memory,
    scenario_chat,
    scenario_knowledge,
    scenario_git,
    run_concurrency_sweep,
)

try:
    import numpy as np
    HAS_NUMPY = True
except ImportError:
    HAS_NUMPY = False


def collect_pi_resources(host, ssh_user):
    """Collect system metrics from Pi via SSH.

    Returns dict with cpu_percent, rss_mb, temp_c, sqlite_size_mb, uptime.
    Works from Mac (remote SSH) or from Pi itself (localhost).
    """
    metrics = {}
    is_local = host in ("localhost", "127.0.0.1")

    def run_cmd(cmd):
        if is_local:
            full_cmd = cmd
        else:
            full_cmd = f"ssh -o ConnectTimeout=5 -o StrictHostKeyChecking=no {ssh_user}@{host} '{cmd}'"
        try:
            result = subprocess.run(
                full_cmd, shell=True, capture_output=True, text=True, timeout=10
            )
            return result.stdout.strip() if result.returncode == 0 else ""
        except Exception:
            return ""

    # CPU usage (1-second sample)
    cpu_out = run_cmd("top -bn1 | grep '%Cpu' | awk '{print $2}'")
    try:
        metrics["cpu_percent"] = round(float(cpu_out), 1)
    except (ValueError, TypeError):
        # Try alternative: /proc/stat based
        cpu_out = run_cmd("grep 'cpu ' /proc/stat | awk '{usage=($2+$4)*100/($2+$4+$5)} END {print usage}'")
        try:
            metrics["cpu_percent"] = round(float(cpu_out), 1)
        except (ValueError, TypeError):
            metrics["cpu_percent"] = -1

    # Levara RSS
    rss_out = run_cmd("ps aux | grep '[l]evara' | awk '{sum += $6} END {print sum}'")
    try:
        metrics["rss_mb"] = round(int(rss_out) / 1024, 1)
    except (ValueError, TypeError):
        metrics["rss_mb"] = -1

    # CPU temperature
    temp_out = run_cmd("cat /sys/class/thermal/thermal_zone0/temp 2>/dev/null || vcgencmd measure_temp 2>/dev/null | grep -oP '[0-9.]+'")
    try:
        t = float(temp_out)
        metrics["temp_c"] = round(t / 1000 if t > 1000 else t, 1)
    except (ValueError, TypeError):
        metrics["temp_c"] = -1

    # SQLite DB size
    db_out = run_cmd("du -sm /home/stek0v/levara/data/*.db 2>/dev/null | awk '{sum += $1} END {print sum}'")
    try:
        metrics["sqlite_size_mb"] = round(float(db_out), 2)
    except (ValueError, TypeError):
        metrics["sqlite_size_mb"] = -1

    # Uptime
    uptime_out = run_cmd("uptime -p 2>/dev/null || uptime")
    metrics["uptime"] = uptime_out or "unknown"

    return metrics


def measure_cold_start(host, port, ssh_user):
    """Measure cold start time: restart Levara and time until healthy.

    Only works when running remotely via SSH or locally with systemd.
    Returns cold_start_ms or -1 if not possible.
    """
    is_local = host in ("localhost", "127.0.0.1")

    def ssh_cmd(cmd):
        if is_local:
            full_cmd = f"sudo {cmd}"
        else:
            full_cmd = f"ssh -o ConnectTimeout=5 {ssh_user}@{host} 'sudo {cmd}'"
        try:
            subprocess.run(full_cmd, shell=True, capture_output=True, timeout=10)
        except Exception:
            pass

    import requests

    # Restart
    ssh_cmd("systemctl restart levara")
    start = time.monotonic()

    # Poll health
    url = f"http://{host}:{port}/health"
    for _ in range(100):  # up to 10 seconds
        time.sleep(0.1)
        try:
            r = requests.get(url, timeout=1)
            if r.status_code == 200:
                return round((time.monotonic() - start) * 1000, 1)
        except Exception:
            continue

    return -1


def format_summary(results):
    """Print a human-readable summary table."""
    print("\n" + "=" * 80)
    print("LEVARA MCP BENCHMARK RESULTS")
    print("=" * 80)
    print(f"Date:     {results['meta']['timestamp']}")
    print(f"Host:     {results['meta']['host']}:{results['meta']['port']}")
    print(f"Duration: {results['meta']['duration_s']:.1f}s")
    print()

    # Tool latency table
    tools = results.get("tools", {})
    if tools:
        print(f"{'Tool':<25} {'p50':>8} {'p95':>8} {'p99':>8} {'Mean':>8} {'Calls':>6} {'Errs':>5}")
        print("-" * 70)
        for name, s in sorted(tools.items()):
            print(f"{name:<25} {s['p50']:>7.1f} {s['p95']:>7.1f} {s['p99']:>7.1f} {s['mean']:>7.1f} {s['calls']:>6} {s['errors']:>5}")
        print()

    # Quality metrics
    quality = results.get("quality", {})
    if quality:
        print("Quality Metrics:")
        for metric, val in quality.items():
            if isinstance(val, float):
                print(f"  {metric}: {val:.4f}")
            else:
                print(f"  {metric}: {val}")
        print()

    # Concurrency
    concurrency = results.get("concurrency", {})
    if concurrency:
        print("Concurrency Sweep:")
        print(f"  {'Level':>6} {'Tool':<25} {'QPS':>8} {'p50':>8} {'p95':>8} {'Errors':>7}")
        print("  " + "-" * 65)
        for level, tools_data in sorted(concurrency.items(), key=lambda x: int(x[0])):
            for tool, data in tools_data.items():
                lats = sorted(data.get("latencies", []))
                if lats:
                    from mcp_client import _percentile
                    p50 = _percentile(lats, 50)
                    p95 = _percentile(lats, 95)
                else:
                    p50 = p95 = 0
                print(f"  {level:>6} {tool:<25} {data.get('effective_qps', 0):>7.1f} {p50:>7.1f} {p95:>7.1f} {data.get('errors', 0):>7}")
        print()

    # Resource usage
    resources = results.get("resources", {})
    if resources:
        print("Pi Resources:")
        for k, v in resources.items():
            print(f"  {k}: {v}")
        print()

    print("=" * 80)


async def main():
    parser = argparse.ArgumentParser(description="Levara MCP Benchmark")
    parser.add_argument("--host", default="10.23.0.53", help="Levara host")
    parser.add_argument("--port", type=int, default=8080, help="Levara port")
    parser.add_argument("--iterations", type=int, default=50, help="Iterations per scenario")
    parser.add_argument("--concurrency", default="1,5,10", help="Concurrency levels (comma-separated)")
    parser.add_argument("--ssh-user", default="stek0v", help="SSH user for Pi metrics")
    parser.add_argument("--cold-start", action="store_true", help="Measure cold start (restarts Levara!)")
    parser.add_argument("--output", default=None, help="Output directory for results JSON")
    parser.add_argument("--scenarios", default="memory,chat,knowledge,git",
                        help="Scenarios to run (comma-separated)")
    parser.add_argument("--username", default=None, help="Auth username")
    parser.add_argument("--password", default=None, help="Auth password")
    parser.add_argument("--skip-auth", action="store_true", help="Skip authentication")
    parser.add_argument("--skip-resources", action="store_true", help="Skip SSH resource collection")
    args = parser.parse_args()

    # Resolve output dir
    if args.output is None:
        script_dir = os.path.dirname(os.path.abspath(__file__))
        output_dir = os.path.join(script_dir, "results")
    else:
        output_dir = args.output
    os.makedirs(output_dir, exist_ok=True)

    concurrency_levels = [int(x) for x in args.concurrency.split(",")]
    scenario_list = [s.strip() for s in args.scenarios.split(",")]

    client = MCPClient(host=args.host, port=args.port)
    bench_start = time.monotonic()

    results = {
        "meta": {
            "timestamp": datetime.now().isoformat(),
            "host": args.host,
            "port": args.port,
            "iterations": args.iterations,
            "concurrency_levels": concurrency_levels,
            "scenarios": scenario_list,
        },
        "scenarios": {},
        "tools": {},
        "quality": {},
        "concurrency": {},
        "resources": {},
    }

    async with aiohttp.ClientSession() as session:
        # 1. Health check
        print(f"[1/7] Health check: {client.base_url} ...", end=" ", flush=True)
        healthy = await client.health(session)
        if not healthy:
            print("FAILED")
            print(f"ERROR: Levara not reachable at {client.base_url}")
            sys.exit(1)
        print("OK")

        # 2. Auth
        headers = {}
        if not args.skip_auth:
            print("[2/7] Authenticating ...", end=" ", flush=True)
            try:
                headers = await client.auth(session, args.username, args.password)
                print("OK")
            except Exception as e:
                print(f"WARN: {e} (continuing without auth)")
        else:
            print("[2/7] Skipping auth")

        # 3. Run scenarios
        print(f"[3/7] Running scenarios: {', '.join(scenario_list)} (iterations={args.iterations})")

        if "memory" in scenario_list:
            print("  - memory ...", end=" ", flush=True)
            r = await scenario_memory(client, session, headers, iterations=args.iterations)
            results["scenarios"]["memory"] = {
                "save_count": len(r["saves"]),
                "recall_count": len(r["recalls"]),
                "list_count": len(r["lists"]),
                "hit_rate": r["hit_rate"],
                "errors": r["errors"],
            }
            results["quality"]["memory_hit_rate"] = r["hit_rate"]
            print(f"hit_rate={r['hit_rate']:.2%}")

        if "chat" in scenario_list:
            print("  - chat ...", end=" ", flush=True)
            r = await scenario_chat(client, session, headers, iterations=min(args.iterations, 20))
            results["scenarios"]["chat"] = {
                "save_count": len(r["saves"]),
                "recall_count": len(r["recalls"]),
                "search_count": len(r["searches"]),
                "hit_rate": r["hit_rate"],
                "errors": r["errors"],
            }
            results["quality"]["chat_hit_rate"] = r["hit_rate"]
            print(f"hit_rate={r['hit_rate']:.2%}")

        if "knowledge" in scenario_list:
            print("  - knowledge ...", end=" ", flush=True)
            r = await scenario_knowledge(client, session, headers, iterations=min(args.iterations, 10))
            results["scenarios"]["knowledge"] = {
                "add_text_count": len(r["add_text"]),
                "cognify_count": len(r["cognify"]),
                "search_count": len(r["search"]),
                "errors": r["errors"],
            }
            print("done")

        if "git" in scenario_list:
            print("  - git ...", end=" ", flush=True)
            r = await scenario_git(client, session, headers)
            results["scenarios"]["git"] = {
                "analyze_count": len(r["analyze"]),
                "search_count": len(r["search"]),
                "errors": r["errors"],
            }
            print("done")

        # 4. Concurrency sweep
        print(f"[4/7] Concurrency sweep: levels={concurrency_levels}")
        sweep = await run_concurrency_sweep(
            client, session, headers, concurrency_levels,
            iterations_per_level=min(args.iterations, 50)
        )
        for level, tools_data in sweep.items():
            results["concurrency"][str(level)] = {}
            for tool, data in tools_data.items():
                results["concurrency"][str(level)][tool] = {
                    "effective_qps": data["effective_qps"],
                    "wall_time_ms": data["wall_time_ms"],
                    "errors": data["errors"],
                    "iterations": data["iterations"],
                    "latencies": [round(l, 2) for l in data["latencies"]],
                }
        print("  done")

        # 5. Tool stats
        print("[5/7] Calculating tool statistics ...", end=" ", flush=True)
        results["tools"] = client.stats()
        print(f"{len(results['tools'])} tools")

    # 6. Pi resources
    if not args.skip_resources:
        print(f"[6/7] Collecting Pi resources via SSH ({args.ssh_user}@{args.host}) ...", end=" ", flush=True)
        results["resources"] = collect_pi_resources(args.host, args.ssh_user)
        print("done")
    else:
        print("[6/7] Skipping resource collection")

    # Cold start
    if args.cold_start:
        print("  Cold start measurement ...", end=" ", flush=True)
        cold_ms = measure_cold_start(args.host, args.port, args.ssh_user)
        results["resources"]["cold_start_ms"] = cold_ms
        print(f"{cold_ms:.0f}ms")

    # 7. Save results
    bench_duration = time.monotonic() - bench_start
    results["meta"]["duration_s"] = round(bench_duration, 1)

    ts = datetime.now().strftime("%Y%m%d_%H%M%S")
    output_file = os.path.join(output_dir, f"benchmark_{ts}.json")
    latest_file = os.path.join(output_dir, "latest.json")

    with open(output_file, "w") as f:
        json.dump(results, f, indent=2)
    with open(latest_file, "w") as f:
        json.dump(results, f, indent=2)

    print(f"[7/7] Results saved: {output_file}")

    # Print summary
    format_summary(results)

    return results


if __name__ == "__main__":
    asyncio.run(main())
