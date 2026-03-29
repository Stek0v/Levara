# Bug Report: Cognify Pipeline Silent Failure

## Summary

Cognify MCP tool accepted data and returned "pipeline started" but never produced vector chunks. The pipeline silently failed during LLM entity extraction.

**Impact:** HIGH — cognify is the primary data ingestion path. Without it, no documents can be indexed for search.

**Status:** FIXED (commit 278e52d)

## Root Cause

Two independent issues combined to make cognify silently fail:

### Issue 1: Missing LLMProvider in orchestrator config

**File:** `internal/http/mcp.go:toolCognify()` (line 633)

`toolCognify()` built the `orchestrator.Config` struct without passing `h.cfg.LLMProvider`:

```go
// BEFORE (broken):
pipeCfg := orchestrator.Config{
    LLMEndpoint: os.Getenv("LLM_ENDPOINT"),  // legacy HTTP path
    LLMModel:    os.Getenv("LLM_MODEL"),
    // LLMProvider NOT passed — pipeline falls back to legacy raw HTTP
}

// AFTER (fixed):
pipeCfg := orchestrator.Config{
    LLMEndpoint: os.Getenv("LLM_ENDPOINT"),
    LLMModel:    os.Getenv("LLM_MODEL"),
    LLMProvider: h.cfg.LLMProvider,  // uses multi-provider abstraction
}
```

**Why it matters:** When `LLM_PROVIDER` env var is set, `main.go` creates a `llm.Provider` abstraction (`cfg.LLMProvider`). But `toolCognify` never passed it to the pipeline. The pipeline fell through to the legacy raw HTTP path.

### Issue 2: Qwen3 thinking mode consumes all tokens

**Model:** `qwen3.5:2b` and `qwen3:0.6b` (Qwen3 family)

Qwen3 models have an internal "thinking" mode. When called via OpenAI-compatible API:
- The model generates a `reasoning` field (internal chain-of-thought)
- The `content` field (actual response) gets zero tokens
- With `max_tokens: 200`, all 200 tokens go to reasoning → empty content

**Workaround (documented in memory):**
- Prefix user message with `/no_think`
- Set `num_predict: 3000-5000` (not 200)
- Set `temperature: 0.1` for structured extraction

### Issue 3: CPU timeout with large models

**Environment:** Mac (Apple M2, no GPU for LLM inference)

`qwen3.5:2b` (2.3B params, Q8_0, 4GB) on CPU:
- Simple chat: ~30s
- Structured output (JSON Schema): >600s (HTTP timeout)
- 3 retries × 600s = 30 minutes per chunk → pipeline appears frozen

**Fix:** Changed Mac LLM model to `qwen3:0.6b` (0.6B params, ~400MB, ~7s/call on CPU).

## How the bug manifested

1. User calls `cognify(data="...", collection="test")`
2. MCP tool returns: `"Cognify pipeline started. Run ID: xxx"`
3. Background goroutine starts `orchestrator.Run()`
4. Pipeline chunks the text (works fine)
5. Pipeline calls LLM for entity extraction:
   - **With Provider:** Uses `Provider.ChatCompletion()` — works but Qwen3 returns empty content
   - **Without Provider (legacy):** Uses raw HTTP POST — Qwen3.5 timeouts after 600s
6. All retries fail (3 retries × 600s each)
7. Pipeline logs `[pipeline] structured output failed, fallback to regex`
8. Regex fallback finds no entities in empty LLM response
9. Pipeline writes 0 entities, 0 vectors to collection
10. `cognify_status` shows "COMPLETED" with 0 chunks — **silent success with no data**

## Timeline

- **Problem existed since:** MCP server initial implementation (commit 9ddd864, 2026-03-26)
- **Discovered:** 2026-03-29 during E2E testing
- **Root cause found:** 2026-03-29 by tracing logs `[llm] structured retry 2/3 failed: context deadline exceeded`
- **Fixed:** commit 278e52d (2026-03-29)

## Verification

```bash
# 1. Check cognify produces chunks
curl -s http://localhost:8081/mcp -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize",...}'
# Get session ID, then:
curl -s http://localhost:8081/mcp -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"cognify","arguments":{"data":"Test text","collection":"verify"}}}'
# Wait 30-60s, then check:
curl -s http://localhost:8081/api/v1/collections | grep verify
# Should show record_count > 0

# 2. Check logs for LLM activity
tail -f ~/levara-local/levara.log | grep -i "llm\|pipeline\|cognify\|structured"
```

## Lessons Learned

1. **Always pass all config fields** — missing one field caused silent fallback to broken path
2. **Test cognify E2E, not just MCP tool response** — "pipeline started" ≠ "pipeline succeeded"
3. **Log pipeline completion with chunk count** — "COMPLETED with 0 chunks" should be a WARNING
4. **Qwen3 thinking models need special handling** — `/no_think` prefix + high `num_predict`
5. **CPU inference for LLM structured output is prohibitively slow** — use smallest model possible
