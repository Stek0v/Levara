# T21 sync acceptance and preserved evidence — 2026-10-07

## T21 full acceptance — 2026-10-07

All11 change tasks and originalT21 behavioral DoD are accepted; roadmap22/32. Public protocol3 uses external native logical-generation/state-revision/immutable-alias ledgers, whole-memory-envelope SQL/outbox atomicity and canonical logical-lineage ranking. Native Save UPSERT retains the actual persisted incarnation; retirement/revert advances state revision; terminal deletion defeats unseen stale UUIDs; fresh native recreation allocates the next generation independently of wall-clock content timestamps. Historical unresolved original keys are explicitly rejected rather than guessed.

Actual final native race gate exits0,531PASS/0FAIL/0SKIP, all3packages/no failed parents: `/tmp/levara-t21-generation-complete-native.jsonl`, SHA256 `ee42dc1ff3596a75dec36871046c2befe0dc6bee1cc088350f8523e5b0f12a88`, receipt `4872937f-63ec-46f7-9b08-6a2dfaa63486`. Actual whole `make test-commit` exits0/S0–S4green,4539PASS/0FAIL/2existingopt-inSKIP,all11packages/no failed parents: `/tmp/levara-t21-generation-complete-integration.log`, SHA256 `a0f4eb72f703359bf9277f3c02ee1138111f87740474aa521e7b248c2a06516c`, receipt `2e898d75-dd9a-4d4b-958b-ad4a8d44bba5`. Skips are TestDCDVSALoadBaseline and TestT11NativeRAGLocalQuality. Every Go gate excludes `^TestMemoryREST` per user instruction.

Native/full before/after revision is identical: `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:52e1f6e7eb2649340de0b2891958245cdf2456b517b72ff7c0452b7e134d65ba` (334 untracked). Functional1544-file maps agree at `d08b4da50357142858f0f7329532cd74a077208b3cdef67a98014ab89187c492`, using only established13 metadata exclusions.

Current bothSQL proof includes actual native Consolidation Apply/Revert plus public-v3 exchange and real index worker; receiver foreign Revert denied without a journal; stale applied snapshot cannot retire restored sources or resurrect deleted summary; real HTTP response loss after receiver commit followed by SKIP-only retry; fresh generation1 beats future-clock generation0; SKIP aliases persist; both independent roots reopen the exact SQLite file/private PostgreSQL database/schema with fresh native pools/outboxes and unchanged six-table snapshots; missing/cross-scope/canonical-self lineage reports truthful HTTP200/error/all-failed counts and exact rollback. Required auth flow, ordinary-user rejection, header injection, untrusted destination, redirect and local collection credential controls pass in the same current gate. Current collection re-embedding/status/chunk/contract tests pass; earlier actual independent SQLite OS collection-job proof is retained below.

Current `make contract-check`, installed OpenSpec strict validation and `git diff --check` exit0. Standard full/core generators include the three generation-ledger tables and scope/unresolved indexes for both SQL dialects. Independent read-only audit checked raw logs, every current functional byte, originalT21 DoD and required scenarios: no remaining behavioral blocker. Postacceptance bookkeeping/design-status delta and its docs/strict checks are recorded separately below; no compiled source/spec/test mutation is inferred from bookkeeping.

Limits: fresh-pool SQL reopen is not an OS memory crash/power-loss claim. Preserving a live local Revert journal after reverse peer import is not established. These are unclaimed extras, not requirements in originalT21/spec. All prior failed gates below remain historical, not relabeled. I150/I151 and I190–I199 are closed by the current proof.


## Historical generation phase — failures and interim evidence

Focused corrected gate28/0/0 exits0, SHA256 `554e8abc7a2e47b8a01bad79eed75838bed59af27dd8a99375981cf1a1628c87`, frozen dirty `df4b2eff60e0582b9a636107e207f62f4f62c2c76a71836fabef93a416e9c62a`, receipt `1ce01173-265f-4447-8788-4c15f55ff175`. Metadata fixture seeded real canonical pending outbox publications because native Save without embedding creates no jobs; exact unchanged-job checks retained. Independent review accepts pending-job deduplication coverage only.

Expanded native race gate468/0/0 exits0, all3 packages/no failed parents: `/tmp/levara-t21-generation-fixed-expanded-native.jsonl`, SHA256 `8e8535614fec81079f9be23acc5f093a5c61dbae2cb9f0f43e8d31fdf02a07cc`, receipt `71f5f752-4af0-4b06-97db-0652685c097c`. Frozen dirty `f828fff3aa21565732f88e15b13ebc7273506d7c223926fc6cddd54f630845f2`, functional1542-file maps identical SHA256 `852e63b07c8d4476bb2733a0c672ad3021776dd3ac7cfeeef12092c4d8f47c20` using established13 exclusions. Standard contract/core generation and check all0; installed OpenSpec strict and diff-check0. BothSQL ledger tables and partial index are in generated inventory.

Whole `make test-commit` actually exits2,4312PASS/6FAIL/2existing opt-in SKIP: `/tmp/levara-t21-generation-fixed-integration.log`, SHA256 `9400ae886cd0f048ddb37d492ad0e413ade177d3191566d406638467fc7d5f84`, receipt `b4c106a6-c082-46ab-b447-b774b81e601f`; frozen before/after revision remains `f828fff3…`. HTTP failure stops remaining stage; no whole acceptance. All6 failures are bothSQL index fixtures mutating bound incarnation owner; intended immutable guards reject them. Test-only initial-scope/fresh-UUID replacement repair pending verification. Actual native public consolidation, committed lost-ACK, delete/recreate clock-skew and malformed-lineage rollback tests are being added. No task checkboxes changed; originalT21OPEN6/11,roadmap21/32.

Joint native generation/ledger/wire gate completed exit0,28/0/0, no failed parents: `/tmp/levara-t21-generation-joint-native.jsonl`, SHA256 `f1c43f208bf256debd2f330789db91849c837b7531839cf657913afce21b86af`, frozen dirty `b03c54ffd2c35556269f5be6840ec8923b205674166b716dc19f1f5998936820`. Preregistration, terminal-history and fixture repairs pass. Expanded native gate exits1,459/1/0, SHA256 `9c56d509bd71d505162fc3a7c0abb40c9d0db40e19835e93ce79f19cecff0ffe`, frozen dirty `b9ff4bbf1343fad6ba170bc2ad826feeeca50b639ad3eaa59ccaa698a645d0af`: only SQLite10001 fixture interrupts at its unchanged60s deadline. Shared unresolved-history partial index avoids scanning resolved history.

Current focused repair gate actually exits1,22/6/0: `/tmp/levara-t21-generation-repair-native.jsonl`, SHA256 `74542bb45cb37079caa69cd6a7668b43154a846aba5d9b66289e689b3c189540`; identical before/after HEAD plus dirty `a7fedb9bf9ad2cbc44b51c00499abe6a7a266c4bb81bd9b8f9a117fd8a8de2b7` (332 untracked), receipt `7cbcfede-288d-4720-9b48-556f9044d950`. BothSQL10001 fixture and actual native different-key Supersede/public-v3 fixed points pass. All6 metadata scenarios converge but fail the final canonical index-intent snapshot assertion; source versus fixture investigation is pending. Independent current-source review clears predecessor original-key archive and logical-lineage ranking fixes, but is not runtime acceptance. All Go commands retain `-skip '^TestMemoryREST'`. OriginalT21 remains OPEN6/11; no current-generation full integration accepted.

Genuine independent native UUIDs for the same owner/collection/logical key fail bothSQLite/PostgreSQL exchange: unknown stale UUID revives a natively deleted generation; native supersede uses the target predecessor as active successor. Actual `/tmp/levara-t21-generation-first.jsonl`, exit1,0/4/0, SHA256 `27d8cf8d37f14c3066ba140a50407947d5402085223ca4b8246fae4e94aab671`; frozen before/after `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:7c2b4692135fc0368195cacf9955121877f2f2e8aba59ec6897a824460ecbe75` (327 untracked). This is native product generation exchange, not REST owner spoofing.

External head/incarnation/immutable-alias ledgers and bothSQL native triggers are being implemented without changing memories columns/consolidation hashes. First ledger gate `/tmp/levara-t21-generation-ledger-native.jsonl`, actualexit1,4/2/0, SHA256 `6fba8f903f3e96806eb0c1ca2f6bf728d9991aa021014f30aa76cefc633cab8d`, frozen `17cf7695c86d42b53e54fc0eabcea0c49bd15ad0116f2ac4ee6500c3e568a95f` (328 untracked). Failed SQLite placeholder and PostgreSQL incomplete snapshot ordering are retained; source preregistration/historical ambiguity review fixes are pending. No ledger/generation acceptance yet. Public protocol3 history/aliases and atomic import are under implementation; prior protocol2/full4501 proof below is historical unchanged-source evidence, not a current-generation gate. T21 remains6/11 and roadmap21/32.

Original T21 remains open; the roadmap is 21/32. Collection phase 3.3 is accepted after the current verification below; this change is 6/11. Memory lifecycle/deletion/identity aliases and whole-change acceptance remain open. This phase supersedes the design's pre-proof statement that duplicate/job ownership verification was pending. Earlier evidence and failures below remain historical, not relabeled as current-source results.

## Lifecycle first repair — interim evidence, not T21 acceptance

Native save/supersede/delete on independent both-SQL pool-one stores first proved 0 PASS / 4 FAIL: retired rows became active on import and a predelete export recreated a natively deleted UUID. Log `/tmp/levara-t21-lifecycle-first.jsonl`, SHA256 `09d909208fc68062f7c86b503a5d2c24b29c9132b80124292509d0575f24ef12`, terminal exit1; before/final-after revision `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:a2ee7acbca7abe86e623e118da76cb695fc5b70ab5e320c95c68c24a0cec17cd` (323 untracked).

A bounded repair adds mirrored native physical-deletion journals/triggers, exact deleted-ID replay admission, preserved validity/supersession links and canonical index intent. Lifecycle memory groups resolve references and write SQL/outbox in one transaction. Native gate `/tmp/levara-t21-lifecycle-repair.jsonl`, terminal exit0, 4/0/0, SHA256 `62373459fe3d59b358e88daf9d9bf70331188901bfe9f9fb687c7123e1331976`; before/after revision `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:93132e374ff776c8cc2295541d34c33e7618815ffe3bc72f3dae56e06e2cff51` (323 untracked). Reviewer subsequently found canonical self-reference after foreign-ID mapping; explicit canonical comparison was added before effects.

Version2 memory HTTP envelopes separate memories and deletions, reject legacy/missing/future payloads before SQL effects and require versioned count acknowledgements. Wire exports deliberately retain the complete lifecycle snapshot independently of since until a durable revision cursor exists. Each deletion's journal/SQL/index intent is atomic; a linked memory group is independently atomic. The whole wire envelope is not one transaction. Source native supersede queues retirement and successor publication in its existing transaction and keeps the historical inline fallback only without an outbox.

Interim native/HTTP gate `/tmp/levara-t21-lifecycle-wire.jsonl`, terminal exit0, 24/0/0, both packages pass, SHA256 `61950738350917548b3121c80e2f5b8049acf717094d03168db161728cf329f8`; equal before/after revision `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:763dd70cdb95f29e7d1f8eb66ae67078c6f9461e4538f09be32e38865f3d3753` (326 untracked). It covers bothSQL native rollback under failed index-intent writes, repeated migration, PostgreSQL changed search_path, fresh native UUID after known delete, versioned HTTP deletion-only exchange/replays, unsupported payload no-effects, canonical self-references, standalone Pull, old decoder Push rejection and native supersede second-enqueue/foreign-pending rollback. Fixtures use trusted-local native authority; no new JWT/process/power-loss proof is claimed.

Independent review found validity-only-retired Supersede admission after the interim gate. SELECT and UPDATE CAS now require open validity; bothSQL complete row/outbox no-effects controls and active positive cases pass. A first expanded gate exited1 with319 passing leaves but two failed selector parents because its positive peer fixture still returned a legacy memory array; production admission correctly rejected it. Failed log `/tmp/levara-t21-lifecycle-final-native.jsonl`, SHA256 `02fdfbee3be1b0a4d4ec1f5461a90cb86eb95c297ee857e010b034e83bf809f4`, retained. Corrected v2 fixture gate `/tmp/levara-t21-selector-fixture.jsonl` exits0,16/0/0, SHA256 `7450f77fccb9b6ae954f0c573c28483e8e5ee6c1c9ed52d15827a5b373c95b6d`.

Final native race gate `/tmp/levara-t21-lifecycle-repaired-native.jsonl` exits0,319/0/0, all3 packages/no failed parents, SHA256 `da827bee2ce18c3623e84a290e1135444695d8c3841feefa7884df372abc6b3c`. Actual frozen whole `make test-commit` exits0/S0–S4 green:4501/0/2, all11 packages/no failed branches; log `/tmp/levara-t21-lifecycle-integration.log`, SHA256 `b5ce35ffc0928d58839e6e7e6918e6d8ce28b392f639aa842333bc5b0eeda543`, receipt `265f8db8-cbb7-4fe5-9fd4-7319c84f2da5`. Both skips are existing opt-in workload/quality cases, not passing evidence. All Go commands exclude `^TestMemoryREST` per user instruction.

Frozen native/whole source: `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:f2f1524fc013ebc6d00c7c54bba7d9dc4b9cd606486148111eb91437b5b94c40` (326 untracked). Before/after functional1536-file map agrees at `93ae5fe0855220b63e8d589c6f11b82026e3b861c164017eb355a934e67f537c`, using only the established13 metadata exclusions. Independent read-only audit verified raw logs/current source and no REST-owner-spoof execution.

Final contract-check first exits2 because the new deletion journal was missing from generated schema inventory; failed `/tmp/levara-t21-lifecycle-contract.log` is retained. Standard `make contract` and `make contract-core` both exit0; repaired `make contract-check` exits0, `/tmp/levara-t21-lifecycle-contract-repaired.log`. Only generated contract inventory changes are carried forward; postgeneration/postmetadata docs and exact functional-diff continuity are checked separately. Installed OpenSpec strict validation passes.

Logical generations, persistent aliases including skipped foreign IDs, divergent-ID retirement/delete convergence, complete consolidation lineage and lost-ack/process proof remain open. Tasks2.1/2.3 and original T21 remain unchecked; roadmap21/32 and this change6/11 are unchanged.

## Collection receiver-contract and independent-status acceptance

Frozen source revision: `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:36fb28dbdf0e16052c588e5118d2ea531a351bb8ada01c76fd58e6278116e6ed` (322 untracked). Native before/after revisions agree. The full integration and final postmetadata checks are recorded below only after actual terminal results.

Receiver-local re-embedding selects the same complete native contract as collection creation, including custom tokenizer/pooling/normalization. Existing model/dimension/metric/fingerprint incompatibility fails before inserting units; source fingerprints do not grant admission to vectors generated by another encoder. Successful embeddings replace both reserved metadata fields while preserving business fields and source bytes. Missing/null metadata becomes an object. Unsupported short nonobject units fail visibly; existing oversized-document wrapping stays compatible.

Single native insertion rechecks its actual collection identity/current contract and retains the manager read lock through the actual database write. Explicitly stamped vectors cannot enter an unknown current contract. Metadata snapshots copy their contract, and count updates use exclusive manager admission with native database counting and a database-identity check. The fence regression observes actual native DiskStore progress before checking the retained manager lock.

Authenticated SQLite and PostgreSQL fixtures use real source-native stamps, private persistent vector roots, custom receiver contracts and pool-one SQL admission. Positive different-model/dimension and same-contract cases, incompatible same-dimension target refusal, exact source/business metadata preservation, target vectors/full contracts, and native reopen pass.

Two real Darwin/Linux receiver processes use separate SQLite files/vector roots, pool-one SQL, live JWT-admin loopback HTTP and receiver-local embedding providers. Own jobs reach terminal results; foreign job IDs return 404 and ordinary users receive 403. Repeated differing payloads with the same ID count two processed units but retain one physical record, with the last sequential payload winning. Rejected short-array units report FAILED/Processed0/Failed1 and preserve the prior record. Native close/reopen verifies payloads, vectors, both contract fields and zero occupied SQL connections; child exits are observed successful.

Long-null/object document tests verify bounded chunk text, lineage, receiver vectors/contracts, unrelated sentinel preservation, stable identity sets on replay, and exact ID-to-vector/raw-metadata equality immediately after native reopen. All oversized-whitespace input reports COMPLETED/Total0/Processed0/Skipped1 and never calls the embedding provider.

| Actual gate under /tmp | Exit | Leaf PASS/FAIL/SKIP | SHA256 |
|---|---:|---:|---|
| levara-t21-reembedding-red.jsonl | 1 | 2/4/0 | cb57de471df52447efb2d708464e15fad89ec01a6fef3a4ee13fa3b540b44262 |
| levara-t21-contract-green.jsonl | 0 | 17/0/0 | a53f61418c89652d9da7c0d5e980f4a8aa70b33d7f1f36a1af4304a1bc46f5f2 |
| levara-t21-final-native.jsonl | 0 | 204/0/0 | ed6a2b39bdffd210b01bf38b89ed49527af92cd04181e82cfca60b8c367a28f6 |
| levara-t21-chunk-red.jsonl | 1 | 0/3/0 | 3e04123db7475fccf8b6d575efa055e43f6a0a66e2eed25ff12aedde2f38658c |
| levara-t21-process-chunk.jsonl | 0 | 5/0/0 | 48da2288358179244c1ffc42a56dd1855397c4c89564dcea3c85515ba0b90a0c |
| levara-t21-collection-final-native.jsonl | 0 | 220/0/0 | c5acea8e354a4c6523d611c99f3014350f7c7c2ac2be00884322d7db56a782ae |

The first contract failures prove bothSQL native rejection of valid re-embedding and acceptance of wrongly labelled same-dimensional receiver vectors. The chunk gate proves the long-null panic and source-record Total on zero units. Its long-object branch was an unordered-ID test oracle failure; sorting copied IDs corrected the oracle without changing native storage. Every failed gate is retained.

Final native command: `LEVARA_TEST_POSTGRES_DSN='postgres://levara_test@127.0.0.1:63530/levara_roadmap_test?sslmode=disable' GOFLAGS='-p=1 -ldflags=-w' go test -race -count=1 -timeout=20m -json -skip '^TestMemoryREST' -run 'Sync|Collection.*(Contract|Snapshot)|ChunkMeta|ExpandRecords' ./pkg/mcp ./internal/http ./internal/store`. All three packages passed, with no failed parents or skipped tests. Dedicated PostgreSQL port63530 is used; production databases are untouched.

Functional baseline: 1532 tracked/nonignored-untracked files, SHA256 `c2a347dbc2ba89ea702ec91201ec60563b8c4fc7f84deeb52b6cbb78dbaf7a1d`; only the established 13 acceptance metadata files are excluded. Source/spec/design/test bytes are included. Independent read-only review reconciled actual logs, digests, frozen revisions and source invariants without blockers; final whole/contract/strict/postmetadata checks are root-owned.

Limits: OS-process proof is SQLite-only; separate fixtures cover both SQL dialects. Status authority is instance-wide administrator scope. Job status durability across restart, concurrent-job winner ordering, BatchInsert replacement concurrency, arbitrary power loss, provider embedding quality and memory lifecycle/deletion convergence are not established by this phase.

Actual whole command: `LEVARA_TEST_POSTGRES_DSN=...63530... LEVARA_TEST_POSTGRES_BIN=/opt/homebrew/opt/postgresql@16/bin GOFLAGS='-p=1 -ldflags=-w -count=1 -timeout=20m -json -skip=^TestMemoryREST' make test-commit`, terminal exit0, S0–S4 green. Log `/tmp/levara-t21-collection-final-integration.log`, SHA256 `0cb6a277d8978b9febb9cb23cb94e574bf6436371e82db33fe7afa409096283b`: 4475 PASS / 0 FAIL / 2 existing opt-in SKIP, all11 packages including actual server tests, no failed parents. Only TestDCDVSALoadBaseline and TestT11NativeRAGLocalQuality skipped; they are not passing workload/quality evidence.

Whole global revision and functional map after match the frozen baseline exactly. Final `GOFLAGS='-p=1 -ldflags=-w' make contract-check` session27131 terminal exit0; log `/tmp/levara-t21-collection-final-contract.log`. Installed OpenSpec strict validation and `git diff --check` pass. Final postmetadata docs/functional continuity and current-revision reviewer receipt remain root-owned gates recorded by Task Runtime before continuing.

## Earlier accepted phases and preserved history

## Historical: T21 bounded sync/consolidation evidence — 2026-10-07

Original T21 remains open; roadmap is 18/32 after supported local T22 acceptance. This change accepts 5/11 tasks, including active-memory conflict/canonical identity and index publication. Lifecycle/deletion/identity aliases, independent collection status and whole-change acceptance remain unfinished. The earlier 4/11 proof below is historical.

## Historical frozen proof

Revision: `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:527076e1e6abcf4aefabb5deab2db0e4ee3a43c1835ae0d457b40a4616eb1893` (276 untracked). Whole before/after maps identical.

- Actual `go test -p=1 -ldflags=-w -count=1 -race -json ./internal/http ./pkg/mcp -run 'Sync|Consolidation' -skip '^TestMemoryREST'` exit0: 288 PASS / 0 FAIL / 0 SKIP on SQLite and isolated PostgreSQL. Log `/tmp/levara-t21-final-phase-native-race.jsonl`, SHA256 `fb90b374c4d65015cd58b0aaa68c46079d6bf8a6242c288b6c0a1ba47f12654b`.
- Dedicated PostgreSQL port63530; no production database used. Prerequisites and selected tests were verified before execution.
- Actual final `GOFLAGS='-p=1 -ldflags=-w' make contract-check` exit0, log `/tmp/levara-t21-final-contract-check.log`. Full/core artifacts updated only by standard generators (both exit0).
- Current-source independent read-only review accepted this bounded phase without blockers. Full original T21 and full-repository acceptance are not implied.
- Native proofs cover pool-one transaction paths, independent SQL roots, changed/identical counters, repeated/reordered exchange, immutable graph identity rejection, retirement temporal joins, confidence native precision, complete over-limit export, malformed selectors with zero network effects, default/opt-in controls, parsed inclusive incremental boundaries, and actual legacy/latest consolidation RPC restoration.
- Consolidation retirement uses actual wall time for valid_until and a separate monotonic microsecond revision for updated_at; future prior revisions do not postpone retirement. Revert restores semantic content with a fresh revision, preserving hash guards and legacy journal compatibility.
- Graph temporal joins preserve known bounds independently of content rank; inverted intervals are rejected before mutation. Export uses normalized native confidence. Incremental export scans under existing request deadline and compares parsed instants inclusively; normalized indexed revision storage is the upgrade path for larger corpora.
- New graph fixed-point cases exercise native helpers and actual query_entity visibility; no new authenticated-transport conflict coverage, power-loss proof, physical deletion replication, or per-node collection-status proof is claimed.

## Failure history retained

| Gate/log under /tmp | Actual exit | Leaf PASS/FAIL/SKIP | SHA256 |
|---|---:|---:|---|
| levara-t21-first-mcp-race.jsonl | 1 | 88/21/0 | 5ff75c74227fecc3266372bcf1db7a78c705b3e581b44a4b5de4b9c52b64c7d8 |
| levara-t21-mcp-repair-race.jsonl | 0 | 109/0/0 | 7bc13f4736cfa62c175a37de92c15bb61f742a9a0f70a56c15958853f1969a59 |
| levara-t21-first-native-race.jsonl | 1 | 273/4/0 | fd962d0f13cb8d3799094525b6f705d0cc335cdfed9cdbda9f1d40b8614c9169 |
| levara-t21-native-repair-race.jsonl | 1 | 10/1/0 | 9388a0cd2b969741c688d7b4935616154b1562234ca46e84364db9eb5f0962c8 |
| levara-t21-second-native-race.jsonl | 1 | 281/1/0 | 51dc59b278e432ac073978dad093b18efd6bf7db7e5e6889aa2f8822e47a7d3c |
| levara-t21-third-native-race.jsonl | 0 | 286/0/0 | 0b3420c5dc8e9568ce87e3bdacc95fe9e8be0fe92fc852e9e5eeef561b294497 |
| levara-t21-incremental-race.jsonl | 0 | 165/0/0 | 744ecc785979cc2146c0a1cdc909d8246f9ec513ba3446e0d0048ca61281f08b |

First combined gate had six actual failing branches despite four unique leaf failures: four legacy/latest consolidation restore revision assertions across both dialects, PostgreSQL REAL confidence, and manifest COUNT ordering. Earlier failures also included SQLite duplicate placeholder arguments and inherited successful-restore timestamp oracles. All repaired source and explicit native-precision oracles passed the final frozen gate; failed hash/rollback checks were retained.

The earlier `npx openspec validate` invocation failed before validation because npm could not resolve an executable; validation is performed through the installed `openspec` CLI.

## Metadata continuity

Before evidence/task/issue metadata edits, functional map excluded only this change's tasks/evidence and the three roadmap metadata files: 1495 files, SHA256 `ece4345510fd754388d8e72be6038cd559a5bb11974bc61f9efc4cb638e22ba0`. The same map must match after edits; strict validation and diff checks are run again. No source mutation is accepted from metadata-only continuity.

Runtime checkpoint `c57faa9d-87a9-44a1-84f1-e67478127fbd`, native receipt `94496735-2b33-43c0-a401-b48efcd6260e`; both bound to the frozen revision. Criterion_complete=false.


## Active memory convergence — 2026-10-07

This phase accepts task 2.2, raising this change to 5/11. Original T21 remains OPEN for lifecycle/delete and independent collection status. The earlier 4/11 phase and its failures above are historical receipts.

Frozen native source revision: 2eb1dca16b0047918185760b41dcb22dee79090a+dirty:285ccdcfc2203da233a59df478791c3e105ae07a594439b5b977b74f3229cd9f (287 untracked); whole native before/after maps equal.
Actual Sync|Consolidation|MemoryIndex race gate exit0: 361 PASS / 0 FAIL / 0 SKIP; /tmp/levara-t21-final-active-native-race.jsonl, SHA256 f4b5fa03526420e3346721e6864c6b8e210fd67e9a3094434436fffc4cd8ad3c.
Native receipt 26d0019c-6c4f-43bb-a601-fa9c13b77d55. Independent bounded review receipt c4c56332-6013-4dbf-975f-a37b5f399ce6.

Imports order active content deterministically by parsed native-microsecond revision and fixed payload, preserve persisted scoped-key ID/CreatedAt, reject foreign physical-ID collision and retired-target replacement, and atomically queue index intent for the canonical ID. Changed value clears value-bound proof; same-value metadata retains it. Losing/identical replay is skipped; imported counts require actual commit. Export normalizes timestamp instants and native precision.

Blocked-provider tests prove a type-only update reuses the key/value embedding safely while final fenced publication uses current SQL type. Changed-value/owner/collection/key and retired-source guards remain enforced; newer digest work publishes the canonical ID. Pool-one SQLite/PostgreSQL tests cover concurrency, rollback, repeats/reordering, exact outbox and physical vector outcomes.

| Log under /tmp | Actual exit | PASS/FAIL/SKIP | SHA256 |
|---|---:|---:|---|
| levara-t21-type-race-first.jsonl | 1 | 0/2/0 | a9e339547d6715543e265911912e7c6e0fcfc23258283bca34a8c692b8895c1f |
| levara-t21-active-memory-first-race.jsonl | 1 | 69/5/0 | 8f688cb329f9b20acc1182a7267da7473dd8c166f2856059a217f3674858c2f0 |
| levara-t21-active-memory-second-race.jsonl | 0 | 361/0/0 | 86bec91ca8ddeb49d24e30dfb1e4f3479e7cc601a2e0a576e9223c820c9cce81 |

The first expanded gate retained old type-only assertions, PostgreSQL timestamp-wire differences and a SQLite proof-seed placeholder-order fixture error; native current-type/timestamp oracles and verified QArgs proof seed repaired them. The development pass overlapped unrelated T22 writes and is not a frozen whole-tree receipt. Final native proof above is frozen.



## Combined integration failure history

The first frozen S0 gate exited2 with36 PASS/1 FAIL/0 SKIP because of a broken central-evidence self-link; /tmp/levara-t21-t22-final-test-commit.jsonl, SHA256 fb3c11586973f7bbeb1ef7453609e52f0eb4799b7ff86d17c6739063d85645fc, receipt46e7472d-6e20-4795-ad3d-58d120231042. Only that metadata link was repaired.
The restarted frozen b7000fed gate exited2 with4152 PASS/1 FAIL/2 opt-in SKIP. Only the immediate SQLite concurrent pool InUse oracle failed; S4 did not run. /tmp/levara-t21-t22-final-repair-test-commit.jsonl, SHA256 cd0b5ad71704b8577b1dfba8f1d8190f088101e2f4543d6a3aaae847cfa9dc40, receipt6142c1c5-455c-44c0-8639-2c6c9d85b536. Both whole pre/post maps equal.
The test-only repair reserves the entire native two-connection pool simultaneously and retains two idle connections before workers start, preserving strict final zero-InUse and convergence assertions. database/sql includes pending opens in InUse; raising MaxOpen alone retained the previous MaxIdle ceiling. Actual race count20 exit0:40 dialect leaf executions, no FAIL/SKIP or failed parents; /tmp/levara-t21-pool-oracle-repair-race.jsonl, SHA256 24cc9c02e53b8e7dec87f74505d8c527a0d98de04c95befd72b3f2e8f90ee6c8, receipt17d142d4-512f-4fb1-9f30-39246cc15f87.
Independent source/functional-map review confirmed ONLY sync_convergence_test.go changed: prior native production/backup receipts carry forward by exact byte continuity; no old failed gate is relabeled passing.

The next frozen full command hit the Go HTTP-package aggregate default10m budget: exit2,4125 completed PASS/0 completed FAIL/2 SKIP, but package FAILED and watcher PostgreSQL branch aborted after only1s. /tmp/levara-t21-t22-pool-repair-final-test-commit.jsonl, SHA256 d94be4319384c704817d4fdb27f9dc9bb2edd188fab7a5a62a6466f0ab9b1e74, receipt f4f0f0bc-5d6f-4bcf-8d4c-3780609dd644. Whole before/after513397 revision equal; this is not a passing gate. S0-S2 were successful on that identical revision. Only S3 is rerun with20m aggregate budget, per-case deadlines unchanged, and S4 runs afterward. Acceptance is assembled from actual per-stage commands on the same frozen source, never a fabricated make exit0.


## Final assembled integration acceptance — 2026-10-07

Frozen revision: 2eb1dca16b0047918185760b41dcb22dee79090a+dirty:5133979cef892f799b39023a7446f6d04330fb2222c8751725460932cca27cb7. Whole before/after maps agree; no source edits during commands.
S0–S2: nine packages passed, 1713 leaf PASS, from /tmp/levara-t21-t22-pool-repair-final-test-commit.jsonl. That whole command failed on the later HTTP aggregate budget and is not relabeled passing.
S3: actual separate command exit0, 2440 PASS / 0 FAIL / 2 opt-in SKIP; /tmp/levara-t21-t22-final-s3-budget-repair.jsonl, SHA256 75fc0534e8fae4694ee322b1db56505e53fef17124118badb59106a8b82ee16b.
S4: actual separate command exit0, 188 PASS / 0 FAIL / 0 SKIP; /tmp/levara-t21-t22-final-s4-budget-repair.jsonl, SHA256 21f4a2bdea8411ade994538bc53b9b3073fed7e1a0e6406a6d45d861411813c0.
Assembled S0–S4: 4341 PASS / 0 FAIL / 2 opt-in SKIP on identical source; no failed parents in successful stages. Go commands used -p=1 -ldflags=-w -count=1; S3/S4 used -timeout=20m. All test commands exclude ^TestMemoryREST per user instruction.
GOFLAGS='-p=1 -ldflags=-w' make contract-check: actual exit0, /tmp/levara-t21-t22-final-assembled-contract.log.
Both OpenSpec changes passed installed CLI strict validation on the same revision (receipt374f64f1-d204-4724-885e-b03c0cea1162).
Functional map: 1504 files, SHA256 e09d6665635e8685f8f77aee205dbdab2b53ec06f848841c03b2da07985fe6fe. Only the seven acceptance metadata files are excluded. Native backup and production sync files retain exact bytes from original native receipts; only concurrent pool test warmup changed, separately proved by40 race executions and independent review.
REST and MCP sync dispatch were source-audited: verified live active global-superuser authority precedes remote I/O/imports; ordinary write credentials cannot invoke instance replication. No REST owner-spoof reproduction was performed.
