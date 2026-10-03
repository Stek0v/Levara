#!/usr/bin/env python3
"""FRIDA-Decisions gate runner: every candidate integration stage of Levara,
one model, real prod data. Writes results.json and prints a summary.

Stages:
  supersession — noul gate before consolidation/supersession (variant 1)
  dup          — noul duplicate gate for consolidation pre-filter (variant 1)
  hall         — choice classification of hall at save/distill (variant 4)
  relevance    — ranking head vs cosine bi-encoder on query->doc (variants 2/3)
  grounding    — noul "does this text answer the question" for answerer feed (variant 6)
  routing      — choice head vs production heuristic router (variant 5)
  latency      — MPS and CPU timings for hot/cold path budgeting
"""
import json
import os
import statistics
import sys
import time
from collections import Counter, defaultdict
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))
from embed_client import embed  # noqa: E402

HERE = Path(__file__).parent
DATA = HERE / "data"
HALL_CRITERIA = {
    "fact": "объективное стабильное свойство системы или окружения (версия, адрес, порт, параметр)",
    "event": "что-то произошло в конкретный момент времени, дата или срок упомянуты в тексте",
    "decision": "принятое архитектурное или проектное решение вместе с обоснованием почему",
    "preference": "стилистическое или рабочее предпочтение пользователя",
    "advice": "переиспользуемое практическое правило или рекомендация на будущее",
    "discovery": "неочевидный инсайт, найденный баг, подводный камень или root cause",
}
ROUTE_CRITERIA = {
    "bm25": "нужен дословный поиск по точной строке, токену, имени файла или цитате",
    "vector": "нужен смысловой поиск по перефразированному описанию без точных слов",
    "hybrid": "обычный информационный запрос, подходят и смысл, и ключевые слова",
    "graph": "нужны связи между сущностями: кто кому назначен, что с чем связано",
    "filtered": "запрос явно ограничивает комнату, тег, тип записи или дату",
}
BATCH = 32


def load(name):
    with open(DATA / f"{name}.json", encoding="utf-8") as f:
        return json.load(f)


def auc(scores_pos, scores_neg):
    """Mann-Whitney AUC from margins (ties get average ranks)."""
    if not scores_pos or not scores_neg:
        return float("nan")
    allv = sorted([(s, 1) for s in scores_pos] + [(s, 0) for s in scores_neg],
                  key=lambda x: x[0])
    n1, n0 = len(scores_pos), len(scores_neg)
    rank_sum_pos, i, n = 0.0, 0, len(allv)
    while i < n:
        j = i
        while j < n and allv[j][0] == allv[i][0]:
            j += 1
        avg_rank = (i + j + 1) / 2.0  # 1-based
        for k in range(i, j):
            if allv[k][1] == 1:
                rank_sum_pos += avg_rank
        i = j
    return (rank_sum_pos - n1 * (n1 + 1) / 2) / (n1 * n0)


def prf(items, probs, thr=0.5):
    tp = sum(1 for it, p in zip(items, probs) if it["label"] == 1 and p > thr)
    fp = sum(1 for it, p in zip(items, probs) if it["label"] == 0 and p > thr)
    fn = sum(1 for it, p in zip(items, probs) if it["label"] == 1 and p <= thr)
    tn = len(items) - tp - fp - fn
    prec = tp / (tp + fp) if tp + fp else float("nan")
    rec = tp / (tp + fn) if tp + fn else float("nan")
    f1 = 2 * prec * rec / (prec + rec) if prec + rec > 0 else float("nan")
    acc = (tp + tn) / len(items)
    return {"acc": round(acc, 3), "precision": round(prec, 3), "recall": round(rec, 3),
            "f1": round(f1, 3), "tp": tp, "fp": fp, "fn": fn, "tn": tn}


def best_f1(items, probs):
    best = (0, None)
    for thr in [i / 40 for i in range(1, 40)]:
        r = prf(items, probs, thr)
        if r["f1"] == r["f1"] and r["f1"] > best[0]:
            best = (r["f1"], thr)
    return {"f1": round(best[0], 3), "thr": best[1]}


def run_noul(judge, items, instructions, name, results, state_fmt=None):
    state_fmt = state_fmt or ("ЗАПИСЬ A:\n{a}\n\nЗАПИСЬ B:\n{b}")
    reqs = [{"state": state_fmt.format(a=it["a_text"], b=it["b_text"]),
             "questions": {"q": {"type": "noul", "instructions": instructions}}}
            for it in items]
    probs = []
    t0 = time.perf_counter()
    for i in range(0, len(reqs), BATCH):
        tb = time.perf_counter()
        out = judge.judge_batch(reqs[i : i + BATCH])
        probs.extend(o["answers"]["q"]["noul"] for o in out)
        print(f"  {name} batch {i//BATCH}: {time.perf_counter()-tb:.1f}s "
              f"(elapsed {time.perf_counter()-t0:.0f}s)", flush=True)
    dt = time.perf_counter() - t0
    pos = [p for it, p in zip(items, probs) if it["label"] == 1]
    neg = [p for it, p in zip(items, probs) if it["label"] == 0]
    res = {
        "n": len(items), **prf(items, probs), "auc": round(auc(pos, neg), 3),
        "best_f1": best_f1(items, probs),
        "mean_p_pos": round(statistics.fmean(pos), 3), "mean_p_neg": round(statistics.fmean(neg), 3),
        "sec": round(dt, 1), "per_item_ms": round(dt * 1000 / len(items), 1),
    }
    results[name] = res
    print(name, json.dumps(res, ensure_ascii=False))
    return probs


def run_hall(judge, items, results):
    reqs = [{"state": it["text"],
             "questions": {"hall": {"type": "choice",
                                    "instructions": "К какому типу относится эта запись памяти?",
                                    "criteria": HALL_CRITERIA}}}
            for it in items]
    preds, confs = [], []
    t0 = time.perf_counter()
    for i in range(0, len(reqs), BATCH):
        out = judge.judge_batch(reqs[i : i + BATCH])
        for o in out:
            preds.append(o["answers"]["hall"]["choice"])
            confs.append(o["answers"]["hall"]["confidence"])
    dt = time.perf_counter() - t0
    labels = [it["label"] for it in items]
    acc = sum(p == g for p, g in zip(preds, labels)) / len(items)
    classes = sorted(set(labels))
    per = {}
    for c in classes:
        tp = sum(1 for p, g in zip(preds, labels) if p == c and g == c)
        fp = sum(1 for p, g in zip(preds, labels) if p == c and g != c)
        fn = sum(1 for p, g in zip(preds, labels) if p != c and g == c)
        per[c] = {"n": tp + fn, "prec": round(tp / (tp + fp), 3) if tp + fp else None,
                  "rec": round(tp / (tp + fn), 3) if tp + fn else None}
    macro_f1 = None
    f1s = []
    for c, v in per.items():
        if v["prec"] and v["rec"] and v["prec"] + v["rec"] > 0:
            f1s.append(2 * v["prec"] * v["rec"] / (v["prec"] + v["rec"]))
    macro_f1 = round(statistics.fmean(f1s), 3) if f1s else None
    maj = Counter(labels).most_common(1)[0]
    cov = {}
    for tau in (0.5, 0.7):
        sel = [(p, g) for p, g, c2 in zip(preds, labels, confs) if c2 >= tau]
        cov[str(tau)] = {"coverage": round(len(sel) / len(items), 3),
                         "acc": round(statistics.fmean(p == g for p, g in sel), 3) if sel else None}
    res = {"n": len(items), "acc": round(acc, 3), "majority_acc": round(maj[1] / len(items), 3),
           "majority_class": maj[0], "macro_f1": macro_f1, "per_class": per,
           "coverage_by_confidence": cov, "sec": round(dt, 1),
           "per_item_ms": round(dt * 1000 / len(items), 1)}
    results["hall"] = res
    print("hall", json.dumps(res, ensure_ascii=False))
    return preds


def run_relevance(judge, items, by_id, results):
    # FRIDA ranking head
    reqs = []
    for it in items:
        reqs.append({"state": it["query"],
                     "questions": {"rank": {"type": "ranking",
                                            "instructions": "Расположи записи памяти по релевантности запросу.",
                                            "criteria": {d: by_id[d] for d in it["doc_ids"]}}}})
    pos1, rrs, frida_scores = [], [], []
    t0 = time.perf_counter()
    for i in range(0, len(reqs), 16):
        out = judge.judge_batch(reqs[i : i + 16])
        for it, o in zip(items[i : i + 16], out):
            ans = o["answers"]["rank"]
            order = ans["ranking"]
            pos1.append(order[0] == it["pos_id"])
            rrs.append(1 / (order.index(it["pos_id"]) + 1))
            frida_scores.append(ans["scores"][it["pos_id"]] -
                                statistics.fmean(ans["scores"][d] for d in it["doc_ids"] if d != it["pos_id"]))
    dt_frida = time.perf_counter() - t0
    # cosine bi-encoder baseline (the production recall ranking signal)
    docs = sorted({d for it in items for d in it["doc_ids"]})
    table = {d: i for i, d in enumerate(docs)}
    qm = embed([it["query"] for it in items])
    dm = embed([by_id[d] for d in docs])
    cpos1, crrs = [], []
    t1 = time.perf_counter()
    sims = qm @ dm.T
    dt_cos = time.perf_counter() - t1
    for k, it in enumerate(items):
        order = sorted(it["doc_ids"], key=lambda d: -sims[k, table[d]])
        cpos1.append(order[0] == it["pos_id"])
        crrs.append(1 / (order.index(it["pos_id"]) + 1))
    res = {"n": len(items),
           "frida": {"pos_at_1": round(statistics.fmean(pos1), 3), "mrr": round(statistics.fmean(rrs), 3),
                     "sec": round(dt_frida, 1), "ms_per_query": round(dt_frida * 1000 / len(items), 1)},
           "cosine_baseline": {"pos_at_1": round(statistics.fmean(cpos1), 3),
                               "mrr": round(statistics.fmean(crrs), 3),
                               "embed_ms": round(dt_cos * 1000, 1)}}
    results["relevance"] = res
    print("relevance", json.dumps(res, ensure_ascii=False))


def run_grounding(judge, items, by_id, results, rnd):
    import random
    pos_items, neg_items = [], []
    for it in items:
        pos_items.append({"a_text": f"ВОПРОС: {it['query']}", "b_text": by_id[it["pos_id"]], "label": 1})
        neg = [d for d in it["doc_ids"] if d != it["pos_id"]]
        d = rnd.choice(neg)
        neg_items.append({"a_text": f"ВОПРОС: {it['query']}", "b_text": by_id[d], "label": 0})
    allg = pos_items + neg_items
    probs = run_noul(judge, allg, "Отвечает ли текст на заданный вопрос содержательно "
                                 "(содержит ли сведения, нужные для ответа)?",
                     "grounding", results, state_fmt="{a}\n\nТЕКСТ:\n{b}")


def run_routing(judge, items, results):
    reqs = [{"state": it["query"],
             "questions": {"route": {"type": "choice",
                                     "instructions": "Какой поисковый маршрут выбрать для запроса?",
                                     "criteria": ROUTE_CRITERIA}}}
            for it in items]
    out = judge.judge_batch(reqs)
    preds = [o["answers"]["route"]["choice"] for o in out]
    confs = [o["answers"]["route"]["confidence"] for o in out]
    labels = [it["label"] for it in items]
    acc = statistics.fmean(p == g for p, g in zip(preds, labels))
    per = defaultdict(Counter)
    for p, g in zip(preds, labels):
        per[g][p] += 1
    res = {"n": len(items), "acc": round(acc, 3), "confusion_gold_to_pred": {k: dict(v) for k, v in per.items()},
           "batch24_ms": out[0]["usage"]["milliseconds"]}
    results["routing_frida"] = res
    print("routing_frida", json.dumps(res, ensure_ascii=False))
    return preds, confs


def run_latency(results, backend):
    if backend == "onnx":
        from frida_decisions import OnnxJudge
        judge = OnnxJudge.from_pretrained("ai-forever/FRIDA-Decisions", threads=8)
    else:
        from frida_decisions import Judge
        judge = Judge.from_pretrained("ai-forever/FRIDA-Decisions", device="mps")
    state = "2026-09-24 выполнена миграция эмбеддингов на Gemma 768 dim, 320 коллекций, " \
            "нативный /embedding-migrations API, память восстановлена; инцидент с 0-hits " \
            "был вызван рассинхроном контракта и повреждённым plist-флагом -embed-model."
    req1 = {"state": state, "questions": {"q": {"type": "noul", "instructions": "A заменяет B?"}}}
    judge(req1)  # warmup
    ts = []
    for _ in range(20):
        t = time.perf_counter()
        judge(req1)
        ts.append((time.perf_counter() - t) * 1000)
    single = round(statistics.median(ts), 1)
    batch20 = [{"state": state + str(k), "questions": {"q": {"type": "noul",
               "instructions": "A заменяет B?"}}} for k in range(20)]
    t = time.perf_counter()
    judge.judge_batch(batch20)
    b20 = round((time.perf_counter() - t) * 1000, 1)
    hall_req = {"state": state, "questions": {"hall": {"type": "choice",
               "instructions": "Тип записи?", "criteria": HALL_CRITERIA}}}
    judge(hall_req)
    ts = []
    for _ in range(10):
        t = time.perf_counter()
        judge(hall_req)
        ts.append((time.perf_counter() - t) * 1000)
    hall_ms = round(statistics.median(ts), 1)
    results["latency_" + backend] = {"noul_single_ms": single, "noul_batch20_ms": b20,
                                     "choice6_single_ms": hall_ms}
    print("latency", backend, results["latency_" + backend], flush=True)
    # deployment probe: MPS packed vs cached (torch), 3 reps each, to size the
    # Mac-GPU option against the observed ~8s/request packed pathology
    if os.environ.get("MPS_CHECK"):
        from frida_decisions import Judge
        mj = Judge.from_pretrained("ai-forever/FRIDA-Decisions", device="mps")
        mj(req1)
        packed = []
        for k in range(3):
            r = {"state": state + "-x" + str(k), "questions": req1["questions"]}
            t = time.perf_counter()
            mj(r)
            packed.append(round((time.perf_counter() - t) * 1000))
        cached = []
        for _ in range(3):
            t = time.perf_counter()
            mj(req1)  # same state -> state-cache path
            cached.append(round((time.perf_counter() - t) * 1000))
        results["latency_mps_probe"] = {"packed_ms": packed, "cached_ms": cached}
        print("latency mps probe", results["latency_mps_probe"], flush=True)


def main():
    backend = os.environ.get("FRIDA_BACKEND", "onnx")  # onnx int8 CPU keeps prod MPS free
    print("backend:", backend, flush=True)
    if backend == "onnx":
        from frida_decisions import OnnxJudge
        judge = OnnxJudge.from_pretrained("ai-forever/FRIDA-Decisions", threads=8)
    else:
        from frida_decisions import Judge
        judge = Judge.from_pretrained("ai-forever/FRIDA-Decisions", device="mps")
    results = {"backend": backend, "model": "ai-forever/FRIDA-Decisions",
               "started": time.strftime("%Y-%m-%d %H:%M:%S"),
               "trim": {"dup_semantic": 60, "hall": 120, "relevance": 24}}
    rnd = __import__("random").Random(42)

    with open(DATA / "relevance.json", encoding="utf-8") as f:
        rel = json.load(f)[:24]
        corpus = {}
        import csv
        for r in csv.DictReader(open(DATA / "memories.csv", encoding="utf-8")):
            corpus[r["id"]] = (r["value_excerpt"] or "").strip()

    sup = load("supersession")
    print("supersession n=", len(sup), flush=True)
    run_noul(judge, sup, "Определи, является ли запись B обновлением того же факта, что и запись A "
                         "(та же суть, изменённые детали, дата или статус), либо записи про разные факты.",
             "supersession", results)

    print("dup_jaccard n=", len(load("dup")), flush=True)
    run_noul(judge, load("dup"), "Являются ли записи A и B дубликатами одного и того же факта "
                                 "(возможно, перефразированного)?",
             "dup_jaccard", results)

    with open(DATA / "dup_semantic.json", encoding="utf-8") as f:
        dsem = json.load(f)[:60]
    print("dup_semantic n=", len(dsem), flush=True)
    run_noul(judge, dsem, "Являются ли записи A и B дубликатами одного и того же факта "
                          "(возможно, перефразированного или на другом языке)?",
             "dup_semantic", results)

    stage = os.environ.get("STAGE", "all")
    if stage in ("1", "2", "all"):
        part = "results_partial1.json" if stage == "1" else None
        if stage in ("2", "all"):
            # merge: stage 2 loads partial1 and adds hall
            pass
        if part:
            with open(HERE / part, "w", encoding="utf-8") as f:
                json.dump(results, f, ensure_ascii=False, indent=1)
            print("saved", part, flush=True)
    if stage == "1":
        return
    if stage == "2":
        with open(HERE / "results_partial1.json", encoding="utf-8") as f:
            results = json.load(f)

    # stratified hall subsample: keep rare classes whole, trim event/discovery
    hall_items = load("hall")
    by_hall = defaultdict(list)
    for it in hall_items:
        by_hall[it["label"]].append(it)
    caps = {"discovery": 40, "event": 30, "decision": 30, "advice": 9, "fact": 2, "preference": 2}
    hall_trim = []
    for h, items in by_hall.items():
        rnd.shuffle(items)
        hall_trim.extend(items[: caps.get(h, len(items))])
    print("hall n=", len(hall_trim), flush=True)
    run_hall(judge, hall_trim, results)
    results["trim"]["hall_actual"] = len(hall_trim)

    if stage == "2":
        with open(HERE / "results.json", "w", encoding="utf-8") as f:
            json.dump(results, f, ensure_ascii=False, indent=1)
        print("saved results.json (stage 2)", flush=True)
        return

    run_relevance(judge, rel, corpus, results)
    run_grounding(judge, rel, corpus, results, rnd)

    routing = load("routing")
    frida_preds, _ = run_routing(judge, routing, results)

    # production heuristic router over the same queries
    import subprocess
    with open(DATA / "routing.json", "w", encoding="utf-8") as f:
        json.dump(routing, f, ensure_ascii=False)
    p = subprocess.run(["go", "run", "./router_probe"], cwd=HERE, input=json.dumps(routing),
                       capture_output=True, text=True, timeout=300)
    if p.returncode == 0:
        go_rows = json.loads(p.stdout)
        gacc = statistics.fmean(r["route"] == r["label"] for r in go_rows)
        results["routing_go_heuristic"] = {"n": len(go_rows), "acc": round(gacc, 3),
                                           "rows": go_rows}
        print("routing_go", round(gacc, 3))
    else:
        results["routing_go_heuristic"] = {"error": p.stderr[-500:]}
        print("routing_go FAILED:", p.stderr[-300:])

    run_latency(results, backend)
    results["frida_routing_preds"] = frida_preds
    with open(HERE / "results.json", "w", encoding="utf-8") as f:
        json.dump(results, f, ensure_ascii=False, indent=1)
    print("saved results.json")


if __name__ == "__main__":
    main()
