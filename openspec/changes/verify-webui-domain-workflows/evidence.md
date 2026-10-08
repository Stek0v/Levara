# T30 evidence — accepted 2026-10-07

Original T30 is accepted for the declared local product scope after current gates and independent review; roadmap23/32. The dated development observations below preserve historical failures and pending states. Final acceptance is recorded at the end.

## Imported-chat mocked checks — 2026-10-07

Preflight: Node22.22.3, installed Chromium1217 executable, free loopback Next port3022, explicit mock backend origin127.0.0.1:1. No live server target.

Root command:
`LEVARA_API_URL=http://127.0.0.1:1 PLAYWRIGHT_PORT=3022 ./node_modules/.bin/playwright test imported-chats.spec.ts`

First actualexit1:5passed/6failed; log `/tmp/levara-t30-chat-mocked-first.log`, SHA256 `16526e80e136b2b9578a5822fbaf317bc0b60630ac94c4973191e3b5ec7817a9`. All six failures were strict selectors matching the component alert and Next route announcer. The observed error texts were correct. Only alert selection was scoped; expected denial, transcript clearing and ACK guards were retained.

Fixed actualexit0:11passed/0failed; log `/tmp/levara-t30-chat-mocked-fixed.log`, SHA256 `08c70ddfcae9de5fa8dce5fb68bc67697fcc8b634e99ef7f1048e6bf46c6aba7`. Covers exact encoded canonical identities, attach/detach acknowledgements, null/empty, failed list/retry, revoked focus read, account change, delayed selection/mutation and invalid ACK. Route mocks prove browser state only; native backend sharing/revocation is still pending.

Frozen relevant hashes: component `95e407f7cb44980b4413cf51e4959d74fcdfede76ff8d99180bcc824fad75424`; account chat page `7efc8957938b6c0502696f84457bf95b28752c20b662c4066c4e3059064dcab1`; current API `b70485da9f0cda9c2a3a24e2f8fe7e3b15e0e53d811c47e78f1efe8164d4501d`; fixed mocked spec `a198b05da4aa4d48d7fa894d60dddacf4953a1cbff505387a619dd214088cf23`.

## Historical development gaps

Workspace/task/sync browser fixes and focused checks are being integrated. Native browser suite, lint/build/curated suite, approved release artifact and final independent wholeT30 matrix remain required. No publish/deploy/live migration or user-forbidden TestMemoryREST reproduction is authorized or performed.

## Subsequent observed gates — 2026-10-07

The preceding phase is historical; native chat sharing and release packaging have since passed. Whole T30 remains open.

- Actual imported-chat account/cached-bearer/held-RAG checks: session70251 exit0,13passed; log `/tmp/levara-t30-chat-account-final.log`, SHA256 `fef5bdaf1c14e8b854ac2c0ed6951b790231da07cc52c1ed67c98ac915873957`.
- Earlier curated session45906 exit0,96passed, SHA256 `93acd29644ff4f6b7e20abb77e9a6781d25cfb1dc34ad6ab0e99b6ef0f1106c0`; precedes the latest workspace denial and query-domain additions, so it is not final acceptance.
- Workspace read/search denial invalidation is integrated. Focused14case session28922 exit1,12passed/2failed; log `/tmp/levara-t30-domain-repaired.log`, SHA256 `de7bc136c3e3d6628957e27f7d31f2483f262cd0c41826922a5263f7a4f1f2a3`. Task initial detail and workspace cached jobs alert were not observed. Concurrent edits to other routes occurred during this development run; no root cause is assigned from the log. Unchanged focused rerun session7940 exit0,2passed; log `/tmp/levara-t30-domain-rerun-two.log`, SHA256 `b11c226074899cdd114e1243d0d287ba60419519bf0ab0faa4cfe4146f1e14a8`. Full frozen curated repeat is running.
- Native private server: release binary, authenticated standalone profile, SQLite `/private/tmp/levara-t30-backend/data/levara.db`, workspace derived from private data directory, API19330, Next3022; no existing service touched. Health/config and actual identity verified.
- Native chat session43401 exit0,1passed: actual owner consent, admin grants/revoke, colleague403 and browser clearing, admin detach, private owner retention/reload/logout/account change. Log `/tmp/levara-t30-native-fixed.log`, SHA256 `03772c3e21667a841e77597b2fa680e90135e3d7a13fef8fd6ff563bcf1b482e`. Earlier failed logout mouse action was obstructed by Next dev indicator; ordinary keyboard activation passed. Extended current native memory/workspace cases are pending.
- Actual release session94817 exit0; exact inventory inspection exit0: six binaries, seven profiles and LICENSE only. Archive `/tmp/levara-t30-release.tar.gz`, SHA256 `9942e76f628110d3c3eb77ea5a10907ffa51b60891c7aa29a0614d07e4a3fff0`. First successful build had forbidden AppleDouble metadata; retained failed inventory archive. One packaging line sets COPYFILE_DISABLE. No publication/deployment.
- Contract-check session74462 exit0 with mandatory TestMemoryREST exclusion. Repaired lint94099 exit0,0errors/two cleanup-ref warnings; repaired build38429 exit0. Later query lint12604 exit0 precedes final Search ref guard, so final current lint/build remain required.
- Independent query review found concurrent Enter searches could apply a late successful result after a newer403. Serialized submission via synchronous ref now blocks concurrent requests; held-response/next-denial regression added. No new dependency.
- Frozen UI revision before current curated: `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:e4b325cc77017393aef1d37179ec1cd97aa4c79c370795700d1477b7cf148fe0`. Evidence metadata updates are excluded from any claim of unchanged whole worktree; source hashes and observed logs must be rechecked for final acceptance.

## Development observations — 2026-10-07

- Repeated full curated gate31532 exit0,115passed; SHA38da8608c37ee0d8c7ac9997a573004e2e81cec97301aef2c4500079cf9c0569. Latest graph wire adapter gate68285 actualexit0,115passed; /tmp/levara-t30-curated-graph-adapter.log SHA1f2efe87d3926ab5e7982f66bb519b31ab485f448d9722e839ae90c663de4de3. Task receipt802325ae-f5d7-4c4c-8563-89b20858bc23 explicitly partial, criterion_complete:false.
- Native three-domain gate33535 exit0,3passed: real chat admin sharing/revocation, exact workspace CAS/draft/no-overwrite, memory room/hall reload and ID deletion. SHAa15c9e914abdd66eebc2bf14c2acb3a4ba18cbaf7be3f398613f447bcc4b9711.
- Native Graph first failed UI name/label DTO; repaired adapter then exposed actual revoked-project Graph GET200. Historical logs /tmp/levara-t30-native-graph-first.log SHA63ef60263e49cb4d99b1bf2155f44e92e6b1f4df4d6659028ca35e836b191c23 and /tmp/levara-t30-native-graph-repaired.log SHA7bb9d96e3aebdb172cea86c9e7fca15fe6b153eb8e1aa4c989b719059d692800. Backend fix uses existing credential/dataset/tenant and individual publication/document checks, same SQL transaction, actual response Close fence.
- Task read gate23497 actualexit0,23leafPASS/0FAIL/0SKIP bothSQL; /tmp/levara-t30-task-read-expiry-fixed.jsonl SHAe1ba5eb1487fb5ba8b40ab3aca9b8bdd5b7fddd150c0072ed0612dd9a0db7812. Independent source review accepts owner/shared contract and held-body expiry; no separate selected-tenant test matrix claimed.
- Combined Task/Graph/SQLGraph gate67618 actualexit0,35leafPASS/0FAIL/0SKIP, both packages passed; /tmp/levara-t30-graph-task-authority-pointer.jsonl SHAc83611671d824182fd02b73d59a3b3a70f40e06001e7022c9fcded133664ab99. Earlier54992 and78548 build failures retained (misplaced transaction call, recursive value config); corrected without unrelated import edits. Subsequent formatting only.
- Graph mock focused33906 exit0,5passed; /tmp/levara-t30-graph-wire-mock.log SHA795fc92de8aab58b4a9948336758bc66f8613ecb434c25fc09345a40877d8c96. Current adapter build2704 exit0 SHA9e8d96656bd2354c85c18d91d442344361d3687aa7b1071fde9d7c60421194c4. Lint retry55858 exit0, two cleanup-ref warnings. Initial parallel lint encountered test-results directory deletion by Playwright, retained failed log; sequential repeat succeeded.
- Rebuilt six-binary release13059 exit0, exact six binaries/seven presets/LICENSE inventory inspection0; /tmp/levara-t30-release-graph-authority.tar.gz SHA306fd988b119aba6919ec348cafbc1b73b86c3965f3338ec5aa9046769df66d3. Private backend restart only, Task runtime enabled, dim2 local deterministic embedding fixture; no real model-quality claim.
- Native six gate73408 actualexit1,4passed/2failed. Chat/workspace/memory/Graph passed, including actual Graph revocation403 and hidden browser properties/path. Task fixture omitted latest MCP transport metadata/headers; corrected. Document upload/index completed and persisted canonical per-inclusion status. Initial missing Processed expectation was later disproved as a locale oracle error: actual authenticated API returned current canonical COMPLETED and default user locale was Russian. No backend status fix or legacy fallback was retained. Health/private database/Task tool availability now verified using actual required protocol headers. Failed preflight transport requests retained; first native launch mistakenly preceded successful Task-tools preflight.

Later native Task focused29181 failed only the exact inline text locator; screenshot and actual REST showed failed/active steps and lease. Updated listitem oracle, canonical transport metadata/header and real task_receipt fixture plus read-only criteria section now pass in six-workflow gate52389:5passed/1failed. The remaining document failure occurred after successful native upload/reload/exact download, explicit collection search, restricted policy activation, CAS409/refresh/retry and colleague200/exactbytes; loose grant selector resolved two revoke buttons. Exact recipient/role locator corrected for final repeat. Latest user search UI accepts known collection on operator-inventory403, preserving server result authority and401/404 failure states; new mocked check added. No inventory endpoint permission was broadened.

At this development checkpoint those gates were pending and roadmap22/32. The final results below supersede these pending statements.


## Final original T30 acceptance — 2026-10-07

Frozen tested revision: `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:87dc57e2cac1f030d06f5136f68e1d9871f01efb52948654dcbe8f8a524532db`. All source and documentation were unchanged through the expanded Go repeat; only acceptance metadata is updated afterwards. No publish, deployment, production restart or live migration. User-forbidden TestMemoryREST reproduction remained excluded.

| Gate | Actual result | Log/artifact SHA256 |
|---|---|---|
| Curated browser, session81382 | exit0,116passed | /tmp/levara-t30-curated-criteria-search-final.log; 5a3fce5e7bcdf76972fd5433a6795977fc596b9d1e38793f13b9515c879beac3 |
| Native browser, session3880 | exit0,6passed | /tmp/levara-t30-native-six-final.log; bf6b18b02ac4d975bbe25145c8e462e49a167242487452b7c319e64f5b110120 |
| Lint20013 | exit0,0errors,2cleanup-ref warnings | /tmp/levara-t30-lint-criteria-search-final.log; bccfb9273692e0e5f3ef5a66156f3e1e5cc8c007ca2eaf990f41c6667f3b0532 |
| Build16648 | exit0 | /tmp/levara-t30-build-criteria-search-final.log; 86fe1956d6a91f2f998af0059f844335262f54d3e0fc03b6da1e7249c6ff119a |
| OpenSpec strict6666 | exit0 | /tmp/levara-t30-openspec-final-strict.log; 5904f9525f848a84baa02b7df2a12852c3a8e525b019fba67dceb0339e8afca7 |
| HTTP/graphstore/server73941 | exit0,2763leafPASS/0FAIL/2SKIP; all3packagesPASS | /tmp/levara-t30-http-graph-integration-20m.jsonl; 8d701e1ae510130784c3acd903103e9713e8048b62c99a4318403610e6af5184 |
| Current full/core contract20123 | exit0,quiet command | /tmp/levara-t30-contract-final.log; e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855 |
| Release13059 + inventory | buildexit0,exact six binaries/seven profiles/LICENSE | /tmp/levara-t30-release-graph-authority.tar.gz; 306fd988b119aba6919ec348cafbc1b73b86c3965f3338ec5aa9046769df66d3 |
| All six binary metadata | inspectionexit0,go1.27.1/darwin/arm64,HEAD2eb1dca+dirty | /tmp/levara-t30-binary-metadata-final.json; 127f6de4c3edbc81d9e30412cb4cea590c3059a357d8dbcbebe478ffa164f672 |

Expanded Go command: `LEVARA_TEST_POSTGRES_DSN=postgres://levara_test@127.0.0.1:63530/levara_roadmap_test?sslmode=disable GOFLAGS='-p=1 -ldflags=-w -count=1 -skip=^TestMemoryREST' go test ./internal/http ./pkg/graphstore ./cmd/server -timeout=20m -json`. PostgreSQL readiness was verified before launch. The two existing opt-in skips are TestDCDVSALoadBaseline and TestT11NativeRAGLocalQuality; neither is passing evidence. Earlier12m gate88151 exited1 on the suite alarm while the current test had run12seconds:2043leafPASS/0FAIL/2SKIP; log /tmp/levara-t30-http-graph-integration.jsonl SHA7bdc6464e10a55e2dba0a759579608ffac88bfef4b0d7c5c890a609a30aa6793. Only overall suite budget was increased; per-case assertions/deadlines stayed intact. The repeated run used the same frozen revision without concurrent frontend work.

Native six workflows use actual authenticated private SQLite/API19330 with no route mocks: owner chat publication/admin audience/revocation/private retention/account reload; exact workspace CAS and preserved draft; memory room/hall reload and deletion; real MCP Task plan/claim/receipt and visible criteria/failed steps/lease/foreign-owner denial; document upload/index/reload/exact download/search/grants/CAS409/revoked peer denial; Graph properties/path followed by actual grant deletion/GET403 and hidden browser data. A deterministic local embedding fixture proves plumbing, not model quality. PostgreSQL authority/time/fencing parity comes from separate native Go gates, not SQLite browser inference.

Curated mocks additionally prove empty, denied, unavailable, delayed/partial outcomes, settings/locale/profile states and account resets. Operator-only diary/history/consolidation/temporal Graph/executor/backup/source-distillation remain documented supported surfaces. External AD/IdP, model quality, Neo4j atomicity and deployment remain separately gated.

Independent reviewer accepted original T30 after independently parsing the final backend log and checking native6/curated116/release scope. No remaining blocking finding. All8change tasks and originalT30 may be checked; no other roadmap criterion is completed by this receipt.
