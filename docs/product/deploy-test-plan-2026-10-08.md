# Levara post-deploy test plan — 2026-10-08

Target revision: `47a50e03e448bab0f6b12282ec2a7896ac63f143`

## Current deployment baseline

| Environment | Artifact | Service | Data baseline | Dependency baseline |
|---|---|---|---|---|
| Local Mac arm64 | `dfe75f…efffad` | launchd `com.stek0v.levara`, HTTP `127.0.0.1:8081` | 326 collections | PostgreSQL, embed and LLM connected |
| `base` linux/amd64 | `502a43…136b3` | systemd `levara.service`, HTTP `127.0.0.1:8080`, gRPC `50051` | 114 collections | PostgreSQL, embed and LLM connected; Qwythos context 32768 |
| RPi5 linux/arm64 | `afa4b3…550723` | systemd `levara.service`, HTTP `0.0.0.0:8090` | 330 collections | SQLite and embed connected; external LLM currently unreachable |

## P0 — release acceptance on every deployment

1. **Provenance and process identity**
   - Compare the installed SHA-256 with the artifact baseline.
   - Require `/version.version` to equal the full target revision and `build_time` to be non-empty/non-`unknown`.
   - Verify one intended service-manager PID and listener; make sure eval/sandbox processes use their original ports.
   - DoD: all assertions pass twice, 20 seconds apart, with no PID or restart-counter change.

2. **Readiness and dependencies**
   - Require `/health` and `/metrics` HTTP 200.
   - Require `/health/details`: backend, configured database and embed are `connected`; collection count equals the baseline.
   - Local additionally requires PostgreSQL and LLM connected. Base additionally requires Qwythos, embed and rerank units active and model endpoints reachable. Pi records external LLM degradation separately and must not classify it as database/embed failure.
   - DoD: no service restart, panic, fatal log entry or collection-count drift during a 10-minute observation.

3. **MCP transport and authentication**
   - Use stateless `/mcp/2026-07-28` with exact protocol metadata and matching `Mcp-Method`/`Mcp-Name` headers.
   - Public `server/discover` and `tools/list` must succeed; `set_context` must not be advertised.
   - Protected tool calls without/with invalid credentials must return the configured hidden denial (`404` on auth hosts). A valid admin credential performs one read-only `runtime_stats`/`doctor` call.
   - DoD: response/error shapes match the generated contract; no legacy session ID appears on the stateless endpoint.

4. **Read-only data verification**
   - Select one known collection per host and run deterministic search/recall queries with expected IDs/source evidence.
   - Compare collection and SQL metadata counts before and after.
   - DoD: expected records are returned and no count or metadata changes occur.

## P1 — security, isolation and persistence

5. **Tenant/owner/project isolation matrix**
   - In disposable tenants/projects, create admin, member A and member B credentials.
   - Verify REST owner hints cannot read/write another caller's memory or diary.
   - Verify gRPC and MCP enforce the same active user, tenant membership and superuser boundaries.
   - Verify imported private chats remain private; an administrator can grant project sharing to a colleague, revoke it, and revocation immediately removes REST/MCP/UI access.
   - Verify artifact/document reads require the same project/dataset grant and cannot bypass it through guessed IDs, stale sessions, owner fields or batch APIs.
   - Corner cases: trimmed agent names, empty tenant, historical anonymous diary owner, renamed identity, disabled user, revoked group, cross-tenant project ID, concurrent revoke/read.
   - DoD: every allow/deny cell matches policy; denial is terminal and never replaced by an automatic retry success.

6. **Write/read/reopen durability**
   - Use a deployment-specific disposable collection. Write single and batch records with string, byte, empty and near-limit metadata; verify parity before/after reopen.
   - Exercise delete, supersession, consolidation/revert and memory-index reconciliation with explicit cleanup.
   - Restart only inside an agreed maintenance window and verify acknowledged writes after an interrupted-tail fixture survive a second independent reopen.
   - DoD: no acknowledged write disappears; malformed/null snapshot inventories are rejected without replacing prior state.

7. **Sync Mac ↔ Pi**
   - Use a unique fixture collection and explicit push/pull direction; vectors remain excluded unless deliberately requested.
   - Verify ownership/tenant metadata, idempotent replay, conflict handling and duplicate/supersession semantics.
   - Run once with Pi temporarily unreachable, then resume through `ProxyJump base`.
   - DoD: both sides converge to the expected logical records with no cross-tenant exposure or unrelated collection change.

## P1 — restart and rollback gates

8. **Cold restart measurements**
   - Mac: schedule a 60-minute maintenance window; measure WAL replay progress and time to listener for both large chat collections. Current observed cold start was about 47 minutes, including 962,786 records in `chat-imports` and the larger `chat-imports-v2` replay.
   - Base: require readiness within 5 minutes after model endpoints are healthy; start model dependencies first, verify Qwythos context `32768`, then start Levara. Fail if CUDA OOM or dependency restart causes a PID cascade.
   - Pi: require readiness within 5 minutes; verify SQLite, embed sidecar and service ordering, then confirm temperature below 80°C and `get_throttled=0x0` after restart.
   - DoD: each host returns to its exact version/count baseline inside its measured maintenance budget and remains stable for 10 minutes.

9. **Rollback drill**
   - Restore only the timestamped binary backup created by this deployment, restart the same service manager, and verify old `/version`, health and collection counts.
   - Base additionally removes/archives the `zzzz-levara-stable.conf` drop-in only if rolling back the Qwythos context decision; validate GPU capacity before restoring 65536.
   - Roll forward again with the recorded SHA and repeat P0.
   - DoD: rollback and roll-forward are both reproducible without data restore, count drift or orphan listeners.

## P2 — load, soak and resource limits

10. **Representative load**
    - Run isolated read-heavy and mixed read/write profiles with fixed corpus/query digests; record throughput, error rate, p50/p95/p99, RSS, CPU and goroutines.
    - Base records GPU memory for Levara-adjacent models. Pi records temperature, throttling, swap and storage latency. Mac separates startup replay from steady-state latency.
    - Numeric gates: warm heartbeat baseline `500 RPS` for `60s`, achieved rate `>=98%`, errors `<=0.1%`, p95 `<200ms`; durable-audit run `500 RPS` for `600s` with the same rate/error gates and p95 overhead `<=2ms`; mixed heartbeat/recall/save for `120s` with each foreground p95 `<200ms` and errors `<=0.1%`; healthy backlog drain `<=300s`; owned server peak RSS `<=1GiB`.
    - DoD: zero correctness failures; all numeric gates hold without OOM, restart or unbounded memory growth. Preserve failed measurements instead of lowering a gate after execution.

11. **Soak and failure injection**
    - Four-hour steady read soak, then controlled embed/LLM unavailability, PostgreSQL restart on Mac/base, Pi network interruption and sync retry.
    - Health details must distinguish optional-provider degradation from database/embed failure; recovery must not require manual data repair.
    - DoD: no data loss, restart loop, authorization weakening or stale private data after provider recovery.

## Execution order

Run P0 immediately and after every binary/config change. Run P1 isolation tests against disposable tenants and collections before enabling wider access. Schedule the Mac cold restart and rollback drill in a maintenance window because the measured startup path is roughly 47 minutes. Run P2 only after P0/P1 pass and retain the exact artifact SHA, fixture digest and environment snapshot with every result.
