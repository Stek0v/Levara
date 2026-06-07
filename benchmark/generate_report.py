#!/usr/bin/env python3
"""Generate markdown report from benchmark results JSON."""
import json
import os
import sys
from datetime import datetime

try:
    import numpy as np
    HAS_NUMPY = True
except ImportError:
    HAS_NUMPY = False


def _percentile(sorted_data, p):
    """Pure-Python percentile (fallback when numpy unavailable)."""
    if not sorted_data:
        return 0.0
    k = (len(sorted_data) - 1) * (p / 100.0)
    f = int(k)
    c = f + 1
    if c >= len(sorted_data):
        return sorted_data[f]
    d = k - f
    return sorted_data[f] + d * (sorted_data[c] - sorted_data[f])


def generate_markdown(data, output_path=None):
    """Convert benchmark JSON to markdown report with tables.

    Returns markdown string. If output_path is set, also writes to file.
    """
    meta = data.get("meta", {})
    tools = data.get("tools", {})
    quality = data.get("quality", {})
    resources = data.get("resources", {})
    concurrency = data.get("concurrency", {})
    scenarios = data.get("scenarios", {})

    lines = []
    lines.append("# Levara MCP Benchmark Report\n")
    lines.append(f"**Date:** {meta.get('timestamp', 'N/A')}")
    lines.append(f"**Host:** {meta.get('host', 'N/A')}:{meta.get('port', 'N/A')}")
    lines.append(f"**Duration:** {meta.get('duration_s', 0):.1f}s")
    lines.append(f"**Iterations:** {meta.get('iterations', 'N/A')}")
    lines.append(f"**Scenarios:** {', '.join(meta.get('scenarios', []))}")
    lines.append("")

    # Tool Latency Table
    if tools:
        lines.append("## Tool Latency (ms)\n")
        lines.append("| Tool | p50 | p95 | p99 | Mean | Min | Max | Calls | Errors |")
        lines.append("|------|-----|-----|-----|------|-----|-----|-------|--------|")
        for name in sorted(tools.keys()):
            s = tools[name]
            lines.append(
                f"| {name} | {s['p50']:.1f} | {s['p95']:.1f} | {s['p99']:.1f} | "
                f"{s['mean']:.1f} | {s.get('min', 0):.1f} | {s.get('max', 0):.1f} | "
                f"{s['calls']} | {s['errors']} |"
            )
        lines.append("")

    # Quality Metrics
    if quality:
        lines.append("## Quality Metrics\n")
        lines.append("| Metric | Value |")
        lines.append("|--------|-------|")
        for metric, val in sorted(quality.items()):
            if isinstance(val, float):
                lines.append(f"| {metric} | {val:.4f} ({val:.1%}) |")
            else:
                lines.append(f"| {metric} | {val} |")
        lines.append("")

    # Scenario Summaries
    if scenarios:
        lines.append("## Scenario Results\n")
        for name, info in sorted(scenarios.items()):
            lines.append(f"### {name.capitalize()}\n")
            lines.append("| Metric | Value |")
            lines.append("|--------|-------|")
            for k, v in sorted(info.items()):
                if isinstance(v, float):
                    lines.append(f"| {k} | {v:.4f} |")
                else:
                    lines.append(f"| {k} | {v} |")
            lines.append("")

    # Concurrency Analysis
    if concurrency:
        lines.append("## Concurrency Analysis\n")

        # Grouped by tool
        all_tools = set()
        for level_data in concurrency.values():
            all_tools.update(level_data.keys())

        for tool in sorted(all_tools):
            lines.append(f"### {tool}\n")
            lines.append("| Concurrency | QPS | p50 | p95 | p99 | Wall Time (ms) | Errors |")
            lines.append("|-------------|-----|-----|-----|-----|----------------|--------|")

            for level in sorted(concurrency.keys(), key=lambda x: int(x)):
                if tool not in concurrency[level]:
                    continue
                d = concurrency[level][tool]
                lats = sorted(d.get("latencies", []))
                if lats:
                    p50 = _percentile(lats, 50)
                    p95 = _percentile(lats, 95)
                    p99 = _percentile(lats, 99)
                else:
                    p50 = p95 = p99 = 0
                lines.append(
                    f"| {level} | {d.get('effective_qps', 0):.1f} | {p50:.1f} | "
                    f"{p95:.1f} | {p99:.1f} | {d.get('wall_time_ms', 0):.0f} | "
                    f"{d.get('errors', 0)} |"
                )
            lines.append("")

    # Resource Usage
    if resources:
        lines.append("## Resource Usage (Raspberry Pi)\n")
        lines.append("| Metric | Value |")
        lines.append("|--------|-------|")
        field_labels = {
            "cpu_percent": "CPU (%)",
            "rss_mb": "Levara RSS (MB)",
            "temp_c": "Temperature (C)",
            "sqlite_size_mb": "SQLite Size (MB)",
            "uptime": "Uptime",
            "cold_start_ms": "Cold Start (ms)",
        }
        for k, v in resources.items():
            label = field_labels.get(k, k)
            lines.append(f"| {label} | {v} |")
        lines.append("")

    # Key Findings
    lines.append("## Key Findings\n")

    # Find fastest/slowest tools
    if tools:
        sorted_tools = sorted(tools.items(), key=lambda x: x[1]["p50"])
        fastest = sorted_tools[0]
        slowest = sorted_tools[-1]
        lines.append(f"- **Fastest tool (p50):** {fastest[0]} at {fastest[1]['p50']:.1f}ms")
        lines.append(f"- **Slowest tool (p50):** {slowest[0]} at {slowest[1]['p50']:.1f}ms")

    # Max QPS from concurrency
    max_qps = 0
    max_qps_tool = ""
    max_qps_level = 0
    for level, tools_data in concurrency.items():
        for tool, d in tools_data.items():
            qps = d.get("effective_qps", 0)
            if qps > max_qps:
                max_qps = qps
                max_qps_tool = tool
                max_qps_level = int(level)
    if max_qps > 0:
        lines.append(f"- **Peak QPS:** {max_qps:.1f} ({max_qps_tool} at concurrency={max_qps_level})")

    # Quality
    for metric, val in quality.items():
        if isinstance(val, float):
            status = "PASS" if val >= 0.8 else "WARN" if val >= 0.5 else "FAIL"
            lines.append(f"- **{metric}:** {val:.1%} [{status}]")

    lines.append("")
    lines.append("---")
    lines.append(f"*Generated at {datetime.now().isoformat()}*")
    lines.append("")

    md = "\n".join(lines)

    if output_path:
        os.makedirs(os.path.dirname(output_path) or ".", exist_ok=True)
        with open(output_path, "w") as f:
            f.write(md)

    return md


def generate_plots(data, output_dir):
    """Generate latency plots if matplotlib is available.

    Silently skips if matplotlib not installed.
    """
    try:
        import matplotlib
        matplotlib.use("Agg")
        import matplotlib.pyplot as plt
    except ImportError:
        print("matplotlib not installed, skipping plots")
        return

    os.makedirs(output_dir, exist_ok=True)
    tools = data.get("tools", {})
    concurrency = data.get("concurrency", {})

    # 1. Tool latency bar chart
    if tools:
        names = sorted(tools.keys())
        p50s = [tools[n]["p50"] for n in names]
        p95s = [tools[n]["p95"] for n in names]

        fig, ax = plt.subplots(figsize=(max(10, len(names) * 0.8), 6))
        x = range(len(names))
        width = 0.35
        ax.bar([i - width/2 for i in x], p50s, width, label="p50", color="#2196F3")
        ax.bar([i + width/2 for i in x], p95s, width, label="p95", color="#FF9800")
        ax.set_ylabel("Latency (ms)")
        ax.set_title("Tool Latency Distribution")
        ax.set_xticks(list(x))
        ax.set_xticklabels(names, rotation=45, ha="right")
        ax.legend()
        plt.tight_layout()
        plt.savefig(os.path.join(output_dir, "tool_latency.png"), dpi=150)
        plt.close()

    # 2. Concurrency QPS chart
    if concurrency:
        all_tools = set()
        for level_data in concurrency.values():
            all_tools.update(level_data.keys())

        fig, ax = plt.subplots(figsize=(8, 5))
        for tool in sorted(all_tools):
            levels = []
            qps_vals = []
            for level in sorted(concurrency.keys(), key=lambda x: int(x)):
                if tool in concurrency[level]:
                    levels.append(int(level))
                    qps_vals.append(concurrency[level][tool].get("effective_qps", 0))
            if levels:
                ax.plot(levels, qps_vals, marker="o", label=tool)

        ax.set_xlabel("Concurrency Level")
        ax.set_ylabel("QPS")
        ax.set_title("Throughput vs Concurrency")
        ax.legend()
        ax.grid(True, alpha=0.3)
        plt.tight_layout()
        plt.savefig(os.path.join(output_dir, "concurrency_qps.png"), dpi=150)
        plt.close()

    print(f"Plots saved to {output_dir}/")


def main():
    if len(sys.argv) < 2:
        # Try latest.json
        script_dir = os.path.dirname(os.path.abspath(__file__))
        default_json = os.path.join(script_dir, "results", "latest.json")
        if os.path.exists(default_json):
            input_path = default_json
        else:
            print(f"Usage: {sys.argv[0]} <benchmark_results.json> [output.md]")
            print(f"       No arguments: reads {default_json}")
            sys.exit(1)
    else:
        input_path = sys.argv[1]

    with open(input_path) as f:
        data = json.load(f)

    # Output path
    if len(sys.argv) >= 3:
        output_md = sys.argv[2]
    else:
        base_dir = os.path.dirname(input_path)
        ts = datetime.now().strftime("%Y%m%d_%H%M%S")
        output_md = os.path.join(base_dir, f"report_{ts}.md")

    md = generate_markdown(data, output_md)
    print(md)
    print(f"\nReport saved to: {output_md}")

    # Try generating plots
    plots_dir = os.path.join(os.path.dirname(output_md), "plots")
    generate_plots(data, plots_dir)


if __name__ == "__main__":
    main()
