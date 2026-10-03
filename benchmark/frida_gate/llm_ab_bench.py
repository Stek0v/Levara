#!/usr/bin/env python3
"""A/B the consolidation summarizer models against the REAL prod clusters,
replicating pkg/consolidate coverage-guard semantics in Python:
  - numbers (\\d+): every source number must appear in the output (all-or-nothing);
    every output number must exist in the sources (no invention);
  - entities (capitalized tokens minus stopwords/digit/camel rules): at most
    10% of source entities may be omitted;
  - empty output -> fail.
"""
import json
import os
import re
import sys
import time
import urllib.parse
import urllib.request

BASE = "http://127.0.0.1:11434"
PROMPT_HEAD = ("Combine the following memory notes into ONE concise statement. "
               "Preserve every fact, number, name, and port exactly. "
               "Do NOT add any information not present below. Notes:\n")
MODELS = ["gemma4:e2b", "glm4:9b", "ornith-1.5:9b"]  # override: argv model names

CLUSTERS = {
    "ub-main-1": ["6675cfb3-30e6-4e63-acd9-a1bc44ed7ddd", "80cf736d-4066-48d3-a4d5-03e8baf68b3b"],
    "ub-main-2": ["1980ae5c-0dbb-4f5c-b473-70753dc43b7c", "95174cba-fde3-49f5-99eb-481be976ff5d"],
    "unreal-1": ["1a50089e-805a-44f8-8000-59aa207f13af", "4a5eaacb-d14a-4c8d-a8d2-cf665acdc6dc",
                 "52aa6c76-2064-4aae-ade8-45e95a6bb1d4"],
    "unreal-2": ["204bda53-87f0-433c-aca2-ad32add53661", "9181a6fc-6698-4e04-b5e9-718504fea571"],
    "unreal-3": ["56d984b0-c7fa-4613-bb6b-1fbfae41c1d8", "eab47ff2-21d1-4133-bb6b-1fbfae41c1d8"],
    "unreal-4": ["0d6a6242-2ec5-448a-8328-6587121fdd79", "20b0c74b-e687-487b-aa48-4f80258a9b45"],
    "labirint-1": ["8559384b-321c-47df-91e6-632130cff57b", "c593fd69-4669-4e9d-939f-39cc97ae272f",
                   "cb2fc320-fa03-4a85-ab21-37e56be6f9d3"],
    "labirint-2": ["715d124e-4701-4c5f-88e4-538816fa8164", "7afe173a-99d4-45ab-8524-783719360367"],
}

STOP = set("""the this that these those there then their them they when where while what which who whom why how
and but nor not for from into onto over under after before with been being have has had does did can could may
might must shall should will would its all any each both more most other some such only own same than too very
just now new also real use used using add added set get got run runs note see here yes are was were repl null
nil true false void select insert update delete create drop alter table join group order limit return func
const let var todo fixme""".split())

numberRe = re.compile(r"\d+")
entityRe = re.compile(r"\b[A-Z][A-Za-z0-9]+\b")


def is_entity_token(tok):
    if any(c.isdigit() for c in tok):
        return True
    if tok[1:] != tok[1:].lower() and tok.upper() != tok:  # inner upper, not ALLCAPS
        return True
    return tok.lower() not in STOP


def load_sources():
    out = {}
    cur_id, cur_val = None, []
    for line in open("data/cluster_sources.tsv", encoding="utf-8"):
        line = line.rstrip("\n")
        m = re.match(r"^([0-9a-f]{8}-[0-9a-f-]{27,36})\x01?(.*)$", line)
        if m and len(m.group(1)) == 36:
            if cur_id:
                out[cur_id] = "\n".join(cur_val).strip()
            cur_id, cur_val = m.group(1), [m.group(2)]
        elif cur_id is not None:
            cur_val.append(line)
    if cur_id:
        out[cur_id] = "\n".join(cur_val).strip()
    return out


def chat(model, prompt, max_tokens, timeout=300):
    if os.environ.get("THINK") == "0":
        # native /api/chat with thinking disabled — models the wireable prod
        # fix path (request-level option, no nothink tag exists for granite4.2)
        body = json.dumps({"model": model,
                           "messages": [{"role": "user", "content": prompt}],
                           "stream": False, "think": False,
                           "options": {"temperature": 0, "num_predict": max_tokens}}).encode()
        req = urllib.request.Request(BASE + "/api/chat", data=body,
                                     headers={"Content-Type": "application/json"})
        t0 = time.perf_counter()
        with urllib.request.urlopen(req, timeout=timeout) as r:
            d = json.loads(r.read())
        ms = (time.perf_counter() - t0) * 1000
        msg = d.get("message", {})
        return (msg.get("content") or "").strip(), ms, msg.get("thinking")
    body = json.dumps({"model": model,
                       "messages": [{"role": "user", "content": prompt}],
                       "temperature": 0, "max_tokens": max_tokens}).encode()
    req = urllib.request.Request(BASE + "/v1/chat/completions", data=body,
                                 headers={"Content-Type": "application/json"})
    t0 = time.perf_counter()
    with urllib.request.urlopen(req, timeout=timeout) as r:
        d = json.loads(r.read())
    ms = (time.perf_counter() - t0) * 1000
    msg = d["choices"][0]["message"]
    content = (msg.get("content") or "").strip()
    return content, ms, msg.get("reasoning")


def unload(model):
    """Explicitly drop the model from RAM — the host is 16 GB and we test
    strictly one model at a time (no dual-model residency)."""
    body = json.dumps({"model": model, "keep_alive": 0}).encode()
    req = urllib.request.Request(BASE + "/api/chat", data=body,
                                 headers={"Content-Type": "application/json"})
    try:
        urllib.request.urlopen(req, timeout=60).read()
    except Exception:
        pass


def ollama_ps(model):
    """Honest residency: /api/ps size/size_vram (ps RSS is meaningless for
    Metal-mmapped weights). GET with query param — POST returns 405."""
    url = f"{BASE}/api/ps?model={urllib.parse.quote(model)}"
    try:
        with urllib.request.urlopen(url, timeout=10) as r:
            for m in json.loads(r.read()).get("models", []):
                if m["name"] == model:
                    return {"size_gb": round(m["size"] / 1e9, 2),
                            "size_vram_gb": round(m.get("size_vram", 0) / 1e9, 2)}
    except Exception:
        pass
    return None


def ollama_rss_mb():
    import subprocess
    out = subprocess.run(["ps", "-o", "rss=", "-o", "comm=", "-p",
                          ",".join(map(str, ollama_pids()))],
                         capture_output=True, text=True).stdout
    total = sum(int(l.split()[0]) for l in out.splitlines() if l.strip())
    return int(total / 1024)


def ollama_pids():
    import subprocess
    out = subprocess.run(["pgrep", "-f", "ollama( serve)?$|ollama serve"],
                         capture_output=True, text=True).stdout
    return [int(p) for p in out.split()]


def rss_sampler(stop, bucket):
    import threading
    def run():
        while not stop.is_set():
            bucket.append(ollama_rss_mb())
            time.sleep(1.0)
    th = threading.Thread(target=run)
    th.start()
    return th


def guard(sources, out):
    joined = "\n".join(sources)
    if not out:
        return False, "empty content"
    src_nums = set(numberRe.findall(joined))
    out_nums = set(numberRe.findall(out))
    dropped = sorted(n for n in src_nums if n not in out_nums)
    if dropped:
        return False, f"dropped numbers: {dropped[:6]}"
    invented = sorted(n for n in out_nums if n not in src_nums)
    if invented:
        return False, f"invented numbers: {invented[:6]}"
    src_ents = {t for t in entityRe.findall(joined) if is_entity_token(t)}
    out_ents = {t for t in entityRe.findall(out) if is_entity_token(t)}
    missing = src_ents - out_ents
    if src_ents and len(missing) / len(src_ents) > 0.10:
        return False, f"entities dropped {len(missing)}/{len(src_ents)}: {sorted(missing)[:6]}"
    return True, "ok"


def main():
    models = sys.argv[1:] or MODELS
    src = load_sources()
    print(f"loaded {len(src)} source records", flush=True)
    results = {}
    for model in models:
        import threading
        stop = threading.Event()
        bucket = []
        th = rss_sampler(stop, bucket)
        rows = []
        for name, ids in CLUSTERS.items():
            notes = [src[i] for i in ids if i in src and src[i]]
            if len(notes) < 2:
                rows.append({"cluster": name, "verdict": "SKIP", "why": "sources missing"})
                continue
            prompt = PROMPT_HEAD + "".join(f"- {n}\n" for n in notes)
            max_tokens = max(512, min(4096, sum(len(n) for n in notes) // 3))
            try:
                out, ms, _reason = chat(model, prompt, max_tokens)
            except Exception as e:
                rows.append({"cluster": name, "verdict": "ERROR", "why": str(e)[:120], "ms": None})
                continue
            ok, why = guard(notes, out)
            rows.append({"cluster": name, "verdict": "PASS" if ok else "FAIL", "why": why,
                         "ms": round(ms), "out": out[:140]})
            print(f"  [{model}] {name}: {'PASS' if ok else 'FAIL'} ({why}) {ms:.0f}ms", flush=True)
        ps = ollama_ps(model)  # honest residency, must be sampled BEFORE unload
        unload(model)  # sequential testing: no two models in RAM at once
        stop.set(); th.join()
        time.sleep(3)
        passed = sum(1 for r in rows if r["verdict"] == "PASS")
        results[model] = {"rows": rows, "passed": passed, "total": len(rows),
                          "ps": ps,
                          "rss_peak_mb": max(bucket) if bucket else None,
                          "rss_after_unload_mb": ollama_rss_mb()}
        print(f"== {model}: {passed}/{len(rows)} passed, rss peak {results[model]['rss_peak_mb']}MB "
              f"-> after unload {results[model]['rss_after_unload_mb']}MB", flush=True)
    json.dump(results, open("data/llm_ab_results.json", "w"), ensure_ascii=False, indent=1)
    print("saved data/llm_ab_results.json")


if __name__ == "__main__":
    main()
