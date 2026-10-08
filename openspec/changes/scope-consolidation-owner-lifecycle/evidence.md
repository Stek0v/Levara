# Owner lifecycle acceptance evidence

Observed on 2026-10-05. This bounded change addresses the owner/classification/SQL/async portion of T06; the 32-task roadmap remains active with T01–T05 complete. T06 stays open for stale vector deletion/reactivation, completed-job requeue and end-to-end indexed recall.

## Scope and authority

Ordinary callers select exact verified owner plus explicit collection. Shared mode is explicit and requires live administrator/trusted-local authority. Engine edges and actions preserve owner/collection/type/room/hall. Apply captures and revalidates complete persisted rows; the atomic journal stores hashes and retirement fields, no source text. Revert verifies both complete after-state and reconstructed before-state, roles, target closure, lineage and full run footprint before any effect. Configured index jobs commit in the same transaction.

Provider transfer fences survive observer cancellation until actual provider return. Context/credential clock checks precede outgoing calls. Trusted-local maintenance handles SQL namespaces independently; authenticated background context without verified authority is rejected. Async work preserves verified context, claims pending jobs once, scopes status exactly and records deadline-before-claim failure without overwriting another runner. Restart never recreates credentials from owner/args; interrupted jobs report unknown outcome/resubmit. Only unclaimed trusted-local shared pending jobs resume automatically.

## Service preflight

SQLite and an isolated PostgreSQL 16.15 database `levara_roadmap_test`, loopback port 53350, were used. PostgreSQL identity/version and pg_ctl status were observed before tests. Every focused SQL/HTTP acceptance run supplied `LEVARA_TEST_POSTGRES_DSN`; no PostgreSQL skip was accepted. No external paid provider or real model quality gate ran: provider spies/local embedding HTTP endpoints made egress observable. No production migration, restart, deployment, commit or push was performed. Levara MCP bootstrap failed at its local endpoint; these are local OpenSpec evidence, not Task Runtime leases/receipts or durable-memory writes.

## Reproductions

| Proof | Observation | Raw log |
|---|---|---|
| Existing JWT candidate scope | SQLite/PostgreSQL, legacy/latest returned 3 instead of exact-owner 1 | `/tmp/levara-consolidation-owner-http-red.log` |
| Existing stale apply/revert | Newly pinned source retired; changed survivor ignored without timestamp update | `/tmp/levara-consolidation-stale-sql-red.log` |
| Existing recovery/status | Pool=1 recovery blocked; legacy shared job result exposed to another owner, both SQL | `/tmp/levara-consolidation-async-red.log` |
| Existing engine classification/vocabulary | Mixed axes/unknown IDs reached clustering/provider, hall changed to semantic, invalid legacy halls called LLM, mutable shared hall slice | `/tmp/levara-consolidation-classification-red.log`, `/tmp/levara-consolidation-owner-axes-red.log`, `/tmp/levara-consolidation-hall-red.log` |
| Journal corruption during implementation | Truncated payload and generated→survivor role accepted, both SQL | `/tmp/levara-consolidation-journal-corruption-red.log` |
| PostgreSQL legacy schema during implementation | Cached SELECT-star projection before ALTER failed repeat initialization with SQLSTATE 0A000; fixed by stable per-column probes | Worker legacy-schema reproduction and subsequent `/tmp/levara-consolidation-async-authority-green.log` |
| BeforeHash guard removal | Type-valid retirement Before tamper accepted, both SQL | `/tmp/levara-consolidation-before-hash-probe-red.log` |
| Old provider lifetime probe | Observer cancellation let revocation pass before actual transfer drain, both SQL | `/tmp/levara-consolidation-provider-lifetime-probe-red.log` |
| Detached context-loss probe | Verified actor lost after real JWT HTTP request cancellation, both transports/dialects | `/tmp/levara-consolidation-async-context-loss-red.log` |
| Failed-claim guard removal | Busy pool plus execution deadline left pending indefinitely, both SQL | `/tmp/levara-consolidation-async-claim-guard-removal-red.log` |

The last four are explicitly isolated Go-overlay probes against the current implementation with one guard removed/replaced. They are not HEAD-baseline runs and did not revert workspace edits. Their expected failing exits were observed before accepting the corresponding GREEN regression.

## Passed focused checks

| Command / covered behavior | Observed result | Raw log |
|---|---|---|
| `go test -count=1 -v ./pkg/consolidate ./pkg/memoryhall` | 33 top tests / 50 runs / 0 skips | `/tmp/levara-consolidation-classification-green.log` |
| Focused MCP hall/save/chunk vocabulary | 9 tests / 0 skips | `/tmp/levara-consolidation-hall-green.log` |
| Captured plans, mixed revert, full-row pin/provenance/source/survivor/generated changes, malformed journal, foreign/legacy run, duplicate/overlap/uncaptured ID, revoked credential, rollback, actual-drain fence | 3 top tests / 63 runs / 0 skips, both SQL | `/tmp/levara-consolidation-journal-green.log` |
| Authenticated explicit shared administrator apply/revert, grant/revoke, exact classification, namespace controls and atomic/idempotent index enqueue | 2 top tests / 6 runs / 0 skips, both SQL | `/tmp/levara-consolidation-shared-index-green.log` |
| Three trusted-local namespaces with separate provider prompts, authenticated maintenance denial, revoked-before-egress and real expiry during first embed | 2 top tests / 10 runs / 0 skips, both SQL | `/tmp/levara-consolidation-provider-scope-green.log` |
| Real JWT async cancellation, foreign status, 8 concurrent claims, bounded duration, checked/idempotent legacy schema, restart and busy-pool claim finalization | 7 top tests / 52 runs / 0 skips, both SQL | `/tmp/levara-consolidation-async-authority-green.log` |
| Async authority/claim/recovery race | 4 top tests / 20 runs / 0 skips | `/tmp/levara-consolidation-async-authority-race.log` |
| `go test ./pkg/mcp ./internal/http -run 'Consolidat\|Hall' -count=1 -v` | 37 top tests / 160 runs / 0 skips | `/tmp/levara-consolidation-final-focused.log` |
| `go test -race ./pkg/mcp ./pkg/consolidate ./pkg/memoryhall ./internal/http -run 'Consolidat\|Hall' -count=1 -v` | 45 top tests / 193 runs / 0 skips | `/tmp/levara-consolidation-final-race.log` |
| Native constraint after earlier merge UPDATE and FK delete after source restoration | Mixed rollback/apply/revert passed both SQL | Included in focused suites |
| Generated contract validation and strict OpenSpec validation | Exit 0 observed | `/tmp/levara-consolidation-contract-check.log`; strict CLI output |

The final focused run precedes the addition of maintenance/expiry test file; its production revision matches the separately observed maintenance/expiry and final race runs. An early broad package run compiled an unfinished test fixture and the pre-fix schema initializer, failed, and is retained at `/tmp/levara-consolidation-current-packages.log`; it is not acceptance. The corrected broad gate passed S0–S4 at `/tmp/levara-consolidation-current-test-commit.log`. The second stable-source `make test-commit` passed S0–S4 with exit 0 at `/tmp/levara-consolidation-final-test-commit.log`: docs 0.722s, MCP 42.855s, HTTP 181.597s; unchanged packages were cached. The 28-file SHA256 manifest was unchanged after completion (`manifest_changed: []`). Focused SQL acceptance had zero skips; the broad gate is not claimed to be a zero-skip external/load acceptance.

## Independent review and limits

Read-only review found and drove fixes for transfer lifetime, malformed journal roles/completeness, retirement-before integrity and claim-timeout pending state. Current review found no further blocking production defects. The parent read the combined diff and actual logs. Gortex impact/detect was used; its traversal is a lower bound and omits some untracked files. Signature verification reported receiver-parser mismatches despite unchanged wrapper API; actual Go compilation/tests are authoritative evidence.

Async final status persistence remains bounded best effort if SQL stays unavailable; this is not an exactly-once outcome guarantee. The legacy `llm_calls` status counter remains a separately tracked source-confirmed limitation (I19). External LLM quality, live deployment/upgrade, stale-delete generation, completed-job requeue and derived recall after delayed jobs are not accepted by this change.

The final read-only review of the credential-clock guard, shared-admin SQL lifecycle and trusted-local maintenance/expiry tests found no blocking findings. That reviewer inspected their observed GREEN logs, but did not independently rerun the full race or broad gate. Parent-observed full race and stable-source gate are recorded above.

Current code/test/generated/doc source hashes were unchanged after the stable-source gate. Earlier roadmap manifests remain historical evidence of their own accepted revisions; this manifest records the current overlapping sources. Runtime evidence is not promoted as durable memory.

```text
edf847578d358dad485e5a67f63cd8a0bc2f51f2b491ae20fae46fba34bf5833  pkg/mcp/consolidation_store.go
918e4b747d02412501a63bbc1c186bd6383d8a63f52eec1fc122b9a99f966e51  pkg/mcp/tool_consolidate.go
18bfb31f55dbc02d06c2dfe3c0c444094a5961007074e7fd29ac10f568c00e15  pkg/mcp/tools.go
7efc426a753d7a43b9219415096a17d25510ad2399f9f11fd0df8357fb75fd29  pkg/mcp/tool_consolidate_test.go
633481d4a04e759a50d11431ad1704e2824bd269c25778791370afd1ea05a5ee  pkg/mcp/tool_consolidate_sql_test.go
2a4b307b589d9a6fd3b47e1fd964f193359aec4dbbd0b12d3fb0b140ff0e9f2e  pkg/mcp/tool_consolidation_guard_test.go
46053014809836bfcc0d2c4d22682bb3e3c4224b2dda6e1af97f1e3cd08a6c9c  pkg/mcp/consolidation_lifecycle_test.go
7cd4d1449f6cb285e69e9c46c7c9b66b00d5a1954c2e5512a750fbfff43b2555  pkg/mcp/consolidation_namespace_test.go
4c461b91c925f6f7688cb6d51c50b60c0dc7ac996f0100a63b88c528c09bf667  pkg/mcp/consolidation_provider_scope_test.go
4e387fdfe2a346b83c2aaad8ac96afc5ff75ba8e4e2f08018c916d3eca2f9275  pkg/mcp/hall.go
b6ff55115ef59481d54528d69ad3c5835e785bebe31a22c883fe1219f7d47603  pkg/mcp/hall_forwarding_test.go
abf361445208d9d25a9b675405b7d7b68ced929fc8b20c4cbb7ae68a0e211448  pkg/consolidate/types.go
0c7e3e7bb1babe22121d931796f5b122e35a75700332b2db41cf03d6c8a32c87  pkg/consolidate/run.go
b42312948c6f0228d84e96278a9ae9df1cf383e4ad4d9b829f3714775c9c496f  pkg/consolidate/plan.go
f8e2e352aa6ff70a0f21e80d38cc8c0e9c630f5b1137917b461a4058cda6f81f  pkg/consolidate/run_test.go
3341d673336a6c6fe62f44e9115883ae39aa2ca99fc4319a8bddb4efc662e424  pkg/consolidate/classification_test.go
25f382a71e0e5e0cb0d83915f3f3a9c0537efcab51301d3ad17cffbb716dd6e1  pkg/memoryhall/hall.go
bee2700b6116332bbf97a7509bd71db68c830509d95fe27ed8ebce3c94d33a47  pkg/memoryhall/hall_test.go
24841be111e6bc22eebfc39a58831626dbb9a4341f29aec3cb12ae90883d9390  pkg/access/read_fence.go
4161b30dafe5783be3e97919be96e93a86061204977e3a6dd4d7977fd6b7291e  internal/http/mcp_consolidation_async.go
73452115cd27bf549a8f42ac862edbcf8cdba7eb2fd7c5bd10b64fbd022d9ec2  internal/http/mcp_consolidation_async_authority_test.go
7bcff63b938ffc683f767f0d0dc800e7942cca5492b0c917be8a89e01774cce0  internal/http/mcp_consolidation_scope_test.go
42b455016bebdf6a15deec0ac2911247dca2b6aabb8ceb7c28c4bff368345e29  internal/http/mcp_consolidation_recovery_test.go
6542e90581a61f0a3aa2efa1ee11de6a0f6ee554131f6037c9aad5d0cffa9a42  internal/http/schema.go
0463117e8df86c3f94265361cbdc2a6bbb938592486040f4fe48dd43779ba17c  docs/api-contract.md
f2359648d1fedf62299b676b0e6d96754b7095469a34fbbe3c6561b4644e8408  docs/contract.json
e03f6b0fa8ac8e759c864c60f0445c07ff720f1814b3a23500e27585c92aa12e  docs/features-guide.md
56a1f4b2a95f8ac0c451f9fcbdc56d77c96c3bafd2627eb03da587a31ed8c4f6  docs/product/memory-model.md
```
