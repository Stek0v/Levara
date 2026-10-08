# Accepted index lifecycle evidence

Observed on 2026-10-05. Roadmap T01–T05 and the preceding bounded owner lifecycle are accepted; T06 and this bounded 7/7 change were accepted on 2026-10-06 after the complete DoD. Initial evidence was local because Levara MCP bootstrap was unavailable; later recovery and actual runtime receipts are distinguished below.

## Service preflight

SQLite and the root-owned disposable PostgreSQL 16.15 database `levara_roadmap_test`, user `levara_test`, loopback port 53350, were verified before focused tests. PostgreSQL tests use per-test disposable schemas through `LEVARA_TEST_POSTGRES_DSN`. Vector roots and provider endpoints are temporary/local. No production migration/restart, external paid provider, commit or push.

## Accepted outbox step 1.1

Parent inspected the actual outbox diff, final logs and current source hashes. Native UPSERT reopens only completed identical work with a new opaque publication ID and resets retry budget; pending/running/failed/dead-letter duplicates retain canonical fields. Completed owner/collection mismatch fails without changing the row; current model/dimension can change. Both APIs behave equivalently and caller rollback undoes requeue. Full claim tuple plus namespace guards Finish/Defer against old-ID reuse by a different publication.

- Original RED: `/tmp/levara-memory-index-republication-red.log`; completed remained unclaimable, metadata reply was noncanonical and empty Enqueue ID was retained; 0 SQL skips.
- Additional actual stale-ID RED: `/tmp/levara-memory-index-republication-id-reuse-red.log`; 12 leaf failures across both APIs/dialects after a caller reused the old ID.
- Final full GREEN: `/tmp/levara-memory-index-republication-green.log`, 14 top tests / 72 RUN / 59 leaf, exit 0, 0 skips, 3.162s.
- Final focused race: `/tmp/levara-memory-index-republication-race.log`, 55 RUN / 44 leaf, exit 0, 0 skips, 3.224s.
- Contract check: `/tmp/levara-memory-index-republication-contract.log`, observed exit 0.

```text
4bec8b7592ab4f47463a270fc161b3b1052f72a2f2a90347760e649bd28fcdd1  pkg/memoryindex/outbox.go
d45d52e135d576d1989aefa3fe5061cfd4d113f6b12b161738436029280aa30f  pkg/memoryindex/outbox_republication_test.go
```

## Observed vector reproductions and current limits

- Corrected original worker RED: `/tmp/levara-memory-index-effects-corrected-red.log`; actual restored/foreign/sibling vectors were deleted, cancellation/SQL failure were ignored, and an obsolete type was published after blocked embedding, both SQL.
- Core current-state GREEN: `/tmp/levara-memory-index-effects-final-green.log`, HTTP 2.115s, both SQL, 0 skips. Includes restored/retired/missing/foreign/sibling/cancel/error, type/owner/collection/key/value/retired/rollback, independent SQLite WAL/PostgreSQL writer protection, observer cancellation and pool=1 acquisition. This precedes the later migration-hook split and is not the final combined acceptance.
- Migration runtime RED: `/tmp/levara-memory-index-migration-red.log`; network hook blocked an independent authoritative writer and late migration embedding published a retired ID into a shadow, both SQL. Current implementation separates native effect from callback and rechecks memory SQL before shadow publication; final integration/review is pending.
- Early effects-red used a nonexistent fixture column; effects-green used an old missing-owner fixture; expanded-green reused a unique key between children; migration-green first failed compilation due to missing import. These intermediate failed logs are retained and are not acceptance or separate production defect claims. The corrected RED/GREEN paths above distinguish fixture repair from behavior proof.

At that checkpoint still required: current native hook/contract regressions, complete consolidation indexed recall/revert, final combined race/current hashes/broad gate and independent review; these are resolved by the final sections below. Real model quality, general migration cutover/recovery and the separate llm_calls issue remain outside this bounded change.

## Current effect/hook steps 1.2 and 2.1–2.3

- Native compatibility: `/tmp/levara-memory-index-native-green.log`, 3 top-level Insert tests, exit 0, 0 skips, store 0.310s. Existing synchronous Insert still invokes its hook before returning; deferred insert preserves dimensional/embedding-contract validation and stamping, native presence and counts.
- Owner-presence actual RED: `/tmp/levara-memory-index-owner-presence-red.log`, explicit shared owner against private SQL and explicit null wrongly called the provider and published shadow, both SQL (4 failing leaves). Non-string rejected and absent legacy/matching private/matching shared positive passed.
- Presence-aware fix full focused GREEN: `/tmp/levara-memory-index-owner-presence-green.log`, observed exit 0. Owner omission is distinct from explicit empty; null/non-string fail; supplied owner equality includes empty.
- Current focused race: `/tmp/levara-memory-index-current-race.log`, HTTP 16.229s, exit 0, 0 skips. Includes current migration test cleanup guards, all core current-state tests, network outside SQL protection, retired shadow rejection, owner-presence matrix and existing generic document dual write.
- Documentation source-to-guide review and `go test ./docs`: observed exit 0, docs 0.367s. No cross-store atomicity claim; failed/dead-letter duplicate does not reset retry, full-table short-lock concurrency ceiling and general migration cutover limits documented.
- `git diff --check` observed exit 0. Gortex detect/impact output was a truncated lower bound and is not a full correctness receipt.

The final integration and gate sections below satisfy the remaining T06 checks.

## Runtime recovery and independent bounded review

Levara became available later in the same session: set_context/wake_up and the versioned instructions returned successfully. Runtime stats explicitly reported Task Runtime enabled. Task `41e33fda-3ca8-4805-a59a-8281f233f9b9` now contains the original remaining T06–T32 criteria and 27 serial manual steps; root claimed step-t06 before its final gate. Earlier work is historical local evidence, not retroactively claimed execution or server receipts. Current task status is active; the complete roadmap is not finished.

Three de-duplicated discoveries were saved in collection levara, room memory, hall discovery: completed publication requeue, WAL/native-hook protection and owner presence. Save returned ok with index_status pending; no receipt-validation or semantic readiness is inferred.

The independent read-only reviewer re-read final owner presence/native hook/outbox code and actual RED/GREEN/race logs, reporting no remaining blocker in that bounded scope. It did not rerun tests and explicitly leaves full consolidation integration and broad gates to root. Metadata callbacks remain best effort, general migration cutover is outside scope.

## Final integration and unchanged-source gate (2026-10-06 Moscow)

Root inspected the actual 378-line new integration test, its unchanged hash and raw focused/race logs. SQLite/PostgreSQL × legacy/latest MCP perform real JWT save/consolidate/recall/revert with local embedding and a local provider stub. Physical cosine 0.9400 creates one abstract; source vectors disappear and generated recall succeeds. The probe has no SQL text matches and SQL-only recall is empty. Revert restores every persisted source/control column, obtains fresh identical-digest publication IDs, recovers one lost claim and restores physical source recall. A historical delete replay and repeated revert/drain(0) preserve rows, jobs and vectors.

- `/tmp/levara-consolidation-index-integration-green.log`: observed exit 0, HTTP 2.874s, 4 complete SQL×transport cycles, 0 skips.
- `/tmp/levara-consolidation-index-integration-race.log`: observed exit 0, HTTP 3.891s, 4 cycles, 0 skips.
- Root fresh combined HTTP race under the runtime lease: `/tmp/levara-index-final-http-race.log`, exit 0, 11 top / 77 RUN/PASS, 0 fail/skip, HTTP 15.342s.
- Root fresh outbox/native race: `/tmp/levara-index-final-outbox-native-race.log`, exit 0, 58 RUN/PASS, 0 fail/skip, memoryindex 2.785s and store 1.471s.
- `make test-commit` with verified isolated PostgreSQL: `/tmp/levara-index-final-test-commit.log`, observed exit 0, S0–S4 green. MCP 24.248s, store 26.953s, HTTP 159.690s and server 9.752s freshly; unchanged other packages cached.
- All 102 dirty tracked/untracked files in `/tmp/levara-index-final-source-manifest.json` remained unchanged at broad-gate completion. HEAD `2eb1dca16b0047918185760b41dcb22dee79090a`, whole dirty digest `1e36fece6cbe4c55dca751ae4da9e259ea8c1dd5e8a7d0b5b0d07b9e78fdca30`. Later changes are acceptance/docs metadata only and receive their own docs/strict checks.

Independent reviewer read the complete integration test and actual logs, confirmed the hash and no blocking finding; prior production/outbox bounded audit remains clean. Reviewer did not rerun tests. Recovery models loss after claim, not OS-kill durability or arbitrary native partial writes; the timer is not needed for actual worker iterations. Providers are local and do not validate external model quality. General migration batch/cutover/recovery, T07 evidence/history and the separate llm_calls issue remain open. SQL/vector cross-store atomicity is not claimed.

Runtime currently has actual command receipts `32985c4e-ec7c-4cdc-9db9-132a84be1049` and `cdf7c2d7-3405-4da8-bc68-352827d59ab2` for the fresh root race commands, bound to the tested dirty revision. No full roadmap completion is claimed.

## Final documentation/spec checks and source hashes

After acceptance metadata and the dual-write owner-presence scenario, `go test ./docs` passed (0.633s), `openspec validate repair-memory-index-lifecycle --strict` passed and `make contract-check` exited 0. `git diff --check` passed. Source hashes are recorded below; acceptance/backlog/evidence metadata are excluded from this content manifest, and their affected docs checks run separately. Both bounded changes and roadmap T06 satisfy their DoD; T07–T32 and the full runtime Task/goal remain active.

```text
4bec8b7592ab4f47463a270fc161b3b1052f72a2f2a90347760e649bd28fcdd1  pkg/memoryindex/outbox.go
d45d52e135d576d1989aefa3fe5061cfd4d113f6b12b161738436029280aa30f  pkg/memoryindex/outbox_republication_test.go
41adf8220967ee0cd49200350f497d3dad6140ae8e41f45a6ab629714384cece  internal/http/memory_index_worker.go
7fcace269e0cd0ee915f785c922a56d710672455b247b80b3b590cf2e46e8af2  internal/http/memory_index_concurrency_test.go
acd4d57303c2dcb3ada28eee9337aebbf99018d771cbd6faa8e1495017722b04  internal/http/memory_index_lifecycle_test.go
e5022216ab19475e46031ad91434fbd7cfddc926eae66a0e8ffd0dd4e04a8354  internal/http/memory_index_migration_test.go
327f4dd96c9f3b34e3aa056bf0cffd1887c3ba9b3790290fe84d23b48ff23ab2  internal/http/mcp_consolidation_index_lifecycle_test.go
b93dbf6ab2922257a33883bb281208b6145a915c12feb1e5e95eef7f5b3f75fc  internal/http/embedding_migration.go
c2ea7f87b53e93e4d7203abdce2589c8f0d3c5a9f702e0f13b261d903022763c  internal/store/collections.go
0418f1cb40f84e2d956979966b0721a57c561033a4b38c0527215c0a079a3021  internal/store/collections_deferred_hook_test.go
37b6c0d44af78a9c8c6c8fdf905cb4e46e3de77bd0e330f5aaa34f522b3dcfdd  docs/features-guide.md
8ce5d8b62151a0907fd352faea28ecd6009391bb712daf83284c632d68e0458c  docs/product/memory-model.md
80f5f2088156f2d950cf12a516a35be9fcfe355e3b8eefda364d106287e50259  openspec/changes/repair-memory-index-lifecycle/specs/memory/index/spec.md
```
