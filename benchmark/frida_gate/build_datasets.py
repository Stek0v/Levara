#!/usr/bin/env python3
"""Build FRIDA-Decisions gate test sets from the prod memory dump (data/memories.csv).

Provenance-first: every positive comes from a structural fact in the DB
(superseded_by edge, hall label, source-of-query), every negative is random with
a fixed seed. Near-dup positives use a Jaccard proxy — documented caveat.
"""
import csv
import json
import random
import re
from collections import Counter, defaultdict
from pathlib import Path

HERE = Path(__file__).parent
SEED = 42
MAX_CHARS = 800

HALLS = {
    "fact": "объективное стабильное свойство системы или окружения (версии, адреса, параметры)",
    "event": "что-то произошло в конкретный момент времени, дата упомянута в тексте",
    "decision": "принятое архитектурное или проектное решение вместе с обоснованием почему",
    "preference": "стилистическое или рабочее предпочтение пользователя",
    "advice": "переиспользуемое практическое правило или рекомендация на будущее",
    "discovery": "неочевидный инсайд, найденный баг, подводный камень или root cause",
}


def load_rows():
    with open(HERE / "data" / "memories.csv", newline="", encoding="utf-8") as f:
        rows = list(csv.DictReader(f))
    for r in rows:
        r["value"] = (r["value_excerpt"] or "").strip()
        r["is_pinned"] = r["is_pinned"] == "t"
    return [r for r in rows if r["value"]]


def load_supersession_pairs():
    with open(HERE / "data" / "supersession_pairs.csv", newline="", encoding="utf-8") as f:
        rows = list(csv.DictReader(f))
    out = []
    for r in rows:
        old_v, new_v = (r["old_value"] or "").strip(), (r["new_value"] or "").strip()
        if len(old_v) > 30 and len(new_v) > 30:
            out.append({
                "a_text": old_v, "b_text": new_v, "label": 1,
                "kind": "superseded-edge", "a_id": r["id"], "b_id": r["new_id"],
                "collection": r["collection_name"],
            })
    return out


def load_corpus():
    with open(HERE / "data" / "corpus_big.csv", newline="", encoding="utf-8") as f:
        rows = list(csv.DictReader(f))
    for r in rows:
        r["value"] = (r["value_excerpt"] or "").strip()
    return [r for r in rows if len(r["value"]) > 60]


def tokens(text):
    return set(w for w in re.findall(r"[a-zа-яё0-9]{3,}", text.lower()))


def jaccard(a, b):
    ta, tb = tokens(a), tokens(b)
    if not ta or not tb:
        return 0.0
    return len(ta & tb) / len(ta | tb)


def first_sentence(value, cap=160):
    # strip leading ISO date used as a narrative prefix in this corpus
    v = re.sub(r"^20\d\d-\d\d-\d\d\s+", "", value).strip()
    for sep in (". ", "! ", "? ", "\n"):
        i = v.find(sep)
        if 0 < i < cap:
            return v[: i + 1].strip()
    return v[:cap].strip()


def build_supersession(pos_pairs, corpus, rnd):
    pairs = list(pos_pairs)
    n = len(pairs)
    negs, tries = [], 0
    while len(negs) < n and tries < 5000:
        tries += 1
        a, b = rnd.sample(corpus, 2)
        if a["id"] == b["id"] or a["value"] == b["value"]:
            continue
        kind = "same-coll" if a["collection_name"] == b["collection_name"] else "cross-coll"
        negs.append({
            "a_text": a["value"], "b_text": b["value"], "label": 0,
            "kind": kind, "a_id": a["id"], "b_id": b["id"],
            "collection": a["collection_name"] or "empty",
        })
    return pairs + negs


def build_dup(corpus, rnd):
    pos, neg = [], []
    tries = 0
    while (len(pos) < 50 or len(neg) < 50) and tries < 60000:
        tries += 1
        a, b = rnd.sample(corpus, 2)
        if a["id"] == b["id"]:
            continue
        j = jaccard(a["value"], b["value"])
        item = {"a_text": a["value"], "b_text": b["value"],
                "a_id": a["id"], "b_id": b["id"],
                "collection": a["collection_name"] or "empty", "jaccard": round(j, 3)}
        if j >= 0.32 and len(pos) < 50:
            item["label"] = 1
            pos.append(item)
        elif j <= 0.10 and len(neg) < 50:
            item["label"] = 0
            neg.append(item)
    return pos + neg


def build_hall(rows):
    return [{"text": r["value"], "label": r["hall"], "id": r["id"], "room": r["room"]}
            for r in rows if r["hall"] in HALLS]


def build_relevance(rows, rnd, n_queries=40):
    cand = [r for r in rows if len(r["value"]) > 120 and r["hall"]]
    rnd.shuffle(cand)
    items = []
    for src in cand:
        if len(items) >= n_queries:
            break
        q = first_sentence(src["value"])
        if len(q) < 40:
            continue
        negs = [r for r in rnd.sample(rows, 12) if r["id"] != src["id"]][:9]
        items.append({
            "query": q, "pos_id": src["id"],
            "doc_ids": [src["id"]] + [g["id"] for g in negs],
        })
    return items


def build_routing():
    # manual labels: expected primary search route in Levara's unified search
    return [
        {"query": "найди точную строку plist-surgery в документах", "label": "bm25"},
        {"query": "цитата дословно: close of closed channel", "label": "bm25"},
        {"query": "покажи все записи со словом WAL replay", "label": "bm25"},
        {"query": "как называется тот механизм, который спасал пул соединений", "label": "vector"},
        {"query": "что-то было про зависание эмбеддингов при индексации", "label": "vector"},
        {"query": "примерно такая проблема: база растёт, диск кончается", "label": "vector"},
        {"query": "что мы решили про выбор модели эмбеддингов", "label": "hybrid"},
        {"query": "как устроена синхронизация Mac и Pi", "label": "hybrid"},
        {"query": "расскажи про холодный старт сервера", "label": "hybrid"},
        {"query": "какие есть решения насчёт legacy-800", "label": "hybrid"},
        {"query": "кто кому назначен в ownership сущности gateway", "label": "graph"},
        {"query": "какие сущности связаны с HNSW индексом", "label": "graph"},
        {"query": "покажи связи между repos в мультирепо", "label": "graph"},
        {"query": "что упоминалось вместе с watchdog", "label": "graph"},
        {"query": "в комнате deploy покажи все факты про порты", "label": "filtered"},
        {"query": "найди решения в комнате memory про консолидацию", "label": "filtered"},
        {"query": "покажи события по комнате observability за сентябрь", "label": "filtered"},
        {"query": "есть ли предпочтения пользователя в комнате mcp", "label": "filtered"},
        {"query": "EXACT_TOKEN_2026 ZARGS", "label": "bm25"},
        {"query": "что-то похожее на гоча с быстрым файствором", "label": "vector"},
        {"query": "итоги аудита качества и что подтянули", "label": "hybrid"},
        {"query": "какой сейчас владелец сервиса синхронизации", "label": "graph"},
        {"query": "список advice про миграции базы", "label": "filtered"},
        {"query": "найди упоминание строки embed-model в plist", "label": "bm25"},
    ]


def main():
    rnd = random.Random(SEED)
    rows = load_rows()
    by_id = {r["id"]: r for r in rows}
    corpus = load_corpus()
    out = HERE / "data"
    ds = {
        "supersession": build_supersession(load_supersession_pairs(), corpus, rnd),
        "dup": build_dup(corpus, rnd),
        "hall": build_hall(rows),
        "relevance": build_relevance(rows, rnd),
        "routing": build_routing(),
    }
    for name, items in ds.items():
        with open(out / f"{name}.json", "w", encoding="utf-8") as f:
            json.dump(items, f, ensure_ascii=False, indent=1)
        print(name, len(items),
              Counter(i["label"] for i in items if "label" in i).most_common())


if __name__ == "__main__":
    main()
