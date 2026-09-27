#!/usr/bin/env python3
"""Personal-profile install smoke: install state -> MCP handshake -> wake_up.

Implements the real legacy /mcp session handshake (initialize -> Mcp-Session-Id
-> initialized notification -> tools/list -> tools/call) so the numbers it
reports are what an agent actually sees. Exits non-zero on any failed check.
Usage: python3 scripts/smoke_personal.py --url http://127.0.0.1:18099
"""
import argparse
import json
import sys
import time
import urllib.request

PROTOCOL_VERSION = "2025-03-26"  # legacy session-based /mcp


def post(url, payload, session=None):
    req = urllib.request.Request(url, data=json.dumps(payload).encode(), method="POST")
    req.add_header("Content-Type", "application/json")
    req.add_header("Accept", "application/json, text/event-stream")
    if session:
        req.add_header("Mcp-Session-Id", session)
    with urllib.request.urlopen(req, timeout=30) as resp:
        return resp.headers.get("Mcp-Session-Id"), resp.status, resp.read().decode()


def parse(body):
    body = body.strip()
    if body.startswith("{"):
        return json.loads(body)
    data = [line[5:].strip() for line in body.splitlines() if line.startswith("data:")]
    return json.loads(data[-1])


def content_text(response):
    parts = response.get("result", {}).get("content", []) or []
    return "".join(p.get("text", "") for p in parts if isinstance(p, dict))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--url", default="http://127.0.0.1:8080")
    args = ap.parse_args()
    base = args.url.rstrip("/") + "/mcp"

    checks = []

    def check(name, ok, detail=""):
        checks.append((name, ok))
        print(("PASS" if ok else "FAIL"), name, ("| " + detail if detail else ""))

    t0 = time.time()

    sid, status, _ = post(base, {
        "jsonrpc": "2.0", "id": 1, "method": "initialize",
        "params": {"protocolVersion": PROTOCOL_VERSION, "capabilities": {},
                   "clientInfo": {"name": "levara-smoke", "version": "1.0"}},
    })
    check("initialize + session id", status == 200 and bool(sid), f"session={sid}")

    post(base, {"jsonrpc": "2.0", "method": "notifications/initialized"}, sid)

    _, _, body = post(base, {"jsonrpc": "2.0", "id": 2, "method": "tools/list"}, sid)
    tools = parse(body).get("result", {}).get("tools", [])
    names = sorted(t.get("name", "") for t in tools)
    check("tools/list == 13 tools (personal -> core)", len(tools) == 13, f"got {len(tools)}")
    for must in ("wake_up", "save_memory", "recall_memory", "supersede_memory",
                 "delete_memory", "pin_memory", "search", "doctor"):
        check(f"core tool present: {must}", must in names)
    for absent in ("consolidate", "workspace_search", "task_plan", "chat_distill"):
        check(f"non-core tool absent: {absent}", absent not in names)

    _, _, body = post(base, {"jsonrpc": "2.0", "id": 3, "method": "tools/call",
                             "params": {"name": "wake_up", "arguments": {"max_tokens": 300}}}, sid)
    response = parse(body)
    text = content_text(response)
    check("wake_up returns briefing", text != "" and not response.get("result", {}).get("isError", False),
          text[:90].replace("\n", " "))

    _, _, body = post(base, {"jsonrpc": "2.0", "id": 4, "method": "tools/call",
                             "params": {"name": "save_memory",
                                        "arguments": {"room": "smoke", "hall": "fact",
                                                      "key": "install-smoke",
                                                      "value": "Levara personal install smoke passed"}}}, sid)
    response = parse(body)
    check("save_memory ok without embedder",
          not response.get("result", {}).get("isError", False), content_text(response)[:90])

    _, _, body = post(base, {"jsonrpc": "2.0", "id": 5, "method": "tools/call",
                             "params": {"name": "recall_memory",
                                        "arguments": {"query": "install smoke"}}}, sid)
    response = parse(body)
    text = content_text(response)
    check("recall_memory finds record without embedder", "smoke" in text.lower(),
          text[:90].replace("\n", " "))

    elapsed = time.time() - t0
    passed = sum(1 for _, ok in checks if ok)
    print(f"mcp-handshake elapsed: {elapsed:.2f}s; {passed}/{len(checks)} checks passed")
    sys.exit(0 if passed == len(checks) else 1)


if __name__ == "__main__":
    main()
