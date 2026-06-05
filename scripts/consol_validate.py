#!/usr/bin/env python3
"""Post-deploy validation for the potion-256 cutover + dim crash-guard.

Checks, via MCP over HTTP on the Pi:
  1. recall_memory on a real potion-256 collection returns hits
     (proves embed(256) -> CollectionSearch(256) dims now match).
  2. consolidate(dry_run) on a 256 collection runs (candidates>0 possible,
     no crash) — previously this panicked the server.
  3. consolidate(dry_run) on a 768 straggler returns a safe error/no-op and
     the server stays up (the crash guard, Fix #1).

Fresh MCP session per tools/call — the server invalidates a session after the
first call when notifications/initialized isn't replayed, so we don't reuse.
"""
import json, subprocess, sys, time

BASE = "http://localhost:8090/mcp"


def session():
    init = json.dumps({"jsonrpc": "2.0", "id": 1, "method": "initialize",
                       "params": {"protocolVersion": "2024-11-05", "capabilities": {},
                                  "clientInfo": {"name": "validate", "version": "1"}}})
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


def call(name, args, max_time=120):
    sid = session()
    body = json.dumps({"jsonrpc": "2.0", "id": 10, "method": "tools/call",
                       "params": {"name": name, "arguments": args}})
    cp = subprocess.run(
        ["curl", "-s", "-w", "\n__HTTP__%{http_code}", "--max-time", str(max_time), "-X", "POST", BASE,
         "-H", "Content-Type: application/json",
         "-H", "Accept: application/json, text/event-stream",
         "-H", f"Mcp-Session-Id: {sid}", "-d", body],
        capture_output=True, text=True, timeout=max_time + 5)
    out = cp.stdout
    http = ""
    if "__HTTP__" in out:
        out, http = out.rsplit("__HTTP__", 1)
        http = http.strip()
    blocks = [l[5:].strip() for l in out.splitlines() if l.startswith("data:")]
    raw = blocks[-1] if blocks else out.strip()
    try:
        payload = json.loads(raw)
    except Exception:
        return {"http": http, "text": out.strip()[:300]}
    if "error" in payload:
        return {"http": http, "error": payload["error"]}
    content = payload.get("result", {}).get("content", [])
    txt = "\n".join(c.get("text", "") for c in content if c.get("type") == "text")
    return {"http": http, "text": txt}


def alive():
    out = subprocess.run(["curl", "-s", "--max-time", "5", "http://localhost:8090/health"],
                         capture_output=True, text=True, timeout=8).stdout
    return "healthy" in out


def main():
    print("# potion-256 cutover validation\n")

    print("[1] recall on potion-256 collection (_memories_levara)")
    r = call("recall_memory", {"query": "levara", "collection": "levara", "limit": 3})
    print(f"    http={r.get('http')} {('ERROR '+json.dumps(r['error'])) if 'error' in r else r.get('text','')[:240]}")
    print(f"    server alive after: {alive()}\n")

    print("[2] consolidate dry_run on potion-256 collection (_memories_levara)")
    r = call("consolidate", {"collection": "levara", "dry_run": True})
    print(f"    http={r.get('http')} {('ERROR '+json.dumps(r['error'])) if 'error' in r else r.get('text','')[:240]}")
    print(f"    server alive after: {alive()}\n")

    print("[3] consolidate dry_run on 768 straggler (local-net) — must be safe no-op, no crash")
    r = call("consolidate", {"collection": "local-net", "dry_run": True})
    print(f"    http={r.get('http')} {('ERROR '+json.dumps(r['error'])) if 'error' in r else r.get('text','')[:240]}")
    print(f"    server alive after: {alive()}\n")

    print("# DONE")


if __name__ == "__main__":
    main()
