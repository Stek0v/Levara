#!/usr/bin/env python3
"""Compare a shadow collection against a baseline snapshot.

Re-runs the baseline's queries against the shadow collection, computes
Jaccard@10, top-1 stability, empty-result rate, latency ratio. Emits a
markdown report and exits 0 only if all thresholds pass.

Read-only against the target.

Usage:
    parity_check.py --target-url http://localhost:8090 \
        --token-file ~/.levara/prod-token \
        --baseline baselines/foo.json --shadow foo__potion \
        --out docs/reembed/parity-foo-20260526.md \
        [--jaccard10 0.6 --top1 0.5 --empty 0.05 --latency-ratio 1.2]
"""
from __future__ import annotations

import argparse
import json
import os
import statistics
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from _levara_http import Target, get_meta, search_text, eprint


def _read_token(token_file: str | None, token_inline: str | None) -> str:
    if token_inline:
        return token_inline
    if token_file:
        p = Path(token_file).expanduser()
        if not p.exists():
            raise SystemExit(f"token file not found: {p}")
        return p.read_text().strip()
    return os.environ.get("LEVARA_TOKEN", "")


def _jaccard(a: list[str], b: list[str]) -> float:
    sa, sb = set(a), set(b)
    if not sa and not sb:
        return 1.0
    return len(sa & sb) / max(1, len(sa | sb))


def main() -> int:
    p = argparse.ArgumentParser(description="Parity check vs baseline")
    p.add_argument("--target-url", required=True)
    p.add_argument("--token-file", default=None)
    p.add_argument("--token", default=None)
    p.add_argument("--baseline", required=True)
    p.add_argument("--shadow", required=True)
    p.add_argument("--out", default=None, help="markdown report path; stdout if omitted")
    p.add_argument("--jaccard10", type=float, default=0.6)
    p.add_argument("--top1", type=float, default=0.5)
    p.add_argument("--empty", type=float, default=0.05)
    p.add_argument("--latency-ratio", type=float, default=1.2)
    args = p.parse_args()

    with open(args.baseline, encoding="utf-8") as f:
        baseline = json.load(f)
    if not baseline.get("queries"):
        raise SystemExit("baseline has no queries")

    token = _read_token(args.token_file, args.token)
    target = Target(base_url=args.target_url, token=token)

    shadow_meta = get_meta(target, args.shadow)
    if not shadow_meta or not shadow_meta.get("name"):
        raise SystemExit(f"shadow collection {args.shadow!r} not found")

    top_k = int(baseline.get("top_k", 10))
    queries = baseline["queries"]

    per_q = []
    jaccards = []
    top1_same = 0
    empties = 0
    latencies_shadow = []
    latencies_baseline = []
    for q in queries:
        text = q["text"]
        hits, latency_ms = search_text(target, args.shadow, text, top_k=top_k, rerank=False)
        shadow_ids = [h.get("id") for h in hits if isinstance(h, dict) and h.get("id")][:top_k]
        base_ids = q.get("top_ids", [])[:top_k]
        j = _jaccard(base_ids, shadow_ids)
        jaccards.append(j)
        top1_match = bool(shadow_ids) and shadow_ids[0] == q.get("top_1_id")
        if top1_match:
            top1_same += 1
        if not hits:
            empties += 1
        if isinstance(q.get("latency_ms"), int):
            latencies_baseline.append(q["latency_ms"])
        latencies_shadow.append(latency_ms)
        per_q.append(
            {
                "id": q["id"],
                "text": text[:80],
                "baseline_top1": q.get("top_1_id"),
                "shadow_top1": shadow_ids[0] if shadow_ids else None,
                "jaccard10": round(j, 3),
                "shadow_n_hits": len(hits),
                "shadow_latency_ms": latency_ms,
                "baseline_latency_ms": q.get("latency_ms"),
            }
        )

    n = len(queries)
    j_mean = sum(jaccards) / n
    top1_rate = top1_same / n
    empty_rate = empties / n
    lat_shadow_p50 = statistics.median(latencies_shadow)
    lat_base_p50 = statistics.median(latencies_baseline) if latencies_baseline else None
    lat_ratio = (lat_shadow_p50 / lat_base_p50) if lat_base_p50 else None

    checks = [
        ("Jaccard@10 mean", j_mean, args.jaccard10, ">="),
        ("Top-1 stability", top1_rate, args.top1, ">="),
        ("Empty-result rate", empty_rate, args.empty, "<="),
    ]
    if lat_ratio is not None:
        checks.append(("Latency ratio shadow/baseline (p50)", lat_ratio, args.latency_ratio, "<="))

    failed = []
    for name, val, thr, op in checks:
        ok = (val >= thr) if op == ">=" else (val <= thr)
        if not ok:
            failed.append((name, val, thr, op))

    verdict = "PASS" if not failed else "FAIL"

    lines = []
    lines.append(f"# Parity report — {baseline.get('collection')} → {args.shadow}")
    lines.append("")
    lines.append(f"**Verdict:** {verdict}  ")
    lines.append(f"**Generated:** {time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())}  ")
    lines.append(f"**Baseline captured:** {baseline.get('captured_at')}  ")
    lines.append(
        f"**Baseline model:** {baseline.get('embedding_model')} (dim {baseline.get('embedding_dim')})  "
    )
    lines.append(
        f"**Shadow model:** {shadow_meta.get('embedding_model')} (dim {shadow_meta.get('embedding_dim')})  "
    )
    lines.append(
        f"**Records:** baseline={baseline.get('record_count')}, shadow={shadow_meta.get('record_count')}  "
    )
    lines.append("")
    lines.append("## Aggregate metrics")
    lines.append("")
    lines.append("| metric | value | threshold | pass |")
    lines.append("|---|---|---|---|")
    for name, val, thr, op in checks:
        ok = (val >= thr) if op == ">=" else (val <= thr)
        lines.append(f"| {name} | {val:.3f} | {op} {thr} | {'✓' if ok else '✗'} |")
    lines.append("")
    if lat_base_p50 is not None:
        lines.append(
            f"Shadow p50 latency: {lat_shadow_p50:.0f}ms · baseline p50: {lat_base_p50:.0f}ms"
        )
    else:
        lines.append(f"Shadow p50 latency: {lat_shadow_p50:.0f}ms (no baseline latency recorded)")
    lines.append("")
    lines.append("## Per-query")
    lines.append("")
    lines.append("| q | text | jaccard@10 | base top1 | shadow top1 | n hits | shadow ms |")
    lines.append("|---|---|---|---|---|---|---|")
    for r in per_q:
        lines.append(
            f"| {r['id']} | {r['text']!r} | {r['jaccard10']} | "
            f"{r['baseline_top1']} | {r['shadow_top1']} | "
            f"{r['shadow_n_hits']} | {r['shadow_latency_ms']} |"
        )

    if failed:
        lines.append("")
        lines.append("## Failed thresholds")
        for name, val, thr, op in failed:
            lines.append(f"- **{name}**: got {val:.3f}, need {op} {thr}")

    report = "\n".join(lines) + "\n"

    if args.out:
        os.makedirs(os.path.dirname(os.path.abspath(args.out)) or ".", exist_ok=True)
        with open(args.out, "w", encoding="utf-8") as f:
            f.write(report)
        eprint(f"[parity] wrote {args.out}")
    else:
        print(report)

    eprint(f"[parity] verdict={verdict} jaccard_mean={j_mean:.3f} top1={top1_rate:.3f} empty={empty_rate:.3f}")
    return 0 if not failed else 1


if __name__ == "__main__":
    sys.exit(main())
