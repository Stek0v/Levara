#!/usr/bin/env python3
"""T9 gate: A+ (core, 13 tools) vs B (memory, 23 tools) agent routing.

For each toolset the harness advertises that server's real tools/list to a
live LLM, gives it a task, and scores which tool the model chooses
(right / wrong / no-call), executes recall calls to measure zero-results,
and records input-token cost. Protocol: anthropic (Z.ai coding plan) or
openai (Ollama /v1). Exit code 0 always — the JSON verdict is the product.
"""
import argparse
import json
import urllib.request

# task, expected tool per toolset ("none" = must answer without a tool),
# optional mcp execution probe (tool + args) whose empty result counts as a
# zero-result for the recall-quality metric.
SCENARIOS = [
    {"id": "save_fact", "task": "Сохрани факт: этот проект использует PostgreSQL для общего состояния. room=stack, hall=fact, key=stack-postgres.", "core": "save_memory", "memory": "save_memory"},
    {"id": "save_decision", "task": "Зафиксируй решение: переходим на поэтапную миграцию. room=migration, hall=decision, key=migration-plan.", "core": "save_memory", "memory": "save_memory"},
    {"id": "recall_auth", "task": "Вспомни, какие решения мы принимали по аутентификации.", "core": "recall_memory", "memory": "recall_memory", "exec": {"tool": "recall_memory", "args": {"query": "аутентификация решения"}}},
    {"id": "wake_up", "task": "Дай стартовую сводку по проекту.", "core": "wake_up", "memory": "wake_up"},
    {"id": "pin", "task": "Закрепи запись с ключом stack-postgres в приоритете брифинга.", "core": "pin_memory", "memory": "pin_memory"},
    {"id": "supersede", "task": "Запись stack-postgres устарела — замени её: теперь используем CockroachDB. Сохрани связь со старой записью.", "core": "supersede_memory", "memory": "supersede_memory"},
    {"id": "delete", "task": "Удали из памяти запись с ключом migration-plan — она была ошибочной.", "core": "delete_memory", "memory": "delete_memory"},
    {"id": "list", "task": "Покажи все активные записи памяти этого проекта.", "core": "list_memories", "memory": "list_memories"},
    {"id": "doctor", "task": "Проверь здоровье системы и индексов.", "core": "doctor", "memory": "doctor"},
    {"id": "recall_deploy", "task": "Найди в памяти всё по теме развертывания.", "core": "recall_memory", "memory": "recall_memory", "exec": {"tool": "recall_memory", "args": {"query": "развертывание"}}},
    {"id": "dist_consolidate", "task": "Сожми дубликаты в памяти проекта — записей стало слишком много.", "core": "none", "memory": "consolidate"},
    {"id": "dist_diary", "task": "Запиши в дневник ревьюера заметку: ревизия схемы согласована.", "core": "none", "memory": "diary_write"},
    {"id": "set_context", "task": "Выбери контекст проекта levara для этой сессии.", "core": "set_context", "memory": "set_context"},
    {"id": "unpin", "task": "Убери запись key=stack-postgres из закреплённых.", "core": "unpin_memory", "memory": "unpin_memory"},
]

SYSTEM = (
    "Ты — ассистент проекта Levara с доступом к памяти проекта. На каждый "
    "запрос пользователя выбери ОДИН подходящий инструмент и вызови его с "
    "минимально достаточными аргументами. Если подходящего инструмента нет — "
    "кратко ответь текстом без вызова."
)


def http_json(url, payload, headers):
    body, _ = http_raw(url, payload, headers)
    return body


def http_raw(url, payload, headers):
    req = urllib.request.Request(url, data=json.dumps(payload).encode(), method="POST")
    req.add_header("Content-Type", "application/json")
    req.add_header("Accept", "application/json, text/event-stream")
    for k, v in headers.items():
        req.add_header(k, v)
    import time as _time
    last = None
    raw = ""
    for attempt in range(4):
        try:
            with urllib.request.urlopen(req, timeout=180) as resp:
                raw = resp.read().decode()
            break
        except urllib.error.HTTPError as e:
            last = e
            if e.code in (429, 500, 502, 503, 504) and attempt < 3:
                _time.sleep(3 * (attempt + 1))
                continue
            raise
        except (TimeoutError, OSError) as e:
            last = e
            if attempt < 3:
                _time.sleep(3 * (attempt + 1))
                continue
            raise
    else:
        raise last
    if not raw.strip():
        # JSON-RPC notifications answer 202 with an empty body.
        return {}, {}
    s = raw.strip()
    if s.startswith("{"):
        return json.loads(s), {}
    data = [line[5:].strip() for line in s.splitlines() if line.startswith("data:")]
    return (json.loads(data[-1]) if data else {}), {}


def mcp_rpc(base, session, payload):
    headers = {"Mcp-Session-Id": session} if session else {}
    body, _ = http_raw(base.rstrip("/") + "/mcp", payload, headers)
    return body


def mcp_call(base, session, name, args):
    return mcp_rpc(base, session, {"jsonrpc": "2.0", "id": 1, "method": "tools/call",
                                   "params": {"name": name, "arguments": args}})


def llm_call(cfg, tools, task):
    if cfg["protocol"] == "anthropic":
        payload = {
            "model": cfg["model"], "max_tokens": 512, "temperature": 0,
            "system": SYSTEM,
            "messages": [{"role": "user", "content": task}],
            "tools": [{"name": t["name"], "description": t.get("description", ""),
                       "input_schema": t.get("input_schema", {"type": "object"})} for t in tools],
        }
        r = http_json(cfg["base"].rstrip("/") + "/v1/messages", payload,
                      {"x-api-key": cfg["key"], "anthropic-version": "2023-06-01"})
        call = next((b for b in r.get("content", []) if b.get("type") == "tool_use"), None)
        return (call["name"] if call else None), r.get("usage", {}).get("input_tokens", 0)
    payload = {
        "model": cfg["model"], "temperature": 0,
        "messages": [{"role": "system", "content": SYSTEM}, {"role": "user", "content": task}],
        "tools": [{"type": "function", "function": {"name": t["name"],
                    "description": t.get("description", ""),
                    "parameters": t.get("input_schema", {"type": "object"})}} for t in tools],
    }
    r = http_json(cfg["base"].rstrip("/") + "/v1/chat/completions", payload,
                  {"Authorization": "Bearer " + cfg["key"]} if cfg["key"] else {})
    msg = r["choices"][0]["message"]
    call = (msg.get("tool_calls") or [{}])[0]
    name = call.get("function", {}).get("name")
    return name, r.get("usage", {}).get("prompt_tokens", 0)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--url-a", required=True, help="MCP endpoint advertising core (A+)")
    ap.add_argument("--url-b", required=True, help="MCP endpoint advertising memory (B)")
    ap.add_argument("--llm-base-url", required=True)
    ap.add_argument("--llm-key-env", default="LLM_GATE_KEY")
    ap.add_argument("--model", required=True)
    ap.add_argument("--protocol", choices=["anthropic", "openai"], default="anthropic")
    ap.add_argument("--repeats", type=int, default=3)
    args = ap.parse_args()

    key = __import__("os").environ.get(args.llm_key_env, "")
    cfg = {"base": args.llm_base_url, "key": key, "model": args.model, "protocol": args.protocol}

    def open_session(url):
        _, headers = http_raw(url.rstrip("/") + "/mcp", {
            "jsonrpc": "2.0", "id": 0, "method": "initialize",
            "params": {"protocolVersion": "2025-03-26", "capabilities": {},
                       "clientInfo": {"name": "aplus-vs-b-gate", "version": "1.0"}},
        }, {})
        sid = headers.get("Mcp-Session-Id")
        mcp_rpc(url, sid, {"jsonrpc": "2.0", "method": "notifications/initialized"})
        return sid

    def tools_for(url):
        sid = open_session(url)
        body = mcp_rpc(url, sid, {"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
        return body["result"]["tools"], sid

    results = {}
    for label, url in (("A+ core", args.url_a), ("B memory", args.url_b)):
        tools, session = tools_for(url)
        stats = {"right": 0, "wrong": 0, "none_ok": 0, "zero_result": 0, "recall_exec": 0,
                 "input_tokens": 0, "calls": 0, "per_scenario": {}}
        for sc in SCENARIOS:
            expected = sc["core" if "core" in label else "memory"]
            calls_seen = []
            for _ in range(args.repeats):
                name, in_tokens = llm_call(cfg, tools, sc["task"])
                calls_seen.append(name)
                stats["calls"] += 1
                stats["input_tokens"] += in_tokens
                if expected == "none":
                    ok = name is None
                    stats["none_ok" if ok else "wrong"] += 1
                elif name == expected:
                    stats["right"] += 1
                else:
                    stats["wrong"] += 1
                if "exec" in sc and name == sc["exec"]["tool"]:
                    r = mcp_call(url, session, sc["exec"]["tool"], sc["exec"]["args"])
                    text = json.dumps(r.get("result", {}))
                    stats["recall_exec"] += 1
                    if '"results": []' in text or '"results":null' in text or '"results": []' in text.replace(" ", ""):
                        stats["zero_result"] += 1
            stats["per_scenario"][sc["id"]] = {"expect": expected, "called": calls_seen}
        n = stats["calls"]
        stats["right_pct"] = round(100 * stats["right"] / n, 1)
        stats["wrong_pct"] = round(100 * stats["wrong"] / n, 1)
        stats["avg_input_tokens"] = round(stats["input_tokens"] / n)
        results[label] = stats

    print(json.dumps({
        "model": args.model, "protocol": args.protocol, "repeats": args.repeats,
        "A+_core_13": {k: results["A+ core"][k] for k in
                       ("calls", "right_pct", "wrong_pct", "none_ok", "avg_input_tokens", "zero_result", "recall_exec")},
        "B_memory_23": {k: results["B memory"][k] for k in
                        ("calls", "right_pct", "wrong_pct", "none_ok", "avg_input_tokens", "zero_result", "recall_exec")},
        "per_scenario": {label: results[label]["per_scenario"] for label in results},
    }, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
