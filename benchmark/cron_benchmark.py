#!/usr/bin/env python3
"""Mini-benchmark for hourly cron job on Pi. Records metrics to SQLite."""
import sqlite3
import time
import os
import json
import subprocess
import sys

try:
    import requests
    HAS_REQUESTS = True
except ImportError:
    HAS_REQUESTS = False

DB_PATH = os.getenv("LEVARA_METRICS_DB", "/var/lib/levara/metrics.db")
LEVARA_URL = os.getenv("LEVARA_URL", "http://localhost:8080")
ALERT_THRESHOLD_MS = 5000


def init_db():
    """Initialize SQLite database with metrics table."""
    db_dir = os.path.dirname(DB_PATH)
    if db_dir and not os.path.exists(db_dir):
        os.makedirs(db_dir, exist_ok=True)

    conn = sqlite3.connect(DB_PATH)
    conn.execute("""CREATE TABLE IF NOT EXISTS benchmark_metrics (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        timestamp TEXT NOT NULL,
        tool TEXT NOT NULL,
        latency_ms REAL NOT NULL,
        status TEXT NOT NULL,
        rss_mb INTEGER,
        cpu_percent REAL,
        temp_c INTEGER,
        sqlite_size_mb REAL
    )""")
    conn.execute("""CREATE INDEX IF NOT EXISTS idx_metrics_timestamp
        ON benchmark_metrics(timestamp)""")
    conn.execute("""CREATE INDEX IF NOT EXISTS idx_metrics_tool
        ON benchmark_metrics(tool)""")
    conn.commit()
    return conn


def measure_tool_requests(tool_name, arguments):
    """Call MCP tool via requests and measure latency."""
    start = time.monotonic()
    resp = requests.post(f"{LEVARA_URL}/mcp", json={
        "jsonrpc": "2.0", "id": 1,
        "method": "tools/call",
        "params": {"name": tool_name, "arguments": arguments}
    }, timeout=30)
    latency = (time.monotonic() - start) * 1000
    status = "ok" if resp.status_code == 200 else f"error_{resp.status_code}"
    return latency, status


def measure_tool_urllib(tool_name, arguments):
    """Call MCP tool via urllib (no requests dependency)."""
    import urllib.request
    import urllib.error

    payload = json.dumps({
        "jsonrpc": "2.0", "id": 1,
        "method": "tools/call",
        "params": {"name": tool_name, "arguments": arguments}
    }).encode("utf-8")

    req = urllib.request.Request(
        f"{LEVARA_URL}/mcp",
        data=payload,
        headers={"Content-Type": "application/json"},
        method="POST"
    )

    start = time.monotonic()
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            latency = (time.monotonic() - start) * 1000
            status = "ok" if resp.status == 200 else f"error_{resp.status}"
    except urllib.error.HTTPError as e:
        latency = (time.monotonic() - start) * 1000
        status = f"error_{e.code}"
    except Exception as e:
        latency = (time.monotonic() - start) * 1000
        status = f"error_{type(e).__name__}"

    return latency, status


def measure_tool(tool_name, arguments):
    """Call MCP tool and measure latency. Uses requests if available, else urllib."""
    if HAS_REQUESTS:
        return measure_tool_requests(tool_name, arguments)
    return measure_tool_urllib(tool_name, arguments)


def get_system_metrics():
    """Collect CPU, RAM, temp, SQLite size from local system."""
    metrics = {
        "rss_mb": -1,
        "cpu_percent": -1.0,
        "temp_c": -1,
        "sqlite_size_mb": -1.0,
    }

    # Levara RSS (KB -> MB)
    try:
        result = subprocess.run(
            "ps aux | grep '[l]evara' | awk '{sum += $6} END {print sum}'",
            shell=True, capture_output=True, text=True, timeout=5
        )
        rss_kb = result.stdout.strip()
        if rss_kb:
            metrics["rss_mb"] = int(int(rss_kb) / 1024)
    except Exception:
        pass

    # CPU usage
    try:
        result = subprocess.run(
            "grep 'cpu ' /proc/stat | awk '{usage=($2+$4)*100/($2+$4+$5)} END {print usage}'",
            shell=True, capture_output=True, text=True, timeout=5
        )
        cpu = result.stdout.strip()
        if cpu:
            metrics["cpu_percent"] = round(float(cpu), 1)
    except Exception:
        pass

    # Temperature
    try:
        with open("/sys/class/thermal/thermal_zone0/temp") as f:
            temp_raw = int(f.read().strip())
            metrics["temp_c"] = temp_raw // 1000
    except Exception:
        try:
            result = subprocess.run(
                "vcgencmd measure_temp", shell=True,
                capture_output=True, text=True, timeout=5
            )
            import re
            m = re.search(r"([\d.]+)", result.stdout)
            if m:
                metrics["temp_c"] = int(float(m.group(1)))
        except Exception:
            pass

    # SQLite DB size
    try:
        db_paths = [
            "/home/stek0v/levara/data/",
            "/var/lib/levara/",
        ]
        total_mb = 0
        for dp in db_paths:
            if os.path.isdir(dp):
                for fname in os.listdir(dp):
                    if fname.endswith(".db"):
                        fpath = os.path.join(dp, fname)
                        total_mb += os.path.getsize(fpath) / (1024 * 1024)
        metrics["sqlite_size_mb"] = round(total_mb, 2)
    except Exception:
        pass

    return metrics


def check_alerts(tool, latency, status, ts):
    """Print alerts for slow or failing tools."""
    if latency > ALERT_THRESHOLD_MS:
        print(f"ALERT: {tool} latency {latency:.0f}ms > {ALERT_THRESHOLD_MS}ms at {ts}")
    if status != "ok":
        print(f"ALERT: {tool} returned {status} at {ts}")


def print_recent_stats(conn, hours=24):
    """Print summary of last N hours for quick diagnostics."""
    cursor = conn.execute("""
        SELECT tool, COUNT(*) as calls,
               ROUND(AVG(latency_ms), 1) as avg_lat,
               ROUND(MAX(latency_ms), 1) as max_lat,
               SUM(CASE WHEN status != 'ok' THEN 1 ELSE 0 END) as errors
        FROM benchmark_metrics
        WHERE timestamp > datetime('now', ?)
        GROUP BY tool
    """, (f"-{hours} hours",))

    rows = cursor.fetchall()
    if rows:
        print(f"\nLast {hours}h summary:")
        print(f"  {'Tool':<20} {'Calls':>6} {'Avg(ms)':>8} {'Max(ms)':>8} {'Errors':>7}")
        for tool, calls, avg_lat, max_lat, errors in rows:
            print(f"  {tool:<20} {calls:>6} {avg_lat:>8.1f} {max_lat:>8.1f} {errors:>7}")


def main():
    conn = init_db()
    ts = time.strftime("%Y-%m-%dT%H:%M:%S")
    sys_metrics = get_system_metrics()

    tools_to_test = [
        ("save_memory", {"key": f"cron_{ts}", "value": "health check", "type": "system"}),
        ("recall_memory", {"query": "health check"}),
        ("search", {"search_query": "test", "top_k": 3}),
    ]

    for tool, args in tools_to_test:
        try:
            lat, status = measure_tool(tool, args)
        except Exception as e:
            lat = -1
            status = f"error_{type(e).__name__}"

        check_alerts(tool, lat, status, ts)

        conn.execute(
            "INSERT INTO benchmark_metrics VALUES (NULL, ?, ?, ?, ?, ?, ?, ?, ?)",
            (ts, tool, lat, status,
             sys_metrics["rss_mb"], sys_metrics["cpu_percent"],
             sys_metrics["temp_c"], sys_metrics["sqlite_size_mb"])
        )

    conn.commit()

    # Cleanup: remove old cron memory keys (keep last 100)
    try:
        measure_tool("delete_memory", {"key": f"cron_{ts}"})
    except Exception:
        pass

    # Print 24h summary if running interactively
    if sys.stdout.isatty():
        print_recent_stats(conn, hours=24)

    conn.close()


if __name__ == "__main__":
    main()
