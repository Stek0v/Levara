# Memory behavior optimization

Levara records MCP audit events and builds a higher-level view of how agents
use memory. The goal is not to auto-edit agent instructions. The v1 workflow is:

1. Observe trajectories.
2. Measure behavior metrics.
3. Run a human-readable meta-review.
4. Create scaffold proposals.
5. Approve or reject proposals explicitly.
6. Re-run the golden eval before rollout.

For the full standard/edge/load/canary test matrix, see
`docs/memory-behavior-test-plan.md`.

## Surfaces

| Surface | Use |
|---|---|
| `/api/v1/agent-trajectories` | Recent grouped MCP trajectories from `mcp_audit_events` |
| `/api/v1/agent-trajectories/:id` | Sanitized ordered events for one trajectory |
| `/api/v1/memory-behavior` | Aggregate discipline metrics |
| WebUI `/memory-behavior` | Operator dashboard for rates, problem traces, zero-result-heavy calls |
| `/api/v1/memory-reviews/run` | Meta-review selected trajectories with configured LLM provider |
| `/api/v1/memory-reviews` | Review run list |
| `/api/v1/memory-scaffold/proposals` | Human-gated scaffold/policy proposals |
| WebUI `/memory-scaffold` | Proposal review and approve/reject controls |
| `/api/v1/memory-traces/export` | Admin-only JSONL export of good sanitized traces |
| `benchmark/memory_behavior_eval/run_memory_behavior_eval.py` | Repeatable golden behavior eval |

## Metrics

| Metric | Meaning |
|---|---|
| `recall_before_save_rate` | Share of `save_memory` calls preceded by recall/search/list/wake_up in the same trajectory |
| `repeat_save_rate` | Share of saves that repeat an already-seen key in the selected window or carry `repeat_save=true` |
| `zero_result_rate` | Share of MCP events that returned zero results |
| `empty_recall_rate` | Share of recall/search consults with zero results |
| `memory_ops_per_trajectory` | Average memory/search operations per trajectory |
| `context_bytes_per_trajectory` | Request + response bytes per trajectory |
| `save_without_room_or_hall_count` | Saves missing routing metadata |
| `unknown_hall_error_count` | Invalid hall validation failures |

Recommended v1 thresholds:

- `repeat_save_rate > 10%` → investigate duplicate-write policy.
- `recall_before_save_rate < 60%` → improve AGENTS/scaffold consult-before-write wording.
- `zero_result_rate > 20%` → inspect retrieval/indexing quality.
- `context_bytes_per_trajectory` week-over-week growth > 30% → inspect noisy memory/wake_up pins.

## Running a review

Review requires a configured LLM provider and `LLM_MODEL`.

```bash
curl -sS http://127.0.0.1:8081/api/v1/memory-reviews/run \
  -H 'Content-Type: application/json' \
  -d '{"hours":24,"collection":"levara","client":"codex","limit":25}'
```

Dry-run builds the sanitized prompt but does not persist findings:

```bash
curl -sS http://127.0.0.1:8081/api/v1/memory-reviews/run \
  -H 'Content-Type: application/json' \
  -d '{"hours":24,"collection":"levara","limit":10,"dry_run":true}'
```

The prompt intentionally omits raw args, query text and memory values. Findings
classify patterns such as missed recall, blind save, duplicate save, wrong
room/hall, noisy memory, useful memory pattern, and scaffold recommendation.

## Scaffold proposals

Successful review runs create `open` proposals. v1 does not apply them.

Decision endpoint:

```bash
curl -sS -X POST \
  http://127.0.0.1:8081/api/v1/memory-scaffold/proposals/<id>/decision \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer <admin-token>' \
  -d '{"status":"approved","note":"accepted for next AGENTS.md edit"}'
```

Valid decisions are `approved` and `rejected`. Only `open` proposals can be
decided. Approved means a human accepted the recommendation; it still does not
modify `AGENTS.md` automatically.

## Guardrails

`save_memory` remains backward compatible. The write succeeds as before, but
successful responses may include:

```json
{
  "memory_behavior": {
    "blind_save": true,
    "repeat_save": false,
    "warning": "save_memory was called before recall/search/list_memories in this MCP session",
    "recommendation": "Call recall_memory, search, or list_memories before saving to avoid duplicate or noisy memories."
  }
}
```

The same derived flags are stored in the MCP audit read-model as
`blind_save`/`repeat_save`.

## Golden eval

CI-safe deterministic mode:

```bash
python3 benchmark/memory_behavior_eval/run_memory_behavior_eval.py \
  --fake \
  --label ci-fake
```

Local canary mode against a running backend:

```bash
python3 benchmark/memory_behavior_eval/run_memory_behavior_eval.py \
  --base-url http://127.0.0.1:8081 \
  --label local-mac
```

Output is written under `benchmark/results/` unless `--output` is provided.
Use this before and after scaffold/policy changes.

## Curated trace export

Admin-only export of good examples:

```bash
curl -sS \
  -H 'Authorization: Bearer <admin-token>' \
  'http://127.0.0.1:8081/api/v1/memory-traces/export?hours=720&collection=levara&quality=good' \
  > memory-traces.jsonl
```

Good v1 traces have no errors, no repeat saves, at least one non-zero retrieval,
and save only after a consult operation. The export is sanitized and excludes
raw args/text by default.

## Manual smoke checklist

1. Make 5–10 MCP calls: `set_context`, `wake_up`, `recall_memory`, `save_memory`,
   one intentional invalid `hall`.
2. Open WebUI `/memory-behavior`; confirm metrics and problem trajectories.
3. Run a dry-review; confirm prompt has no raw args/secrets.
4. Run a real review if LLM is configured; confirm findings and proposals.
5. Approve/reject one proposal as admin.
6. Run fake golden eval.
7. Export traces as admin and grep for `token`, `password`, `cookie`, `authorization`.

## Rollout / rollback

- Roll out after MCP audit read-model is healthy; this feature depends on it.
- If review LLM is unavailable, reviews fail explicitly while metrics remain available.
- Disable the WebUI workflow operationally by not running review/proposal decisions.
- Guardrail warnings do not block writes and can be ignored by older clients.
- Trace export is admin-only; revoke endpoint access by disabling admin auth/token.
