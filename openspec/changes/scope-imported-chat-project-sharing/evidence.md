# Local implementation evidence (2026-10-06)

Original roadmap remains 7/32 accepted; T08 is active. These local results do not replace stable combined acceptance.

## Storage and local derivatives

Native PostgreSQL 16.15 preflight confirmed isolated port 53350, database levara_roadmap_test and user levara_test. SQLite fixtures are disposable; providers are local fixtures only.

- Missing-registry RED: both SQL dialects failed before registry DDL; this is schema evidence, not a runtime RED for newly introduced APIs.
- Full chatimport GREEN: exit 0, 122 passing leaves/139 nodes, zero skips, 2.094s.
- ImportScope race: exit 0, 78 passing leaves/93 nodes, zero skips/race reports, 3.969s. Includes both SQL local-janitor/source-loader isolation checks.
- Logs: `/tmp/levara-chat-import-scope-{red,green,race}.log`.

Observed storage hashes before the subsequent FinishRun repair:

```text
writer.go 47ae98999552117457dc9d3e453adac620ae880eb844617b4c93c32630515fc8
scope.go d3f08fbf57f05c4aacf44794561941809cfcb40c469e286f03957c45814edd46
scope_test.go b94ec802757de5a44c7cec6e825e3cd85fa5a5a18ba06285b4bb9f699be013ad
```

Independent source review found the legacy FinishRun mutation missing the local/scoped boundary. The local wrapper now uses a transaction and registry writer lock; it rejects registered authenticated ledgers and preserves explicit local/legacy finish and retries. FinishRunTx remains caller-scope-validated.

- Pre-repair overlay RED: exit 1, eight failing authenticated leaves and four passing local/legacy controls across both SQL dialects, zero skips, 0.658s; complete ledger snapshots show mutated status, counters, warnings and time.
- Current full chatimport GREEN: exit 0, 134 leaves/154 nodes, zero skips, 1.673s.
- Current ImportScope race: exit 0, 90 leaves/108 nodes, zero skips/race reports, 4.384s.
- Commands: `go test -ldflags=-w -overlay=/tmp/levara-chat-import-finish-overlay.json -count=1 -v ./pkg/chatimport -run '^TestImportScopeLocalFinishGuard$'`; current GREEN drops overlay/run; race uses `-race -run ImportScope`.
- Logs: `/tmp/levara-chat-import-finish-{red,green,race}.log`.
- Current writer hash: `e97467ce70e3908d7a34757065a0cdf7e82a194cdd24740e9ba3be478147322e`; scope test: `9f69b8722d60e80960d7e221768bf03b7d79cfde8fb2db5af0181052abf7c61d`. scope.go is unchanged from the hash above.

Storage implementation 1.1 is locally complete. Combined review/gate remains open; previous passes are preserved as pre-repair evidence.

## REST

- Focused TestChatImport GREEN: exit 0, 17.732s.
- Same focused race with JSON: exit 0, package 25.863s, nine passing nodes/seven leaves, zero skips/failures/race reports; real JWT/API-key native SQLite/PostgreSQL fixtures ran.
- Logs: `/tmp/levara-chat-rest-green.log`, `/tmp/levara-chat-rest-race.jsonl`.
- Imported owner/tenant and canonical selectors are preserved; project colleagues see shared chats but not mixed import-run metadata. Full-row controls, private administrator denial, tenant/credential/account checks, ambiguity and owner-consented attachment are exercised.

```text
chat_import.go ac86502e2ffa3657e13b2ac3366fcfc23f0b430bc8dbaae3bd28023216735731
chat_import_test.go 7196dc3ad249fd8fc0b6ce4340c3f8895b18d8beafb87ebab2d765d2b49db872
chat_import_scope_test.go 1b953bf90c4d6051283b7a697e52fe04faa9aa5e6cafdf17d2171d37cd218dbd
```

Deterministic revocation during REST acquisition and corrupted-row fault injection are not claimed from these tests. Production scan, Rows.Err, context and fenced credential checks were reviewed; combined independent review remains pending.

## MCP source and index authority

- Corrected original-source overlay RED: exit 1, four failing native SQLite/PostgreSQL scenario leaves, zero skips, package 1.452s. Foreign physical selector returned unauthorized success; unregistered legacy source reached the provider. The initial RED had legacy fixture FK/ordinal errors and is not accepted for that scenario.
- Current combined GREEN: exit 0, 143 passing leaves/164 nodes, including all 36 source-authority leaves, zero skips, package 21.042s.
- Same combined race: exit 0, 143 passing leaves/164 nodes, zero skips/failures/race reports, package 36.102s.
- Exact common selection: `go test -p 1 -count=1 -ldflags=-w ./pkg/mcp -run 'ChatDistill|MemoryWriteEvidence|TestToolSaveMemory|MemoryCommitEvidenceSharedIndexScopeAndRollback' -json`; race adds `-race`. Native PostgreSQL DSN points only to the disposable port 53350 fixture.
- Logs: `/tmp/levara-chat-distill-authority-red-fixed.jsonl`, `/tmp/levara-chat-distill-authority-green-fixed.jsonl`, `/tmp/levara-chat-distill-authority-race.jsonl`.

```text
tool_chat_distill.go ae4273566c7c8d92ae30949509d5a562c128deca837afcf5cb7d032e2d09ac7e
tool_save_recall_memory.go 7dad9d0d07ba2c27bc8f7e9530493aff1c2c1679d9d5fa4cb4cb320d8433ba74
tool_chat_distill_authority_test.go 9ac33a9d07dc5aa879dee51635de9aa5ca5b49e8016e763c9b672de1a6a838e6
mcp.go fcd85244b4e1b953e561a8482a5cca098beb61714db936df4668cef33f583873
```

Independent review found additional publication-currentness, trusted-local fence lifetime and credential-expiry corners. The 143-leaf passes above apply to pre-repair source. Bounded repair now captures actual canonical key/type, checks active exact owner/collection/key/type/value, rechecks the actor after row-lock acquisition, and retains local fences until actual native return with explicit SQLite writer reservation. Callback success now asserts invocation.

- Corrected pre-repair overlay RED: exit 1, 11 failing leaves/one positive SQLite expiry control, both native SQL backends, zero skips, package 8.278s. Earlier multi-connection search_path/oracle failures are preserved but not accepted as defect proof.
- Final combined GREEN: exit 0, 155 leaves/181 nodes, zero failures/skips, package 23.205s.
- Same final combined race: exit 0, 155 leaves/181 nodes, zero failures/skips/race reports, package 41.232s.
- Exact common selection remains the combined command above. RED uses `-overlay=/tmp/levara-chat-distill-memory-publication-before-overlay.json -run 'TestChatDistill(MemoryPublicationAuthority|LocalStartedPublicationFence|PublicationCredentialExpiry)'`.
- Logs: `/tmp/levara-chat-distill-publication-{red-confirmed,green-final,race-final}.jsonl`.
- Current tool hash: `36a6e0c7103cbd63962faae6f8a4cc0f1a69a2f941890f9611763d87908d059f`; authority test hash: `93202649864be5d8f54fca7795de0e0d7cd077ac08e1d7bceae408de7d02cc59`.
- Independent re-review of these actual hashes and raw logs found all four bounded findings closed, no new must-fix. Native publication tests use a call stub; full HTTP/vector integration is a parent acceptance boundary. The first combined GREEN failed incomplete historical fixtures and a test using local FinishRun on a scoped ledger; corrected fixtures preserve the intended assertions and the later actual GREEN/race results above.

## Remaining acceptance

MCP focused checks passed locally and bounded re-review is clean.

## Project audience writes and combined HTTP

Independent review found project grant/revoke authority checked outside its DML transaction and credential expiry unchecked before commit. Both mutations now use the existing metadata transaction fence, transaction-bound owner/admin and target lookup, plus a final captured credential/exact selected-tenant recheck. Self-admin demotion/revocation remains allowed.

- Original-source RED: six native SQL leaves incorrectly granted after middleware while JWT epoch, membership or API key were revoked; exit 1, 2.217s.
- Pre-expiry-repair RED: PostgreSQL observed row-blocked POST/DELETE still committed expired JWT authority (two failures), SQLite acquisition-expiry controls passed; exit 1, 13.052s.
- Final audience GREEN/race: each exit 0, 40 leaves/64 nodes, zero skips/failures/race reports; 17.971s / 28.180s.
- Selection: `go test -json -p 1 -ldflags=-w -count=1 ./internal/http -run '^TestDatasetAudience|^TestDocumentACLShare|^TestDocumentACLPostgres$' -skip '^TestMemoryREST'`; race adds `-race`.
- Logs: `/tmp/levara-rbac-share-red.jsonl`, `/tmp/levara-rbac-share-expiry-red.jsonl`, `/tmp/levara-rbac-share-final-{green,race}.jsonl`.
- Current rbac.go hash `919991996b9de7d6fb3288e0e6f6a8ea2353e7d44283f7e1d744051ad78006ee`; authority test `a299c7dd61996612278771abad1c0ba27b5f69064a7aa193f8ffcb49a16c9299`; verified-JWT fixture `d61003eda38fde77daa879ae92fd9cf832821baf75bf77c07d3f6fb910775fe8`.
- Independent source re-review closed P1/P2. Reverse-order revoker waiting behind an acquired write fence and separate late external/browser expiry variants are not individually claimed.

Root combined HTTP GREEN/race each exited 0: 67 leaves/97 nodes, zero skips/failures/race reports, 22.818s / 30.894s. This includes 16 real MCP collection-routing cases across both SQL dialects, REST/private-project import and grants, and both daemon auth modes. Daemon mode fixture uses SQLite; native storage and local-derivative SQL behavior are separately exercised on both dialects.

The first root combined GREEN failed the older daemon fixture: it opened SQLite while retaining the PostgreSQL dialect, causing `near LOCK` and zero raw messages. Fixture now selects its actual SQLite provider and restores it afterward; production authorization was unchanged. The corrected combined runs above retain all original assertions.

- Root selection: `go test -p 1 -count=1 -ldflags=-w -json ./internal/http -run '^TestMCPChatDistillCollectionRouting$|^TestChatSourcesAuthenticatedMode$|^TestChatSourcesDaemon|^TestChatImport|^TestDatasetAudience|^TestDocumentACLShare|^TestDocumentACLPostgres$' -skip '^TestMemoryREST'`; race adds `-race`.
- Root logs: `/tmp/levara-chat-http-final-green.jsonl` (preserved failure), `/tmp/levara-chat-http-final-green-fixed.jsonl`, `/tmp/levara-chat-http-final-race.jsonl`.
- CLI focused `go test -p 1 -count=1 -ldflags=-w -json ./cmd/cli -run '^TestChats'` exited 0, two tests, 1.128s. The artifact test exercises actual multipart upload with tenant and ordinary add with no inherited tenant.
- Log: `/tmp/levara-chat-cli-green.jsonl`.

## Remaining public/integration acceptance

Regenerated full/core contracts and contract-check each exited 0. Contract/profile/docs packages passed. The earlier combined public run had three PostgreSQL CLI skips because its command omitted the DSN; that run is not full native CLI acceptance. Root reran complete CLI with the isolated PostgreSQL DSN: exit 0, 75 PASS nodes/63 leaves, no skips/failures, 6.455s. Log `/tmp/levara-chat-cli-native-final.jsonl`, SHA-256 `069c7f4df68c4a365759df809b039aa98ae545f4148da292879f49b16bec0f41`. No additional REST memory-owner probes were performed.

Gortex guarded mutations supplied physical disk evidence. Several impact/detect requests refused stale graph state during concurrent indexing; no exhaustive graph impact claim is made.

Project revocation blocks future source use and publication checks. Previously committed caller-owned distilled memories remain personal copies; existing migration callbacks mirror their SQL identity rather than grant raw-chat access. Once a contextless native insert or deferred migration callback has begun, caller cancellation cannot forcibly interrupt it. No rollback of earlier committed SQL, retroactive private-copy deletion, model-quality acceptance or production rollout is claimed.

## Stable T08 integration acceptance — 2026-10-06

Root observed `make test-commit` exit 0 (session 34391): S0–S4 green, 3564 PASS nodes/3010 leaves, no FAIL. HTTP package 339.761s; server 29.354s. Optional `TestDCDVSALoadBaseline` skipped. Command used the isolated PostgreSQL DSN on port 53350 and `GOFLAGS='-p=1 -ldflags=-w -count=1 -timeout=20m -skip=^TestMemoryREST -json=true'`. The explicit user exclusion of additional REST memory-owner probes is preserved; earlier native GREEN/race proof predates that instruction. `-ldflags=-w` removes debug data to avoid the observed linker delay, not runtime/race checks.

- Frozen full revision: `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:37746f5790fabbae288e16a4891d97745da7a1f119d83ed58bf74f26d7adbf20`.
- Pre/post manifests `/tmp/levara-t08-pre-gate-workspace.json` and `/tmp/levara-t08-post-gate-workspace.json` independently agree. Method: binary tracked diff plus sorted nonignored untracked paths/content; 114 untracked files. Older integration-source manifest is obsolete and is not this acceptance evidence.
- Gate log `/tmp/levara-t08-current-test-commit.jsonl`, SHA-256 `e4e25146923b3b728b03ce93d89979e0ea0562ce22a399a162dcae3a53ea9107`.
- Additional root current full chatimport: exit 0, 134 leaves/154 nodes, no skip/fail, 1.600s; `/tmp/levara-t08-current-chatimport.jsonl`, SHA-256 `8595236048c8454b6a7c08a06b4ccaf0e06c9f31f1ec737655dcd924cac63114`.
- Independent combined source/evidence review accepted T08 with no remaining findings. Sole canonical diary documentation mismatch was corrected and re-reviewed: verified caller/exact tenant/trimmed agent, historical anonymous local only; AGENTS.md SHA-256 `d7ae5f0a9d71162817f617d3c9fcc485f6bcc99dc4570b64b4bfe3b06ef9d41a`.
- Strict OpenSpec validation of all ten changes passed before acceptance. Final acceptance ledger/checklist edits and canonical paragraph are documentation-only follow-ups to the frozen code revision; docs/static/strict checks are repeated after them. No code changes are attributed to the prior gate.

T08 dispatch/security DoD is accepted. The original roadmap advances to 8/32; T09 and remaining milestones stay open. Real model quality, production rollout, retroactive personal-copy deletion and forced interruption of started contextless callbacks are not inferred from these checks.
