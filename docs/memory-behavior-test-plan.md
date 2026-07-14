# Memory behavior optimization test plan

This plan verifies the AUTOMEM-style memory behavior functionality end to end:
trajectory read-model, behavior metrics, WebUI dashboards, meta-review runs,
scaffold proposals, save guardrails, golden eval, and curated trace export.

## Required local commands

Run before merge:

```bash
go test ./pkg/audit ./pkg/mcp ./internal/trajectory ./internal/http
go test ./docs
python3 -m py_compile benchmark/memory_behavior_eval/run_memory_behavior_eval.py
python3 benchmark/memory_behavior_eval/run_memory_behavior_eval.py --fake --label ci-fake --output /tmp/memory_behavior_eval.json
cd webui && npm run lint && npm run build && npm run test:e2e
```

For a live local canary:

```bash
python3 benchmark/memory_behavior_eval/run_memory_behavior_eval.py \
  --base-url http://127.0.0.1:8081 \
  --label local-canary \
  --output /tmp/memory_behavior_eval_local.json
```

## Backend unit coverage

### Agent trajectory builder

Standard cases:

- group by `trace_id`;
- group by `session_id` when `trace_id` is absent;
- fallback grouping by `client_name + collection + 30min window`;
- ordered events oldest-first inside one trajectory;
- returned trajectories newest-first;
- duration in milliseconds;
- counters for search/recall/save/zero/error/request/response bytes.

Edge cases:

- empty event set returns `[]`, not null;
- trace ID wins over session ID;
- mixed clients/collections do not merge in fallback mode;
- events exactly across 30-minute fallback buckets split;
- invalid/empty timestamps do not panic;
- list endpoint omits events even when detail endpoint includes them.

### Memory behavior analyzer

Standard cases:

- recall/search/list/wake_up before save counts as consult-before-write;
- save before consult counts as blind write;
- duplicate save key counts as repeat;
- zero-result recall/search counts as empty recall;
- invalid hall save errors increment `unknown_hall_error_count`;
- request/response bytes produce context bytes per trajectory.

Edge cases:

- empty input returns stable zero shape with non-nil maps/slices;
- audit flags `blind_save`/`repeat_save` work even when raw args are hidden;
- missing room/hall increments only aggregate count and does not expose values;
- unknown tools are ignored for memory-op counters;
- error counters are grouped by tool.

### MCP audit read-model

Standard cases:

- `blind_save` and `repeat_save` persist to `mcp_audit_events`;
- existing JSONL imports remain idempotent;
- new columns are selected by list/detail APIs.

Edge cases:

- old DB table without `blind_save`/`repeat_save` auto-migrates;
- malformed/oversized args are not required for behavior metrics;
- `include_args=false` never returns raw args;
- `include_args=true` requires admin and returns sanitized args only.

## REST API coverage

### `/api/v1/agent-trajectories`

Standard cases:

- `hours=1|24|168|720`;
- `limit`, `offset`;
- filters: `collection`, `client`, `tool`;
- list returns summaries only;
- detail returns ordered sanitized events.

Edge cases:

- missing audit read-model returns 503;
- non-admin `include_args=true` returns 403;
- admin `include_args=true` returns sanitized args;
- out-of-range `limit` falls back to bounded default;
- unknown ID returns 404.

### `/api/v1/memory-behavior`

Standard cases:

- aggregate rates for fixed synthetic trajectories;
- collection/client filters;
- empty selection returns zeros and stable JSON shape.

Edge cases:

- missing audit read-model returns 503;
- old MCP analytics endpoints remain compatible;
- aggregates never expose query text, response content or memory values.

### `/api/v1/memory-reviews/*`

Standard cases:

- dry-run builds prompt but writes no DB rows;
- successful mock LLM run writes run + findings;
- list/detail return persisted findings;
- completed review creates scaffold proposals.

Edge cases:

- missing DB returns 503;
- missing audit read-model returns 503;
- missing LLM provider/model stores failed run;
- malformed LLM JSON stores failed run;
- unknown category normalizes to `scaffold_recommendation`;
- unknown severity normalizes to `medium`;
- prompt excludes raw args/secrets/query/memory text.

### `/api/v1/memory-scaffold/proposals`

Standard cases:

- list/detail proposals;
- finding-to-proposal mapping;
- duplicate proposal collapse by digest;
- admin approve/reject.

Edge cases:

- non-admin decision returns 403;
- invalid decision status returns 400;
- second decision on terminal proposal returns 400;
- missing proposal returns 404;
- approved proposal still does not auto-edit `AGENTS.md`.

### `/api/v1/memory-traces/export`

Standard cases:

- admin streams JSONL;
- empty export is valid empty body;
- good classifier includes traces with consult → non-zero retrieval → save;
- exported rows contain action labels and counters.

Edge cases:

- non-admin returns 403;
- unsupported quality returns 400;
- repeat saves, errors, blind saves, and zero-only retrieval are excluded;
- export contains no raw args, query text, memory values, tokens, cookies or passwords.

## MCP guardrail coverage

Standard cases:

- `save_memory` after no consult returns `memory_behavior.blind_save=true`;
- `save_memory` after recall/search/list/wake_up has no blind warning;
- repeated key returns `repeat_key=true` and `repeat_save=true`;
- audit entry mirrors guardrail flags.

Edge cases:

- missing session is treated as blind rather than panicking;
- failed save does not attach success metadata;
- old clients ignore added `memory_behavior` field;
- response remains valid JSON text and structured content.

## WebUI coverage

Mocked Playwright specs:

- `/memory-behavior` renders cards, recent trajectories and problem traces;
- `/memory-behavior` preserves `hours`, `collection`, `client` in URL params;
- `/memory-scaffold` lists proposals, opens detail and approves;
- `/memory-scaffold` surfaces admin permission failure.

Manual browser smoke:

1. Start backend and WebUI.
2. Generate MCP calls: `wake_up`, `recall_memory`, `save_memory`, invalid hall.
3. Open `/memory-behavior`; verify cards and problem rows update.
4. Run review dry-run and real review if LLM is configured.
5. Open `/memory-scaffold`; approve/reject one proposal as admin.
6. Confirm no raw args/memory values are visible in normal UI.

## Privacy/security fixtures

Use strings:

- `password=hunter2`
- `Authorization: Bearer secret-token`
- `api_key=sk-test-secret`
- `cookie=session=secret`

Assertions:

- absent from prompt;
- absent from non-admin trajectory detail;
- absent from behavior aggregates;
- absent from WebUI pages;
- absent from trace export;
- sanitized or omitted in admin args view.

## Load/performance tests

Local synthetic load:

```bash
python3 benchmark/mcp_load.py --url http://127.0.0.1:8081 --rps 100 --duration 60
curl -sS 'http://127.0.0.1:8081/api/v1/memory-behavior?hours=1'
```

Acceptance:

- MCP audit overhead p95 does not increase by more than 2 ms versus baseline;
- `/memory-behavior?hours=24` p95 < 500 ms on local production-like DB;
- WebUI page remains responsive with 10k audit events;
- review job failure does not affect MCP calls.

## Canary sequence

1. Local Mac:
   - rebuild and restart launchd;
   - run fake eval;
   - run local eval;
   - open WebUI pages.
2. One remote host:
   - deploy new binary;
   - verify `/version`;
   - run `/memory-behavior` and fake eval;
   - inspect logs for audit/read-model errors.
3. Remaining hosts:
   - deploy after first remote is stable;
   - compare behavior metrics and MCP error/zero-result rates.

## Final DoD

- All required commands pass.
- Standard and edge-case Go tests pass.
- Mocked WebUI e2e specs pass.
- Local canary creates trajectories and behavior metrics from real MCP calls.
- Review creates findings when LLM is available and fails explicitly when not.
- Proposals require human decision and never auto-apply.
- Guardrail warnings appear without breaking `save_memory`.
- Exported traces are valid JSONL and sanitized.
- No raw secret fixture appears in API/WebUI/export/review prompt.
