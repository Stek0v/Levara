#!/usr/bin/env python3
"""Phase-B: dry-run consolidation across every real Pi collection.

Reads collection names from argv (one per line via stdin fallback), opens one
MCP session, runs consolidate(dry_run=true) on each, and prints a table:
  collection  candidates  clusters  actions  skipped

dry_run still calls the DeepSeek summarizer for every abstract cluster, so the
actions/skipped columns reflect real LLM spend. Merges (cos>=0.97) are free.
"""
import json, subprocess, sys, time

BASE = "http://localhost:8090/mcp"


def session():
    init = json.dumps({"jsonrpc": "2.0", "id": 1, "method": "initialize",
                       "params": {"protocolVersion": "2024-11-05", "capabilities": {},
                                  "clientInfo": {"name": "scan", "version": "1"}}})
    sid = None
    for attempt in range(4):
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
        raise RuntimeError("no session id after retries")
    # Mark the session ready so the server doesn't 404 later calls. A
    # notification gets a 202 with no body; --max-time keeps curl from hanging.
    note = json.dumps({"jsonrpc": "2.0", "method": "notifications/initialized"})
    subprocess.run(
        ["curl", "-s", "--max-time", "5", "-o", "/dev/null", "-X", "POST", BASE,
         "-H", "Content-Type: application/json",
         "-H", "Accept: application/json, text/event-stream",
         "-H", f"Mcp-Session-Id: {sid}", "-d", note],
        capture_output=True, text=True, timeout=8)
    return sid


def call(sid, name, args, _id=10, max_time=120):
    body = json.dumps({"jsonrpc": "2.0", "id": _id, "method": "tools/call",
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
        return {"_text": out.strip()}
    if "error" in payload:
        return {"_error": payload["error"]}
    content = payload.get("result", {}).get("content", [])
    txt = "\n".join(c.get("text", "") for c in content if c.get("type") == "text")
    return {"_text": txt}


def parse(txt):
    # "consolidate dry_run: run=... candidates=N clusters=N actions=N skipped=N"
    d = {}
    for tok in txt.split():
        if "=" in tok:
            k, v = tok.split("=", 1)
            if v.isdigit():
                d[k] = int(v)
    return d


def main():
    colls = [c.strip() for c in sys.argv[1:] if c.strip()]
    print(f"# collections={len(colls)} (fresh session per call)", flush=True)
    print(f"{'collection':24} {'cand':>5} {'clust':>5} {'act':>4} {'skip':>4}  note", flush=True)
    tot = {"candidates": 0, "clusters": 0, "actions": 0, "skipped": 0}
    sid = session()
    for c in colls:
        r = call(sid, "consolidate", {"collection": c, "dry_run": True})
        if "_error" in r:
            print(f"{c:24} {'':>5} {'':>5} {'':>4} {'':>4}  ERROR {r['_error'].get('message','')[:40]}", flush=True)
            continue
        txt = r.get("_text", "")
        d = parse(txt)
        if not d:
            print(f"{c:24} {'':>5} {'':>5} {'':>4} {'':>4}  ?? {txt[:50]}", flush=True)
            continue
        for k in tot:
            tot[k] += d.get(k, 0)
        note = "LLM-spent" if d.get("actions", 0) or d.get("skipped", 0) else ""
        print(f"{c:24} {d.get('candidates',0):>5} {d.get('clusters',0):>5} "
              f"{d.get('actions',0):>4} {d.get('skipped',0):>4}  {note}", flush=True)
        time.sleep(0.2)
    print(f"{'TOTAL':24} {tot['candidates']:>5} {tot['clusters']:>5} "
          f"{tot['actions']:>4} {tot['skipped']:>4}", flush=True)
    print("# DONE", flush=True)


if __name__ == "__main__":
    main()
