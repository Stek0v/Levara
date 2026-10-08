# Long-Horizon Task Runtime

[Русская версия](long-horizon-runtime.ru.md)

Long-Horizon Task Runtime is Levara's opt-in execution ledger for work that must
survive context compaction, process restarts, agent handoffs, or scheduled
continuation. It stores the task objective, authority, Definition of Done,
versioned plan, atomic step leases, immutable evidence, checkpoints, blockers,
and completion state in SQL.

Work is performed by an external agent/host or the optional bounded server
worker described below. The ledger grants no additional authority; it makes
state and proof recoverable and validates completion deterministically.

## Enable it

Task tools require a SQL database, `LEVARA_LONG_HORIZON_RUNTIME=1`, and the
`long-horizon` or `full` toolset. Configure them before starting the server:

```bash
export DB_PROVIDER=sqlite
export DB_PATH=./data/levara.db
export LEVARA_LONG_HORIZON_RUNTIME=1
export LEVARA_MCP_TOOLSET=long-horizon
./levara-server -profile=standalone -host=127.0.0.1 -port=8080 -grpc-port=0
```

The `full` tool profile also includes Task Runtime tools while the flag is on.
Restart the server and reconnect the MCP client after changing either variable
so the client refreshes `tools/list`.

SQLite and PostgreSQL are supported, but Task Runtime cannot operate in the
WAL-only configuration where the SQL database is disabled. Artifact receipts
additionally require a configured local storage/workspace root or
object-storage backend.

Verify the runtime:

- `runtime_stats` must report `task_runtime.enabled=true`;
- `doctor` must report a reachable Task Runtime schema and no orphan records;
- the MCP tool list must contain the eight `task_*` tools below.

## Optional workspace worker

With the SQL configuration above, enable server execution using:

```bash
export LEVARA_LONG_HORIZON_RUNTIME=1
export LEVARA_TASK_WORKER=1
export LEVARA_MCP_TOOLSET=full
```

The server wires [NewTaskExecutor](../internal/http/task_executor.go), which
supports `workspace_read` and `workspace_write` only; writes require `index`
absent or false. Shell, network, indexing, and unknown arguments are rejected.
The `long-horizon` toolset has `task_step` but lacks the workspace action tools;
`workspace` lacks `task_step`. The execution fence requires both, so use explicit
`full` for this worker and inspect actual `tools/list`.

Each executable step needs an `action` (`kind=mcp_tool`, tool name, arguments,
and output assertions) and `criterion_ids`. Task authority must include
`auto_run=true`, `allowed_tools`, and a digest-pinned [authority manifest](authority-manifests.md)
that permits the tool and an existing canonical directory. Execution also
requires the task's authenticated owner, a live lease, and a workspace grant.
DevMode is denied. Descriptor-based filesystem confinement supports Linux and
macOS; other platforms reject these file actions. Work outside this bounded
surface remains the external host's responsibility under its authorization.

## Lifecycle

```mermaid
flowchart LR
  Open["task_open<br/>objective + DoD"] --> Plan["task_plan<br/>steps + dependencies"]
  Plan --> Claim["task_step: claim"]
  Claim --> Work["perform one step"]
  Work --> Evidence["task_receipt"]
  Evidence --> Checkpoint["task_checkpoint"]
  Checkpoint --> Pass["task_step: pass/fail/release"]
  Pass --> Bootstrap["task_bootstrap"]
  Bootstrap --> Claim
  Pass --> Validate["task_validate"]
  Validate --> Complete["task_complete"]
```

| Tool | Responsibility |
|---|---|
| `task_open` | Create or idempotently reopen a task scoped to one collection and room |
| `task_plan` | Save an acyclic step plan before execution starts |
| `task_bootstrap` | Recover bounded task state, next eligible step, blockers and scoped memories |
| `task_step` | Atomically claim, renew, release, pass or fail a step lease |
| `task_receipt` | Append immutable command, artifact, source, observation or reviewer evidence |
| `task_checkpoint` | Save compact recovery state, add/resolve blockers and propose durable memories |
| `task_validate` | Report missing, stale or failed evidence and incomplete runtime state |
| `task_complete` | Complete a valid task and promote accepted memory candidates |

## Minimal workflow

The snippets below are MCP tool arguments, not raw HTTP request bodies. Always
use the latest returned `version` as the next mutation's `base_version` or
`expected_version`.

### 1. Open a scoped task

```json
{
  "collection": "levara",
  "room": "task-runtime",
  "objective": "Publish the Long-Horizon Runtime with verified documentation",
  "idempotency_key": "publish-long-horizon-v1",
  "risk_level": "medium",
  "authority": {
    "may_edit_repository": true,
    "may_publish_branch": true,
    "may_deploy": false
  },
  "definition_of_done": [
    {
      "criterion_id": "tests",
      "description": "Relevant Go tests pass",
      "required": true
    },
    {
      "criterion_id": "docs",
      "description": "English and Russian guides describe the shipped behavior",
      "required": true
    }
  ]
}
```

Keep the returned `task_id`; it is the stable recovery handle. Reusing the same
owner, collection, and `idempotency_key` returns the existing task.

### 2. Plan verifiable steps

```json
{
  "task_id": "<task-id>",
  "base_version": 2,
  "steps": [
    {
      "step_id": "implement",
      "description": "Implement and test the runtime",
      "criterion_ids": ["tests"]
    },
    {
      "step_id": "document",
      "description": "Document setup, lifecycle and limitations",
      "dependencies": ["implement"],
      "criterion_ids": ["docs"]
    }
  ]
}
```

Dependencies must reference steps in the same task and form an acyclic graph.
The plan cannot be replaced after any step has started.

### 3. Claim one step

Call `task_bootstrap` immediately before claiming work, then claim the returned
eligible step:

```json
{
  "task_id": "<task-id>",
  "step_id": "implement",
  "action": "claim",
  "base_version": 3,
  "actor_id": "agent:implementer",
  "lease_seconds": 900
}
```

Leases are clamped to 30–3600 seconds. Only the lease owner can renew, release,
pass, or fail the step. An expired lease can be reclaimed by another actor.

### 4. Attach observed evidence

```json
{
  "task_id": "<task-id>",
  "base_version": 4,
  "idempotency_key": "go-test-pkg-mcp-1",
  "receipt_type": "command",
  "status": "pass",
  "criterion_ids": ["tests"],
  "observation": "go test ./pkg/mcp completed successfully",
  "exit_code": 0,
  "workspace_revision": "<actual-workspace-revision>"
}
```

Reusing a receipt or checkpoint idempotency key requires the same normalized request, including evidence, verified owner, effective actor and checkpoint side effects. An exact retry may use a stale base version and returns the original ID and current version. A changed payload returns an explicit idempotency conflict without changing the ledger. Object key order and ignored fields do not alter identity; applied array order does.

Historical receipts/checkpoints without a stored request digest remain readable and usable as evidence, but replay returns an explicit unverifiable-payload error. The additive migration does not guess an original request or repeat checkpoint effects. Use a new key only for an intentionally new operation.

Receipts are immutable and idempotent. Record what was actually observed; a
summary or intention is not evidence. `command`, `artifact`, and `reviewer`
receipts require a workspace revision. Evidence tied to an older revision is
reported as stale after the task revision changes.

Artifact receipts require `evidence_uri` and a full SHA-256 digest. Supported
URIs are `file://` paths inside configured storage/workspace roots and
`storage://` objects from the configured backend. Completion re-reads the
artifact bytes and verifies the digest.

Completion validates authoritative SQL inside its bounded write transaction. For workspace artifacts it retains the native project lock through commit or rollback, so cooperating workspace writers cannot replace verified bytes in that interval. Cancellation and credential expiry release guards without completing the task. Completed-task replay still requires live write authority. Arbitrary OS editors and object-backend overwrites do not participate in the workspace lock; no filesystem/SQL power-loss atomicity is claimed.

### 5. Checkpoint, block, and resume

Use checkpoints for compact verified recovery state:

```json
{
  "task_id": "<task-id>",
  "base_version": 5,
  "idempotency_key": "checkpoint-after-tests",
  "step_id": "implement",
  "summary": "Implementation and focused tests are complete",
  "verified": ["tests"],
  "next_action": "Update operator documentation",
  "workspace_revision": "<actual-workspace-revision>"
}
```

When external authority or a decision is required, include a blocker. After the
condition is satisfied, create a new checkpoint with the exact active IDs from
`task_bootstrap`:

```json
{
  "task_id": "<task-id>",
  "base_version": 6,
  "idempotency_key": "publication-approved",
  "summary": "Repository owner approved branch publication",
  "resolved_blocker_ids": ["<blocker-id>"]
}
```

Resolving a blocker preserves its history and records `resolved_at`; it does not
expand the task's stored authority.

### 6. Validate and complete

After all required steps are passed and leases are released, call
`task_validate` with `mode=completion`. Completion is rejected while any of the
following remain:

- a required criterion has no passing receipt;
- evidence is stale or failed;
- a required step is incomplete;
- a blocker or live lease is active;
- a high-risk task lacks a current passing `reviewer` receipt.

Call `task_complete` only with the version returned by the latest bootstrap or
validation. Accepted memory candidates are then saved with task/receipt
provenance; unsupported or insufficiently evidenced candidates are rejected.

## Recovery and concurrency rules

- Treat `task_id` as the recovery handle; do not reconstruct operational state
  from chat history.
- Call `task_bootstrap` after a restart, handoff, compaction, version conflict,
  or expired lease. Its budget is 100–4000 approximate tokens.
- A mutation increments the optimistic task version. On conflict, bootstrap
  again and decide from current state instead of retrying stale arguments.
- Give every open, receipt, and checkpoint operation a stable idempotency key.
- Use one claimed step at a time per actor. Dependency checks and lease claims
  are atomic.

## Security and current limitations

Task Runtime stores state and evidence in SQL. The read-only WebUI `/tasks`
displays tasks, steps, leases, receipts, checkpoints and blockers. Work is
performed by an external agent/host or the bounded worker; the ledger grants
no additional authority.

- Tool profiles control visibility, not authorization.
- Task access includes owner, collection and room scope.
- Artifact verification checks supported sources and path containment;
  unsupported URI schemes are not automatically trusted.
- The bounded executor records an observation receipt from actual tool output
  and checks the step's output assertions; a passing workspace action is not
  evidence that a shell command or unrelated quality check ran.
  `NewLoggingStepExecutor` is a compatibility fallback that fails closed,
  not the server's execution adapter.
- An [authority manifest](authority-manifests.md) binds a task; its digest is
  verified on HTTP claim and rechecked by the bounded workspace executor.
  The executor enforces its tool and directory grants for supported actions;
  manifest helpers are not a universal sandbox for every production action.

Canonical schemas are in [API contract](api-contract.md). See [testing](testing.md)
for observed evidence and remaining integration limits, and the
[Russian version](long-horizon-runtime.ru.md) for the same workflow.
