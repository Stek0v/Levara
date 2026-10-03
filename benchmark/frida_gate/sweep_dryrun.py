#!/usr/bin/env python3
"""Dry-run consolidation sweep over all prod collections, collecting facts:
candidates, clusters, actions, skips, LLM calls, FRIDA gate counters, wall time.
"""
import json
import re
import sys
import time
import urllib.request

BASE = "http://127.0.0.1:8081"
H = {"Content-Type": "application/json", "Accept": "application/json, text/event-stream"}
TOP = ["", "ub-main", "levara", "samobranka", "game", "pd", "memeval_c12b740d6e_scale",
       "ub", "memeval_c12b740d6e", "speccorewhite", "dmitriy", "unreal", "ai-news",
       "labirint", "alpha-1784042315-155e61-bootstrap", "alpha-1784440258-badfec-bootstrap",
       "PD", "local-net", "ten", "urbanbaza", "urban-base", "freetoken-2",
       "pd-audit-saas", "1000btc", "бикон", "local_net"]


def rpc(payload, sid=None, timeout=1800):
    h = dict(H)
    if sid: h["Mcp-Session-Id"] = sid
    req = urllib.request.Request(BASE + "/mcp", data=json.dumps(payload).encode(), headers=h)
    with urllib.request.urlopen(req, timeout=timeout) as r:
        return r.headers.get("Mcp-Session-Id"), r.read().decode()


def texts(raw, rid):
    out = []
    for line in raw.splitlines():
        line = line.strip()
        if line.startswith("data:"): line = line[5:].strip()
        try: obj = json.loads(line)
        except Exception: continue
        if isinstance(obj, dict) and obj.get("id") == rid:
            for c in obj.get("result", {}).get("content", []):
                out.append(c.get("text") or "")
    return "".join(out)


sid, _ = rpc({"jsonrpc":"2.0","id":1,"method":"initialize","params":{
    "protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"sweep","version":"0"}}})
rpc({"jsonrpc":"2.0","method":"notifications/initialized"}, sid)


def consolidate(coll, rid):
    t0 = time.perf_counter()
    _, raw = rpc({"jsonrpc":"2.0","id":rid,"method":"tools/call","params":{
        "name":"consolidate","arguments":{"collection":coll,"dry_run":True}}}, sid, rid)
    body = texts(raw, rid)
    try:
        job = json.loads(body).get("job_id")
    except Exception:
        return {"coll": coll, "error": body[:200], "wall": round(time.perf_counter()-t0,1)}
    for i in range(600):
        time.sleep(3)
        _, sraw = rpc({"jsonrpc":"2.0","id":100000+rid,"method":"tools/call","params":{
            "name":"consolidation_status","arguments":{"job_id": job}}}, sid, 100000+rid)
        t = texts(sraw, 100000+rid)
        try:
            st = json.loads(t).get("status")
        except Exception:
            st = None
        if st in ("completed", "failed") or st is None:
            try:
                d = json.loads(t)
            except Exception:
                return {"coll": coll, "error": t[:200], "wall": round(time.perf_counter()-t0,1)}
            inner = d.get("result") or ""
            gate = re.search(r"decision gate: checked=(\d+) rejected=(\d+) errors=(\d+)", inner)
            return {"coll": coll, "status": d.get("status"),
                    "candidates": d.get("candidates"), "clusters": d.get("clusters"),
                    "actions": d.get("actions"), "llm_calls": d.get("llm_calls"),
                    "last_error": (d.get("last_error") or "")[:120],
                    "gate": {"checked": int(gate.group(1)), "rejected": int(gate.group(2)),
                             "errors": int(gate.group(3))} if gate else None,
                    "wall": round(time.perf_counter()-t0, 1)}
    return {"coll": coll, "error": "timeout", "wall": round(time.perf_counter()-t0,1)}


rid = 1000
rows = []
order = TOP
others = json.load(open("data/sweep_rest.json")) if len(sys.argv) > 1 and sys.argv[1] == "rest" else []
for coll in (order if not others else others):
    r = consolidate(coll, rid); rid += 1
    rows.append(r)
    print(json.dumps(r, ensure_ascii=False), flush=True)
    with open("data/sweep_results.json", "w", encoding="utf-8") as f:
        json.dump(rows, f, ensure_ascii=False, indent=1)
print("SWEEP_DONE")
