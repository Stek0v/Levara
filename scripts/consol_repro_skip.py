#!/usr/bin/env python3
"""Reproduce, on the Pi, exactly why a consolidate cluster was Skipped.

Mirrors the Go pipeline component-for-component:
  Candidates (sqlite, same WHERE) -> potion embed each value (same sidecar)
  -> pairwise cosine, union-find at TauLow=0.85 -> Plan (allTight>=TauHigh=0.97
  ? merge : abstract) -> for each abstract cluster build the SAME DeepSeek
  prompt, call the SAME model, then run the SAME coverage-guard regexes
  (numberRe=\\d+, entityRe=\\b[A-Z][A-Za-z0-9]+\\b) and print the first
  violated token — the precise reason run.go did Skipped++.

Reads config (potion endpoint, DeepSeek key/model) from levara.env. Prints no
secrets. Read-only: no writes to the DB, no consolidation applied.
"""
import json, os, re, sqlite3, subprocess, sys, urllib.request

DB = "/home/stek0v/levara/data/levara.db"
ENV = "/home/stek0v/levara/levara.env"
TAU_LOW, TAU_HIGH = 0.85, 0.97

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
    rows = [{"id": r[0], "value": r[1], "room": r[2] or "", "created_at": r[3]} for r in cur]
    con.close()
    return rows


def embed(texts, ep, model):
    body = json.dumps({"input": texts, "model": model}).encode()
    req = urllib.request.Request(ep, data=body, headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=30) as r:
        data = json.load(r)
    return [d["embedding"] for d in data["data"]]


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


def deepseek(sources, key, model, base):
    prompt = ("Combine the following memory notes into ONE concise statement. "
              "Preserve every fact, number, name, and port exactly. "
              "Do NOT add any information not present below. Notes:\n")
    for s in sources:
        prompt += "- " + s + "\n"
    body = json.dumps({
        "model": model,
        "messages": [{"role": "user", "content": prompt}],
        "temperature": 0, "max_tokens": 512,
    }).encode()
    req = urllib.request.Request(base.rstrip("/") + "/chat/completions", data=body,
                                 headers={"Content-Type": "application/json",
                                          "Authorization": "Bearer " + key})
    with urllib.request.urlopen(req, timeout=60) as r:
        data = json.load(r)
    return data["choices"][0]["message"]["content"].strip()


def guard(sources, out):
    """Returns (ok, reason). Mirrors AbstractValue."""
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
    for e in se:
        if e not in oe:
            return False, f'dropped source entity "{e}"'
    return True, "ok"


def main():
    coll = sys.argv[1]
    e = env()
    ep, emodel = e["EMBEDDING_ENDPOINT"], e["EMBEDDING_MODEL"]
    key, lmodel, lbase = e["LLM_API_KEY"], e["LLM_MODEL"], e["LLM_ENDPOINT"]

    recs = candidates(coll)
    print(f"## collection '{coll}': {len(recs)} candidates")
    vecs = embed([r["value"] for r in recs], ep, emodel)

    edges = []
    for i in range(len(recs)):
        for j in range(i + 1, len(recs)):
            c = cosine(vecs[i], vecs[j])
            if c >= TAU_LOW:
                edges.append((i, j, c))
    clusters = union_find(len(recs), edges)
    print(f"## {len(clusters)} cluster(s) at cosine>={TAU_LOW}")

    for ci, comp in enumerate(clusters):
        pair_scores = [(i, j, c) for (i, j, c) in edges
                       if i in comp and j in comp]
        cmin = min(c for _, _, c in pair_scores)
        cmax = max(c for _, _, c in pair_scores)
        kind = "MERGE (mechanical, no LLM)" if cmin >= TAU_HIGH else "ABSTRACT (LLM)"
        print(f"\n=== cluster #{ci}: {len(comp)} records, cosine {cmin:.4f}..{cmax:.4f} -> {kind}")
        sources = [recs[i]["value"] for i in comp]
        for i in comp:
            print(f"  - [{recs[i]['id'][:8]}] room={recs[i]['room']!r}: {recs[i]['value'][:140]}")
        if cmin >= TAU_HIGH:
            print("  -> mechanical merge: never calls DeepSeek, never skipped.")
            continue
        out = deepseek(sources, key, lmodel, lbase)
        print(f"\n  DeepSeek summary:\n    {out}")
        ok, reason = guard(sources, out)
        if ok:
            print("\n  GUARD: PASS -> would NOT be skipped (re-run may differ).")
        else:
            print(f"\n  GUARD: REJECT -> Skipped++. REASON: {reason}")


if __name__ == "__main__":
    main()
