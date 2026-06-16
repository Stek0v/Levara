#!/usr/bin/env python3
"""
Levara Agent Memory Evaluation Harness
======================================
Maps the Mem0/Zep/LangMem evaluation framework onto Levara MCP memory tools.

Categories (0–3 score each, 12 categories / 36 pts max):
  1. CRUD + memory types (semantic / episodic / procedural)
  2. Retrieval quality (Recall@K, Precision@K, NDCG, MRR, Hit Rate)
  3. Latency & throughput (p50/p95/p99)
  4. Consolidation & dedup
  5. LLM integration surface (wake_up, pin, tools)
  6. Multi-tenant isolation (collection scope)
  7. Edge cases (upsert, unicode, invalid hall)
  8. Observability (doctor, runtime_stats)
  9. Cross-session persistence (reconnect → recall)
 10. Owner isolation (JWT user A vs B)
 11. Context efficiency (wake_up token budget)
 12. Scale smoke (N memories → recall latency)

Usage:
  python3 benchmark/memory_eval/run_memory_eval.py \\
    --url http://localhost:8081 --label local-mac

  python3 benchmark/memory_eval/run_memory_eval.py \\
    --url http://10.23.0.64:8080 --label qwen-64
"""
from __future__ import annotations

import argparse
import asyncio
import json
import logging
import math
import os
import sys
import time
import uuid
from dataclasses import dataclass, field, asdict
from datetime import datetime, timezone
from pathlib import Path
from typing import Any
from urllib.parse import urlparse

import aiohttp

# Allow imports from tests/
REPO_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(REPO_ROOT / "tests"))
from conftest_mcp import MCPTestClient, percentile  # noqa: E402

SCRIPT_DIR = Path(__file__).resolve().parent
GOLDEN_PATH = SCRIPT_DIR / "golden_dataset.json"
RESULTS_DIR = SCRIPT_DIR / "results"


def setup_logging(verbose: bool, log_file: Path | None) -> logging.Logger:
    logger = logging.getLogger("memory_eval")
    logger.handlers.clear()
    logger.setLevel(logging.DEBUG if verbose else logging.INFO)
    fmt = logging.Formatter(
        "%(asctime)s | %(levelname)-7s | %(message)s",
        datefmt="%Y-%m-%d %H:%M:%S",
    )
    sh = logging.StreamHandler(sys.stdout)
    sh.setFormatter(fmt)
    logger.addHandler(sh)
    if log_file:
        log_file.parent.mkdir(parents=True, exist_ok=True)
        fh = logging.FileHandler(log_file, encoding="utf-8")
        fh.setFormatter(fmt)
        logger.addHandler(fh)
    return logger


@dataclass
class StepLog:
    category: str
    step: str
    passed: bool
    latency_ms: float = 0.0
    detail: str = ""
    meta: dict = field(default_factory=dict)


@dataclass
class CategoryScore:
    name: str
    score: int = 0  # 0-3
    max_score: int = 3
    passed: int = 0
    failed: int = 0
    notes: list[str] = field(default_factory=list)
    metrics: dict = field(default_factory=dict)


@dataclass
class EvalReport:
    label: str
    url: str
    started_at: str
    finished_at: str = ""
    duration_s: float = 0.0
    services: dict = field(default_factory=dict)
    categories: list[CategoryScore] = field(default_factory=list)
    retrieval: dict = field(default_factory=dict)
    latency: dict = field(default_factory=dict)
    steps: list[StepLog] = field(default_factory=list)
    timing: dict = field(default_factory=dict)
    overall_score: float = 0.0

    def to_dict(self) -> dict:
        d = asdict(self)
        d["categories"] = [asdict(c) for c in self.categories]
        d["steps"] = [asdict(s) for s in self.steps]
        return d


def _text(result: dict) -> str:
    content = result.get("content", [])
    if content and isinstance(content, list):
        return content[0].get("text", "")
    return str(result)


def _is_error(result: dict) -> bool:
    return result.get("isError", False)


def _parse_recall(result: dict) -> list[dict]:
    txt = _text(result)
    if "No memories found" in txt:
        return []
    try:
        data = json.loads(txt)
        if isinstance(data, dict) and "results" in data:
            return data["results"] if isinstance(data["results"], list) else []
    except json.JSONDecodeError:
        pass
    return []


def _keys_from_results(rows: list[dict]) -> set[str]:
    return {r.get("key", "") for r in rows if r.get("key")}


def _recall_at_k(expected: set[str], retrieved_keys: list[str], k: int) -> float:
    if not expected:
        return 1.0 if not retrieved_keys[:k] else 0.0
    top = set(retrieved_keys[:k])
    return len(expected & top) / len(expected)


def _mrr(expected: set[str], retrieved_keys: list[str]) -> float:
    for i, k in enumerate(retrieved_keys, start=1):
        if k in expected:
            return 1.0 / i
    return 0.0 if expected else 1.0


def _precision_at_k(expected: set[str], retrieved_keys: list[str], k: int) -> float:
    top = retrieved_keys[:k]
    if not top:
        return 0.0
    hits = sum(1 for key in top if key in expected)
    return hits / len(top)


def _ndcg_at_k(expected: set[str], retrieved_keys: list[str], k: int) -> float:
    if not expected:
        return 1.0 if not retrieved_keys[:k] else 0.0
    dcg = 0.0
    for i, key in enumerate(retrieved_keys[:k], start=1):
        if key in expected:
            dcg += 1.0 / math.log2(i + 1)
    ideal = min(len(expected), k)
    idcg = sum(1.0 / math.log2(i + 1) for i in range(1, ideal + 1))
    return dcg / idcg if idcg > 0 else 0.0


def _embed_connected(services: dict) -> bool:
    return services.get("embed", {}).get("status") == "connected"


class MemoryEvaluator:
    def __init__(
        self,
        url: str,
        label: str,
        logger: logging.Logger,
        latency_budgets: dict | None = None,
        scale_memories: int = 50,
    ):
        self.url = url.rstrip("/")
        self.label = label
        self.log = logger
        self.scale_memories = max(10, scale_memories)
        self.latency_budgets = latency_budgets or {
            "save_p95_ms": 100,
            "recall_p95_ms": 200,
            "concurrent_wall_ms": 3000,
        }
        self.client: MCPTestClient | None = None
        self.collection = f"memeval_{uuid.uuid4().hex[:10]}"
        self._auth_creds: tuple[str, str] | None = None
        self._owner_b_creds: tuple[str, str] | None = None
        self._use_auth = False
        self._cat_wall_s: dict[str, float] = {}
        self.report = EvalReport(
            label=label,
            url=url,
            started_at=datetime.now(timezone.utc).isoformat(),
        )
        self._latencies: dict[str, list[float]] = {}

    def _record_step(self, category: str, step: str, passed: bool, latency_ms: float = 0, **meta):
        self.report.steps.append(
            StepLog(category=category, step=step, passed=passed, latency_ms=latency_ms, meta=meta)
        )
        status = "PASS" if passed else "FAIL"
        self.log.info("[%s] %s — %s (%.1f ms) %s", category, step, status, latency_ms, meta.get("detail", ""))

    def _track_latency(self, tool: str, ms: float):
        self._latencies.setdefault(tool, []).append(ms)

    def _finalize_timing(self, t0: float):
        """Split total duration into memory ops vs consolidation (LLM-heavy cat4)."""
        total_s = round(time.perf_counter() - t0, 2)
        consolidate_s = self._cat_wall_s.get("4_consolidation_forgetting", 0.0)
        memory_ops_s = round(
            sum(v for k, v in self._cat_wall_s.items() if k != "4_consolidation_forgetting"),
            2,
        )
        consolidate_ms = 0.0
        for step in self.report.steps:
            if step.step == "consolidate_dry_run":
                consolidate_ms = step.latency_ms
                break
        self.report.timing = {
            "total_s": total_s,
            "memory_ops_s": memory_ops_s,
            "consolidation_cat_s": consolidate_s,
            "consolidate_llm_ms": round(consolidate_ms, 1),
            "by_category_s": dict(self._cat_wall_s),
        }
        self.report.duration_s = total_s

    async def connect(self, use_auth: bool = False):
        self._use_auth = use_auth
        self.client = MCPTestClient(self.url)
        if use_auth:
            if self._auth_creds is None:
                self._auth_creds = (
                    os.environ.get("LEVARA_TEST_EMAIL", "memeval@bench.local"),
                    os.environ.get("LEVARA_TEST_PASSWORD", "MemEval_Bench_Local_2026"),
                )
            token = await self.client.login_or_register(*self._auth_creds)
            self.log.info("Authenticated (JWT sub will scope owner_id)")
            self.report.services["auth"] = {
                "mode": "jwt",
                "token_prefix": token[:16] + "...",
                "email": self._auth_creds[0],
            }
        await self.client.connect()
        details = await self.client.health_details()
        self.report.services = details.get("services", {})
        embed = self.report.services.get("embed", {})
        llm = self.report.services.get("llm", {})
        self.log.info(
            "Connected %s | embed=%s (%s) | llm=%s (%s) | dim=%s",
            self.url,
            embed.get("status"),
            embed.get("model", "?"),
            llm.get("status"),
            llm.get("model", "?"),
            self.report.services.get("collections", {}).get("dimension"),
        )

    async def close(self):
        if self.client:
            await self.client.close()

    async def _call(self, tool: str, args: dict | None = None) -> tuple[dict, float]:
        assert self.client
        result, lat = await self.client.call_tool_timed(tool, args or {})
        self._track_latency(tool, lat)
        return result, lat

    async def _set_context(self):
        await self._call("set_context", {"collection": self.collection})

    # ── Category 1: CRUD + memory types ─────────────────────────────────────

    async def cat1_crud(self) -> CategoryScore:
        cat = CategoryScore(name="1_crud_memory_types")
        await self._set_context()

        # CREATE semantic
        r, lat = await self._call(
            "save_memory",
            {
                "key": "crud_semantic",
                "value": "User loves black coffee",
                "collection": self.collection,
                "room": "personal",
                "hall": "fact",
            },
        )
        ok = not _is_error(r) and "saved" in _text(r).lower()
        cat.passed += int(ok)
        cat.failed += int(not ok)
        self._record_step(cat.name, "create_semantic", ok, lat)

        # CREATE episodic
        r, lat = await self._call(
            "save_memory",
            {
                "key": "crud_episodic",
                "value": "2026-06-10: rescheduled demo to Friday",
                "collection": self.collection,
                "room": "calendar",
                "hall": "event",
            },
        )
        ok = not _is_error(r)
        cat.passed += int(ok)
        cat.failed += int(not ok)
        self._record_step(cat.name, "create_episodic", ok, lat)

        # CREATE procedural (preference)
        r, lat = await self._call(
            "save_memory",
            {
                "key": "crud_procedural",
                "value": "Always run tests before commit",
                "collection": self.collection,
                "room": "workflow",
                "hall": "advice",
            },
        )
        ok = not _is_error(r)
        cat.passed += int(ok)
        cat.failed += int(not ok)
        self._record_step(cat.name, "create_procedural", ok, lat)

        # Early persistence gate — catches silent DB write failures
        r_pre, lat_pre = await self._call("list_memories", {"collection": self.collection})
        pre_ok = "crud_semantic" in _text(r_pre)
        self._record_step(cat.name, "persistence_after_create", pre_ok, lat_pre)
        cat.passed += int(pre_ok)
        cat.failed += int(not pre_ok)

        # READ
        r, lat = await self._call(
            "recall_memory", {"query": "crud_semantic", "collection": self.collection}
        )
        rows = _parse_recall(r)
        ok = any(row.get("key") == "crud_semantic" for row in rows)
        cat.passed += int(ok)
        cat.failed += int(not ok)
        self._record_step(cat.name, "read_recall", ok, lat, detail=f"rows={len(rows)}")

        # UPDATE (upsert same key)
        r, lat = await self._call(
            "save_memory",
            {
                "key": "crud_semantic",
                "value": "User switched to espresso",
                "collection": self.collection,
                "room": "personal",
                "hall": "fact",
            },
        )
        ok = not _is_error(r)
        r2, _ = await self._call(
            "recall_memory", {"query": "crud_semantic", "collection": self.collection}
        )
        rows = _parse_recall(r2)
        ok = ok and any("espresso" in (row.get("value") or "") for row in rows)
        cat.passed += int(ok)
        cat.failed += int(not ok)
        self._record_step(cat.name, "update_upsert", ok, lat)

        # LIST
        r, lat = await self._call("list_memories", {"collection": self.collection})
        ok = not _is_error(r) and "crud_" in _text(r)
        cat.passed += int(ok)
        cat.failed += int(not ok)
        self._record_step(cat.name, "list_memories", ok, lat)

        # DELETE
        r, lat = await self._call("delete_memory", {"key": "crud_procedural", "collection": self.collection})
        ok = not _is_error(r)
        r2, _ = await self._call(
            "recall_memory", {"query": "crud_procedural", "collection": self.collection}
        )
        rows = _parse_recall(r2)
        ok = ok and not any(row.get("key") == "crud_procedural" for row in rows)
        cat.passed += int(ok)
        cat.failed += int(not ok)
        self._record_step(cat.name, "delete_memory", ok, lat)

        total = cat.passed + cat.failed
        cat.score = min(3, round(3 * cat.passed / total)) if total else 0
        cat.metrics = {"passed": cat.passed, "failed": cat.failed, "total": total}
        return cat

    # ── Category 2: Retrieval quality ───────────────────────────────────────

    async def cat2_retrieval(self, golden: dict) -> CategoryScore:
        cat = CategoryScore(name="2_retrieval_quality")
        await self._set_context()

        recall_at_3: list[float] = []
        recall_at_5: list[float] = []
        precision_at_3: list[float] = []
        ndcg_at_3: list[float] = []
        mrr_scores: list[float] = []
        hit_rates: list[float] = []
        query_logs: list[dict] = []
        embed_ok = _embed_connected(self.report.services)
        skipped_embed = 0
        golden_prefix = f"{self.collection}_golden"

        for case in golden["cases"]:
            case_id = case["id"]
            # One collection per case — no CRUD pollution or cross-case vector bleed.
            coll = f"{golden_prefix}_{case_id}"
            case_needs_embed = case.get("requires_embed", False)

            if "sequence" in case:
                for step in case["sequence"]:
                    await self._call("save_memory", {**step, "collection": coll})
            elif "saves" in case:
                for s in case["saves"]:
                    await self._call("save_memory", {**s, "collection": coll})
            elif "save" in case:
                await self._call("save_memory", {**case["save"], "collection": coll})

            for d in case.get("distractor_saves", []):
                await self._call("save_memory", {**d, "collection": coll})

            for q in case.get("queries", []):
                q_needs_embed = q.get("requires_embed", case_needs_embed)
                if q_needs_embed and not embed_ok:
                    skipped_embed += 1
                    cat.notes.append(f"skipped embed-required: {case_id}/{q['query'][:24]}")
                    self._record_step(
                        cat.name,
                        f"golden_{case_id}:{q['query'][:30]}",
                        False,
                        0,
                        detail="embed required but unreachable",
                    )
                    continue

                args = {"query": q["query"], "collection": coll}
                if q.get("hall"):
                    args["hall"] = q["hall"]
                if q.get("room"):
                    args["room"] = q["room"]

                r, lat = await self._call("recall_memory", args)
                rows = _parse_recall(r)
                retrieved_keys = [row.get("key", "") for row in rows]
                text_blob = json.dumps(rows, ensure_ascii=False)

                expect_keys = set(q.get("expect_keys", []))
                r3 = _recall_at_k(expect_keys, retrieved_keys, 3)
                r5 = _recall_at_k(expect_keys, retrieved_keys, 5)
                p3 = _precision_at_k(expect_keys, retrieved_keys, 3)
                ndcg3 = _ndcg_at_k(expect_keys, retrieved_keys, 3)
                mrr = _mrr(expect_keys, retrieved_keys)
                hit = 1.0 if (not expect_keys or expect_keys & set(retrieved_keys)) else 0.0

                subs_ok = all(sub in text_blob for sub in q.get("expect_substrings", []))
                must_not = all(bad not in text_blob for bad in q.get("must_not_contain", []))
                min_hits = q.get("min_hits", len(expect_keys) if expect_keys else 0)
                hits_ok = len(set(retrieved_keys) & expect_keys) >= min_hits if expect_keys else True
                min_p3 = q.get("min_precision_at_3")
                prec_ok = p3 >= min_p3 if min_p3 is not None else True
                min_mrr = q.get("min_mrr")
                mrr_ok = mrr >= min_mrr if min_mrr is not None else True

                passed = subs_ok and must_not and hits_ok and prec_ok and mrr_ok
                if expect_keys:
                    passed = passed and hit > 0

                recall_at_3.append(r3)
                recall_at_5.append(r5)
                precision_at_3.append(p3)
                ndcg_at_3.append(ndcg3)
                mrr_scores.append(mrr)
                hit_rates.append(hit)

                cat.passed += int(passed)
                cat.failed += int(not passed)
                self._record_step(
                    cat.name,
                    f"golden_{case_id}:{q['query'][:30]}",
                    passed,
                    lat,
                    detail=f"keys={retrieved_keys[:5]} r@3={r3:.2f} p@3={p3:.2f}",
                )
                query_logs.append(
                    {
                        "case": case_id,
                        "query": q["query"],
                        "expect_keys": list(expect_keys),
                        "retrieved_keys": retrieved_keys,
                        "recall@3": round(r3, 4),
                        "precision@3": round(p3, 4),
                        "ndcg@3": round(ndcg3, 4),
                        "mrr": round(mrr, 4),
                        "passed": passed,
                        "requires_embed": q_needs_embed,
                        "note": q.get("note", ""),
                    }
                )

        metrics = {
            "recall@3_mean": round(sum(recall_at_3) / len(recall_at_3), 4) if recall_at_3 else 0,
            "recall@5_mean": round(sum(recall_at_5) / len(recall_at_5), 4) if recall_at_5 else 0,
            "precision@3_mean": round(sum(precision_at_3) / len(precision_at_3), 4) if precision_at_3 else 0,
            "ndcg@3_mean": round(sum(ndcg_at_3) / len(ndcg_at_3), 4) if ndcg_at_3 else 0,
            "mrr_mean": round(sum(mrr_scores) / len(mrr_scores), 4) if mrr_scores else 0,
            "hit_rate_mean": round(sum(hit_rates) / len(hit_rates), 4) if hit_rates else 0,
            "queries": len(query_logs),
            "skipped_embed_required": skipped_embed,
        }
        self.report.retrieval = {"metrics": metrics, "queries": query_logs}
        cat.metrics = metrics

        m = metrics["recall@3_mean"]
        query_pass_rate = cat.passed / (cat.passed + cat.failed) if (cat.passed + cat.failed) else 0
        metrics["query_pass_rate"] = round(query_pass_rate, 4)
        if query_pass_rate >= 0.95 and m >= 0.85:
            cat.score = 3
        elif query_pass_rate >= 0.8 and m >= 0.6:
            cat.score = 2
        elif query_pass_rate >= 0.5 and m >= 0.35:
            cat.score = 1
        else:
            cat.score = 0
        if not embed_ok:
            cat.notes.append("embed unreachable — SQL LIKE fallback; embed-required queries failed/skipped")
        elif skipped_embed:
            cat.notes.append(f"{skipped_embed} embed-required queries skipped")
        return cat

    # ── Category 9: Cross-session persistence ───────────────────────────────

    async def cat9_cross_session(self, golden: dict) -> CategoryScore:
        cat = CategoryScore(name="9_cross_session_persistence")
        cfg = golden.get("cross_session")
        if not cfg:
            cat.score = 3
            cat.notes.append("no cross_session block in golden")
            return cat

        coll = f"{self.collection}_xs"
        await self._call(
            "save_memory",
            {
                "key": cfg["key"],
                "value": cfg["value"],
                "collection": coll,
                "room": cfg.get("room", "logistics"),
                "hall": cfg.get("hall", "fact"),
            },
        )
        await self.close()
        await self.connect(use_auth=self._use_auth)

        r, lat = await self._call(
            "recall_memory", {"query": cfg["query"], "collection": coll}
        )
        text = json.dumps(_parse_recall(r), ensure_ascii=False)
        ok = all(s in text for s in cfg.get("expect_substrings", []))
        self._record_step(cat.name, "recall_after_reconnect", ok, lat, detail=text[:120])
        cat.passed = int(ok)
        cat.failed = int(not ok)
        cat.score = 3 if ok else 0
        return cat

    # ── Category 10: Owner isolation (JWT) ────────────────────────────────────

    async def cat10_owner_isolation(self, golden: dict) -> CategoryScore:
        cat = CategoryScore(name="10_owner_isolation")
        cfg = golden.get("owner_isolation")
        if not cfg:
            cat.score = 3
            return cat
        if not self._use_auth:
            cat.notes.append("skipped — pass --auth for owner isolation")
            cat.score = 0
            cat.failed = 1
            self._record_step(cat.name, "requires_auth", False, 0)
            return cat

        coll = f"{self.collection}_owner"
        await self._call(
            "save_memory",
            {
                "key": cfg["key"],
                "value": cfg["value"],
                "collection": coll,
                "room": cfg.get("room", "classified"),
                "hall": cfg.get("hall", "fact"),
            },
        )

        other = MCPTestClient(self.url)
        if self._owner_b_creds is None:
            self._owner_b_creds = (
                os.environ.get("LEVARA_TEST_EMAIL_B", "memeval_b@bench.local"),
                os.environ.get("LEVARA_TEST_PASSWORD_B", "MemEval_Bench_Local_B_2026"),
            )
        try:
            await other.login_or_register(*self._owner_b_creds)
            await other.connect()
            try:
                r_b, lat_b = await other.call_tool_timed(
                    "recall_memory", {"query": cfg["query"], "collection": coll}
                )
                text_b = json.dumps(_parse_recall(r_b), ensure_ascii=False)
                b_leak = any(s in text_b for s in cfg.get("forbidden_substrings", []))
                b_ok = not b_leak
                self._record_step(cat.name, "user_b_no_leak", b_ok, lat_b, detail=text_b[:100])
            finally:
                await other.close()
        except Exception as exc:
            cat.notes.append(f"user_b setup failed: {exc}")
            b_ok = False
            self._record_step(cat.name, "user_b_no_leak", False, 0, detail=str(exc))

        r_a, lat_a = await self._call(
            "recall_memory", {"query": cfg["query"], "collection": coll}
        )
        text_a = json.dumps(_parse_recall(r_a), ensure_ascii=False)
        a_ok = cfg["key"] in text_a or any(s in text_a for s in cfg.get("forbidden_substrings", []))
        self._record_step(cat.name, "user_a_can_recall", a_ok, lat_a)

        cat.passed = int(b_ok) + int(a_ok)
        cat.failed = 2 - cat.passed
        cat.score = min(3, cat.passed + (1 if b_ok and a_ok else 0))
        return cat

    # ── Category 11: Context efficiency ─────────────────────────────────────

    async def cat11_context_efficiency(self, golden: dict) -> CategoryScore:
        cat = CategoryScore(name="11_context_efficiency")
        cfg = golden.get("context_efficiency", {})
        coll = f"{self.collection}_ctx"
        pin_count = cfg.get("pin_count", 6)
        tmpl = cfg.get(
            "value_template",
            "Pinned fact #{n}: infra node {n} replication RF=3 backup 02:00 UTC.",
        )
        max_tokens = cfg.get("max_tokens", 120)

        for n in range(pin_count):
            await self._call(
                "save_memory",
                {
                    "key": f"pin_{n}",
                    "value": tmpl.format(n=n),
                    "collection": coll,
                    "room": "infra",
                    "hall": "fact",
                    "pin": True,
                    "pin_priority": 5 + n,
                },
            )

        r, lat = await self._call("wake_up", {"max_tokens": max_tokens, "collection": coll})
        wake_ok = not _is_error(r)
        bundle = r.get("structuredContent") or {}
        if not bundle:
            try:
                bundle = json.loads(_text(r))
            except json.JSONDecodeError:
                bundle = {}
        tokens_used = bundle.get("tokens_used", 9999)
        budget_ok = tokens_used <= max_tokens + 5
        pinned = bundle.get("pinned") or []
        trim_ok = isinstance(pinned, list)

        self._record_step(
            cat.name,
            "wake_up_respects_budget",
            wake_ok and budget_ok,
            lat,
            detail=f"tokens_used={tokens_used} max={max_tokens}",
        )
        self._record_step(cat.name, "wake_up_returns_pins", trim_ok and len(pinned) > 0, lat)

        cat.metrics = {"tokens_used": tokens_used, "max_tokens": max_tokens, "pinned_count": len(pinned)}
        cat.passed = int(wake_ok and budget_ok) + int(trim_ok and len(pinned) > 0)
        cat.failed = 2 - cat.passed
        cat.score = min(3, cat.passed + (1 if budget_ok else 0))
        return cat

    # ── Category 12: Scale smoke ─────────────────────────────────────────────

    async def cat12_scale_smoke(self) -> CategoryScore:
        cat = CategoryScore(name="12_scale_recall")
        n = self.scale_memories
        coll = f"{self.collection}_scale"
        prefix = f"scale_{uuid.uuid4().hex[:6]}"

        t0 = time.perf_counter()
        for i in range(n):
            await self._call(
                "save_memory",
                {
                    "key": f"{prefix}_{i}",
                    "value": f"Scale bench memory item {i} unique token {prefix}",
                    "collection": coll,
                    "hall": "fact",
                },
            )
        save_wall = (time.perf_counter() - t0) * 1000

        mid = n // 2
        r, lat = await self._call(
            "recall_memory",
            {"query": f"{prefix}_{mid}", "collection": coll},
        )
        rows = _parse_recall(r)
        hit = any(row.get("key") == f"{prefix}_{mid}" for row in rows)
        self._record_step(
            cat.name,
            f"recall_at_n={n}",
            hit,
            lat,
            detail=f"save_wall={save_wall:.0f}ms recall={lat:.1f}ms",
        )

        budget_ms = 500 if n <= 50 else 2000
        lat_ok = lat < budget_ms
        self._record_step(cat.name, "recall_latency_budget", lat_ok, lat, detail=f"target<{budget_ms}ms")

        cat.metrics = {"n_memories": n, "save_wall_ms": round(save_wall, 2), "recall_ms": round(lat, 2)}
        cat.passed = int(hit) + int(lat_ok)
        cat.failed = 2 - cat.passed
        cat.score = min(3, cat.passed + (1 if hit and lat_ok else 0))
        return cat

    # ── Category 3: Latency ─────────────────────────────────────────────────

    async def cat3_latency(self, iterations: int = 30) -> CategoryScore:
        cat = CategoryScore(name="3_latency_scalability")
        await self._set_context()
        prefix = f"lat_{uuid.uuid4().hex[:6]}"

        for i in range(iterations):
            key = f"{prefix}_{i}"
            await self._call(
                "save_memory",
                {"key": key, "value": f"latency bench value {i}", "collection": self.collection},
            )
            await self._call("recall_memory", {"query": key, "collection": self.collection})

        lat_stats = {}
        for tool in ("save_memory", "recall_memory"):
            lats = sorted(self._latencies.get(tool, []))
            if lats:
                lat_stats[tool] = {
                    "p50": round(percentile(lats, 50), 2),
                    "p95": round(percentile(lats, 95), 2),
                    "p99": round(percentile(lats, 99), 2),
                    "mean": round(sum(lats) / len(lats), 2),
                    "n": len(lats),
                }

        self.report.latency = lat_stats
        cat.metrics = lat_stats

        recall_p95 = lat_stats.get("recall_memory", {}).get("p95", 9999)
        save_p95 = lat_stats.get("save_memory", {}).get("p95", 9999)
        recall_budget = self.latency_budgets.get("recall_p95_ms", 200)
        save_budget = self.latency_budgets.get("save_p95_ms", 100)
        burst_budget = self.latency_budgets.get("concurrent_wall_ms", 3000)

        ok_recall = recall_p95 < recall_budget
        ok_save = save_p95 < save_budget
        self._record_step(
            cat.name, "recall_p95_budget", ok_recall, recall_p95,
            detail=f"p95={recall_p95}ms target<{recall_budget}",
        )
        self._record_step(
            cat.name, "save_p95_budget", ok_save, save_p95,
            detail=f"p95={save_p95}ms target<{save_budget}",
        )

        # Concurrent burst
        async def burst(i: int):
            k = f"{prefix}_burst_{i}"
            await self._call("recall_memory", {"query": prefix, "collection": self.collection})

        t0 = time.perf_counter()
        await asyncio.gather(*[burst(i) for i in range(10)])
        wall_ms = (time.perf_counter() - t0) * 1000
        ok_burst = wall_ms < burst_budget
        self._record_step(cat.name, "concurrent_10_recall", ok_burst, wall_ms, detail=f"wall={wall_ms:.0f}ms target<{burst_budget}")
        cat.metrics["concurrent_10_wall_ms"] = round(wall_ms, 2)

        score_pts = int(ok_recall) + int(ok_save) + int(ok_burst)
        cat.score = min(3, score_pts)
        cat.passed = score_pts
        cat.failed = 3 - score_pts
        return cat

    # ── Category 4: Consolidation ───────────────────────────────────────────

    async def cat4_consolidation(self, golden: dict) -> CategoryScore:
        cat = CategoryScore(name="4_consolidation_forgetting")
        await self._set_context()
        cfg = golden.get("consolidation", {})
        prefix = cfg.get("key_prefix", "weather_obs")

        for n in range(cfg.get("messages", 5)):
            await self._call(
                "save_memory",
                {
                    "key": f"{prefix}_{n}",
                    "value": cfg.get("value_template", "obs {n}").format(n=n),
                    "collection": self.collection,
                    "room": cfg.get("room", "weather"),
                    "hall": cfg.get("hall", "fact"),
                },
            )

        r, lat = await self._call(
            "list_memories",
            {"collection": self.collection, "room": cfg.get("room", "weather")},
        )
        before_text = _text(r)
        before_count = before_text.count(prefix)

        r, lat = await self._call(
            "consolidate",
            {"collection": self.collection, "dry_run": True},
        )
        dry_ok = not _is_error(r)
        self._record_step(cat.name, "consolidate_dry_run", dry_ok, lat, detail=_text(r)[:120])

        # Pin/unpin + wake_up
        await self._call(
            "save_memory",
            {
                "key": "pinned_fact",
                "value": "Critical: production DB is Postgres",
                "collection": self.collection,
                "room": "infra",
                "hall": "fact",
                "pin": True,
                "pin_priority": 9,
            },
        )
        r, lat = await self._call("wake_up", {"max_tokens": 500, "collection": self.collection})
        wake_ok = not _is_error(r) and "pinned_fact" in _text(r)
        self._record_step(cat.name, "wake_up_includes_pin", wake_ok, lat)

        cat.metrics = {"rows_before_consolidate": before_count}
        cat.passed = int(dry_ok) + int(wake_ok)
        cat.failed = 2 - cat.passed
        cat.score = min(3, cat.passed + (1 if before_count >= 3 else 0))
        if not dry_ok:
            cat.notes.append("consolidate dry_run failed or LLM required")
        return cat

    # ── Category 5: Integration surface ─────────────────────────────────────

    async def cat5_integration(self) -> CategoryScore:
        cat = CategoryScore(name="5_llm_agent_integration")
        tools = await self.client.tools_list()
        names = {t["name"] for t in tools}

        required = {
            "save_memory",
            "recall_memory",
            "wake_up",
            "pin_memory",
            "unpin_memory",
            "set_context",
            "consolidate",
            "diary_write",
            "diary_read",
        }
        missing = required - names
        tools_ok = len(missing) == 0
        self._record_step(cat.name, "mcp_tools_registered", tools_ok, detail=f"missing={missing}")
        cat.metrics["tool_count"] = len(names)
        cat.metrics["missing_tools"] = sorted(missing)

        await self._call(
            "diary_write",
            {
                "agent": "memory_eval",
                "key": f"run_{uuid.uuid4().hex[:8]}",
                "value": "Eval harness diary entry",
            },
        )
        r, lat = await self._call("diary_read", {"agent": "memory_eval", "query": "eval"})
        diary_ok = not _is_error(r) and "diary" in _text(r).lower() or "eval" in _text(r).lower()
        self._record_step(cat.name, "diary_namespace", diary_ok, lat)

        r, lat = await self._call("levara_instructions", {})
        instr_ok = not _is_error(r) and len(_text(r)) > 100
        self._record_step(cat.name, "agent_contract", instr_ok, lat)

        passed = int(tools_ok) + int(diary_ok) + int(instr_ok)
        cat.passed = passed
        cat.failed = 3 - passed
        cat.score = min(3, passed)
        return cat

    # ── Category 6: Isolation ─────────────────────────────────────────────────

    async def cat6_isolation(self, golden: dict) -> CategoryScore:
        cat = CategoryScore(name="6_multitenant_isolation")
        for pair in golden.get("isolation_pairs", []):
            ta = pair["tenant_a"]
            tb = pair["tenant_b"]
            coll_a = f"{self.collection}_{ta['collection']}"
            coll_b = f"{self.collection}_{tb['collection']}"

            await self._call("save_memory", {**ta, "collection": coll_a})
            await self._call("save_memory", {**tb, "collection": coll_b})

            qa = pair["query_a"]
            qb = pair["query_b"]
            ra, lat_a = await self._call(
                "recall_memory",
                {"query": qa["query"], "collection": coll_a},
            )
            rb, lat_b = await self._call(
                "recall_memory",
                {"query": qb["query"], "collection": coll_b},
            )
            text_a = json.dumps(_parse_recall(ra), ensure_ascii=False)
            text_b = json.dumps(_parse_recall(rb), ensure_ascii=False)

            ok_a = all(s in text_a for s in qa.get("expect_substrings", []))
            ok_a = ok_a and all(b not in text_a for b in qa.get("must_not_contain", []))
            ok_b = all(s in text_b for s in qb.get("expect_substrings", []))
            ok_b = ok_b and all(b not in text_b for b in qb.get("must_not_contain", []))

            self._record_step(cat.name, f"isolation_{pair['id']}_tenant_a", ok_a, lat_a)
            self._record_step(cat.name, f"isolation_{pair['id']}_tenant_b", ok_b, lat_b)
            cat.passed += int(ok_a) + int(ok_b)
            cat.failed += int(not ok_a) + int(not ok_b)

        total = cat.passed + cat.failed
        cat.score = min(3, round(3 * cat.passed / total)) if total else 0
        return cat

    # ── Category 7: Edge cases ──────────────────────────────────────────────

    async def cat7_edge_cases(self) -> CategoryScore:
        cat = CategoryScore(name="7_edge_cases_resilience")
        await self._set_context()

        # Invalid hall
        r, lat = await self._call(
            "save_memory",
            {"key": "bad_hall", "value": "x", "collection": self.collection, "hall": "rumor"},
        )
        invalid_ok = _is_error(r)
        self._record_step(cat.name, "reject_invalid_hall", invalid_ok, lat)

        # Empty query
        r, lat = await self._call("recall_memory", {"query": ""})
        empty_ok = _is_error(r)
        self._record_step(cat.name, "reject_empty_query", empty_ok, lat)

        # Unicode
        r, lat = await self._call(
            "save_memory",
            {
                "key": "unicode_test",
                "value": "Кириллица и emoji 🚀✅",
                "collection": self.collection,
                "hall": "fact",
            },
        )
        save_u_ok = not _is_error(r)
        r2, _ = await self._call(
            "recall_memory", {"query": "unicode_test", "collection": self.collection}
        )
        rows = _parse_recall(r2)
        recall_u_ok = any("🚀" in (row.get("value") or "") for row in rows)
        self._record_step(cat.name, "unicode_roundtrip", save_u_ok and recall_u_ok, lat)

        # Contradiction upsert
        await self._call(
            "save_memory",
            {"key": "age_fact", "value": "User is 30 years old", "collection": self.collection, "hall": "fact"},
        )
        await self._call(
            "save_memory",
            {"key": "age_fact", "value": "User is 31 years old", "collection": self.collection, "hall": "fact"},
        )
        r3, _ = await self._call(
            "recall_memory", {"query": "age_fact", "collection": self.collection}
        )
        rows = _parse_recall(r3)
        conflict_ok = len([x for x in rows if x.get("key") == "age_fact"]) <= 1
        conflict_ok = conflict_ok and any("31" in (x.get("value") or "") for x in rows)
        self._record_step(cat.name, "contradiction_upsert", conflict_ok, 0)

        checks = [invalid_ok, empty_ok, save_u_ok and recall_u_ok, conflict_ok]
        cat.passed = sum(int(x) for x in checks)
        cat.failed = len(checks) - cat.passed
        cat.score = min(3, cat.passed)
        return cat

    # ── Category 8: Observability ───────────────────────────────────────────

    async def cat8_observability(self) -> CategoryScore:
        cat = CategoryScore(name="8_observability")
        for tool in ("doctor", "runtime_stats", "heartbeat"):
            r, lat = await self._call(tool, {})
            ok = not _is_error(r) and len(_text(r)) > 10
            self._record_step(cat.name, tool, ok, lat)
            cat.passed += int(ok)
            cat.failed += int(not ok)

        cat.score = min(3, cat.passed)
        return cat

    async def run_all(self, iterations: int) -> EvalReport:
        golden = json.loads(GOLDEN_PATH.read_text(encoding="utf-8"))
        t0 = time.perf_counter()

        self.log.info("=== Levara Memory Eval | %s | collection=%s ===", self.label, self.collection)

        cat_fns = [
            ("2_retrieval_quality", lambda: self.cat2_retrieval(golden)),
            ("1_crud_memory_types", lambda: self.cat1_crud()),
            ("3_latency_scalability", lambda: self.cat3_latency(iterations)),
            ("4_consolidation_forgetting", lambda: self.cat4_consolidation(golden)),
            ("5_llm_agent_integration", lambda: self.cat5_integration()),
            ("6_multitenant_isolation", lambda: self.cat6_isolation(golden)),
            ("7_edge_cases_resilience", lambda: self.cat7_edge_cases()),
            ("8_observability", lambda: self.cat8_observability()),
            ("9_cross_session_persistence", lambda: self.cat9_cross_session(golden)),
            ("10_owner_isolation", lambda: self.cat10_owner_isolation(golden)),
            ("11_context_efficiency", lambda: self.cat11_context_efficiency(golden)),
            ("12_scale_recall", lambda: self.cat12_scale_smoke()),
        ]
        categories: list[CategoryScore] = []
        for _name, fn in cat_fns:
            t_cat = time.perf_counter()
            categories.append(await fn())
            self._cat_wall_s[categories[-1].name] = round(time.perf_counter() - t_cat, 2)

        self.report.categories = categories
        max_pts = len(categories) * 3
        self.report.overall_score = round(
            sum(c.score for c in categories) / max_pts * 100, 1
        )
        self._finalize_timing(t0)
        self.report.finished_at = datetime.now(timezone.utc).isoformat()
        return self.report


def print_summary(report: EvalReport, logger: logging.Logger):
    logger.info("")
    logger.info("=" * 72)
    logger.info("MEMORY EVAL SUMMARY — %s (%s)", report.label, report.url)
    logger.info("=" * 72)
    max_pts = len(report.categories) * 3
    logger.info("Duration: %.1fs | Overall: %.1f%% (%s/%s pts)",
                report.duration_s, report.overall_score,
                sum(c.score for c in report.categories), max_pts)
    if report.timing:
        t = report.timing
        logger.info(
            "Timing: memory_ops=%.1fs | consolidate(cat4)=%.1fs | consolidate_llm=%.0fms",
            t.get("memory_ops_s", 0),
            t.get("consolidation_cat_s", 0),
            t.get("consolidate_llm_ms", 0),
        )
    logger.info("Embed: %s | LLM: %s",
                report.services.get("embed", {}).get("status"),
                report.services.get("llm", {}).get("status"))
    if report.retrieval:
        m = report.retrieval.get("metrics", {})
        logger.info("Retrieval: R@3=%.2f P@3=%.2f NDCG=%.2f MRR=%.2f Hit=%.2f (%d queries)",
                    m.get("recall@3_mean", 0), m.get("precision@3_mean", 0),
                    m.get("ndcg@3_mean", 0), m.get("mrr_mean", 0),
                    m.get("hit_rate_mean", 0), m.get("queries", 0))
    if report.latency:
        for tool, s in report.latency.items():
            if isinstance(s, dict) and "p95" in s:
                logger.info("Latency %-15s p50=%.1f p95=%.1f p99=%.1f ms",
                            tool, s["p50"], s["p95"], s["p99"])
    logger.info("-" * 72)
    logger.info("%-35s %5s  %s", "Category", "Score", "Notes")
    for c in report.categories:
        notes = "; ".join(c.notes[:2]) if c.notes else ""
        logger.info("%-35s %2d/3   %s", c.name, c.score, notes)
    logger.info("=" * 72)


async def run_single(
    url: str,
    label: str | None,
    use_auth: bool,
    iterations: int,
    output_dir: str,
    verbose: bool,
    latency_budgets: dict | None = None,
    scale_memories: int = 50,
) -> EvalReport:
    label = label or urlparse(url).netloc.replace(":", "_")
    ts = datetime.now().strftime("%Y%m%d_%H%M%S")
    out_dir = Path(output_dir)
    out_dir.mkdir(parents=True, exist_ok=True)
    log_file = out_dir / f"memory_eval_{label}_{ts}.log"
    json_file = out_dir / f"memory_eval_{label}_{ts}.json"
    latest = out_dir / f"memory_eval_{label}_latest.json"

    logger = setup_logging(verbose, log_file)
    ev = MemoryEvaluator(url, label, logger, latency_budgets=latency_budgets, scale_memories=scale_memories)

    try:
        await ev.connect(use_auth=use_auth)
        report = await ev.run_all(iterations)
    except Exception as e:
        logger.exception("Eval aborted: %s", e)
        raise
    finally:
        await ev.close()

    payload = report.to_dict()
    json_file.write_text(json.dumps(payload, indent=2, ensure_ascii=False), encoding="utf-8")
    latest.write_text(json.dumps(payload, indent=2, ensure_ascii=False), encoding="utf-8")

    print_summary(report, logger)
    logger.info("Artifacts: %s", json_file)
    logger.info("Log file:  %s", log_file)
    return report


async def run_targets(targets: list[dict], args: argparse.Namespace) -> None:
    ts = datetime.now().strftime("%Y%m%d_%H%M%S")
    out_dir = Path(args.output_dir)
    out_dir.mkdir(parents=True, exist_ok=True)
    summary_log = out_dir / f"memory_eval_all_{ts}.log"
    logger = setup_logging(args.verbose, summary_log)

    summaries: list[dict] = []
    failed = 0
    for i, t in enumerate(targets, start=1):
        label = t.get("label") or urlparse(t["url"]).netloc.replace(":", "_")
        use_auth = bool(t.get("auth", False))
        latency = t.get("latency")
        logger.info("")
        logger.info(">>> Host %d/%d: %s (%s) auth=%s", i, len(targets), label, t["url"], use_auth)
        try:
            report = await run_single(
                t["url"], label, use_auth, args.iterations, args.output_dir, args.verbose,
                latency, args.scale_memories,
            )
            max_pts = len(report.categories) * 3
            summaries.append(
                {
                    "label": label,
                    "url": t["url"],
                    "overall_score": report.overall_score,
                    "points": sum(c.score for c in report.categories),
                    "max_points": max_pts,
                    "embed": report.services.get("embed", {}).get("status"),
                    "postgres": report.services.get("postgres", {}).get("status"),
                    "duration_s": report.duration_s,
                    "memory_ops_s": report.timing.get("memory_ops_s") if report.timing else None,
                    "consolidate_llm_ms": report.timing.get("consolidate_llm_ms") if report.timing else None,
                }
            )
        except Exception as e:
            failed += 1
            logger.error("Host %s failed: %s", label, e)
            summaries.append({"label": label, "url": t["url"], "error": str(e)})

    summary_path = out_dir / f"memory_eval_all_{ts}.json"
    summary_path.write_text(json.dumps({"hosts": summaries}, indent=2), encoding="utf-8")
    latest = out_dir / "memory_eval_all_latest.json"
    latest.write_text(json.dumps({"hosts": summaries}, indent=2), encoding="utf-8")

    logger.info("")
    logger.info("=" * 72)
    logger.info("ALL-HOSTS SUMMARY (%d targets, %d failed)", len(targets), failed)
    logger.info("=" * 72)
    for s in summaries:
        if "error" in s:
            logger.info("  %-12s ERROR: %s", s["label"], s["error"])
        else:
            logger.info(
                "  %-12s %.1f%% (%s/%s) mem=%.1fs llm=%sms embed=%s pg=%s total=%.1fs",
                s["label"],
                s["overall_score"],
                s["points"],
                s.get("max_points", 36),
                s.get("memory_ops_s") or 0,
                s.get("consolidate_llm_ms") or "?",
                s.get("embed"),
                s.get("postgres"),
                s.get("duration_s", 0),
            )
    logger.info("Combined report: %s", summary_path)
    if failed:
        sys.exit(1)


async def main():
    parser = argparse.ArgumentParser(description="Levara agent memory evaluation harness")
    parser.add_argument("--url", default=os.environ.get("LEVARA_URL", "http://localhost:8081"))
    parser.add_argument("--label", default=None, help="Run label for reports")
    parser.add_argument("--iterations", type=int, default=30, help="Latency benchmark iterations")
    parser.add_argument("--output-dir", default=str(RESULTS_DIR))
    parser.add_argument("--auth", action="store_true", help="Register/login and send JWT on MCP calls")
    parser.add_argument(
        "--targets",
        default=None,
        help="JSON file with [{url, label, auth, latency}] — run all hosts sequentially",
    )
    parser.add_argument(
        "--scale-memories",
        type=int,
        default=int(os.environ.get("MEMORY_EVAL_SCALE", "50")),
        help="Memories to write in cat12 scale smoke (default 50)",
    )
    parser.add_argument("-v", "--verbose", action="store_true")
    args = parser.parse_args()

    if args.targets:
        targets = json.loads(Path(args.targets).read_text(encoding="utf-8"))
        await run_targets(targets, args)
        return

    label = args.label or urlparse(args.url).netloc.replace(":", "_")
    try:
        await run_single(
            args.url, label, args.auth, args.iterations, args.output_dir, args.verbose,
            scale_memories=args.scale_memories,
        )
    except Exception:
        sys.exit(1)


if __name__ == "__main__":
    asyncio.run(main())
