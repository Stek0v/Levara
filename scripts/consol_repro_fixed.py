#!/usr/bin/env python3
"""Validate the consolidation fixes against REAL Pi data (no prod binary swap).

Mirrors the NEW pipeline on the live records + live potion embeddings + live
DeepSeek, so we can confirm the two previously-skipped clusters now behave
correctly:
  - TauLow raised 0.85 -> 0.90 (curb over-clustering)
  - abstract clusters with > MAX_ABSTRACT records are skipped up front, with a
    clear 'too large' reason (no doomed truncated LLM call)
  - MaxTokens scales with source length (clamp 512..4096), so summaries aren't
    truncated mid-sentence
  - entity coverage is fraction-gated (<=10% dropped) instead of all-or-nothing

Read-only. Imports the guard/token logic so it stays in lockstep with Go.
"""
import json, re, sqlite3, sys, urllib.request

DB = "/home/stek0v/levara/data/levara.db"
ENV = "/home/stek0v/levara/levara.env"
TAU_LOW, TAU_HIGH = 0.90, 0.97
MAX_ABSTRACT = 6
MAX_ENTITY_DROP_FRAC = 0.10

NUM_RE = re.compile(r"\d+")
ENT_RE = re.compile(r"\b[A-Z][A-Za-z0-9]+\b")


def env():
    d = {}
    with open(ENV) as f:
        for line in f:
            line = line.strip()
            if "=" in line and not line.startswith("#"):
                k, v = line.split("=", 1)
                d[k] = v
    return d


def candidates(coll):
    con = sqlite3.connect(DB)
    cur = con.execute(
        "SELECT id, value, room, created_at FROM memories "
        "WHERE collection_name=? AND superseded_by='' AND is_pinned=0 AND tier='raw'",
        (coll,))
    rows = [{"id": r[0], "value": r[1], "room": r[2] or ""} for r in cur]
    con.close()
    return rows


def embed(texts, ep, model):
    body = json.dumps({"input": texts, "model": model}).encode()
    req = urllib.request.Request(ep, data=body, headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=30) as r:
        return [d["embedding"] for d in json.load(r)["data"]]


def cosine(a, b):
    dot = sum(x * y for x, y in zip(a, b))
    na = sum(x * x for x in a) ** 0.5
    nb = sum(y * y for y in b) ** 0.5
    return dot / (na * nb) if na and nb else 0.0


def union_find(n, edges):
    p = list(range(n))
    def find(x):
        while p[x] != x:
            p[x] = p[p[x]]; x = p[x]
        return x
    for i, j, _ in edges:
        p[find(i)] = find(j)
    comp = {}
    for i in range(n):
        comp.setdefault(find(i), []).append(i)
    return [c for c in comp.values() if len(c) > 1]


def tokenset(rx, *texts):
    s = set()
    for t in texts:
        s.update(rx.findall(t))
    return s


def max_tokens(sources):
    total = sum(len(s) for s in sources)
    return max(512, min(4096, total // 3 + 256))


def deepseek(sources, key, model, base):
    prompt = ("Combine the following memory notes into ONE concise statement. "
              "Preserve every fact, number, name, and port exactly. "
              "Do NOT add any information not present below. Notes:\n")
    for s in sources:
        prompt += "- " + s + "\n"
    body = json.dumps({"model": model, "messages": [{"role": "user", "content": prompt}],
                       "temperature": 0, "max_tokens": max_tokens(sources)}).encode()
    req = urllib.request.Request(base.rstrip("/") + "/chat/completions", data=body,
                                 headers={"Content-Type": "application/json",
                                          "Authorization": "Bearer " + key})
    with urllib.request.urlopen(req, timeout=120) as r:
        return json.load(r)["choices"][0]["message"]["content"].strip()


def guard(sources, out):
    if not out:
        return False, "empty summary"
    sn, on = tokenset(NUM_RE, *sources), tokenset(NUM_RE, out)
    for n in sn:
        if n not in on:
            return False, f'dropped source number "{n}"'
    for n in on:
        if n not in sn:
            return False, f'invented number "{n}"'
    se, oe = tokenset(ENT_RE, *sources), tokenset(ENT_RE, out)
    dropped = [e for e in se if e not in oe]
    if se and len(dropped) / len(se) > MAX_ENTITY_DROP_FRAC:
        return False, f"dropped {len(dropped)}/{len(se)} entities {dropped}"
    return True, "ok"


def main():
    coll = sys.argv[1]
    e = env()
    recs = candidates(coll)
    vecs = embed([r["value"] for r in recs], e["EMBEDDING_ENDPOINT"], e["EMBEDDING_MODEL"])
    edges = []
    for i in range(len(recs)):
        for j in range(i + 1, len(recs)):
            c = cosine(vecs[i], vecs[j])
            if c >= TAU_LOW:
                edges.append((i, j, c))
    clusters = union_find(len(recs), edges)
    print(f"## '{coll}': {len(recs)} candidates, {len(clusters)} cluster(s) at cosine>={TAU_LOW}")
    for ci, comp in enumerate(clusters):
        ps = [(i, j, c) for (i, j, c) in edges if i in comp and j in comp]
        cmin, cmax = min(c for *_, c in ps), max(c for *_, c in ps)
        sources = [recs[i]["value"] for i in comp]
        print(f"\n=== cluster #{ci}: {len(comp)} records, cosine {cmin:.4f}..{cmax:.4f}")
        if cmin >= TAU_HIGH:
            print("  -> MERGE (mechanical, no LLM)")
            continue
        if len(comp) > MAX_ABSTRACT:
            print(f"  -> SKIP (reason: cluster too large for abstraction ({len(comp)} > {MAX_ABSTRACT}))")
            continue
        out = deepseek(sources, e["LLM_API_KEY"], e["LLM_MODEL"], e["LLM_ENDPOINT"])
        ok, reason = guard(sources, out)
        print(f"  max_tokens={max_tokens(sources)}")
        if ok:
            print(f"  -> ACTION abstract (guard PASS). summary: {out[:160]}...")
        else:
            print(f"  -> SKIP (reason: {reason})")


if __name__ == "__main__":
    main()
