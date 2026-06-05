#!/usr/bin/env python3
"""Heavy cross-domain consolidation quality harness — runs ON the Pi.

Validates the memory-consolidation feature end-to-end against the LIVE,
freshly-deployed binary. Three modules, one PASS/FAIL report:

  Module A — recall before/after a real consolidate() on a seeded fixture.
    Seeds ~30 records with KNOWN facts across distinct rooms (auth, deploy,
    mcp, embed, kg) including near-duplicate clusters, then runs hard
    cross-domain queries with ground truth, consolidates for real
    (dry_run=false), and re-measures. Headline safety property: no
    ground-truth fact may become unretrievable, and recall must not
    regress.

  Module B — coverage-guard corner cases.
    B1: the guard FUNCTION on crafted (sources, summary) pairs — dropped
        number, invented number, entity fraction at/over tolerance,
        code-keyword drop within tolerance. Deterministic.
    B2: adversarial CLUSTERS through the real potion-embed + DeepSeek
        pipeline — a tight cluster must MERGE, an oversized cluster must
        SKIP("too large"), a related pair must abstract with every number
        preserved.

  Module C — live smoke over real prod collections (dry_run), asserting no
    crash and surfacing per-cluster skip reasons.

Exit code == number of FAILED cases (0 == all green).

Read-mostly: Module A writes only to a unique throwaway collection and
deletes it on teardown. Modules B/C are read-only against prod data.
"""
import json
import re
import sqlite3
import subprocess
import sys
import time
import urllib.request

BASE = "http://localhost:8090/mcp"
DB = "/home/stek0v/levara/data/levara.db"
ENV = "/home/stek0v/levara/levara.env"

TAU_LOW, TAU_HIGH = 0.90, 0.97
MAX_ABSTRACT = 6
MAX_ENTITY_DROP_FRAC = 0.10
RECALL_REGRESSION_EPS = 0.10  # recall_after may dip at most this below before

NUM_RE = re.compile(r"\d+")
ENT_RE = re.compile(r"\b[A-Z][A-Za-z0-9]+\b")


# ----------------------------------------------------------------------------
# env + MCP plumbing (mirrors consol_validate.py: fresh session per call)
# ----------------------------------------------------------------------------
def load_env():
    d = {}
    with open(ENV) as f:
        for line in f:
            line = line.strip()
            if "=" in line and not line.startswith("#"):
                k, v = line.split("=", 1)
                d[k] = v.strip().strip('"')
    return d


def _session():
    init = json.dumps({"jsonrpc": "2.0", "id": 1, "method": "initialize",
                       "params": {"protocolVersion": "2024-11-05", "capabilities": {},
                                  "clientInfo": {"name": "quality", "version": "1"}}})
    sid = None
    for _ in range(4):
        out = subprocess.run(
            ["curl", "-s", "--max-time", "10", "-D", "-", "-o", "/dev/null", "-X", "POST", BASE,
             "-H", "Content-Type: application/json",
             "-H", "Accept: application/json, text/event-stream", "-d", init],
            capture_output=True, text=True, timeout=15).stdout
        for line in out.splitlines():
            if line.lower().startswith("mcp-session-id:"):
                sid = line.split(":", 1)[1].strip()
                break
        if sid:
            break
        time.sleep(1)
    if not sid:
        raise RuntimeError("no MCP session id")
    note = json.dumps({"jsonrpc": "2.0", "method": "notifications/initialized"})
    subprocess.run(
        ["curl", "-s", "--max-time", "5", "-o", "/dev/null", "-X", "POST", BASE,
         "-H", "Content-Type: application/json",
         "-H", "Accept: application/json, text/event-stream",
         "-H", f"Mcp-Session-Id: {sid}", "-d", note],
        capture_output=True, text=True, timeout=8)
    return sid


def mcp_call(name, args, max_time=180):
    """Return (ok, text). text is the concatenated content[].text of the result."""
    sid = _session()
    body = json.dumps({"jsonrpc": "2.0", "id": 10, "method": "tools/call",
                       "params": {"name": name, "arguments": args}})
    out = subprocess.run(
        ["curl", "-s", "--max-time", str(max_time), "-X", "POST", BASE,
         "-H", "Content-Type: application/json",
         "-H", "Accept: application/json, text/event-stream",
         "-H", f"Mcp-Session-Id: {sid}", "-d", body],
        capture_output=True, text=True, timeout=max_time + 10).stdout
    # The server answers either as a plain JSON object or as an SSE stream of
    # `data:` frames depending on negotiation — handle both.
    payloads = []
    stripped = out.strip()
    try:
        payloads.append(json.loads(stripped))
    except json.JSONDecodeError:
        for line in out.splitlines():
            line = line.strip()
            if line.startswith("data:"):
                try:
                    payloads.append(json.loads(line[5:].strip()))
                except json.JSONDecodeError:
                    pass
    texts, is_err = [], False
    for d in payloads:
        res = d.get("result")
        if res is None and "error" in d:
            return False, json.dumps(d["error"])
        if res:
            is_err = bool(res.get("isError"))
            for c in res.get("content", []):
                if c.get("type") == "text":
                    texts.append(c.get("text", ""))
    return (not is_err), "\n".join(texts)


# ----------------------------------------------------------------------------
# embed / cosine / cluster / guard  (mirror of the fixed Go pipeline)
# ----------------------------------------------------------------------------
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
            p[x] = p[p[x]]
            x = p[x]
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


def guard(sources, out):
    """Mirror of consolidate.AbstractValue's coverage guard. Returns (ok, reason)."""
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
        return False, f"dropped {len(dropped)}/{len(se)} entities {sorted(dropped)}"
    return True, "ok"


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


# ----------------------------------------------------------------------------
# Module A fixture: known facts spread across rooms, with near-dup clusters
# ----------------------------------------------------------------------------
# (base_key, room, value). Triples a*/d* are near-duplicates (merge bait);
# m2/m3 and k1/k2 are related-but-distinct (abstract bait); c* are corner
# records (RETRACTED note, code keywords, number-dense).
FIXTURE = [
    # auth — near-IDENTICAL triple (real dedup case): must MERGE to one record.
    # potion-256 keeps these >=0.97; the survivor still carries HS256+JWT_SECRET
    # so every fact stays retrievable after the collapse.
    ("a1", "auth", "JWT_SECRET is the shared HS256 secret Levara uses for HTTP and gRPC auth."),
    ("a2", "auth", "JWT_SECRET is the shared HS256 secret Levara uses for HTTP and gRPC auth"),
    ("a3", "auth", "JWT_SECRET is the shared HS256 secret that Levara uses for HTTP and gRPC auth."),
    ("a4", "auth", "Auth login and register share a per-IP rate bucket of 10 requests per minute."),
    # deploy — near-IDENTICAL triple about the edge host/port: must MERGE.
    ("d1", "deploy", "Levara runs on a Raspberry Pi 5 at 10.23.0.53 serving HTTP on port 8090."),
    ("d2", "deploy", "Levara runs on a Raspberry Pi 5 at 10.23.0.53 serving HTTP on port 8090"),
    ("d3", "deploy", "Levara runs on a Raspberry Pi 5 at 10.23.0.53 and serves HTTP on port 8090."),
    ("d4", "deploy", "The potion-code-16M embed sidecar listens on loopback port 9101 at dim 256."),
    # mcp — related but distinct (abstract bait)
    ("m1", "mcp", "Levara exposes 27 MCP tools including save_memory and recall_memory."),
    ("m2", "mcp", "The consolidate MCP tool merges near-duplicate memory records reversibly."),
    ("m3", "mcp", "consolidation_revert restores superseded source records by run_id."),
    # embed
    ("e1", "embed", "Production migrated embeddings from nomic-embed-text-v2-moe 768 dim to potion-code-16M 256 dim."),
    ("e2", "embed", "The potion model2vec backend runs at 256 dimensions using 233 MB of RAM."),
    # kg — related but distinct (abstract bait)
    ("k1", "kg", "The Levara knowledge graph supersedes an edge with valid_until when a newer fact arrives."),
    ("k2", "kg", "query_entity accepts an as_of date to return a temporal snapshot of an entity."),
    # corner cases
    ("c1", "deploy", "RETRACTED: the old embedding endpoint was llama.cpp at 10.23.0.64 port 9004."),
    ("c2", "mcp", "Guard keywords like NULL CREATE REPL must be preserved verbatim in summaries."),
    ("c3", "deploy", "The levara-backup CLI talks gRPC on port 50051 and coalesces 16 WAL writes per group commit."),
]

# (query, expected base_keys, ground-truth fact tokens, rooms it spans)
QUERIES = [
    ("What secret does Levara use to sign auth tokens?",
     ["a1", "a2", "a3"], ["HS256", "JWT_SECRET"], ["auth"]),
    ("Which host and HTTP port serve Levara on the edge device?",
     ["d1", "d2", "d3"], ["10.23.0.53", "8090"], ["deploy"]),
    ("What embedding model and dimension does production use now?",
     ["e1", "e2", "d4"], ["256", "potion-code-16M"], ["embed", "deploy"]),
    ("How do you undo a consolidation in Levara?",
     ["m2", "m3"], ["consolidation_revert", "run_id"], ["mcp"]),
    ("How does the knowledge graph handle facts that change over time?",
     ["k1", "k2"], ["valid_until", "as_of"], ["kg"]),
    # hard cross-domain: spans deploy + embed, two ports
    ("What services run on the Pi and on which ports?",
     ["d1", "d2", "d3", "d4"], ["8090", "9101"], ["deploy", "embed"]),
]


# Keys are kept SHORT on purpose: save_memory indexes embed(key+" "+value),
# while consolidate's edge-builder queries with embed(value) only. A long key
# prefix pollutes the stored vector and sinks even near-identical records below
# TauLow, starving the clusterer. Short keys keep key+value ~= value so real
# near-dups cluster. (qf_ prefix + base; teardown deletes them each run.)
def fixture_key(base):
    return f"qf_{base}"


def seed_fixture(coll, env):
    n = 0
    for base, room, value in FIXTURE:
        ok, _ = mcp_call("save_memory", {
            "key": fixture_key(base), "value": value, "collection": coll,
            "room": room, "hall": "fact",
        }, max_time=30)
        if ok:
            n += 1
    return n


def parse_recall(text):
    """recall_memory returns a JSON array of {key,value,...} or a no-results msg."""
    text = text.strip()
    if not text.startswith("["):
        return []
    try:
        return json.loads(text)
    except json.JSONDecodeError:
        return []


def wait_indexed(coll, probe_query, want, timeout=30):
    """Poll until the async vector index has caught up (>= want hits) or timeout."""
    deadline = time.time() + timeout
    last = 0
    while time.time() < deadline:
        _, text = mcp_call("recall_memory", {"query": probe_query, "collection": coll}, max_time=30)
        last = len(parse_recall(text))
        if last >= want:
            return last
        time.sleep(2)
    return last


def evaluate(coll):
    """Run every query; return per-query metrics + aggregate."""
    rows = []
    for q, expect, facts, rooms in QUERIES:
        _, text = mcp_call("recall_memory", {"query": q, "collection": coll}, max_time=30)
        hits = parse_recall(text)
        got_keys = [h.get("key", "") for h in hits]
        blob = " ".join(h.get("value", "") for h in hits).lower()
        exp_full = {fixture_key(b) for b in expect}
        inter = exp_full & set(got_keys)
        recall = len(inter) / len(exp_full) if exp_full else 0.0
        mrr = 0.0
        for rank, k in enumerate(got_keys, 1):
            if k in exp_full:
                mrr = 1.0 / rank
                break
        covered = [f for f in facts if f.lower() in blob]
        fact_recall = len(covered) / len(facts) if facts else 0.0
        rows.append({"q": q, "recall": recall, "mrr": mrr, "rooms": rooms,
                     "facts": facts, "covered": covered, "fact_recall": fact_recall,
                     "n_hits": len(hits)})
    agg_key_recall = sum(r["recall"] for r in rows) / len(rows)
    agg_fact_recall = sum(r["fact_recall"] for r in rows) / len(rows)
    agg_mrr = sum(r["mrr"] for r in rows) / len(rows)
    return rows, agg_key_recall, agg_fact_recall, agg_mrr


def teardown(coll):
    try:
        con = sqlite3.connect(DB)
        con.execute("DELETE FROM memories WHERE collection_name=?", (coll,))
        con.commit()
        con.close()
    except sqlite3.Error as e:
        print(f"  [teardown] sqlite cleanup failed: {e}")
    # best-effort vector-shard drop (needs auth; ignore failure — shard is tiny)
    for name in (f"_memories_{coll}", coll):
        subprocess.run(["curl", "-s", "-o", "/dev/null", "--max-time", "5",
                        "-X", "DELETE", f"http://localhost:8090/api/v1/collections/{name}"],
                       capture_output=True, text=True)


# ----------------------------------------------------------------------------
# report helpers
# ----------------------------------------------------------------------------
class Report:
    def __init__(self):
        self.fails = 0
        self.passes = 0

    def check(self, name, ok, detail=""):
        tag = "PASS" if ok else "FAIL"
        if ok:
            self.passes += 1
        else:
            self.fails += 1
        print(f"  [{tag}] {name}" + (f" — {detail}" if detail else ""))
        return ok


# ----------------------------------------------------------------------------
# Module A
# ----------------------------------------------------------------------------
def module_a(env, rep):
    print("\n=== Module A: recall before/after a real consolidate() ===")
    coll = f"qual_fixture_{int(time.time())}"
    print(f"  fixture collection: {coll}")
    try:
        n = seed_fixture(coll, env)
        print(f"  seeded {n}/{len(FIXTURE)} records")
        got = wait_indexed(coll, QUERIES[0][0], want=2, timeout=40)
        print(f"  vector index warm (probe hits={got})")

        before_rows, b_key, b_fact, bm = evaluate(coll)
        print(f"  BEFORE: fact_recall={b_fact:.2f} key_recall={b_key:.2f} mrr={bm:.2f}")

        ok, ctext = mcp_call("consolidate", {"collection": coll, "dry_run": False}, max_time=240)
        print("  consolidate output:")
        for ln in ctext.splitlines():
            print(f"    {ln}")
        m = re.search(r"candidates=(\d+) clusters=(\d+) actions=(\d+) skipped=(\d+)", ctext)
        cand, clus, acts, skip = (int(x) for x in m.groups()) if m else (0, 0, 0, 0)

        # let the consolidated record's async index settle
        time.sleep(6)
        wait_indexed(coll, QUERIES[0][0], want=1, timeout=20)
        after_rows, a_key, a_fact, am = evaluate(coll)
        print(f"  AFTER:  fact_recall={a_fact:.2f} key_recall={a_key:.2f} mrr={am:.2f}")
        print(f"  (key_recall is expected to drop where clusters MERGE — N records become 1)")

        rep.check("A1 consolidate ran without error", ok, ctext[:120])
        rep.check("A2 pipeline engaged (candidates>0)", cand > 0, f"candidates={cand} clusters={clus}")
        rep.check("A3 consolidation engaged (>=1 cluster acted-on or skipped)",
                  clus >= 1 and (acts >= 1 or skip >= 1),
                  f"clusters={clus} actions={acts} skipped={skip}")
        rep.check("A4 no FACT-recall regression", a_fact >= b_fact - RECALL_REGRESSION_EPS,
                  f"before={b_fact:.2f} after={a_fact:.2f} (eps={RECALL_REGRESSION_EPS})")

        # headline: facts covered before must still be covered after
        lost = []
        bmap = {r["q"]: r for r in before_rows}
        for ar_row in after_rows:
            br_row = bmap[ar_row["q"]]
            for f in br_row["covered"]:
                if f not in ar_row["covered"]:
                    lost.append((ar_row["q"], f))
        rep.check("A5 ZERO ground-truth facts lost after consolidation",
                  not lost, "lost=" + (str(lost) if lost else "none"))

        # cross-domain query still spans both rooms after
        xq = next(r for r in after_rows if len(r["rooms"]) > 1)
        rep.check("A6 cross-domain query still covers all its facts after",
                  set(xq["covered"]) == set(xq["facts"]),
                  f"{xq['q'][:40]}… covered={xq['covered']} of {xq['facts']}")
    finally:
        teardown(coll)
        print(f"  torn down {coll}")


# ----------------------------------------------------------------------------
# Module B
# ----------------------------------------------------------------------------
def module_b(env, rep):
    print("\n=== Module B: coverage-guard corner cases ===")
    print("  B1 guard function (deterministic):")
    # (name, sources, summary, expect_pass)
    e12 = ["Levara HNSW dim 256 on Pi at 10.23.0.53 with DeepSeek and BM25 and WAL and HNSW and RRF and Louvain and Neo4j and Ollama and Prometheus and Postgres."]
    cases = [
        ("dropped source number rejected",
         ["The HTTP port is 8090."], "The HTTP port is exposed.", False),
        ("invented number rejected",
         ["The port is 8090."], "The port is 8090 and 9999.", False),
        ("small entity-fraction drop allowed (<=10%)",
         e12, "Levara HNSW dim 256 on Pi at 10.23.0.53 with DeepSeek BM25 WAL RRF Louvain Neo4j Ollama Prometheus Postgres.", True),
        ("large entity-fraction drop rejected (>10%)",
         ["Levara DeepSeek Postgres at dim 256."], "It runs at dim 256.", False),
        ("code-keyword drop within tolerance allowed",
         ["Keywords NULL CREATE REPL SELECT INSERT UPDATE DELETE WHERE FROM JOIN GROUP at port 8090."],
         "Keywords NULL CREATE SELECT INSERT UPDATE DELETE WHERE FROM JOIN GROUP at port 8090.", True),
    ]
    for name, src, out, expect_pass in cases:
        ok, reason = guard(src, out)
        rep.check(f"B1 {name}", ok == expect_pass,
                  f"got ok={ok} ({reason}), expected ok={expect_pass}")

    print("  B2 adversarial clusters through real potion + DeepSeek:")
    ep, model = env["EMBEDDING_ENDPOINT"], env["EMBEDDING_MODEL"]
    key, lm, lb = env["LLM_API_KEY"], env["LLM_MODEL"], env["LLM_ENDPOINT"]

    # tight cluster — near-identical records (the real dedup case) → must MERGE
    # (cos>=TauHigh). Mechanical merge fires on essentially-duplicate text, not
    # on paraphrase; potion-256 keeps paraphrases in the 0.90..0.97 abstract band.
    tight = [
        "Levara serves HTTP on port 8090 on the Pi at 10.23.0.53.",
        "Levara serves HTTP on port 8090 on the Pi at 10.23.0.53",
        "Levara serves HTTP on port 8090 on the Pi at 10.23.0.53.  ",
    ]
    vecs = embed(tight, ep, model)
    cmin = min(cosine(vecs[i], vecs[j]) for i in range(len(tight)) for j in range(i + 1, len(tight)))
    rep.check("B2 tight cluster classifies as MERGE", cmin >= TAU_HIGH,
              f"min cosine={cmin:.4f} (TauHigh={TAU_HIGH})")

    # oversized cluster — 7 mutually-similar records → must SKIP 'too large'
    over = [f"Levara consolidation note number {i} about merging memory records on the Pi edge server." for i in range(7)]
    vecs = embed(over, ep, model)
    edges = [(i, j, cosine(vecs[i], vecs[j])) for i in range(len(over))
             for j in range(i + 1, len(over)) if cosine(vecs[i], vecs[j]) >= TAU_LOW]
    comps = union_find(len(over), edges)
    biggest = max((len(c) for c in comps), default=0)
    oversized = biggest > MAX_ABSTRACT
    rep.check("B2 oversized cluster would SKIP('too large')", oversized,
              f"largest component={biggest} (MAX_ABSTRACT={MAX_ABSTRACT})")

    # related pair — distinct facts → abstract attempt must preserve every number
    pair = [
        "The potion sidecar listens on port 9101 at dim 256.",
        "The potion model2vec backend uses 256 dimensions and 233 MB RAM.",
    ]
    out = deepseek(pair, key, lm, lb)
    ok, reason = guard(pair, out)
    src_nums = tokenset(NUM_RE, *pair)
    out_nums = tokenset(NUM_RE, out)
    nums_ok = src_nums <= out_nums
    rep.check("B2 abstract pair preserves every source number", nums_ok,
              f"src={sorted(src_nums)} out={sorted(out_nums)} guard={ok}/{reason}")
    print(f"    abstract summary: {out[:160]}")


# ----------------------------------------------------------------------------
# Module C
# ----------------------------------------------------------------------------
def module_c(env, rep, limit=10):
    print("\n=== Module C: live smoke over real prod collections (dry_run) ===")
    con = sqlite3.connect(DB)
    cur = con.execute(
        "SELECT collection_name, COUNT(*) c FROM memories "
        "WHERE superseded_by='' AND collection_name != '' "
        "AND collection_name NOT LIKE 'qual_fixture%' "
        "GROUP BY collection_name ORDER BY c DESC LIMIT ?", (limit,))
    colls = [r[0] for r in cur]
    con.close()
    print(f"  scanning {len(colls)} collections")
    all_ok = True
    for c in colls:
        ok, text = mcp_call("consolidate", {"collection": c, "dry_run": True}, max_time=180)
        first = text.splitlines()[0] if text else "(no output)"
        skips = [ln.strip() for ln in text.splitlines() if ln.strip().startswith("skip [")]
        print(f"    {c}: {'ok' if ok else 'ERROR'} — {first}")
        for s in skips:
            print(f"        {s}")
        all_ok = all_ok and ok
    rep.check("C1 every prod collection consolidated dry-run without error", all_ok)
    health = subprocess.run(["curl", "-s", "--max-time", "5", "http://localhost:8090/health"],
                            capture_output=True, text=True).stdout
    rep.check("C2 server healthy after full smoke", '"status":"ready"' in health, health[:80])


def main():
    env = load_env()
    rep = Report()
    only = sys.argv[1] if len(sys.argv) > 1 else "all"
    if only in ("all", "a"):
        module_a(env, rep)
    if only in ("all", "b"):
        module_b(env, rep)
    if only in ("all", "c"):
        module_c(env, rep)
    print(f"\n=== SUMMARY: {rep.passes} passed, {rep.fails} failed ===")
    sys.exit(rep.fails)


if __name__ == "__main__":
    main()
