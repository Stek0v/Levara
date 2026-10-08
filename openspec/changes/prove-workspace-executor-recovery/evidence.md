# T27 — bounded workspace executor acceptance

Date: 2026-10-07. Original T27; existing implementation verified without a production behavior, schema, descriptor or dependency change.

## Observable DoD and corner cases

| Requirement | Actual native evidence |
| --- | --- |
| Allowed read/write effects | Existing three-step workspace chain plus exact read text/write bytes, unchanged authority/publication manifests and passing observation receipts |
| Read-only grant | Real viewer share reads, cannot write; upgrading same share to editor permits actual write |
| Profile without workspace tools | Actual long-horizon profile retains task_step but denies workspace_read/write; full profile positive controls |
| Manifest changes | Valid atomic manifest replacement after action/SQL admission while native project lock is held; first byte/digest read rejects it, restoration/fresh task succeeds |
| Path/symlink/hardlink denial | Existing native denied-before-effect and confined-path regressions |
| SafeID collision | Existing UUID/project mapping and alias collision controls |
| Lease/deadline | Existing replaced-lease denial; worker action deadline expires during real project-lock wait, preserves bytes/no receipt/releases SQL; fresh positive succeeds |
| Unreceipted write/restart | Actual child write, verified zero receipts, confirmed SIGKILL, persisted natural minimum30s lease expiry without SQL edits, independent reopened child runs actual HTTP executor |
| CAS reconciliation | Exact one pass receipt at attempt2, two claims, no remaining lease; source bytes/inode/mtime and publication manifest unchanged |
| Audit | Expected real dispatcher audit outcomes verified separately; only exact project audit path excluded from product-tree comparison |

## Scope boundaries

The worker reserves five seconds before lease expiry for its final transition. The new lock-wait test proves the EARLIER action deadline; natural persisted lease expiry is proved separately AFTER SIGKILL. Neither is a claim that file mutation continues across lease expiry.

The changed-manifest test acts before the FIRST manifest digest read under the project lock. It does not prove the separate final post-digest recheck race.

First crash child uses the existing production MCP executor and validated HTTP dispatcher callback seam to pause after write before receipt; recovery uses unchanged HTTP NewTaskExecutor. These are actual native persisted-owner/ACL and independent-process checks, not public authentication transport or power-loss evidence. Shell/network actions remain unsupported.

## Focused actual gates

All Go tests/list commands excluded ^TestMemoryREST per user. Dedicated native PostgreSQL127.0.0.1:63530 and SQLite both executed, no skips.

- ACL/profile: terminal exit0,6 PASS/0 FAIL/0 SKIP, /tmp/levara-t27-acl-profile-first.jsonl, SHA256 de3660aee0c889fd68a2c325730b42c67e00aa067314d286dae39e6d792ba811. Before/after identical dirty6656e1ae1c09f582a70d216776078b0e41ef9f3b870027e51c4865dd28d857a9,316 nonignored untracked. Receipt f3e1ae5e-6f94-463b-aaa6-3af238163cd1.
- Process/deadline/manifest: terminal exit0,6/0/0, /tmp/levara-t27-process-deadline-first.jsonl, SHA256 b55eaacfbdd159f67e14f77f91d9baf5fd1408b00a1c7f1dbdd508511efab2cf. Before/after identical dirtya650c5e442f3b2f4d439a2a44cdde7a14fe3125aab729c5e6f16ea7b09ae87d9,318 untracked. Receipt413225e7-94f0-436f-b35c-bae2d6bc95a9.

## Expanded native gate

Actual `go test -race -count=1 -timeout=20m -json -skip '^TestMemoryREST' -run 'TaskWorker|TaskExecutor|TaskWorkspace|Authority' ./pkg/mcp ./internal/http ./cmd/server`.
Terminal exit0,441 PASS/0 FAIL/0 SKIP, no failed parents; /tmp/levara-t27-final-native-race.jsonl SHA256696683b7fa2f7a31317587345d4c67d5fb7456f61689b76f41c3c16c8552ef57.
MCP/HTTP scoped tests executed; cmd/server only compiled because selector has no matching tests, so bootstrap evidence comes from whole S4.
Before/after unchanged `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:dc709f2bb38e269d68e5cd5f5ddda3d35fdb3dd8e61bafaec213cfb217ca6aff`,318 untracked. Native receipt39b87bcb-5b44-452c-abd6-334c327f5413.
Independent source and raw-log reviews confirm the mapped checks and preserved direct-core/first-read/action-deadline limits. No production defect or failed command was observed in these focused/native gates.

## Whole integration and final acceptance

Actual whole `make test-commit` with dedicated PostgreSQL and `GOFLAGS='-p=1 -ldflags=-w -count=1 -timeout=20m -json -skip=^TestMemoryREST'`: terminal exit0, S0–S4 green, all11 packages PASS, **4457 PASS / 0 FAIL / 2 SKIP**, no failed parents.
Log /tmp/levara-t27-final-integration.log, SHA2562fad1c595c7f780bcecd8d1349dc6e45fdd5eed925a3cd51cfa641823a0d644b.
S4 executes193 server leaf tests. Skips are the existing TestDCDVSALoadBaseline and TestT11NativeRAGLocalQuality opt-ins, not passed coverage.
Whole passing receipt4f583631-212b-4900-9974-4c9b217175d9, runtimev351.

Whole before/after unchanged dirtydc709f2bb38e269d68e5cd5f5ddda3d35fdb3dd8e61bafaec213cfb217ca6aff,318untracked. Functional1529-file maps equal SHA256a5049164e03f3173973dcc9434eb3d8aa031b8aa574cee1615aede9c4790c9ed; /tmp/levara-t27-functional-map.py excludes13 named acceptance metadata files only.
Final strict OpenSpec and diff checks exit0; actual contract session64209 terminal exit0 observed by root. Independent raw-log/source/map review recommends originalT27 acceptance; its conditional contract requirement is fulfilled by that actual terminal confirmation.
Original T27 accepted and own change6/6; roadmap21/32. Global Goal/Task remains active. No failed native/full command observed for this change; no speculative product issue added.
