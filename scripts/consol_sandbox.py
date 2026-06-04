#!/usr/bin/env python3
"""Phase-A sandbox driver for memory consolidation on the Pi.

Drives the MCP HTTP endpoint (localhost:8090/mcp) with a persistent session:
seeds known clusters, then runs dry_run / apply / verify / revert / idempotency
on demand. Stage selected by argv[1].
"""
import json, os, subprocess, sys, time, traceback, urllib.request

BASE = "http://localhost:8090/mcp"
COLL = os.environ.get("COLL", "consol_sandbox")
HDR = {"Content-Type": "application/json",
       "Accept": "application/json, text/event-stream"}


def _post(body, sid=None, read_body=True):
    h = dict(HDR)
    if sid:
        h["Mcp-Session-Id"] = sid
    req = urllib.request.Request(BASE, data=json.dumps(body).encode(), headers=h)
    resp = urllib.request.urlopen(req, timeout=120)
    sid_out = resp.headers.get("Mcp-Session-Id")
    if not read_body:
        resp.close()
        return None, sid_out
    # Stream line-by-line; the server sends one SSE `data:` frame then closes
    # the response for this JSON-RPC id. Reading the first frame avoids blocking
    # on the long-lived event stream.
    payload = None
    for line in resp:
        s = line.decode().strip()
        if s.startswith("data:"):
            payload = json.loads(s[5:].strip())
            break
        if s.startswith("{"):
            payload = json.loads(s)
            break
    resp.close()
    return payload, sid_out


def session():
    # urllib blocks on the initialize response on this transport; curl with
    # header-dump returns the session id instantly and reliably.
    init = json.dumps({"jsonrpc": "2.0", "id": 1, "method": "initialize",
                       "params": {"protocolVersion": "2024-11-05", "capabilities": {},
                                  "clientInfo": {"name": "sandbox", "version": "1"}}})
    out = subprocess.run(
        ["curl", "-s", "--max-time", "10", "-D", "-", "-o", "/dev/null", "-X", "POST", BASE,
         "-H", "Content-Type: application/json",
         "-H", "Accept: application/json, text/event-stream", "-d", init],
        capture_output=True, text=True, timeout=15).stdout
    for line in out.splitlines():
        if line.lower().startswith("mcp-session-id:"):
            return line.split(":", 1)[1].strip()
    raise RuntimeError("no session id from initialize:\n" + out)


def call(sid, name, args, _id=10):
    body = json.dumps({"jsonrpc": "2.0", "id": _id, "method": "tools/call",
                       "params": {"name": name, "arguments": args}})
    out = subprocess.run(
        ["curl", "-s", "--max-time", "60", "-X", "POST", BASE,
         "-H", "Content-Type: application/json",
         "-H", "Accept: application/json, text/event-stream",
         "-H", f"Mcp-Session-Id: {sid}", "-d", body],
        capture_output=True, text=True, timeout=65).stdout
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
    # tool results are JSON-in-text; try parse
    try:
        return json.loads(txt)
    except Exception:
        return {"_text": txt}


# Seed: paraphrase clusters with shared numbers/entities for the coverage guard.
SEED = [
    # G1 — tight paraphrases of one fact (numbers: 2.6, entities: Levara, HNSW)
    ("g1a", "Levara HNSW search latency is 2.6 ms mean on the benchmark.", "bench", "fact", False),
    ("g1b", "Levara's HNSW mean search latency measures 2.6 ms in the benchmark.", "bench", "fact", False),
    ("g1c", "On the benchmark, Levara HNSW shows a mean search latency of 2.6 ms.", "bench", "fact", False),
    # G2 — related throughput facts (numbers: 719, entities: Levara, QPS)
    ("g2a", "Levara sustains 719 QPS for concurrent search on a laptop.", "bench", "fact", False),
    ("g2b", "Under concurrent read load Levara reaches 719 QPS on a laptop.", "bench", "fact", False),
    ("g2c", "Levara's concurrent search throughput is about 719 QPS on a laptop.", "bench", "fact", False),
    # Singleton — unrelated, must stay untouched
    ("s1", "The DeepSeek API key for the Pi lives in levara.env with mode 600.", "ops", "fact", False),
    # Pinned paraphrase of G1 — must be EXCLUDED from candidates entirely
    ("p1", "Levara HNSW mean search latency is 2.6 ms on the benchmark run.", "bench", "fact", True),
]


def stage_seed(sid):
    for key, val, room, hall, pin in SEED:
        args = {"collection": COLL, "key": key, "value": val, "room": room, "hall": hall}
        if pin:
            args["pin"] = True
            args["pin_priority"] = 5
        r = call(sid, "save_memory", args)
        ok = "_error" not in r
        print(f"  seed {key:4} pin={pin!s:5} -> {'ok' if ok else r}", flush=True)
    print("seeded; waiting 8s for async HNSW indexing of the _memories sidecar...")
    time.sleep(8)


def stage_dryrun(sid):
    r = call(sid, "consolidate", {"collection": COLL, "dry_run": True})
    print(json.dumps(r, indent=2, ensure_ascii=False))


def stage_apply(sid):
    r = call(sid, "consolidate", {"collection": COLL, "dry_run": False})
    print(json.dumps(r, indent=2, ensure_ascii=False))


def stage_list(sid):
    r = call(sid, "list_memories", {"collection": COLL})
    print(json.dumps(r, indent=2, ensure_ascii=False))


def stage_revert(sid):
    rid = sys.argv[2]
    r = call(sid, "consolidation_revert", {"run_id": rid})
    print(json.dumps(r, indent=2, ensure_ascii=False))


if __name__ == "__main__":
    stage = sys.argv[1] if len(sys.argv) > 1 else "dryrun"
    try:
        sid = session()
        print(f"# session={sid} stage={stage} coll={COLL}", flush=True)
        globals()[f"stage_{stage}"](sid)
        print("# DONE", flush=True)
    except Exception:
        traceback.print_exc()
        print("# FAILED", flush=True)
