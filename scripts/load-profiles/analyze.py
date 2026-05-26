#!/usr/bin/env python3
"""Calibration analysis for RERANK_SCORE_GAP_THRESHOLD (Phase 2.5).

Reads JSONL outputs from p{1,2,3,4,5}_*.py runs and reports score-gap
distributions, threshold-sweep coverage, and a recommended threshold
band. The recommended value optimises for "skip rerank when the
bi-encoder is already confident, otherwise spend the budget" — i.e.
high coverage on profiles where rerank rarely changes the top, but
keep coverage low on profiles where it routinely flips the order.

Usage:
  python3 analyze.py out/p1_rpi64.jsonl out/p2_rpi64.jsonl out/p3_rpi64.jsonl
  python3 analyze.py out/*.jsonl              # auto-detect

Outputs human-readable tables to stdout; pass --json to emit machine-
readable summary alongside.
"""

from __future__ import annotations

import argparse
import json
import math
import os
import sys
from collections import Counter, defaultdict
from typing import Iterable


def pct(values: list[float], p: float) -> float:
    if not values:
        return float("nan")
    arr = sorted(values)
    i = (p / 100.0) * (len(arr) - 1)
    lo = math.floor(i)
    hi = math.ceil(i)
    if lo == hi:
        return arr[lo]
    return arr[lo] + (arr[hi] - arr[lo]) * (i - lo)


def group_by_model(recs: list[dict]) -> dict[str, list[dict]]:
    out: dict[str, list[dict]] = {}
    for r in recs:
        m = r.get("embed_model") or "_unknown"
        out.setdefault(m, []).append(r)
    return out


def summarize_quality(recs: list[dict]) -> dict:
    if not recs:
        return {"n": 0, "mean_recall_top5": 0.0, "top1_keyword_hit_rate": 0.0}
    hits = [int(r.get("keyword_hits_top5", 0)) for r in recs]
    top1 = [bool(r.get("top1_keyword_hit", False)) for r in recs]
    return {
        "n": len(recs),
        "mean_recall_top5": sum(hits) / len(hits),
        "top1_keyword_hit_rate": sum(1 for x in top1 if x) / len(top1),
    }


def render_cross_model_markdown(by_model: dict[str, list[dict]]) -> str:
    lines = ["## Cross-model comparison", ""]
    lines.append("| model | n | mean_recall_top5 | top1_keyword_hit_rate | p50 gap | p50 lat_no_rerank_ms | p50 lat_with_rerank_ms |")
    lines.append("|---|---|---|---|---|---|---|")
    for model in sorted(by_model):
        recs = by_model[model]
        q = summarize_quality(recs)
        gaps = [float(r.get("score_gap_top_bottom", 0.0)) for r in recs]
        lat_no = [float(r.get("latency_no_rerank_ms", 0.0)) for r in recs]
        lat_w = [float(r.get("latency_with_rerank_ms", 0.0)) for r in recs]
        lines.append(
            f"| {model} | {q['n']} | {q['mean_recall_top5']:.3f} | {q['top1_keyword_hit_rate']:.3f} "
            f"| {pct(gaps, 50):.4f} | {pct(lat_no, 50):.1f} | {pct(lat_w, 50):.1f} |"
        )
    return "\n".join(lines)


def load(path: str) -> list[dict]:
    out: list[dict] = []
    with open(path) as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            out.append(json.loads(line))
    return out


def label_for(recs: list[dict], fallback: str) -> str:
    if not recs:
        return fallback
    pid = recs[0].get("profile_id", fallback)
    tgt = recs[0].get("target", "")
    return f"{pid}@{tgt}" if tgt else pid


def per_profile(recs: list[dict]) -> dict:
    gaps = [r["score_gap_top_bottom"] for r in recs if r.get("score_gap_top_bottom") is not None]
    g12 = [r["score_gap_top1_top2"] for r in recs if r.get("score_gap_top1_top2") is not None]
    top1 = [r["score_top1"] for r in recs if r.get("score_top1") is not None]
    lat_n = [r["latency_no_rerank_ms"] for r in recs]
    lat_r = [r["latency_with_rerank_ms"] for r in recs]
    changed = sum(1 for r in recs if r.get("top_changed_by_rerank"))
    zero_hits = sum(1 for r in recs if r.get("n_hits_no_rerank", 0) == 0)
    return {
        "n": len(recs),
        "zero_hits": zero_hits,
        "changed": changed,
        "changed_pct": (100.0 * changed / len(recs)) if recs else 0.0,
        "gap": gaps,
        "g12": g12,
        "top1": top1,
        "lat_n": lat_n,
        "lat_r": lat_r,
        "outcome": Counter(r.get("rerank_outcome") for r in recs),
        "kind": Counter(r.get("query_kind") for r in recs),
    }


def fmt_dist(name: str, arr: list[float], width: int = 7, places: int = 4) -> str:
    if not arr:
        return f"{name}: (no data)"
    return (
        f"{name}: p25={pct(arr,25):.{places}f} p50={pct(arr,50):.{places}f} "
        f"p75={pct(arr,75):.{places}f} p90={pct(arr,90):.{places}f} "
        f"min={min(arr):.{places}f} max={max(arr):.{places}f}"
    )


def print_profile(label: str, s: dict) -> None:
    print(f"\n=== {label} (n={s['n']}) ===")
    print(f"  zero_hits={s['zero_hits']}  top_changed={s['changed']} ({s['changed_pct']:.0f}%)")
    print("  " + fmt_dist("score_gap_top_bottom", s["gap"]))
    print("  " + fmt_dist("score_gap_top1_top2 ", s["g12"]))
    print("  " + fmt_dist("score_top1          ", s["top1"], places=3))
    print(
        f"  latency_ms no/with: p50 {int(pct(s['lat_n'],50))}/{int(pct(s['lat_r'],50))}  "
        f"p95 {int(pct(s['lat_n'],95))}/{int(pct(s['lat_r'],95))}"
    )
    print(f"  rerank_outcome: {dict(s['outcome'])}")
    print(f"  query_kind: {dict(s['kind'])}")


# Score-gap threshold sweep: % of traffic that would skip rerank at each T.
SWEEP_THRESHOLDS = [0.02, 0.03, 0.04, 0.05, 0.06, 0.07, 0.08, 0.10, 0.13, 0.16, 0.20]


def threshold_sweep(profiles: dict[str, list[dict]]) -> None:
    print("\n=== threshold sweep: %% of traffic where gap > T (rerank skipped) ===")
    cols = list(profiles.keys())
    print("  T       " + "  ".join(f"{c:>10}" for c in cols))
    for T in SWEEP_THRESHOLDS:
        row = [f"  {T:6.3f}"]
        for c in cols:
            recs = profiles[c]
            gaps = [r["score_gap_top_bottom"] for r in recs if r.get("score_gap_top_bottom") is not None]
            if not gaps:
                row.append(f"{'-':>10}")
                continue
            skip = sum(1 for g in gaps if g > T)
            row.append(f"{100.0*skip/len(gaps):>9.0f}%")
        print("  ".join(row))


def precision_loss_sweep(profiles: dict[str, list[dict]]) -> None:
    """For each profile and threshold, how many queries where the gate
    would have skipped rerank had `top_changed_by_rerank == True`?
    These are the cases where the gate is "wrong" — bi-encoder looked
    confident but rerank still flipped the top. Lower is better."""
    print("\n=== gate-wrong rate at T: %% of skipped queries where rerank WOULD have changed top ===")
    cols = list(profiles.keys())
    print("  T       " + "  ".join(f"{c:>14}" for c in cols))
    for T in SWEEP_THRESHOLDS:
        row = [f"  {T:6.3f}"]
        for c in cols:
            recs = profiles[c]
            skipped = [r for r in recs if (r.get("score_gap_top_bottom") or 0) > T]
            if not skipped:
                row.append(f"{'-':>14}")
                continue
            wrong = sum(1 for r in skipped if r.get("top_changed_by_rerank"))
            row.append(f"{wrong:>3}/{len(skipped):<3} ({100.0*wrong/len(skipped):>3.0f}%)")
        print("  ".join(row))


def recommend(profiles: dict[str, list[dict]]) -> None:
    """Pick the smallest T at which, averaged across profiles, more
    than 30%% of traffic is skipped AND fewer than 25%% of skipped
    queries lose a rerank flip. This is a heuristic compromise — the
    real choice depends on how much we value the saved latency vs.
    the occasional precision loss. Treat the output as a starting
    point, not a final answer."""
    candidates = []
    for T in SWEEP_THRESHOLDS:
        coverage = []
        wrong_rate = []
        for recs in profiles.values():
            gaps = [r["score_gap_top_bottom"] for r in recs if r.get("score_gap_top_bottom") is not None]
            if not gaps:
                continue
            skipped = [r for r in recs if (r.get("score_gap_top_bottom") or 0) > T]
            coverage.append(len(skipped) / len(gaps))
            if skipped:
                wrong_rate.append(
                    sum(1 for r in skipped if r.get("top_changed_by_rerank")) / len(skipped)
                )
        if not coverage:
            continue
        avg_cov = sum(coverage) / len(coverage)
        avg_wrong = (sum(wrong_rate) / len(wrong_rate)) if wrong_rate else 0.0
        candidates.append((T, avg_cov, avg_wrong))

    print("\n=== recommendation scan ===")
    print(f"  {'T':>6}  {'avg_skip%':>10}  {'avg_wrong%':>11}")
    for T, cov, wrong in candidates:
        print(f"  {T:>6.3f}  {100*cov:>9.1f}%  {100*wrong:>10.1f}%")

    eligible = [
        (T, cov, wrong) for T, cov, wrong in candidates if cov > 0.30 and wrong < 0.25
    ]
    if eligible:
        T, cov, wrong = min(eligible, key=lambda x: x[0])
        print(
            f"\n  suggested RERANK_SCORE_GAP_THRESHOLD={T:.3f} "
            f"(skips ~{100*cov:.0f}% of traffic, gate-wrong ~{100*wrong:.0f}%)"
        )
    else:
        print("\n  no threshold meets both targets (skip>30%, wrong<25%) — leave gate off or widen the corpus")


def main() -> int:
    p = argparse.ArgumentParser()
    p.add_argument("paths", nargs="+", help="JSONL output files (any number)")
    p.add_argument("--json", action="store_true", help="also emit JSON summary on stdout")
    p.add_argument("--by-model", action="store_true", help="group records by embed_model and emit per-model + cross-model report")
    args = p.parse_args()

    profiles: dict[str, list[dict]] = {}
    for path in args.paths:
        recs = load(path)
        if not recs:
            print(f"[warn] {path}: empty", file=sys.stderr)
            continue
        key = label_for(recs, os.path.basename(path).split(".")[0])
        profiles[key] = recs

    if not profiles:
        print("no data", file=sys.stderr)
        return 1

    for label, recs in profiles.items():
        print_profile(label, per_profile(recs))

    threshold_sweep(profiles)
    precision_loss_sweep(profiles)
    recommend(profiles)

    if args.by_model:
        all_recs: list[dict] = []
        for path in args.paths:
            all_recs.extend(load(path))
        by_model = group_by_model(all_recs)
        for model, recs in sorted(by_model.items()):
            print(f"\n=== model: {model} (n={len(recs)}) ===")
            profiles_m = per_profile(recs)
            print_profile(f"{model}", profiles_m)
            threshold_sweep({model: recs})
        print()
        print(render_cross_model_markdown(by_model))

    if args.json:
        summary = {
            label: {
                k: (list(v) if isinstance(v, (set,)) else v)
                for k, v in per_profile(recs).items()
                if k not in ("gap", "g12", "top1", "lat_n", "lat_r", "outcome", "kind")
            }
            for label, recs in profiles.items()
        }
        print("\n--- JSON ---")
        print(json.dumps(summary, indent=2))
    return 0


if __name__ == "__main__":
    sys.exit(main())
