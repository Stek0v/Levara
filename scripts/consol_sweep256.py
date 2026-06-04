#!/usr/bin/env python3
"""Full consolidation dry-run sweep across the 256-dim memory collections.

dry_run=true still invokes the DeepSeek summarizer (AbstractValue) for every
abstract cluster (cosine 0.85..0.97); only the write-back (Store.Apply) is
gated by dry_run. So this is a full exercise of the LLM path WITHOUT mutating
any memory — safe and reversible (nothing is written).

Per-call fresh MCP session (the server invalidates a session after the first
tools/call when notifications/initialized isn't replayed). Logical collection
names are passed on argv; the tool resolves each to its _memories_<name>
vector sidecar internally.

Columns: candidates clusters actions skipped  (+ run_id, note)
- actions>0  => DeepSeek produced an abstraction or a mechanical merge was planned
- skipped>0  => a cluster was found but not acted on (coverage guard / merge skip)
"""
import json, subprocess, sys, time

BASE = "http://localhost:8090/mcp"


def session():
    init = json.dumps({"jsonrpc": "2.0", "id": 1, "method": "initialize",
                       "params": {"protocolVersion": "2024-11-05", "capabilities": {},
                                  "clientInfo": {"name": "sweep256", "version": "1"}}})
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
        raise RuntimeError("no session id")
    note = json.dumps({"jsonrpc": "2.0", "method": "notifications/initialized"})
    subprocess.run(
        ["curl", "-s", "--max-time", "5", "-o", "/dev/null", "-X", "POST", BASE,
         "-H", "Content-Type: application/json",
         "-H", "Accept: application/json, text/event-stream",
         "-H", f"Mcp-Session-Id: {sid}", "-d", note],
        capture_output=True, text=True, timeout=8)
    return sid


def call(name, args, max_time=180):
    sid = session()
    body = json.dumps({"jsonrpc": "2.0", "id": 10, "method": "tools/call",
                       "params": {"name": name, "arguments": args}})
    out = subprocess.run(
        ["curl", "-s", "--max-time", str(max_time), "-X", "POST", BASE,
         "-H", "Content-Type: application/json",
         "-H", "Accept: application/json, text/event-stream",
         "-H", f"Mcp-Session-Id: {sid}", "-d", body],
        capture_output=True, text=True, timeout=max_time + 5).stdout
    blocks = [l[5:].strip() for l in out.splitlines() if l.startswith("data:")]
    raw = blocks[-1] if blocks else out.strip()
    try:
        payload = json.loads(raw)
    except Exception:
        return {"text": out.strip()[:200]}
    if "error" in payload:
        return {"error": payload["error"]}
    content = payload.get("result", {}).get("content", [])
    txt = "\n".join(c.get("text", "") for c in content if c.get("type") == "text")
    return {"text": txt}


def parse(txt):
    d = {}
    for tok in txt.split():
        if "=" in tok:
            k, v = tok.split("=", 1)
            d[k] = v
    return d


def alive():
    out = subprocess.run(["curl", "-s", "--max-time", "5", "http://localhost:8090/health"],
                         capture_output=True, text=True, timeout=8).stdout
    return "healthy" in out


def main():
    colls = [c.strip() for c in sys.argv[1:] if c.strip()]
    print(f"# 256-dim memory consolidation sweep (dry_run, DeepSeek live) — {len(colls)} collections\n")
    print(f"{'collection':22} {'cand':>4} {'clus':>4} {'act':>3} {'skip':>4}  {'alive':5}  note", flush=True)
    tot = {"candidates": 0, "clusters": 0, "actions": 0, "skipped": 0}
    t0 = None
    for c in colls:
        r = call("consolidate", {"collection": c, "dry_run": True})
        a = alive()
        if "error" in r:
            print(f"{c:22} {'':>4} {'':>4} {'':>3} {'':>4}  {str(a):5}  ERROR {json.dumps(r['error'])[:60]}", flush=True)
            continue
        d = parse(r.get("text", ""))
        for k in tot:
            tot[k] += int(d.get(k, 0))
        run = d.get("run", "")[:8]
        note = []
        if int(d.get("actions", 0)) > 0:
            note.append("DeepSeek/merge")
        if int(d.get("skipped", 0)) > 0:
            note.append("skipped")
        print(f"{c:22} {d.get('candidates','0'):>4} {d.get('clusters','0'):>4} "
              f"{d.get('actions','0'):>3} {d.get('skipped','0'):>4}  {str(a):5}  run={run} {' '.join(note)}", flush=True)
    print(f"\n{'TOTAL':22} {tot['candidates']:>4} {tot['clusters']:>4} "
          f"{tot['actions']:>3} {tot['skipped']:>4}", flush=True)
    print(f"# final server alive: {alive()}")
    print("# DONE")


if __name__ == "__main__":
    main()
