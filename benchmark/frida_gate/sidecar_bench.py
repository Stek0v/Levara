#!/usr/bin/env python3
"""Run the FRIDA gate against the deployed ONNX sidecar (deploy/decisions/app.py
on :9200), not the in-process judge.

PART=parity — same trimmed sets as run_gate, quality must match results.json
PART=full   — untrimmed sets (hall 237, relevance 40) + grounding
PART=load   — concurrency 1/2/4/8 noul load: p50/p95, throughput, sidecar CPU%
"""
import json
import os
import statistics
import subprocess
import sys
import time
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

import httpx

HERE = Path(__file__).parent
sys.path.insert(0, str(HERE))
from embed_client import embed  # noqa: E402
from run_gate import HALL_CRITERIA, ROUTE_CRITERIA, auc, best_f1, load, prf  # noqa: E402

BASE = "http://127.0.0.1:9200"
INSTR_SUP = ("Определи, является ли запись B обновлением того же факта, что и запись A "
             "(та же суть, изменённые детали, дата или статус), либо записи про разные факты.")
INSTR_DUP = "Являются ли записи A и B дубликатами одного и того же факта (возможно, перефразированного)?"
INSTR_DUP_SEM = ("Являются ли записи A и B дубликатами одного и того же факта "
                 "(возможно, перефразированного или на другом языке)?")
INSTR_GROUND = ("Отвечает ли текст на заданный вопрос содержательно "
                "(содержит ли сведения, нужные для ответа)?")


def noul_req(state_fmt, it, instr):
    return {"state": state_fmt.format(a=it["a_text"], b=it["b_text"]),
            "questions": {"q": {"type": "noul", "instructions": instr}}}


SUP_FMT = "ЗАПИСЬ A:\n{a}\n\nЗАПИСЬ B:\n{b}"
GROUND_FMT = "{a}\n\nТЕКСТ:\n{b}"


def judge_batch(reqs, concurrency=1):
    """POST /judge with thread-pool concurrency; returns list of answers dicts."""
    out = [None] * len(reqs)

    def one(i):
        with httpx.Client(timeout=120) as c:
            r = c.post(f"{BASE}/judge", json=reqs[i])
            r.raise_for_status()
            return i, r.json()

    if concurrency == 1:
        for i in range(len(reqs)):
            _, body = one(i)
            out[i] = body
    else:
        with ThreadPoolExecutor(max_workers=concurrency) as ex:
            for i, body in ex.map(one, range(len(reqs))):
                out[i] = body
    return out


def probs_of(bodies):
    return [b["answers"]["q"]["noul"] for b in bodies]


def run_noul_set(name, items, instr, results, state_fmt=SUP_FMT, concurrency=1):
    reqs = [noul_req(state_fmt, it, instr) for it in items]
    t0 = time.perf_counter()
    bodies = judge_batch(reqs, concurrency)
    dt = time.perf_counter() - t0
    probs = probs_of(bodies)
    pos = [p for it, p in zip(items, probs) if it["label"] == 1]
    neg = [p for it, p in zip(items, probs) if it["label"] == 0]
    res = {"n": len(items), **prf(items, probs), "auc": round(auc(pos, neg), 3),
           "best_f1": best_f1(items, probs),
           "mean_p_pos": round(statistics.fmean(pos), 3),
           "mean_p_neg": round(statistics.fmean(neg), 3),
           "sec": round(dt, 1), "per_item_ms": round(dt * 1000 / len(items), 1)}
    results[name] = res
    print(name, json.dumps(res, ensure_ascii=False), flush=True)
    return probs


def hall_trim_items(rnd):
    by_hall = {}
    for it in load("hall"):
        by_hall.setdefault(it["label"], []).append(it)
    caps = {"discovery": 40, "event": 30, "decision": 30, "advice": 9, "fact": 2, "preference": 2}
    out = []
    for h, items in by_hall.items():
        rnd.shuffle(items)
        out.extend(items[: caps.get(h, len(items))])
    return out


def run_hall(items, results, concurrency=1, name="hall"):
    reqs = [{"state": it["text"],
             "questions": {"hall": {"type": "choice",
                                    "instructions": "К какому типу относится эта запись памяти?",
                                    "criteria": HALL_CRITERIA}}}
            for it in items]
    t0 = time.perf_counter()
    bodies = judge_batch(reqs, concurrency)
    dt = time.perf_counter() - t0
    preds = [b["answers"]["hall"]["choice"] for b in bodies]
    confs = [b["answers"]["hall"]["confidence"] for b in bodies]
    labels = [it["label"] for it in items]
    acc = statistics.fmean(p == g for p, g in zip(preds, labels))
    per = {}
    for c in sorted(set(labels)):
        tp = sum(1 for p, g in zip(preds, labels) if p == c and g == c)
        fp = sum(1 for p, g in zip(preds, labels) if p == c and g != c)
        fn = sum(1 for p, g in zip(preds, labels) if p != c and g == c)
        per[c] = {"n": tp + fn, "prec": round(tp / (tp + fp), 3) if tp + fp else None,
                  "rec": round(tp / (tp + fn), 3) if tp + fn else None}
    cov = {}
    for tau in (0.5, 0.7):
        sel = [(p, g) for p, g, c2 in zip(preds, labels, confs) if c2 >= tau]
        cov[str(tau)] = {"coverage": round(len(sel) / len(items), 3),
                         "acc": round(statistics.fmean(p == g for p, g in sel), 3) if sel else None}
    res = {"n": len(items), "acc": round(acc, 3), "per_class": per,
           "coverage_by_confidence": cov, "sec": round(dt, 1),
           "per_item_ms": round(dt * 1000 / len(items), 1)}
    results[name] = res
    print(name, json.dumps(res, ensure_ascii=False), flush=True)


def corpus_map():
    import csv
    out = {}
    for r in csv.DictReader(open(HERE / "data" / "memories.csv", encoding="utf-8")):
        out[r["id"]] = (r["value_excerpt"] or "").strip()
    return out


def run_relevance(items, corpus, results, concurrency=1, name="relevance"):
    reqs = [{"state": it["query"],
             "questions": {"rank": {"type": "ranking",
                                    "instructions": "Расположи записи памяти по релевантности запросу.",
                                    "criteria": {d: corpus[d] for d in it["doc_ids"]}}}}
            for it in items]
    t0 = time.perf_counter()
    bodies = judge_batch(reqs, concurrency)
    dt = time.perf_counter() - t0
    pos1, rrs = [], []
    for it, b in zip(items, bodies):
        order = b["answers"]["rank"]["ranking"]
        pos1.append(order[0] == it["pos_id"])
        rrs.append(1 / (order.index(it["pos_id"]) + 1))
    docs = sorted({d for it in items for d in it["doc_ids"]})
    table = {d: i for i, d in enumerate(docs)}
    qm = embed([it["query"] for it in items])
    dm = embed([corpus[d] for d in docs])
    sims = qm @ dm.T
    cpos1, crrs = [], []
    for k, it in enumerate(items):
        order = sorted(it["doc_ids"], key=lambda d: -sims[k, table[d]])
        cpos1.append(order[0] == it["pos_id"])
        crrs.append(1 / (order.index(it["pos_id"]) + 1))
    res = {"n": len(items),
           "frida": {"pos_at_1": round(statistics.fmean(pos1), 3),
                     "mrr": round(statistics.fmean(rrs), 3),
                     "sec": round(dt, 1), "ms_per_query": round(dt * 1000 / len(items), 1)},
           "cosine_baseline": {"pos_at_1": round(statistics.fmean(cpos1), 3),
                               "mrr": round(statistics.fmean(crrs), 3)}}
    results[name] = res
    print(name, json.dumps(res, ensure_ascii=False), flush=True)


def run_grounding(items, corpus, results, rnd, concurrency=1):
    import random
    allg = []
    for it in items:
        allg.append({"a_text": f"ВОПРОС: {it['query']}", "b_text": corpus[it["pos_id"]], "label": 1})
        neg = [d for d in it["doc_ids"] if d != it["pos_id"]]
        allg.append({"a_text": f"ВОПРОС: {it['query']}", "b_text": corpus[rnd.choice(neg)], "label": 0})
    run_noul_set("grounding", allg, INSTR_GROUND, results, state_fmt=GROUND_FMT,
                 concurrency=concurrency)


def sidecar_cpu(pid):
    out = subprocess.run(["ps", "-o", "%cpu=", "-p", str(pid)],
                         capture_output=True, text=True).stdout.strip()
    return float(out or 0)


def run_load(results):
    import random
    rnd = random.Random(42)
    sup = load("supersession")
    sample = [sup[i] for i in rnd.sample(range(len(sup)), 24)]
    reqs = [noul_req(SUP_FMT, it, INSTR_SUP) for it in sample]
    pid = int(subprocess.run(["pgrep", "-f", "uvicorn deploy.decisions"],
                             capture_output=True, text=True).stdout.split()[0])
    for conc in (1, 2, 4, 8):
        judge_batch(reqs[:conc * 2], conc)  # warm
        lat = []
        t0 = time.perf_counter()

        def timed(i):
            tc = time.perf_counter()
            judge_batch([reqs[i]], conc)
            return (time.perf_counter() - tc) * 1000

        with ThreadPoolExecutor(max_workers=conc) as ex:
            lat = list(ex.map(timed, range(24)))
            # CPU sampling while the pool works
            cpu = []
            import threading

            stop = threading.Event()

            def sampler():
                while not stop.is_set():
                    cpu.append(sidecar_cpu(pid))
                    time.sleep(0.5)

            th = threading.Thread(target=sampler)
            th.start()
            lat = list(ex.map(timed, range(24)))
            stop.set()
            th.join()
        wall = time.perf_counter() - t0
        lat.sort()
        res = {"conc": conc, "n": 24,
               "p50_ms": round(statistics.median(lat)),
               "p95_ms": round(lat[int(0.95 * len(lat)) - 1]),
               "throughput_rps": round(24 / wall, 2),
               "sidecar_cpu_pct_mean": round(statistics.fmean(cpu), 1) if cpu else None}
        results.setdefault("load", []).append(res)
        print("load", json.dumps(res), flush=True)


def main():
    part = os.environ.get("PART", "parity")
    results = {"part": part, "base": BASE,
               "started": time.strftime("%Y-%m-%d %H:%M:%S")}
    rnd = __import__("random").Random(42)

    if part in ("parity", "all"):
        r = {}
        run_noul_set("supersession", load("supersession"), INSTR_SUP, r)
        run_noul_set("dup_jaccard", load("dup"), INSTR_DUP, r)
        run_noul_set("dup_semantic", load("dup_semantic"), INSTR_DUP_SEM, r)
        run_hall(hall_trim_items(rnd), r)
        gate = json.load(open(HERE / "results.json"))
        parity = {}
        for k in ("supersession", "dup_jaccard", "dup_semantic", "hall"):
            for field in ("auc", "acc", "n"):
                if field in gate.get(k, {}) and field in r[k]:
                    parity[f"{k}.{field}"] = [gate[k][field], r[k][field]]
        results["parity"] = parity
        results["parity_sets"] = r
        print("PARITY", json.dumps(parity, ensure_ascii=False), flush=True)

    if part in ("full", "all"):
        corpus = corpus_map()
        r = results.setdefault("full_sets", {})
        rel_all = load("relevance")
        run_relevance(rel_all, corpus, r, name="relevance_full40")
        run_grounding(rel_all, corpus, r, rnd)
        run_hall(load("hall"), r, name="hall_full237")

    if part in ("load", "all"):
        run_load(results)

    suffix = "" if part == "all" else f"_{part}"
    with open(HERE / f"sidecar_results{suffix}.json", "w", encoding="utf-8") as f:
        json.dump(results, f, ensure_ascii=False, indent=1)
    print("saved", f"sidecar_results{suffix}.json", flush=True)


if __name__ == "__main__":
    main()
