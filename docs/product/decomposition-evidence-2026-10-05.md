# Проверки вех декомпозиции

Current T21 acceptance (2026-10-07): native race531/0/0 and frozen S0–S4 whole4539/0/2 pass; all11 change tasks and original behavioral DoD accepted, roadmap22/32. Final evidence is in the T21 full acceptance section below. Earlier bounded gates remain historical.

Historical bounded phase: T21 lifecycle bounded repair (2026-10-07): native race319/0/0 and actual frozen S0–S4 whole4501/0/2 pass; generated full/core contract checks repaired through standard generators. [Current lifecycle evidence](../../openspec/changes/repair-independent-sync-convergence/evidence.md) retains failed gates and exact hashes. Known-ID deletion replay, retirement links/canonical no-self-reference and native Supersede SQL/outbox are covered. Full generations/aliases remain open: T21 6/11, roadmap21/32 unchanged.

Начало: 2026-10-05; приёмка T06 и T07: 2026-10-06 (Europe/Moscow). [Backlog](decomposition-tasklist-2026-10-05.md), [отдельные проблемы](decomposition-issues-2026-10-05.md).
Base HEAD: `2eb1dca16b0047918185760b41dcb22dee79090a` плюс uncommitted manifest ниже. Окружение: Go 1.27.1, darwin/arm64, SQLite и изолированная PostgreSQL 16.15 test DB на loopback. PostgreSQL fixtures создают/удаляют собственные schemas; production DSN не использовался. После ранней combined-проверки собственный PostgreSQL был остановлен с exit 0. Для дальнейших T06/T07 тестов тот же отдельный экземпляр снова запущен; port 53350, database/user/version проверены. Позднее прежний экземпляр заменён отдельным PostgreSQL на port63530 для текущих T24 проверок; production DSN не использовался.

## T01 — матрица capabilities

[Матрица](capability-matrix.md) сопоставляет MCP legacy/latest, REST, gRPC v1/v2, CLI и stdio bridge; product profile, functional bootstrap и MCP toolset описаны отдельно. Для интерфейсов зафиксированы аргументы, defaults, verified caller, response/error shapes, backend/flag gates и отсутствующие поверхности. Полные schemas остаются в descriptors/proto/handlers, а не копируются в документацию.

Наблюдённая команда:

```sh
go test -count=1 -v ./pkg/mcp ./internal/http ./cmd/contract -run 'ToolDescriptors|ToolProfiles|Toolset|MCPLatest|TestTaskToolProfileFeatureFlag|TestRESTRouteInventory|TestSchemaInventoryCoversCoreTables|TestCollectIsDeterministic|TestRenderJSONByteIdentical|TestRenderMarkdownByteIdentical|TestValidateDetectsDrift|TestRewriteAgentsMD'
```

Exit 0, 34 top-level tests, zero skips. Реально выполнены personal default/explicit override/unknown toolset checks, выключенный task flag, stateless set_context и metadata/auth gates, inventory и schema coverage, generator determinism и drift checks. Raw log: `/tmp/levara-v0-contract-checks.log`. Отдельно `make contract-check` завершился с exit 0 без generated diff.

Независимое source-to-matrix ревью подтвердило документальный DoD. I05/I10/I11/I12 остаются source candidates с отдельными runtime regressions: успешный inventory test не означает исправление dispatch binding, полноту conditional REST router, error parity или CLI exit status.

## T02 — контракт осей памяти

[Модель](memory-model.md) отделяет collection/owner/key identity от room/hall/type/tier/pin/provenance/history, объясняет six public halls и совместимые пустые значения, shared read/mutation policy, SQL truth/derived index, chunk tags и REST/MCP различия. SQLite и PostgreSQL DDL сверены как одна модель; это не live upgrade старой базы.

```sh
LEVARA_TEST_POSTGRES_DSN='<isolated-test-DSN>' go test -count=1 -v ./pkg/mcp -run 'TestMemoryWriteEvidence|TestMemoryProvenanceSurfaces|TestRecallHistoricalCandidates$|TestToolMemoryPinCollectionScope|TestSupersedeTrustSharedAuthority'
LEVARA_TEST_POSTGRES_DSN='<isolated-test-DSN>' go test -count=1 -v ./pkg/mcp -run '^TestToolMemoryIdentityRoomHall$'
LEVARA_TEST_POSTGRES_DSN='<isolated-test-DSN>' go test -count=1 ./pkg/mcp
```

Все команды завершились с exit 0. Parity suite выполняет обе SQL, zero skips; log `/tmp/levara-t02-parity.log`. Текущий identity regression выполняет SQLite/PostgreSQL branches, zero skips; log `/tmp/levara-t02-current-identity.log`. Fresh полный MCP пакет после последней правки теста: PASS, 23.540s; log `/tmp/levara-roadmap-mcp-current.log`.

Новый минимальный regression сохраняет три одинаковых key в разных owner/collection identities. Изменённые room/hall обновляют одну canonical row без изменения ID/created_at; explicit empty и omitted classification очищают непустые значения; unknown hall не меняет ни одной строки. Старые sentinel timestamps исключают same-second ложный PASS; sibling rows и timestamps сравниваются целиком. Независимое ревью выявило эти два первоначальных proof gaps, они исправлены и обе SQL повторно прошли. Ошибка повторного SQL placeholder в усилении fixture исправлена до GREEN и не считается production defect. Production save code не менялся.

Полный MCP пакет включает hall/save/recall/list и chunk metadata tests. В T02 не заявляются semantic index readiness, внешняя model quality, безопасность известных REST/consolidation mismatches или отсутствие всех будущих defects. Эти границы записаны отдельно.

## T03 — bootstrap, профили и ограниченный executor

Исправлены два существующих skills, memory-policy, EN/RU memory-workflow/runtime guides, authority manifests, profile presets и features guide. Руководства различают legacy session bootstrap и latest explicit collection, current core supersede/delete, advertised personal binding и реальные prerequisites bounded workspace worker. I05 raw-env initialize/dispatch mismatch остаётся открытым и обозначен явно.

Наблюдённые проверки:

- Два запуска `python3 /Users/stek0v/.codex/skills/.system/skill-creator/scripts/quick_validate.py <skill-folder>` для memory-workflow и run-long-task: exit 0.
- `go test -count=1 -v ./docs ./cmd/server -run 'Docs|Profile|ConfigCheck|Task'`: exit 0, 14 top-level tests, zero skips после последней guide правки; log `/tmp/levara-t03-current-docs.log`.
- `make profile-config-check` и `make contract-check`: exit 0.
- Runtime command с isolated PostgreSQL: `go test -count=1 -v ./pkg/mcp ./internal/http -run 'TestToolProfilesAreExplicitAndBackwardCompatible|TestTaskToolProfileFeatureFlag|TestTaskExecutorContractAndVisibility|TestTaskExecutorThreeObservableWorkspaceSteps|TestTaskExecutorDeniesBeforeWorkspaceEffect|TestEffectiveMCPToolset|TestConfiguredMCPToolDescriptorsPersonalBinding|TestMCPLatestContract'`: exit 0, 11 top-level tests/46 runs, zero skips; SQLite/PostgreSQL executor branches PASS, raw log `/tmp/levara-t03-runtime.log` прочитан основным агентом.
- 76 локальных Markdown references в десяти итоговых файлах проверены на существование; JSON/code-fences, skill metadata и whitespace checks PASS.

Независимый source-to-guide reviewer проверил toolsets, статeless routing, SQL/full/runtime/worker prerequisites, task/manifest/workspace authority, live lease, directory confinement, DevMode denial и Linux/macOS scope. Найденный stale logging-executor текст в features guide исправлен; финальное ревью не имеет substantive findings. Production code и AGENTS.md в T03 не менялись. Реальный IDE handshake и Linux deployment здесь не проверялись и не заявляются.

## Общие проверки и следующие вехи

Последний `make test-commit`: T07 S0–S4 green с JSON-прогрессом, raw log `/tmp/levara-t07-final-test-commit.log`; full MCP/store/server свежие, HTTP reused после свежего полного PASS 189.375s, 1504 tests / 1 opt-in DCD load skip. Все 113 hashes final manifest оставались неизменны; acceptance-only docs проверяются отдельно. Подробности и исходные failed/diagnostic attempts: [T07 evidence](../../openspec/changes/align-memory-digest-evidence/evidence.md). Предыдущий T06 gate был S0–S4 green после полного index lifecycle, raw log `/tmp/levara-index-final-test-commit.log`; MCP 24.248s, store 26.953s, HTTP 159.690s, server 9.752s freshly, неизменённые прочие пакеты cached. Все 102 dirty files manifest остались неизменными во время gate. Свежий объединённый HTTP race: 11 top / 77 RUN/PASS / 0 skips, 15.342s; outbox/native race: 58 RUN/PASS / 0 skips, 2.785s/1.471s. Поздние acceptance/docs изменения проверены отдельно. Предыдущий owner gate `/tmp/levara-consolidation-final-test-commit.log` относился к своему revision. Targeted consolidation/hall race: 45 top tests / 193 runs / 0 skips, SQLite и PostgreSQL; focused SQL acceptance выполнен без skips. Предыдущий gate `/tmp/levara-roadmap-current-test-commit.log` и приведённые ниже manifests относятся к своим историческим revisions; overlapping current hashes записаны в owner lifecycle evidence. Broad gate не доказывает external/load acceptance: opt-in DCD baseline остаётся отдельной проверкой.

- T04: [pin/unpin evidence](../../openspec/changes/scope-memory-pins-by-collection/evidence.md).
- T05: [context RED/GREEN, auth/race и source manifest](../../openspec/changes/scope-project-context/evidence.md).
- T06, первый шаг: [SQL Boolean parity и apply/revert rollback](../../openspec/changes/fix-consolidation-postgres-boolean/evidence.md).
- T06, второй шаг: [owner/classification, guarded apply/revert, provider/async/recovery и актуальные hashes](../../openspec/changes/scope-consolidation-owner-lifecycle/evidence.md), 11/11 bounded подзадач.
- T06, третий шаг: [current-state fence, requeue, deferred shadow, полный indexed recall/revert, hashes и финальные gates](../../openspec/changes/repair-memory-index-lifecycle/evidence.md), 7/7 bounded подзадач.
- T07: [receipt-backed digest, reciprocal retirement attribution, historical tests и I27 native fixture repair](../../openspec/changes/align-memory-digest-evidence/evidence.md), 3/3 bounded подзадач; original T07 DoD принят отдельно.
- T01–T07 закрыты по своим DoD (7/32); T08 и последующие задачи остаются открытыми до собственных проверок. Полный backlog из 32 задач не завершён.

Levara MCP первоначально отсутствовал, затем bootstrap и runtime stats подтвердили восстановление. Runtime Task `41e33fda-3ca8-4805-a59a-8281f233f9b9` содержит оставшиеся T06–T32; свежие root race команды получили реальные receipts, ранние проверки не выдаются за leased execution. Это локальный evidence view; полная roadmap completion не заявляется. Commits, push, deployment, live migration и внешняя публикация не выполнялись; unrelated worktree edits сохранены.

## Verified manifest T01–T03

```text
02888ac96d6ba691963482a7938a102bab0158f6e94a2b37a01129989f89e9e0  docs/product/capability-matrix.md
d23e6851ba3645c99a7d507fd4b0e29b61a4a0d32a20b7538ffdcd2498ebd61e  docs/product/memory-model.md
f8a6e1e38b2f5c362b7773f166364f7456c24d0f50cc69c4ebb171cda056a987  pkg/mcp/tool_memory_identity_test.go
c78171dd95a134418224a17d219c04f63a4c6d2666aeb64ee71b27ed2857f864  .agents/skills/levara-memory-workflow/SKILL.md
88c81378d8cbc65e700ec2a25e4151e90a4124df398582795664d1f3e8ce584e  .agents/skills/levara-memory-workflow/references/memory-policy.md
a9206a165051cab774317faa2502ab9a4b79a7955d30fdff2acc90015acf711e  .agents/skills/levara-run-long-task/SKILL.md
76b310f37848e6189db21c7264fd0eea7a7d235cf2ca5644f68ad0dc93b7ba40  docs/memory-workflow-skill.md
862552fa328f10482739c8ed41892e2c392aa1623ed9d5131a5e58baa46786db  docs/memory-workflow-skill.ru.md
00d4b554ad227ce176e20c6f808ae4c36af98bbb1187548ad79eedfa99f7aaf6  docs/long-horizon-runtime.md
0f586c05084acd4448b4bc5aa547d6296637c15f5137c1996255c12d21c4065c  docs/long-horizon-runtime.ru.md
5600488db541651a17a5e35508e1c97ad1c2c85a9fcd202b34b756889fb5b754  docs/authority-manifests.md
d72491fa7581f686715f6851629a54e857280125cc16ba3ac3d9dd290e8d9d4f  docs/profile-presets.md
8e8415fcfd9b8b991055295210e2a984aca94f10916312598e38df137e0e9a1b  docs/features-guide.md
```

## T09 — search strategy dispatch: current evidence 2026-10-06

T08 accepted separately; T09 accepted 2026-10-06 after the full checks below, advancing the original roadmap to 9/32. Scope: MCP unsupported strategy/mode rejection, BM25 lexical alias, retrieval-only AUTO, honest degraded MULTI_QUERY/RERANK/GRAPH_RERANK labels; PARENT_CHILD explicitly retains its selected algorithm label during native missing/empty-child vector fallback. REST router/unknown compatibility and rerank defaults are unchanged. MCP capabilities now require EmbedClient for vectors, and REST unavailable RAG names its abstaining strategy.

Observed before/current checks:

- Dispatch baseline RED: exit 1, 44 failing leaves/48 nodes, 0.950s; `/tmp/levara-t09-dispatch-red.jsonl`. Unsupported labels called vector pipeline; AUTO reported missing graph/RAG/summary paths. Intermediate label fix runs are preserved separately.
- Reviewer-found empty-ACL RERANK inconsistency RED: exit 1, two leaves/three nodes, 0.819s; `/tmp/levara-t09-dispatch-emptyacl-red.jsonl`.
- Final dispatch GREEN/race: each exit 0, 108 leaves/117 nodes, zero skips/failures/races, 0.706s / 2.052s; `/tmp/levara-t09-dispatch-{green-reviewed,race-reviewed}.jsonl`. Earlier full MCP exit0 47.825s precedes empty-ACL repair and is not final integration proof.
- Root availability baseline overlay RED: exit 1, two test failures, 1.085s; `/tmp/levara-t09-availability-red.jsonl`. Missing EmbedClient advertised vectors; unavailable REST RAG omitted strategy. Overlay restores only the two corresponding previous source files.
- Real JWT transport GREEN/race: each exit 0, 88 leaves/97 nodes, zero skips/failures/races, 2.989s / 6.980s; `/tmp/levara-search-transport-parity-{green-final,race-final}.jsonl`. Both native SQLite/PostgreSQL × legacy/latest × core/full: discovery, text/structured schemas, allowed hit/private hit exclusion, BM25 noembed, AUTO HYBRID and missing-client lexical, unsupported labels/graph mode provider tripwires.
- First transport fixture runs failed before intended checks because preparation lacked a deadline, then latest requests lacked Accept. Logs preserved; these failures are not product regression RED proof.
- Root current affected integration GREEN: MCP123leaves132nodes0skip0fail0.675s; HTTP246leaves265nodes0skip0fail10.882s. Same selection race: MCP2.423s, HTTP33.573s, same counts, zero skip/fail/race. Logs `/tmp/levara-t09-search-integration-{green,race}.jsonl`.
- Root affected selection: `go test -p 1 -count=1 -ldflags=-w -json ./pkg/mcp ./internal/http -run 'Search|Strategy|Rerank|OutputSchema|HTTPBackedToolOutputs|^TestWorkspaceMCP|^TestWorkspaceSearchHonorsProjectRBAC$|^TestGlobalSearchRejectsBeforeEffectAndRechecksDemotion$|^TestDocumentEgressRechecksGrantAndCredentialBeforeModel$' -skip '^TestMemoryREST'`; race adds `-race`. Exact isolated PostgreSQL DSN on port53350 supplied. No paid provider or further REST memory-owner probe.

Independent actual source review closed both bounded findings after empty-ACL normalization and ParentChild descriptor/guide qualification; no new must-fix. Transport source/evidence independently reviewed; provider fixtures prove dispatch/authorization rather than model quality. Gortex guarded mutations observed on disk; impact/detect graph traversal can be stale/truncated and is not exhaustive.

Final dispatch hashes: tool_search.go `fcb6f33f510fb1400ebe4d809d2e4cccac2b2b7b371043361699ecfe1ca59009`; tool_search_test.go `6bb75b9912512e836a98382d7b31a6db696aee5c6a36cf5cd654df89013df653`; dispatch test `d55bf8328c97427d05952c778d161125b49f720f766c5264df5aa4499f79ff82`; public transport test `719c9b66feabfadd80e572de62b8cea579f62cd20f61d7dc8b79a528feeb46b4`.

T09 acceptance: sequential generated full/core contracts and drift check each observed exit 0. Public docs/profile/contract tests passed (55 leaves, no fail/skip); strict all ten OpenSpec changes passed. Generation logs are silent; actual root tool exits supply command evidence. Independent final source/artifact review accepted T09 with no technical findings. Search quality, T10 provider/filter corners and downstream roadmap tasks remain separate.

Final T09 stable S0–S4 root session68671 exited 0: 3724 PASS nodes/3155 leaves, no FAIL. Full MCP47.886s/HTTP211.252s/server11.454s. Only optional `TestDCDVSALoadBaseline` skipped; `^TestMemoryREST` explicitly excluded by user instruction. Command used isolated PostgreSQL port53350 and `GOFLAGS='-p=1 -ldflags=-w -count=1 -timeout=20m -skip=^TestMemoryREST -json=true' make test-commit`. Log `/tmp/levara-t09-current-test-commit.jsonl`, SHA256 `39379c645807c76bb5b3fed2ffbda52a75beb278197e0bf2ab3cd6590f8cfc59`.

Pre/post full manifests independently match: `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:d81f5833930e8cc58f1e4bee7e73b737945810d7c3f708c0fde547ba04451797`, 117 nonignored untracked files plus binary tracked diff. Manifests `/tmp/levara-t09-{pre,post}-gate-workspace.json`. Subsequent acceptance ledger edits are documentation-only; static/docs checks repeated after them, with no later code mutation attributed to the earlier gate.

## T10 — текущая реализация и отдельные доказательства, 2026-10-06

T10 принят 2026-10-06 после проверок ниже; исходный roadmap теперь 10/32. T11 и последующие задачи остаются открытыми.

- Embedding breaker: source-confirmed caller cancel/deadline/guard poisoning и concurrent half-open reproduced baseline overlay RED exit1: 4 failing leaves; healthy provider не достигался после трёх caller failures, half-open пропускал пять лишних calls. Полный pkg/embed GREEN exit0 — 44 leaves, 3.802s; race exit0 — 44 leaves, 5.123s, 0fail/skip/race warnings. Root прочитал raw JSONL и физический scoped diff. Фикс отделяет provider origin, резервирует одну probe после admission, освобождает abort/cache/guard probe и игнорирует старое generation completion. Логи: `/tmp/levara-t10-embed-{red,green-fixed,race}.jsonl`. Первый GREEN был остановлен из-за cleanup held httptest fixture; это не выдаётся за product regression.
- Admin dual search: root baseline exact-source overlay RED exit1 — wrong same-dimension encoder дал b-answer score0, precanceled request вернул answer. Current GREEN exit0 — 3 leaves, 1.636s; race exit0 — 3 leaves, 2.769s. Проверяются обе очередности collections, persisted reload, custom incompatible fingerprint, query model aliases, missing client и precancel. Held-provider test подтверждает inherited 500ms deadline: fake provider наблюдает request cancellation, handler заканчивается. Логи: `/tmp/levara-t10-dual-{red,green,race}.jsonl`. Native vector store + localhost provider fixtures проверяют контракт, а не model quality.
- REST: meaningful baseline RED — 14 failing leaves и 1 positive control; отдельный HYBRID RED — 5 failures и 2 controls. GREEN — 28 leaves; broad native SQLite/PostgreSQL GREEN/race — 272 leaves каждый, 0fail/skip/race. Проверены tags ANY/no-match до model egress, lexical domain, ACL до fusion cap и seen-ID dedup, rerank invalid/duplicate indices, bounded timeout с фактической отменой provider. Логи `/tmp/levara-t10-rest-search-{red-complete,hybrid-red,green,broad-green,broad-race}.jsonl`.
- Specialized native retrieval: operation-local metadata callback дополняет ACL, single/batch фильтруются до fusion; exact parents — до cap. Parent fields относятся к возвращаемым parents. Baseline native/MCP RED — 9 failures/1 control; specialized RED — 3 failures; missing-pipeline lexical-outage RED — 2 failures. Behavior-only guard-removal RED — 2 failures/1 control (не исторический full-source snapshot). Current native/MCP GREEN/race — 185 leaves каждый, включая позднюю terminal repair; логи `/tmp/t10-terminal-affected-{green,race}.jsonl`. Parent filtering использует programmable ACL double; эти новые tests не доказывают live SQL revoke во время parent lookup.
- Go checks выполняются последовательно. По указанию пользователя дополнительные REST memory owner-spoofing тесты не выполняются; broad selection сохраняет `-skip '^TestMemoryREST'`.
- Gortex guarded mutations подтвердили disk hashes. Несколько intermediate impact/detect отклонили stale graph; subsequent detect вернул bounded lower-bound result, не exhaustive blast-radius proof. Нативные тесты и фактический combined diff остаются приёмкой.

- I55 terminal errors: native first-success→cancel/embedding guard/result-filter rejection и MCP branches reproduced RED — 19 failing leaves/9 controls; current focused GREEN — 28 leaves, 0fail/skip. Обычный второй HTTP503 сохраняет исправную первую выдачу; terminal failure отбрасывает partial и не достигает reranker. Логи `/tmp/t10-terminal-{red,green}.jsonl`. Независимое actual-source/raw-log review: must-fix отсутствуют; root final integration/public/S0–S4 впереди. Ранние 478 GREEN/race предшествуют этому исправлению и не являются final proof.

Root current combined GREEN session38659 / race53829: actual exit0 каждый, 506 leaves, 0fail/skip/race. Selection включает шесть пакетов pipeline/BM25/embed/store/MCP/HTTP, native SQL port53350, public output schema и authority rechecks; `^TestMemoryREST` исключён. Логи `/tmp/levara-t10-final-integration-{green,race}.jsonl`. Этот current evidence покрывает I55; общий S0–S4 ещё ожидается.

T10 final acceptance: root S0–S4 session10850 actual exit0 — 3806 PASS nodes/3226 passed leaves, 0fail; optional DCD baseline skipped, пользовательский `^TestMemoryREST` исключён. Log `/tmp/levara-t10-current-test-commit.jsonl`, SHA256 `73e24138baccf75094372a9d1949884cb8add8fa0f59e3991937532dd3a1a2c5`. Pre/post manifests `/tmp/levara-t10-{pre,post}-gate-workspace.json` совпадают: `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:5efbbf760864bbe149898dd5615287c48f6bfd74497f33afb734cd80b43f8b2e`, 127 untracked. Contract-check30914, public16416 (55 leaves), strict61413 (10 changes) — root observed exit0. Независимое финальное actual-source/raw-evidence review подтвердило original DoD без unresolved must-fix. Последующие acceptance edits только в документах, static/docs повторены отдельно; более поздние изменения не приписываются этому gate.


## T11 — frozen answer-quality gate, 2026-10-06

[Подробный baseline/A/B, hashes и ограничения](t11-quality-evidence-2026-10-06.md).
Принят измеряемый gate и behavior repairs, roadmap11/32. Native confidence/null
metadata, actual-context grounding/short facts, failed generation и feedback SQL
имеют meaningful RED→GREEN/race. Current integration111 passed leaves каждый,
full S0–S4 root49417 exit0:3255passed/2optional skips/0fail, frozen revision16052d.
Full/core contracts и strict OpenSpec прошли; independent source/raw review clean.
Реальный retrieval47/47; external39/39 executed с повторяемым failed strict
aggregate24/39. Native6 captured, source-instruction baseline execution устранено
на том же случае; q001 citation gap остаётся failed quality criterion. Никакой
общей model safety, semantic classifier или candidate-binary end-to-end A/B
не заявляется. Acceptance edits только документальные.


## T12 — versioned ingest/publication, 2026-10-06

[Native SQL и actual embedding evidence](t12-ingest-evidence-2026-10-06.md).
476passed leaves GREEN/race,0fail/race/SQLskip; единственный skip — посторонний
optional gRPC rate-limit test. Real current-binary smoke47842exit0: bad-second
preflight без provider effects, два источника с exact attempt/publication/lineage,
explicit native CHUNKS и retry с новой generation при прежней source version.
Independent actual-source/raw/stopped-SQL review clean. Продуктовый код уже
соответствовал DoD и не переписывался; trusted-local partial compatibility
остаётся отдельной областью. Roadmap12/32; следующие acceptance edits только docs.

## T13 — parser/OCR corpus: 2026-10-06

Принят ограниченный authored corpus: [gold, normalization, native OCR, corner cases и текущие gates](../../pkg/extract/testdata/t13/report.md).
20 fixtures, восемь ledger formats; confirmed I64 CLI stderr contamination исправлен.
Current extract/chunker GREEN130/race129 leaves, platform compile5 и stable S0–S4
3255 passed leaves/2optional skips, exit0; independent review без bounded mustfix.
Actual English CER0/47, WER0/9, numeric17/23/40. Whisper quality, Russian OCR и
сложные layouts неизмерены; scanned PDF не объявлен автоматически OCR-supported.
Текущее выполнение: 13/32. Следующий T24 предшествует T14 по исходной зависимости.

## T24 prerequisite — bounded workspace index hardening: 2026-10-06

T24 остаётся открытым; исходный T14 зависит от него. Закрыт один ограниченный участок:
REST/MCP index стабилизирует verified credential, live tenant membership и workspace policy
в существующей transfer transaction до actual index drain. Branch lock берётся раньше SQL,
как в Task write. Reserved `_memories` / `_memories_*` отвергаются до manifest/provider/vector
эффектов: это исключает синхронный memory-migration SQL callback при pool=1.

Native обе SQL проверяют полный authorized index с настоящим native vector store и HTTP
embedding fixture, удержание/освобождение transaction, reserved denial и authority errors.
REST status regression: RED2 (503 превращался в400) → typed403/503 preserved.
Current Workspace120 GREEN/race, zero skips/failures/race; independent review clean.
Root current S0–S4:3271 passed leaves/2existing optional skips, exit0; `make contract-check` exit0.
Raw logs `/tmp/levara-t24-workspace-final-{green,race}.jsonl`,
`/tmp/levara-t24-index-current-test-commit.jsonl`; full SHA-256
`e0cd1c181ac1745fc5dee8e686d00c87c41c73cfd5e6153e0fca8fab12e9597b`.
Before/post current gate revision unchanged:
`2eb1dca16b0047918185760b41dcb22dee79090a+dirty:ae0180fe6d73277d86c36910ec2997d46d37c9c5091adc9a335d90611458e2dc`.

Concurrent provider/revoker runtime RED не получен: сервис отклонил turn подагента по risk
classification. Защитный fix и обычные native authority checks выполнены; это не подменяет
revoke-ordering evidence. Read/manifest/log/artifact и остальные workspace effects/egress,
wall-clock expiry и полная ACL matrix требуют следующих шагов. REST memory-owner probes
не запускались. Production restart/deploy не выполнялись. Прогресс остаётся13/32.


## T24 prerequisite — workspace read response fence: 2026-10-06

REST workspace read/manifest/log и MCP ответы этих tools повторно проверяют verified credential, выбранный tenant и read policy после построения результата и SQL audit. Existing transfer fence удерживается до Close потока ответа. Incoming deadline и credential expiry ограничивают чтение; уже отменённый context/expired credential отклоняются до выдачи защищённого текста.

Root native TestWorkspaceReadResponseFence: **16 leaf checks GREEN**, SQLite/PostgreSQL с pool=1. Обычные owner/viewer REST и подписанный JWT MCP возвращают authored текст; partial Read держит InUse=1, Close освобождает до InUse=0. Existing Workspace suite: **136 leaves race GREEN**, без skips/failures. Reviewer подтвердил корректность wiring/cleanup; тесты лично не запускал. Native partial-drain coverage относится к read; manifest/log проверены по коду и existing suite. Реальный disconnect, expiry посреди drain и concurrent revoke ordering этим не доказаны.

Stable current S0–S4: **3287 passed leaves / 2 existing optional skips / 0 failures**, без SQL skips; contract-check и diff-check exit0. Перед/после gate revision совпал: `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:a05a02f9d45e3a58dd6d185d0df75c3ab5f4480e2cddbbcde598789ad254f406`.

Raw evidence: `/tmp/levara-t24-partial-drain-green.jsonl` SHA256 `bef08748d5752841bb458b91e7139316da53e8208f9262e8d8fe4272d389e660`; race `/tmp/levara-t24-stream-workspace-race.jsonl` SHA256 `e6242e89d63075c73111cb77a07c30815e6d78a1033fc685c36c8b023ac482be`; full `/tmp/levara-t24-stream-current-test-commit.jsonl` SHA256 `fbea32d48275c43b7df0af542fdb2a1ea7700388ae5803dcc38c264f8cca76ae`. T24/T14 остаются открыты; принято **13/32**. Следующий участок: ordinary workspace write/delete/GC authority и validation до файловых эффектов.

## T24 prerequisite — ordinary effects and queued jobs: 2026-10-06

Write/delete/GC теперь удерживают verified actor/tenant/write policy под SQL fence до фактического эффекта и освобождения native index. Reserved collection validation перенесена до файловой записи. Native pre-fix RED4 подтвердил изменение source file после отказа индексирования; current component Workspace158 GREEN/race обе SQL, pool=1, без skips/failures/race. Cross-store rollback этим не обеспечивается.

Synchronous reindex/reconcile/retry используют один branch lock перед SQL и Locked cores без повторного захвата. Retry проверяет фактические persisted project/branch/job ID. Component Workspace174 race GREEN, обе SQL; actual embedding видит InUse=1, после операции InUse=0. Raw log `/tmp/levara-t24-jobs-final-workspace-race.jsonl`, SHA256 `ddf3e39668724ae8123a8441da5a3a1d0be0ab574d89f6b21dcb834973e4e552`. Branch-before-SQL test — bounded scheduling smoke, не доказательство межпроцессного locking.

Queued jobs сохраняют private submitting actor; публичный JSON исключает authority/credential metadata. Worker перечитывает job под branch lock и проверяет live authority перед attempt/provider. Idempotency разделена по user/tenant/service; duplicate сохраняет первоначальную credential delegation. Legacy unbound job в authenticated mode и local→authenticated mode flip отклоняются без mutation. Recovery также перечитывает actual job под branch lock, чтобы stale scan не перезаписал completed status.

Watcher service authority требует actual running instance; persisted enabled status недостаточен. Dedicated service admission mutex удерживается через SQL acquisition/native drain; stop ждёт окончания допущенных операций, а status callbacks используют отдельный mutex. Source-review finding этой гонки исправлен; runtime RED не заявляется.

Первый queued component GREEN: **188 passed leaves / 0 skips / 0 failures**, log `/tmp/levara-t24-queue-workspace-green.jsonl`, SHA256 `490c47b35cd6036f4d378a6cd64edcadc7a3f11be5568c0c2933ec8761863603`. Этот прогон предшествует добавлению full native service-job oracle; final race/current S0–S4 ещё выполняются. Independent source review не обнаружил bounded must-fix; лично тесты reviewer не запускал. Stop-wait test содержит 50ms scheduling smoke. Whole T24/T14 остаются открыты, **13/32**; далее commit/revert/run artifacts и остаточная ACL/expiry matrix.

### Current batch verification outcome

Final Workspace **188 race GREEN**, включая добавленный full native service job, обе SQL/pool=1, zero skips/failures/race. Raw `/tmp/levara-t24-queue-final-workspace-race.jsonl`, SHA256 `5d702af94152272da9e1b890b13f900f7d1c5c6f207437c7b16ecd36ceeeef9f`; independent source/raw reviewer clean, тесты лично не запускал. Code revision race: `3e3ffb476bb7ff4de3011ea473cac4849146097de7307ae45a87d0a31f5caedd`; во время race были только documentary progress edits.

Full current S0–S4 **не принят**: actual exit1, **3337 passed leaves / 2 existing optional skips / 2 failures**. Два failures — TestDocumentACLMCPTransports/legacy и latest, owner add: Fiber test client timeout1000ms. Путь — local durable filesystem/fsync и SQL; он не вызывает новые workspace/queue helpers или external provider. Причина не установлена. Frozen pre/post revision совпала: `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:75b84e21a8299419098833f2886df1f6ebe27131682d7f155f7031c03b682664`.

Raw failed gate `/tmp/levara-t24-queue-current-test-commit.jsonl`, SHA256 `21424e3bc3e588c4042ec54341d791fec706138517be6815fdd64ccd84b4ce7d`. На той же неизменённой версии focused TestDocumentACLMCPTransports count3: **6 leaves PASS**, durations0.11–0.33s, actualexit0; исходный timeout1000ms сохранён. Log `/tmp/levara-t24-document-transport-timeout-recheck.jsonl`, SHA256 `edb7765a792842f0808d3a59b6c1dd2580e082f9560f2afa18f95984d1cd6922`. Это не доказывает flake и не заменяет full acceptance. Следующий combined gate после текущих bounded fixes обязателен; T24/T14 открыты,13/32.

### T24 prerequisite: commit/revert и namespace

Commit/revert/run bounded slice: current native GREEN224 и race224, actual exit0, SQLite/PostgreSQL pool1; reserved restore, saved target/NUL/traversal preflight и authorized branch→SQL wrappers. Логи `/tmp/levara-t24-commit-final-workspace-{green,race}.jsonl`; race SHA256 `4273694c5ea0aedadcc685558bf89c293444ecfc94e1ec57e5e29b83c12711ed`. Cross-store atomicity остаётся открыта.

Shared project namespace: native RED2 ambiguity +2 hyphen controls, затем GREEN244 actual exit0. Raw final race248 содержит terminal PASS обоих пакетов, 0fail/skip, SHA256 `151545c806152d27826644c1784a0413471910adc3fa745f60f0a65f88801f18`; command exit code после interruption лично не наблюдался. SQL reader guard до superuser bypass, branch locks используют actual filesystem tuple. Не является acceptance whole T24.

Composite manifest I76: native pre-fix RED2 actual exit1, `/tmp/levara-t24-manifest-namespace-runtime-red.jsonl`, SHA256 `83c83b238e1c4313e417073622998aa81d719cc0e2ece07ac54a7ae8f93750b3`, receipt `dc39daa2-d898-4452-ae64-b81e8659e0d3`. Nested canonical path + normalized target-checked legacy fallback и tuple-specific backup precedence реализованы. Current gate выполняется; 13/32, T14/T24 открыты.

Final bounded manifest acceptance: root race session6779 actual exit0, **263 passed leaves / 0 skips / 0 fail**, SHA256 `b7716076fc5694667f962e5237ea6e727c5053a4122b6c0ea81e90092399a8f4`, receipt `2ce99267-1358-476c-93f3-81a47ba8755c`. Independent actual-source review found no blockers for this slice, receipt `176ae3e9-cb45-403b-8a7b-199ee799ad67`. Current full S0–S4 session16635 actual exit0, **3389 passed leaves / 2 existing opt-in skips / 0 fail**, SHA256 `834a3f9a948d531f6575b01d1217d435e746573e57106d4d724d837c3d87bce8`; contract-check session37317 exit0. Pre/post revision identical: `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:272e2275024db4a0bd53573d7784a9bfcb554f707edc1d7b73f50829eb96b16f`. Both MCP document transport tests passed unchanged, previous I75 cause remains unknown. SQLite/PostgreSQL63530 native, user-prohibited REST memory-owner tests excluded. Whole T24/T14 still open, **13/32**. Next I77 watcher raw-ID resolution.

### T24 prerequisite: watcher project identity

Native pre-fix **RED20** observed actual exit1 on SQLite/PostgreSQL: raw UUID/branch lost in chunks/jobs, ambiguous/unregistered/normalized legacy projects accepted and sync reached local embedding. Log `/tmp/levara-t24-watcher-identity-runtime-red2.jsonl`, SHA256 `fda5efcfbcb35f005478fcc70aeee28db39f3f4286937297036086b951577204`, receipt `39a6bdd6-2d63-4b39-ad6c-6f9699976cee`. First attempt was a test build failure and is not runtime RED evidence.

Corrected native20 actualexit0; final combined **race289 / 0 skips / 0 fail**, root session46876 actualexit0, SHA256 `d1e771e5060b65d14b83c40898509914703b0a0c5a1a93aa2a733e3c47bef8f7`, receipt `802e6393-8314-4bd2-a823-efb33723b126`. Frozen pre/post revision `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:7857684a3410ae53a3603aa03e37e3da4142c107c76b6d681b6d82eca8f61af3`. Resolver uses fenced reader, new watcher persists raw SQL ID/stored branch, native chunks/vector metadata agree; saved alias/collision/orphan jobs rejected with unchanged bytes before attempts. SQL pool1 both engines. Source review has no bounded blocker; local-mode compatibility comes from combined Workspace selection, not the new auth-only matrix. Legacy normalized metadata remains explicitly rejected pending I78 migration. Whole T24/T14 open,13/32; next artifact/ops scope and fences I79, then current full/contract gate.

### T24 prerequisite: metadata scope и artifact authority

Native metadata pre-fix RED24 actual exit1, log `/tmp/levara-t24-metadata-runtime-red.jsonl`, SHA256 `ae07ec7af727c34708f5c80fbf49889f63b6d911dc73d526dd762dea65b0bd55`. Initial corrected race319 exit0 covered explicit authenticated scope, scoped watcher counters, retained REST/legacy response fences and per-branch artifact indexing. Source review then found masked authority status and missing credential-expiry bound. Native expiry RED4 actual exit1 on both SQL engines, SHA256 `89df1e6fe09cd4ac9ef1434940b9c58edc7e6800dee399257ae5148aba74237e`, receipt `04707da9-43b1-47e9-aee5-9a32ea9563e3`; late embedding published despite expired credential.

Final corrected combined **race345 / 0 skips / 0 fail**, root session30925 actual exit0, SHA256 `99182d22b62cc48f1a376cce95bec76970eb34cbb8b83368365b5de7b7e6425c`, receipt `cef9bcb2-8561-4278-9308-7315c932db70`. Includes six latest MCP outer-body partial-drain checks and four expiry checks. First latest fixture omitted mandatory Accept and failed six leaves; corrected before acceptance, not a production RED. Independent source review receipt `a6cf232c-5e75-4b22-8312-1a1ad4801eec` found no bounded blocker.

Current S0–S4 gate session97079 actual exit0: **3455 passed leaves / 2 existing opt-in skips / 0 fail**, SHA256 `d12e38620eeb5c58446a802de23fcf8222efb7a6cfc1fbd73a07aa11815df245`, receipt `67537bd3-7652-44dd-bf11-ab72ac0334a8`. Contract-check session60467 actual exit0. Frozen pre/post revision: `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:211cdc2416c9cc1f831ff8948bcbf9d265b79c947d55c5bcba2a78e6bc06c2b5`. SQLite/PostgreSQL63530 native, pool1. Skips remain TestDCDVSALoadBaseline/TestT11NativeRAGLocalQuality; prohibited additional REST memory-owner tests excluded. Cancellation during embedding is proven; atomic cancellation of native publication and cross-store effects is not. **13/32**, T14/T24 open. Next: workspace_context verified projectless discovery/egress I80; global watch/audit/jobs and sibling effect deadlines I81/I82.


### T24 prerequisite: workspace context discovery и response lifecycle

Native corrected pre-fix **RED14** actual exit1, SQLite/PostgreSQL: foreign watcher metadata, wrong tenant, expired credential, read-disallowed authority and unfenced REST/legacy/latest body. Log `/tmp/levara-t24-context-runtime-red2.jsonl`, SHA256 `d8af041d9a48201232755ebf76adc089b2d263a68dac2178455348efd4699cd7`, receipt `4455b62a-6caf-4888-91b2-f8bc730e9dba`. Initial test incorrectly treated write permission as read-disallowed; corrected to delete before accepted RED.

Shared helper additional **RED2**: SQL rolled back at observer deadline before actual body Close; log `/tmp/levara-t24-discovery-drain-runtime-red.jsonl`, SHA256 `a811929eb411f0b394ab20d515bbc14eeee34ff3e491dede74a1eb60630c9ad9`. Independent review then identified overwritten verified MCP actor and detached queued acquisition; native **RED6** reproduced actual bearer dispatch without actor locals and canceled acquisition on both SQL, SHA256 `8b11eedc5c6ce048c65f7965f787d0df8cff5198879cbdd69c52befc9541d5cd`.

Fix preserves projectless accessible-project discovery, trusted local compatibility and raw SQL IDs; per-project authorization uses verified credential/tenant/key permissions through the same reader. Watcher filtered to admitted projects. Shared helper preserves verified MCP scope, keeps acquisition/build cancelable and transfers SQL ownership through response Close. First integration race failed only legacy local ACL project-list compatibility; forced TrustedLocal reset removed before final run.

Final combined **race391 / 0 skips / 0 fail**, session47574 actual exit0, SHA256 `1c5441abc732abd9825d5b4bd073c78aadf7b1c7ff5bda6a5c79d21f19bf0cd1`, receipt `898f6c12-7809-41fb-903e-56a8aa474cce`. Includes existing document recipient/shared discovery, grant/group-member revoke and user-deactivate lifecycle. Independent current-source review no bounded blocker, receipt `22579ee3-f6ce-4d9b-9b0c-c4a99fea820e`.

Current S0–S4 session92269 actual exit0: **3477 passed leaves / 2 existing opt-in skips / 0 fail**, SHA256 `98ab8a66729679358457ebad286320cabab843bed678f7bbf21afd929fd84068`, receipt `da974295-ba84-4bb4-b3ec-5e65a981bf28`. Contract-check session21658 actual exit0. Frozen pre/post revision `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:60d3d0b640b2b5848477ce9a204727d878622dc9546ef54b515d33546787fae5`. SQLite/PostgreSQL63530 pool1; excluded additional REST memory-owner tests; two opt-ins unchanged. **13/32**, T14/T24 open. Next: sibling effect/provider deadlines I82, global watch/audit/job egress I81. Native publication atomicity and historical identity migration remain separate boundaries.


### T24 prerequisite: deadlines всех workspace index paths

Native pre-fix **RED14**, actual exit1: index, indexed write, reindex, reconcile и persisted reindex/reconcile job на обеих SQL публиковали chunks/active generation/native vectors после credential expiry; два Indexer unit checks обнаружили ignored cancellation и удаление существующих vectors при canceled empty file. Log `/tmp/levara-t24-effect-expiry-runtime-red.jsonl`, SHA256 `5d909a7aaeadc483fcb9f84b3b5a23ea4be444d751bad030930994b5235940b9`, receipt `018aa26e-46f2-4e85-a018-771dbdfeb4f9`.

Independent source review обнаружил отдельный manual retry bypass. Actual native **RED4** на SQLite/PostgreSQL: поздняя публикация и completed job; log `/tmp/levara-t24-manual-retry-expiry-runtime-red.jsonl`, SHA256 `d0b68c75d902f9665b4143246c1c4bbe602354bcc966d348a00519e13a19f016`, receipt `218c4008-cbb4-4a27-a568-e65c82a1c3e5`. Final test различает submitting credential (+1 hour) и current retry caller (+2 seconds); сохранённая delegation остаётся прежней.

Общий stdlib deadline helper ограничивает incoming context/operation timeout/credential expiry; background worker использует fresh persisted actor, manual retry — current caller. Multi-file и empty reconcile проверяют cancellation; Indexer проверяет ctx до generation/deletion и после успешного provider до native upsert. Task writes сохраняют свой executor. Gortex signature verification clean:40 callers,10 actual helper callsites.

Final root **race341 / 0 skips / 0 fail**, session8156 observed exit0, log `/tmp/levara-t24-effect-final-race.jsonl`, SHA256 `d173a0eb35a50a19473901222034ce87d99dfd65038d8ca96040bdb69f4cf78d`, receipt `a776acd9-e32c-43f0-aa9a-9b9fda6eaf60`. Anchored TestWorkspace/TestArtifact selection + cancellation and document discovery checks; counts не сравнивать с прежним unanchored391 как одинаковую selection. Supplemental Task executor/access **race39**, session54971 exit0, log `/tmp/levara-t24-effect-task-access-race.jsonl`, SHA256 `0b5fbf12a455c45bd5e10dca8d8074936dd41d9e38ef6fe94a5d8309c008d705`. Independent actual-source review no bounded blocker, receipt `4a871f70-d9c0-4be8-acc3-54ea1884c0a6`; reviewer Go не запускал.

Current full S0–S4 **3495 passed leaves / 2 existing opt-in skips / 0 fail**, session25854 actual exit0; SHA256 `cb6e0db0b987fb84df73df3dbfad798826fac555fd7d0b05b719c44199778eaf`, log `/tmp/levara-t24-effect-current-test-commit.jsonl`, receipt `cfe6d6b4-9798-41a5-8dd5-2568c16f85fb`. Contract-check session33973 actual exit0. Race/full pre/post frozen revision identical: `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:50d9cc7fb13a1b0961dbabf7d43ee9d875b2b744679d960951c3edde646301fb`. SQLite/PostgreSQL63530 native pool1; existing opt-ins unchanged, prohibited additional REST memory-owner tests excluded.

Bounded I82 принят. Admitted filesystem bytes, attempted/failed metadata и ранее обработанные batch files могут сохраняться; cancellation внутри native publication и cross-store atomic rollback не доказаны. **13/32**, T14/T24 остаются открыты. Далее I81 watch/audit/job egress; historical identity migration отдельно.

### T24 prerequisite: watcher scope, audit/job body и queued cancellation

Native pre-fix **RED32**, actual exit1: watch status accepted foreign/wrong-tenant/expired/read-disallowed callers and global metadata, missing project REST/MCP accepted; watch/audit/indexjobs REST/legacy/latest responses did not retain SQL. SQLite/PostgreSQL pool1, log `/tmp/levara-t24-watch-egress-runtime-red.jsonl`, SHA256 `87a1af5cf5a19024b1c1a20c0f1ef57b74ac62e3417aa301da30f48a660b5a40`, receipt `d4b5cd6b-2621-4bc2-9a32-3625062546dd`.

Shared workspace response helper additional **RED2**, actual exit1: canceled caller still waited for SQL and succeeded after pool connection became available. Log `/tmp/levara-t24-response-acquisition-runtime-red.jsonl`, SHA256 `9d5e30b311f327392a2c99f477edae0751f1e4643b0af605f5948491a551af55`, receipt `cf4f7bf4-9190-4d12-8bc8-67f4bac2c89a`. Admission now observes incoming cancellation; only completed body detaches with the same deadline, transfer SQL releases at actual Close.

Initial focused **race76** exit0; final combined unanchored Workspace/Artifact + cancellation/discovery **race451 / 0 skips / 0 fail**, session19455 actual exit0, log `/tmp/levara-t24-watch-final-race.jsonl`, SHA256 `e04cbc613fced8c9db8e7540c64ef6583526d57fefcae48f73bf4a6f3165de5e`, receipt `a2bc9c03-6bbc-40b0-ad6c-28b6c3351d94`. Includes existing Task/access/backup/structured-artifact regressions. Independent actual-source review no bounded blocker, receipt `17678550-4b29-453f-a844-78e162ef5d5b`; reviewer did not run Go or contract validation.

Current full S0–S4 **3537 passed leaves / 2 existing opt-in skips / 0 fail**, session12130 observed exit0, log `/tmp/levara-t24-watch-current-test-commit.jsonl`, SHA256 `9cd8632e8733d914313e0eda801a70b523cc395a5f052e81002b87db376745f0`, receipt `269c66b8-3b2b-46b9-9545-c459f96d9039`. Watch input descriptor adds project_id/optional branch, output and profile membership unchanged; contracts generated by CLI and contract-check session33829 actual exit0. Frozen pre/post race/full revision: `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:2f77a5755b4d027a3ccefa671557fe4bc64c02094a6b6cefb21e4d6287476f33`. Native PostgreSQL63530+SQLite; prohibited additional REST memory-owner tests excluded; existing opt-ins unchanged.

I81/I84 bounded accepted, **13/32**, whole T14/T24 open. Next source-confirmed I85 run_get/negative authorization diagnostic and I86 project-to-selected-tenant binding. Current checks prove membership rejection, not binding for a caller belonging to multiple tenants; historical migration/cross-store publication atomicity remain separate.

### T24 prerequisite: run_get, authorization diagnostics и selected tenant

I85 native pre-fix RED22, actual exit1: run_get/diagnostic bodies не удерживали SQL, diagnostic терял verified credential/tenant facts и MCP key permissions. Log `/tmp/levara-t24-run-diagnostic-runtime-red.jsonl`, SHA256 `b1e86276e0b589ee81724a5941df2252166c1707a2817f22f7a84cbfd9ff8dfc`, receipt `e79d272e-1cfb-489b-8dcd-6dfc9f39befe`. Две direct-MCP permission cases первоначально имели unverified fixture, поэтому не являются runtime доказательством real-key authentication. Fixture исправлен на persisted key + verified private context; initial GREEN failed2 и первый corrected run buildfail (ошибка Go escaping) сохранены, не считаются production RED.

Corrected I85 component race105 actual exit0, log `/tmp/levara-t24-run-diagnostic-corrected-race2.jsonl`, SHA256 `254dbf74d236f5ec277ac666b2be55141d448cbe9acfbd0d4191285c206704a7`, receipt `8181a0ac-9725-4ac3-af9f-195198e1e8fb`. Negative diagnostic сохраняет HTTP200 allowed:false; callback использует installed fenced reader, без nested SQL при pool1. Run body защищён REST/legacy/latest transport.

I86 native pre-fix RED10 и10 positive controls, actual exit1, log `/tmp/levara-t24-selected-tenant-runtime-red.jsonl`, SHA256 `4f40095bbc0b2f3f0c29b2e11d22d923887c8298437cb4e880efef594ec4c34b`, receipt `dfba8bfb-fbd3-4d59-8eb1-142aa6ba2de5`. Caller tenant membership не связывала project target: shared editor и superuser могли читать/менять проект владельца из другого tenant. Проверка selected caller и existing owner-membership predicate теперь выполняется тем же reader до grants; verified selection проходит Actor facade/effect/context/diagnostic. У проекта нет immutable tenant column; owner в нескольких tenants намеренно делает проект доступным в каждом соответствующем scope. Пустая selection/local/service compatibility и persisted Task owner authority сохранены.

Combined component race125 /0fail/0skip, actual exit0, log `/tmp/levara-t24-tenant-diagnostic-component-race.jsonl`, SHA256 `647f6d14b8b1456392edb96fc79e9374ac7b494a5f042346318f899e3e3e3464`, receipt `44eb0269-d805-4756-b55b-eb9a2318695c`. Independent source review no bounded blocker, receipt `3d32fd26-52ac-442e-973f-5da63f69e696`; reviewer Go не запускал. Gortex variadic arity warnings не считаются clean graph verification; фактические callers скомпилированы.

Final combined unanchored workspace/artifact/cancellation/document discovery race503 /0fail/0skip, session14904 actual exit0, log `/tmp/levara-t24-tenant-final-race.jsonl`, SHA256 `c098a8aa76d8b994d59281c1a97b24d67e49cc5da39d48ee1b31dbb1f2956146`.

Race receipt `d287dceb-e3b8-4819-ba16-772b330d4451`. Current full S0–S4 **3589 passing leaves /2 existing opt-in skips /0fail**, session10538 observed exit0; log `/tmp/levara-t24-tenant-current-test-commit.jsonl`, SHA256 `965f52c35b304a3180c7235d3a3114e7fffcbda8e360f895224b2cccd4616414`, receipt `4e28d5e6-50a0-4401-9d21-1008b933b305`. Contract-check session31971 actual exit0. Race/full frozen pre/post revision: `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:2a41a9c4c47676bf3abc86443c7dd6975c65dc911495d93458753aa110e31f78`.

I85/I86 bounded accepted. **13/32**, whole T14/T24 open: source-confirmed I87 dataset share-list response lacks retained credential/tenant/ACL authority. Source receipt `b9ebfd9c-ce25-43e3-9f95-92573ff01879`; no runtime reproducer claimed. Historical metadata migration and cross-store publication atomicity remain separate.

### T24 final matrix: dataset recipients and self diagnostics

I87 source-confirmed: GET dataset shares имел только initial ACL, cfg.DB SELECT и ordinary JSON recipients; receipt `b9ebfd9c-ce25-43e3-9f95-92573ff01879`. Runtime pre-fix воспроизведение не заявлено. Один existing transfer transaction теперь проверяет verified credential, selected caller membership и owner-derived target scope до dataset read grant; DTO query использует тот же tx. Rows закрываются до передачи тела, SQL — на actual Close. Форматы DTO/[]/403/503 сохранены.

Initial recipient/audience native component race66 /0fail/skip, session62428 actual exit0, log `/tmp/levara-t24-share-list-component-race.jsonl`, SHA256 `708d76349827c75d8c9ca817ae696d6403d36994e48d5c8eafe9d8a4cdba824c`, receipt `9937cc92-6503-4989-b6bd-4f45d81fcd9a`. Pool1 oracle первоначально подтверждал retention connection; дополнен pool2 writer oracle, сохраняющий запрет revoke до body Close.

I88 source-confirmed: own permission diagnostic игнорировал IsSuperuser/query/Scan/rows.Err failures и SQLite TEXT timestamp; receipt `b686faff-da09-44a5-82c5-0691bfddbb10`. Ответ теперь удерживает verified identity/membership reader до body Close, SQL/scan/timestamp failures дают503, invalid authority403; PostgreSQL time.Time и SQLite TEXT нормализуются в RFC3339. Выдача всех own grants сохранена без нового project admission/filter; nilDB fallback сохранён.

Combined initial component race94 /0fail/skip, session25358 actual exit0, log `/tmp/levara-t24-share-permissions-component-race.jsonl`, SHA256 `76215d9c4fa56cc02899b2bdd1716f2e4c6ee8fa69a993f97dcd268be7829f62`, receipt `67f66674-b603-4ef9-bc45-874ab0a7ebfa`. DROP dataset_shares может ломать PostgreSQL acquisition вместо позднего query; final fixture переименовывает только role, отдельно проверяет год10000 вне RFC3339. Final matrix включает partial drain, expiry до Close, pool1/pool2, readonly key, revoked credential, inactive user, missing membership, unverified identity и empty-array controls.

Independent actual-source review no bounded blocker, reviewer Go не запускал; receipt `32f5ca95-4421-4c61-9ab3-d551bc98928b`. Current broad T24 **race1083 /0fail/skip**, session24214 actual exit0, log `/tmp/levara-t24-acl-final-race.jsonl`, SHA256 `34d10f4c2e07051d2c162ae02f3ffe8f1fd031e729f012305e0c72ae1250c045`, receipt `931903de-fd01-4d12-bda8-7cc7b6a8d5a0`. Selection Auth/ACL/Revoke/Credential/Tenant/Document/Workspace/Artifact + audience/diagnostic/cancellation on pkg/access, internal/http, internal/grpc, pkg/workspace, pkg/backup. Native SQLite/PostgreSQL63530, all selected native tests executed; additional prohibited REST memory-owner reproductions excluded.

Initial full failed gate сохранён: session39425 actual make exit2, **3455pass/4fixturefail/2existing opt-in skips**, log `/tmp/levara-t24-acl-current-test-commit.jsonl`, SHA256 `2a1abe2ad6a7312b82868c18db8e27468f244d3fcab014f2811bcb561296f648`, receipt `1306ac4b-a58f-4d71-8a99-5bf15143cb04`. I89 onboarding SQL-error fixtures задавали только user_id и получали403 до SQL. Добавлен совпадающий verified JWT private context в двух fixture middleware; production guards и исходные503/malformed/error-leak assertions сохранены. Это synthetic verified fixture, не real JWT transport proof. Corrected focused race64 exit0, SHA256 `a83358998eda141278a26fed6c2be32bbb40de0fd0da6a70cf76cab2c27f0b10`, log `/tmp/levara-t24-onboarding-corrected-race.jsonl`, receipt `2292ba4d-8291-43fd-83aa-e06b9810ada0`.

Final corrected broad **race1087 /0fail/skip**, session73354 observed exit0, включает прежнюю selection + TeamOnboarding. Log `/tmp/levara-t24-acl-corrected-final-race.jsonl`, SHA256 `94d17d5bcae7ee96ca40642f7bea211b2343fc319952be9f9f4a7d556174e08a`, receipt `8d5efe12-8849-4994-875d-478ff06270e5`. Independent reviewer самостоятельно проверил оба текущих лога, actual source и test oracles; conditional audit receipt `5300d893-885d-4080-ab83-956378f6f9b9`, conditions full/contract/frozen впоследствии выполнены root. Reviewer Go не запускал и shell exit/frozen checksum не наблюдал.

Corrected current full **3647 passed leaves /0fail/2 unchanged opt-in skips**, session95191 observed exit0, all S0–S4 green. Log `/tmp/levara-t24-acl-corrected-test-commit.jsonl`, SHA256 `82c898728a0ecfd084a7fb0e47b284e3e985adec010b96707d7ee3ac607f0a45`, receipt `03a9a507-df72-40e9-95bc-4daeabbbb606`. Contract-check session50327 actual exit0. Race pre/post/full post revision identical: `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:be93ccfc75aba1232fc71b2b5aa9bcf69615d3be3261ce999a567e9abc630c01`. Documentation acceptance edits follow these frozen code gates; no claim that doc-only revision was the tested snapshot.

**T24 accepted, 14/32**. I87/I88 fixed, I89 fixture corrected. T14 remains open: next same-source raw/artifact/native search/graph retirement and local hold/backup lifecycle. Workspace independent revoker + blocked provider lacks its own combined integration oracle: shared tested SQL fence plus actual retained connection during provider support the invariant. Owner-membership tenant model, persisted Task owner authority, historical migration, cross-store publication atomicity и external physical purge boundaries remain explicit.

## T14 lifecycle components — acceptance pending

Root focused race session30674 actual exit0: **3 passed leaves / 0 fail / 0 skip**, log `/tmp/levara-t14-lifecycle-component-race.jsonl`, SHA256 `2264e8f9f7456596769ec2a0127456944ecb41c8ed607b2157847dd075f95de8`, receipt `ffd4f5b5-fa6a-4cb5-a34d-74b277e3fb7b`. Frozen pre/post revision `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:ff98138d584ebbe16a889c965f31a964de4d4fa41d377f00ff1814a3d95574d5`. Same-source native A→CAS B→publish B→two dataset aliases→first/final tombstone checks public raw/artifact/RAG and direct public query_entity adapter on SQLite/PostgreSQL pool1. Graph assertions are injected with actual native publication metadata, not generated through LLM. SQLite local offline backup restores held policy/tenant/revisions/raw/original/structured bytes; after live unhold/delete/structured cleanup and unavailable live roots, prior archive retains held evidence and SHA. No PostgreSQL held restore, external legal-hold lifecycle or raw/nativegraph/vector/archive physical purge claim. Independent source reviews found no component blocker; reviewers did not run Go.

Extended component session77738 actual exit1: **5 pass / 2 fail / 0 skip**, log `/tmp/levara-t14-extended-component-race.jsonl`, SHA256 `11f95b800e169406f779a50ca3f0fd583d61d585aa0599dfc62914b31bd3f826`, receipt `2cb76737-06d6-4ce2-ac0f-fe9308b8d9c9`. Frozen pre/post revision `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:47f2fb8ad6fba0fda7d55e87411090b275ee50c07eb3330344f403b7fb753927`. Both late-embedding tests failed before the concurrency scenario at their positive REST lexical search (200/[]); fixture text was shorter than native pipeline's minimum accepted chunk. Corrected fixture uses long exact source text/raw hash; rerun pending, not production RED. Backend failure/retry tests passed both SQL, preserving inaccessible retired object/inventory on failure and idempotently removing them on retry, with new artifact public positive controls.

T14 remains open, **14/32**: confirmed I90 REST graph/path bypass requires authorization before BFS and retained egress. Source fixes and final native/full/contract gates are pending. Existing inherited revoke, tombstone rollback, held mutations and final publication CAS coverage supplement these new components. Provider test's 70ms started/completed observation is not by itself deterministic proof of SQL writer lock wait; actual provider guard source is the supporting invariant.

Corrected lifecycle component root session61094 actualexit0: **race7 / 0 fail / 0 skip**, log `/tmp/levara-t14-corrected-component-race.jsonl`, SHA256 `4d55166be80ed0e41f09c626604d5b7e3889b4ec9fbc41b189c02caf7655933d`, receipt `ec801dfb-21f7-4d29-b84e-5993d186c0fe`. Frozen pre/post revision `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:a4c0c541233e043d7c988dd3d37033610e0cbd76c9713f9f990a7daa17df8458`. Long raw source/hash fixture reaches the real blocked embed provider; real tombstone and late completion both finish, retired source is logically hidden. Initial fixture failure remains recorded. I90 graph traversal fix is still in progress, so whole T14 is not accepted.

### T14 I90 graph traversal and I93 detached deadline

Root initial graph-path component session80588 actualexit1: **41 pass / 1 fail / 0 skip**, log `/tmp/levara-t14-i90-component-race.jsonl`, SHA256 `d5daf8cdd7e8627da3f3b6e026d18ed87c43e50d43ee75cabb2a625e1ae58b62`, receipt `63845712-f80d-4978-b2c2-28a4b2a7de53`. Frozen pre/post revision `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:54c4fa3a4f92b2926bd71fbb1b1b69cb006147eff983361b8c866df549cd8f6e`. Sole PostgreSQL retained-body failure exposed actual detached-timer lag: request deadline passed while stream ctx.Err remained nil. Shared reader now checks absolute clock deadline before each Read and keeps SQL until Close. Deterministic delayed-timer past/future controls cover this case; no sleep added to mask it.

Corrected component root session42852 actualexit0: **race45 / 0 fail / 0 skip**, log `/tmp/levara-t14-i90-corrected-component-race.jsonl`, SHA256 `0469f2f55df7e5f9126f7351c7652ee3f4cb480ee6ee98482fd58fe77ace5dfc`, receipt `b4c3b8ef-3736-4a85-9d78-7c4189400c39`. Frozen pre/post revision `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:bb6d7255432e7d82a8cc4a998b3a45b9484e47c39ec2043ee25e03f0230ff5c3`. I90 path filters nodes/each edge+both endpoints before BFS using same verified credential/tenant-owner/publication SQL transaction; authorized longer route survives denied shortcut. Both SQL pool1 test missing assertion, stale publication, denied endpoints, selectedtenant, credential denial, cursor400, genericSQL503, retained deadline and actualClose. Existing AsOf0/all, inclusive end and pagination preserved. Actual cognify lifecycle now verifies HTTP path as well as raw/artifact/RAG/direct query_entity. Authenticated Neo4j without SQL authority returns503; Neo4j parity not claimed. SQL fixture graph proofs in route matrix are distinguished from actual native publication metadata in lifecycle. No route-specific independent revoker, readonly-key positive or contradictory metadata assertion claim; existing shared authority coverage supplements these components. Graph loading remains global/uncached, no scalability claim.

Independent actual source review found no bounded blocker; raw and artifact response paths use the corrected shared reader. Permission controls authorize a subsequent Read, not atomic physical socket delivery exactly at deadline. **Whole T14 pending final broad/full/contract gates;14/32.** I91/I92 are separate T15 temporal fixes.

## T14 final acceptance: native retirement and hold backup

**T14 accepted,15/32.** Original native SQL/local DoD and corners passed; I90/I93 fixed. Root final combined race session64180 observed actualexit0: **1503 passed leaves / 0 fail / 1 existing host-condition skip**, log `/tmp/levara-t14-final-race.jsonl`, SHA256 `9584096f601aa1ca4dfed3d45ca50085ebc383b7e37a5db55c697fb8fe089388`, receipt `b20fd638-f491-42d5-b8ec-7c1d2af8068f`. Skip `TestRateLimit_IntegrationPerSourceIP`: this Mac cannot bind127.0.0.2; no PASS claimed for that unrelated integration fixture. Native PostgreSQL verified backup roundtrip ran with PostgreSQL16 binaries; held archive lifecycle remains specifically SQLite/local/offline.

Root current full session43561 actualexit0, S0–S4 green: **3684 passed distinct leaves / 0 fail / 2 existing opt-in skips**, log `/tmp/levara-t14-current-test-commit.jsonl`, SHA256 `8367e7db78adc4c0edf0cbd229796fde1d5a2b57dd851b52f08fcf230aaf9def`, receipt `c83edba4-2cd2-4d9f-a92b-80ea34888395`. Skips remain TestDCDVSALoadBaseline/TestT11NativeRAGLocalQuality. Contract-check root session53498 observedactualexit0, log `/tmp/levara-t14-contract-check.log`. Excluded prohibited additional REST memory-owner tests in every Go invocation.

Frozen pre/racepost/fullpost/contractpost revision identical: `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:54bc7b7d7bd08ac5339a4eb0bc7c44f5f7b478a8ee32082ac994af737662fc1d` (185 nonignored untracked files). Independent read-only reviewer audited ORIGINAL DoD, actual combined source and SQL mirrors, found no required nativeSQL/local blocker; receipt `2fff22a6-bc5a-4f57-8340-7303106aee81`. Its broad/full/contract conditions are now fulfilled by root observed terminal evidence. Acceptance checkbox/issues/evidence edits after these gates are documentation-only, not a claim those documentation bytes were functionally tested.

Existing inherited revoke/lineage, held mutation, stale CAS, deletion rollback and publication serialization coverage supplement new same-source public raw/artifact/native RAG/direct+HTTP graph lifecycle, aliases, real blocked embedding, bothSQL backend retry and local held restore. Logical closure and concrete structured-byte cleanup are accepted. Raw/nativegraph/vector/archive physical erasure, external legal hold/ObjectLock/retention, PostgreSQL held-restore lifecycle, LLM graph extraction and Neo4j authorization parity remain explicitly unclaimed. The timed provider observation does not prove exact writer-lock arrival; the guarded source and final logical oracle are supporting evidence. Permission protects subsequent stream Read, not atomic socket delivery at deadline.

Next **T15**: source-confirmed I91 history-preserving exclusive A→B→A episodes and I92 normalized current/as_of bounds. Whole Task/Goal remains incomplete; no production deploy, restart, commit or push.


## T15 native temporal implementation — acceptance pending

**T15 remains open;15/32 accepted.** I91 creates fresh exclusive episodes on A→B→A, reuses current episodes for retry, preserves closed history and serializes sorted node/source locks before capturing transition time. BothSQL tests include exact adjacency/history, mixed case, coexistence, foreign legacy ID, missing-source/trigger rollback, edge-only and crossed batches. PostgreSQL concurrency uses independent pools/backend PIDs and observes both native lock waits; SQLite coverage is explicitly pool1 serialization.

I92 checks current lower+upper bounds and half-open snapshots. Malformed/non-string `as_of` rejects before SQL; omitted/empty current and valid echo remain. Independent review found SQLite Julian-day loss of sub-millisecond precision; precise typed candidate filtering now replaces that coarse SQL prefilter, keeps paging after hidden candidates and normalizes supported instants to microseconds. A further review found pinned pgx native time.Time binding truncation differs from text parsing; explicit writer normalization and native-binding checks are still pending. No current precision acceptance claimed.

Memify redundant input-snapshot replay removed: enrichment helpers persist themselves and VSA refresh remains. All three published authenticated routes now return503 before work or run metadata; authenticated source-aware enrichment is explicitly unavailable. Trusted local consolidation retains primary properties and source dataset. New bothSQL no-effect denial and real local LLM provenance tests await current gate. Generic graph-store edge writer has no remaining production caller after replay removal; no new generic temporal import contract claimed.

Root command history, all with `-skip '^TestMemoryREST'`, native disposable PostgreSQL63530 and race:

- session48208 actualexit1:102 PASS/2fixtureFAIL/0skip. Frozen pre/post `fd1ecbf7cdf43100b6eeedd07a52176beaace9420795c2610ba95e6ee40eb1eb`; log `/tmp/levara-t15-component-race.jsonl`, SHA256 `f30b1a4d2a5c0691897d1409a9983f27c7cb6d35ce7077b65d22d712dfee9fd7`, receipt `7eab2e79-2711-428b-a3df-39f060a68ea2`. New PG unzoned fixture inherited server timezone; rollback trigger had a single closing dollar. Both fixtures corrected, no productionRED claimed.
- session60764 actualexit1:117 PASS/2fixtureFAIL/0skip. Frozen `d6f20932daf97cc8d14ea07832710b067fe15dfa10dd43912562e20b1ada8893`; log `/tmp/levara-t15-corrected-component-race.jsonl`, SHA256 `ce9ab2c0132a4e774a2a7492351e2d1d292a289b47e8d2dd626a4fdf24d057c8`, receipt `e88ec743-ccbe-48ef-b12f-39e85842596b`. Root's new memify seed supplied NULL superseded_by against productionNOTNULL; corrected to empty string.
- session14333 actualexit0:119 PASS/0fail/0skip. Frozen `b6b4f5a90e88faa74ffb3136f6126e228a0823aa09166ff1b08cf5de85fc041d`; log `/tmp/levara-t15-final-component-race.jsonl`, SHA256 `25395c9c8f17753d41ee4883a86baf2c6fe5e1798e3e5cc2b71589777181b152`, receipt `ba43fca7-ed47-4904-97cb-e52f11ae3441`. Bounded old component success; later independent precision/source-authority findings mean this is not wholeT15 acceptance.
- session11469 actualexit1:80 PASS/1fixtureFAIL/0skip plus HTTP BUILD FAIL. Frozen `c46114398560cabb01b27911e8a00c31723f9ea5ab74d3678b9b9b82dfc9d828`; log `/tmp/levara-t15-precision-authority-race.jsonl`, SHA256 `a82a86ad77bd8efdfab149110eb03cf4992b7af86680b8e66f3589dc099ed932`, receipt `cc1fddf5-24d7-4128-b7aa-1fa9170fb843`. New lightweight paging rows omitted superseded_by default, and new registrar accepted App rather than existing Router. Both corrected; HTTP tests from this command are not passes.

Separate I98 Neo4j parity remains open: test env absent, local listeners absent, Docker inventory not confirmed. Existing live helper deletes the selected graph and requires a dedicated disposable sandbox/APOC; it covers batch write rather than temporal parity. No live probe/mutation/deploy/restart performed. Authenticated REST graph/path uses protected SQL or503withoutSQL; no Neo4j parity claimed. Final native broad/full/contract checks and independent current-source review remain required.

### T15 precision/current native gates and inherited full failure

I96 now normalizes the writer transition once **after sorted source locks**, before binding the same microsecond instant to both new start and old end. PostgreSQL native time.Time truncation therefore cannot change new episodes. SQLite parses exact candidate timestamps rather than prefiltering with Julian days. Native read-only PostgreSQL fractional probes confirmed text parsing uses ties-to-even: .123100500→.123100 and .123101500→.123102. SQLite string bounds/snapshots now use stdlib RoundToEven; tests cover even/odd ties and second carry. This does not recover historical raw-native nanos import intent.

Root session12453 actualexit1:142 PASS/1fixtureFAIL/0skip at frozen0140fad56f7990403c636a7a053270bc65d2b6325e0df1070bfe6631b3f0e033. Log `/tmp/levara-t15-precision-final-race.jsonl`, SHA256 `1ee6281fb551eff413445e895c87e3c78ca9e6e61c583ac88c06807e142e815f`, receipt `9f8916fa-e91a-4856-8d84-bc5126d3fd51`. New binding oracle incorrectly used SQLite TEXT inequalities; corrected to read and parse real stored bounds, require exact normalized instants, then evaluate an independent inclusive interval oracle. Public query_entity half-open boundaries are tested separately.

Root final component session14320 observedactualexit0: **157 PASS/0fail/0skip**, log `/tmp/levara-t15-ties-final-race.jsonl`, SHA256 `6fed795f8b125e635ea71921e337a23a2e873841332f887df50ff743dce6ad45`, receipt `c811c870-4673-4d53-ab19-9f78eb123ddf`. Independent current-source audit found no new concrete native SQL/local blocker; acceptance remains conditional on whole gates, and external Neo4j parity is not claimed.

Root original broad race session63838 observedactualexit0: **231 PASS/0fail/1 existing live-provider skip**, log `/tmp/levara-t15-broad-race.jsonl`, SHA256 `79f7f8eeb366d2bc50d3d26d9d377fc6de7ad7f33dec85df4e06436ee6640c6b`, receipt `243ce812-c3a5-4a87-a23f-7539ed9cc1cd`. Selection `Graph|Entity|AsOf|Temporal|Assertion|Memify`; TestPipeline_DeepSeek_ExtractsGraph skipped because DEEPSEEK_API_KEY is unset. No live model pass claimed. Component/broad/current initial full share frozen revision `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:479ce288edd53847b454d883ba8adf5827c45d583f5fe8991cdce93d634aa9d7`; every observed post snapshot matched.

Root initial full session59086 observed **make actualexit2:3599 PASS/1FAIL/2 existing opt-in skips**. S0–S2 completed; S3 sole SQLite `TestGRPCIngestUsesServerStorageIdentityAndLiveMetadata` failure; S4 did not run. Log `/tmp/levara-t15-current-test-commit.jsonl`, SHA256 `a4cbce179e3495a58b1d4538324e0997b581bab1af6295947be26e50e17625a6`, receipt `6dffe3c8-b5fe-4925-b13b-eb17ea969816`. I99 source audit confirms client Canceled is not server ctx.Done; fixture immediately released a noncooperative Save and snapshotted before actual handler completion. Production checks server ctx after Save and carries it through SQL publication; no production bypass attributed. Fixture repair and fresh focused/full/contract gates pending. Failed log retained; not labelled flaky or silently replaced.

T16 read-only mapping confirmed global community rows/membership lack exact used-source publication provenance; list_communities lacks the selected-tenant/retained global authority boundary; current build reads all graph datasets despite its trusted local caller and rebuild SQL ignores errors/uses SQLite-specific syntax. A membership or scope marker cannot authorize an old summary after node provenance changes. Its global boundary repair and own provenance change are next; no T16 implementation/acceptance claimed. **Whole T15 remains open,15/32 accepted.**


### T15 final native recovery gates — 2026-10-06

I99 test-only repair waits for the actual blocked Save server context cancellation, releases the backend, then waits for the actual unary handler return before inventories. Root fixture session74105 actualexit0: two dialect leaves each passed ten repetitions under race (20 PASS events/0FAIL/0skip). Log `/tmp/levara-t15-grpc-fixture-race.jsonl`, SHA256 `8364cd254ff980421f4c0ae8f358c49a03a780a133c21cc7fd600e0febac3d58`, receipt `24e52dd9-3926-47a8-bd6c-15d5634f03cc`. No production cancellation bypass attributed or production change made for this fixture.

Root final full session80431 actual make exit0, **S0–S4 green, 3788 PASS/0FAIL/2 existing opt-in skips** (TestDCDVSALoadBaseline/TestT11NativeRAGLocalQuality). Log `/tmp/levara-t15-final-test-commit.jsonl`, SHA256 `43a8b75daa92ff9d798df44a28ce1f307c7821e9138f5eafd0db63b0cf461946`, receipt `d918ae11-dee4-4932-9493-f1a952ed46b5`. Root final contract session46143 actualexit0, empty `/tmp/levara-t15-final-contract-check.log`, receipt `196998b3-b47f-47fe-b974-2514c46d0d63`. Native PostgreSQL DSN/BIN supplied. GOFLAGS excluded `^TestMemoryREST` in accordance with the user's stop on additional REST owner probes.

All final fixture/full/contract pre/post snapshots matched `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:0fa6b4f9a3715f3d6597e24bda1f5617430c80c5739617e0ad31c19e77a97697` (186 untracked); prior native component/broad production files unchanged. Independent reviewer inspected the actual barrier repair and both logs and confirmed counts/hashes, no new native blocker; root observed actual terminal exits. Previous failed gates remain above. Gortex graph receipt freshness remains unresolved and is not reported as clean.

Native T15 fixes are verified; **whole T15 still open (15/32 accepted)** because original Neo4j sandbox backend parity gate I98 is not fulfilled. Next independent work: T16 global community admission/response lifetime, then exact-used source provenance and SQL publication in its own change. No T16 acceptance from admission alone.


### T16 admission/lifetime and SQL prerequisites — 2026-10-06

Own OpenSpec `scope-community-publications` has proposal/design/spec/tasks, strict validation exit0 and apply-ready status. Tasks1.1/1.2 completed; source/proof tasks remain open. I100 repaired verified active-admin/no-selected-tenant/read admission for list_communities and both actual MCP transports. Shared completed sender caps verified credential expiry, acquires cancelable transfer fence, and retains it until actualClose; all eight production callers materialize before sender. Local empty/filter/error shapes retained. Documentation states completion and transport limits.

I101 prerequisites: ReplaceCommunities checks each delete/insert and rolls back; $n/ON CONFLICT portable, short-ID safe, rows closed before subsequent SQL/policy. Incremental/lookup errors propagate. Active graph parser filters both bounds precisely; summary snapshot/generation lifecycle still pending. Additive schema fields default legacy generation='',sources_json='[]',lineage_verified=0; populated migration preserves exact text and rejects invalid flag. These defaults do not certify old summaries.

Root session32594 actualexit0 focused native race **126 leafPASS/0FAIL/0skip**, log `/tmp/levara-t16-prerequisite-race.jsonl`, SHA256 `c7ab409e8e35c1c650f0e6872a0c3cdfc343ed536742957c2b92335846ef4039`, receipt `39f1b7ed-f7fc-4bb1-a4f2-983a025b3a5b`. pkg/community compiled but this selection had no matching package tests; tests run in native HTTP fixtures. Root separate wholepkg session98593 actualexit0 race **32 leafPASS/0FAIL/2 existing short-mode Louvain performance skips**, log `/tmp/levara-t16-community-package-race.jsonl`, SHA256 `2f0b0d2de7b58d59c844f424180cd0a8b3ed442dbfae0219ca7eedc472e00971`, receipt `3777c9cb-eac0-4f8a-961c-c4c0f9c4b190`.

All prerequisite pre/post snapshots matched `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:d69d44d1ecec5a5095b9a3cb5d0a45c0d746257993cc44217bc77bd47fa4ee0c` (195 untracked). Independent source/log audit confirmed 126 leaves and SHA, no bounded blocker. Private signature verification observed clean6callers/0implementors; other Gortex detect results vary/truncate/pending, no comprehensive freshness claim. Remaining source gaps explicitly preserved: summary currently rereads temporal-unfiltered edges; vector embedding can return aftercancel; separate UPDATEs lack generation CAS. Full proof/publication/consumers and fresh final gates pending. **T15/T16 remain open;15/32 accepted.**

## T16 full publication: first native race gate (2026-10-06)

Root observed session30643 actual exit1: **399 distinct leaf PASS / 16 FAIL / 0 SKIP**. Frozen revision `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:52f540f3a95a628a9f8d6ff7c7966df667ef98ee9ab656739352250217abbe31`; pre/post manifests `/tmp/levara-t16-publication-first-pre.json` and `/tmp/levara-t16-publication-first-post.json` identical (200 untracked files). Log `/tmp/levara-t16-publication-first-race.jsonl`, SHA256 `dd3d61e10acc15a18b2bc54cb17060b6d6ec69d7ef0f1e8eec61cb371f09d045`, fail receipt `c8a02951-a567-4d83-9d95-677b03f079e8` (v236).

Command: native disposable PostgreSQL DSN + `go test -p=1 -ldflags=-w -count=1 -race -json ./pkg/community ./pkg/mcp ./pkg/access ./internal/http -run 'Community|Global|GraphACL|Prune|ProtectedResponse|FencedResponse|WorkspaceReadResponseFence|GraphPathDocumentAuthorization' -skip '^TestMemoryREST'`. Fourteen builder/provider leaves failed from unsupported Scan of SQL string into JSON v2 RawMessage; model/embedding-start failures are downstream. Two old LOCAL/GLOBAL fixtures failed from missing publication columns/SQL summaries. New native consumer, collection-origin and prune lock-order checks passed in this failed aggregate command; that does not accept whole T16. Independent reviews found and repairs addressed native serializer compatibility, missing community marker origin, legacy summary invalidation; runtime repair rerun pending. T15 Neo4j I98 and full T16 DoD remain open, 15/32 accepted.

## T16 own native change accepted; roadmap dependency retained (2026-10-06)

`scope-community-publications` own tasks 8/8 complete. Original roadmap T16 remains unchecked because original T15/Neo4j I98 prerequisite is open; **15/32** accepted overall. Source-scoped community partitions remain outside this own instance-wide active-admin/no-tenant change.

- Repaired native race: root session1791 actualexit0, **427 leaf PASS / 0 FAIL / 0 SKIP**; log `/tmp/levara-t16-publication-repair-race.jsonl`, SHA256 `88bad17cd0a90e1c9e3d76a93eee90e675c3d262853b1fcd01220a89661b7bcb`, receipt `6c7e2f23-42b2-4501-b1eb-c8c279abc99d` (v237). Pre/post `levara-t16-publication-repair-{pre,post}.json` identical on `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:ebb4d690d722c94c7a6bc2920eec6dfb5ce9697933bae3401e56dcef3d0afba8`.
- Full: root session3244 `make test-commit` actualexit0, **3942 PASS / 0 FAIL / 2 existing opt-in SKIP**, S0–S4 green. Skips: DCDVSALoadBaseline and T11NativeRAGLocalQuality. Log `/tmp/levara-t16-full-test-commit.jsonl`, SHA256 `fd0716b5789ee00d151881fcd54902833036ec573c2841aeea387c6f52e77707`; receipt `20589c0a-e4de-4ca7-bbfe-9344827c3161` (v240). Full pre/post identical on the same dirty ebb4 revision. Native disposable PostgreSQL configured; TestMemoryREST excluded as requested.
- First standalone contract session84312 actualexit2, log `/tmp/levara-t16-contract-check.log`, SHA256 `5e12a855df6c71953e8b7f2474dc345c4ff0661f44ae70775ec09e07a792d61c`, receipt `f162b65b-f439-416b-a1a6-f48f4d8e51c1` (v241). Root generator session73864 actualexit0 updated only schema sections: six graph_communities ALTER entries (three/dialect) in full/core JSON and rendered inventory. MCP/REST unchanged; AGENTS byte-identical. Before copies: `/tmp/levara-t16-contract-before`. Runtime code unchanged after full gate; no handwritten generated files.
- Generated-only delta + whole community/pipeline unit coverage: root session42308 actualexit0, **117 PASS / 0 FAIL / 2 existing Louvain short-mode performance SKIP**, race/no warnings. Log `/tmp/levara-t16-derived-package-race.jsonl`, SHA256 `33b909a26fda52be1538b84f994fdd1596d202b6ab15eed360bb004139a85eff`, receipt `945db360-71b1-4b3e-b1cb-d168d6a0f53a` (v242). Command native `go test -p=1 -ldflags=-w -count=1 -race -short -json ./docs ./pkg/community ./pipeline -skip '^TestMemoryREST'`. Pre/post `/tmp/levara-t16-derived-contract-{pre,post}.json` identical on dirty `598ffc77e130ff8ea57fd29bb49cdbbc2376068f9c12080f8c0117be9da950db`.
- Final standalone contract session18858 actualexit0, empty `/tmp/levara-t16-contract-final-check.log`, SHA256 `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`, receipt `e79700e8-d95c-4c94-b323-0a90d9aadee9` (v243), final manifest matches dirty598 revision. Final standalone OpenSpec strict session24287 actualexit0, `/tmp/levara-t16-openspec-final-check.log`, SHA256 `0cf761982a259538b2e529f491cdad57e8f6fd3ffc06b4d335743d73aa342ef8`, receipt `fe142ed8-0f8a-4b65-afaf-3b612fb9e3e1` (v244).
- Independent current source/log/manifest acceptance audit: receipt `b032bd65-256a-45d1-bbb7-e55e755fa5e7` (v245), own native change has no source blockers; original T15/Neo4j prerequisite retained. Gortex guarded disk edits confirmed, signature checks clean where reported; graph detect freshness refused pending receipts and is not claimed comprehensive.

Coverage includes production lineage serializer, full detection input/endpoint/parent proof, bounded proof parser, immutable prompts and active graph edges, single-TX rollback, source retirement/revision/generation/proof change, noncooperative model drain, same-DB embedding guard/pool1, successful late canceled vectors, SQL-authoritative text and vector generations, native server collection origin through generic search, exact materialized evidence and actual response Close, independent PostgreSQL graph/data prune lock order. Full recompute is supported; verified incremental optimization is deferred. Physical stale vectors may remain, SQL/vector effects are not atomic, and no production deploy/live migration was performed.

## T17 own native taxonomy change accepted; original dependency retained (2026-10-06)

`implement-taxonomy-seed-writer` own tasks **9/9 complete**. Original roadmap T17 remains unchecked because T15/Neo4j I98 is still open; overall original acceptance remains **15/32**. This delivers the bounded private caller/exact-selected-tenant/dataset catalog, additive transactional seed import/list/remove, content-free replay journal, CLI and supported native SQL graph DCD bindings. Source authorization and dataset-owner selected-tenant filtering are checked before grants in the same reader. Shared catalog/proposal generation and external quality/Neo4j parity are not claimed.

Failed history retained:
- First native race session83410 actualexit1 at dirty `a33a9c5415c60d55b26d4e26dbf3592fd77028e8974a9b6e705ea1a558cd0491`: 90 leafPASS/4fixtureFAIL/2optinSKIP plus HTTP abort from confirmed VSA nested-row pool1 deadlock. Log `/tmp/levara-t17-native-first-race.jsonl`, SHA256 `dd32bd90c069022d809e3ca8b2b494d24b412f36d3b4a1bc97d43b6924f00c52`, fail receipt `ff9559ee-940d-4cfb-87ac-d479e7945458` (v249). Exact disposable HTTP test stack preserved via SIGQUIT, no production process interrupted.
- Second race session7082 actualexit1 at dirty `33f5315609320ac88bf18805caeb99817d2e42eef230fd56ed44d3087db85832`: 123 leafPASS/0leafFAIL/2optinSKIP **but two failed lifecycle SQL parent branches**, expired credential GET503 instead of401. Log `/tmp/levara-t17-native-repair-race.jsonl`, SHA256 `f9e79c443e11377b5f907d67b431aa3537a1d36bfb308ae0b2a3225045090170`, fail receipt `93299a08-dcc0-46fc-9bcc-cc0f4e1c2e51` (v250). VSA cursor and native quality repairs passed in this failed aggregate command. Leaf-only counts are not acceptance evidence.

Final observed root gates:
- Authority/parser/CLI/native DCD race session27534 actualexit0, **130 leafPASS/0FAIL/1 existing DCDVSALoadBaseline opt-in SKIP**, no failed parents, all3packagesPASS. Native disposable PostgreSQL configured. Log `/tmp/levara-t17-authority-race.jsonl`, SHA256 `85fefd1f065f14dc99e5035e67fe667c6487944ea7b7ee6d901011c3bcbd161d`, receipt `d2e2cd56-0a0f-4815-a5a3-adb4947bd109` (v252).
- Whole VSA package race session5019 actualexit0, **6PASS/0FAIL/1 existing live embedding SKIP**. Log `/tmp/levara-t17-vsamemory-package-race.jsonl`, SHA256 `f64557af5e5b0836d6cace7805ac412a8ad762129afb0c5bc1aff96112659d83`, receipt `a4e494c0-7845-4609-8cf6-91bca9568756` (v253). Both final race gates share frozen pre/post dirty `dd49b7231324f14c243f1b60a29c234dd07147b21aeb784138d31fcab9ebc8f4`, 217untracked.
- Full session95696 `make test-commit` actualexit0, **3988PASS/0FAIL/2existing opt-in SKIP**, S0–S4 green, all11packagesPASS, no failed parents. Skips DCDVSALoadBaseline/T11NativeRAGLocalQuality. Log `/tmp/levara-t17-full-test-commit.jsonl`, SHA256 `da39e8de508c544d8c558c49e4a5950ce0a84ec38960e19aefc5483851ad07ec`, receipt `19333f3b-c4f7-4842-bea8-548961764328` (v254). Full pre/post manifests and final contract post match `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:9488d14b305fdfff096138e6003396446b7dcf04a545a0f46fd4ce628307d429` (217untracked); runtime unchanged after final race, delta only docs/task tracking/generated contracts.
- Generators `make contract` session39851 and `make contract-core` session86076 each standalone actualexit0. Only REST167→170 (3taxonomy routes) and schema303→305 (2mirrored journal CREATE entries); MCP/gRPC unchanged, AGENTS byte-identical. Before copies `/tmp/levara-t17-contract-before`. Final standalone contract session1905 actualexit0, empty `/tmp/levara-t17-contract-final-check.log`, SHA256 `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`, receipt `d0d45cef-736e-4a0e-be5d-9e3d5d1afd0c` (v255). Standalone strict session66510 actualexit0, `/tmp/levara-t17-openspec-final-check.log`, SHA256 `ec8a2f399961c109e0526c6ff9f0d59cea248c09d05ff4b6d92fe86662cfe4d6`, receipt `afd3d7a7-1d5b-44e4-9643-0a4f523ea830` (v256); separate git diff --check actual0.
- Independent current source/log/manifest audit found blocking findings closed and own change ready; receipt `81365b9d-1cfd-446a-ae36-572e729bd71b` (v257). Every Go command excludes `^TestMemoryREST` as instructed. Documentation acceptance updates after gates are not presented as functionally tested bytes; Gortex disk edits committed while graph detect freshness pending, not a clean graph claim.

Coverage: both SQL/pool1, atomic late SQL/journal rollback, stable identities/additive field preservation, exact request replay/conflict, concurrent imports/cancellation, invalid Unicode/control/size/hierarchy inputs, foreign/read-only/source-denied datasets, selected-tenant foreign-owner grant rejection and empty-tenant compatibility, verified API read-key/revocation, legacy ambiguity/orphans, forced removal preserving raw sources and graph. Actual listing body partial-read/Close/expiry retains SQL until release; queued credential expiry tested for GET. Actual imported native GRAPH_COMPLETION quality fixtures show off=observe order, boost eligible rank2→1, zero-result counts0 and denied/retired exclusion in both SQL. No external model/corpus/performance/default-enable claim; production deploy/restart/commit/push not performed. Next independent implementation: T18 code/git supported contract.

## T18 own code/Git knowledge change accepted; original dependency retained (2026-10-06)

`harden-code-git-knowledge-publication` own tasks **10/10 complete**; original T18 stays unchecked due T15/Neo4j I98. Overall original acceptance **15/32**. Checked Go errors/unsupported extensions, honest Python heuristic, contextual Git, static generation-scoped graph through existing immutable source publication, administrator/no-selected-tenant authority and CLI errors are delivered.

Failed history retained:
- First native race session58156 actualexit1: **137 leafPASS/17leafFAIL/0SKIP**, plus failed parent assertions. Log `/tmp/levara-t18-native-first-race.jsonl`, SHA256 `063cb4c8dd8f10ad340e0e6ac68cb8f53300f68078ad9b1338a6e9b507c2c088`, fail receipt `52b8af60-8672-4384-aab3-1a76a3a97b44` (v259); identical pre/post dirty `7c8823ae2b921839b00fe0cdef8f717dc56f0b6bf5d5b1c34c235d506132e4ff`. Fixture endpoint, Git future date, hidden auth404 and legacy contentRevision expectations corrected without weakening checks.
- Source-preparation diagnostic session70367 actualexit1: **1PASS/1FAIL/0SKIP**, PostgreSQL positive, SQLite `near LOCK: syntax error`. Log `/tmp/levara-t18-source-prepare-diagnostic.jsonl`, SHA256 `abdafaa7bfd0ad36a5205ae00c73b155f29170d954bb8f6ebdf0ecd63aa5f85a`, fail receipt `bb820577-65c5-4f2c-83ab-473ce7520e71` (v261); identical dirty `db732c70ba3a71b6e46d38242164427d31e3503a2792943a83a44072f5aa444e`. Fixture selected HTTP dialect but omitted ingestion SQLite mode; production initializes both. Corrected fixture, retained direct first-source regression.

Final root-observed gates:
- Native race session88476 actualexit0: **164 leafPASS/0FAIL/0SKIP**, no failed parents, all6packagesPASS. Native disposable PostgreSQL + `go test -p=1 -ldflags=-w -count=1 -race -json ./pkg/extract ./pkg/git ./pkg/orchestrator ./pkg/mcp ./cmd/cli ./internal/http -run 'Analyze|ParseLog|Static|Codify|Git' -skip '^TestMemoryREST'`. Log `/tmp/levara-t18-native-repair-race.jsonl`, SHA256 `42fe5e0446fa2b6a4efa06bde8823bd5c0a9f9be517d3d8ef5ebfeba2dd08d7b`, receipt `be36bec2-7214-4c06-badc-161a80d83e00` (v262).
- Full session91634 `make test-commit` actualexit0: **4057PASS/0FAIL/2existing opt-in SKIP**, no failed parents, all11packagesPASS/S0–S4green. Native disposable PostgreSQL; all Go excludes `^TestMemoryREST`. Log `/tmp/levara-t18-full-test-commit.jsonl`, SHA256 `6ed80d63d12263cf5d5af81002443243370f8911749914592402903447f655f2`, receipt `f4765536-687f-41e6-813b-f1de9eb2467a` (v264).
- Native/full pre/post and contract-post manifests identical: `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:fe65a55529f04482e0de8ce5644b2e9fe1aded4bf0116d88f2a5c532250c8742`,231untracked. Full/core generators77442/70537 actualexit0; description-only T18 descriptor creates no schema delta, prior generated changes retained. AGENTS byte-identical to pre-T17 snapshot.
- Standalone contract20941 actualexit0; empty `/tmp/levara-t18-contract-first-check.log`, SHA256 `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`, receipt `b112bbbc-7b3f-4cbd-b1b0-18be0b94bb8f` (v263). Strict17406 actualexit0; `/tmp/levara-t18-openspec-final-check.log`, SHA256 `94105ce31a4dabaf6d8b59555ffda3051d4e16bf93087a1b30cc3f646e3934f4`, receipt `af7c02e8-f14a-4720-a2a3-6bf869e1ebd8` (v265); git diff --check0.
- Independent source/log/hash/manifest acceptance audit confirms own change; receipt `87799938-daad-4623-b32b-b940ca3353c7` (v266). Later task/evidence tracking bytes are metadata updates, not the frozen functionally tested revision. Gortex graph freshness is not claimed comprehensive.

Both-SQL/pool1 controls reach actual embedding failure, processing credential expiry, controlled late source/content CAS after graph insertion, source retirement/current generation and actual retained response Close. Trigger oracle proves late CAS rejection, not independent-writer scheduling. Git empty/filter/invalid/corrupt/repeated/worktree/cancellation and analyzer errors covered. Raw failed-ingestion source and physically stale derived rows can remain; SQL eligibility is authoritative. No external model/Neo4j/cross-store atomicity claim. No commit/push/deploy/live migration/restart. Next T19 file CAS/history/access.

## T19 — file integrity (implementation and failed gates retained)

Own change `harden-workspace-file-integrity` has11 unchecked tasks; source implements confined native regular-file I/O and project locks, exact UTF-8 `file_digest`, staged validated commits/restores, sidecars/audit/watcher containment and Task executor file operations. T20 publication/recovery remains separate. No acceptance from source alone.

First native session6446 actualexit1:33leafPASS/1FAIL/0SKIP, pkg/workspace failed `TestAtomicWorkspaceWritesCompleteAndCleanup`; internal/http build failed due nonexistent actor accessor; pkg/mcp passed. `/tmp/levara-t19-native-first-race.jsonl`, SHA256 `2f174e3b81946e995a3e38d12d0f85da6df1fa7f1522eb7670b624744863420e`; matching pre/post revision `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:2177fddd254730fb4f66e36d5861d2fc9a67b23dc8489586cac5c8dc2f82302d`,248untracked. Receipt `588dfed8-3e61-4fb4-a5c1-47b16b5c2eec` v268. Early git diff --check exit2 (extra EOF blank) also retained; formatting fixed later.

Initial source audit found hard links, queued credential expiry, stale job admission loaded before project lock, and restore case aliases. Reviewer fail receipt `84059ee5-15e3-480b-8f6c-1d3a4da32608` v270. Repairs preserve native nofollow opened-descriptor reads, strict write inode checks, expiry cancellation/no-effects, fenced exact job reload and actual staged-path capability checks. Independent source re-audit confirms causal repairs; runtime acceptance pending. Existing atomic test already serializes cooperating writers with native project lock and retains unlocked complete-byte reader.

Second native session22699 actualexit1: **582leafPASS/3FAIL/0SKIP**, failed SQLite independent-process CAS/restore and watcher debounce/status; pkg/workspace and pkg/mcp passed, internal/http failed. `/tmp/levara-t19-native-repair-race.jsonl`, SHA256 `c87927849e9f71b7227f80368dd9da8b90f6a9dd17be1133fadffb30a576fae7`; matching pre/post dirty `f68e207f0ee94346489d36ffa76d80bb16a2ef6d1c8f6a3279bbfaac3ff62f10`,249untracked. Fail receipt `b467af05-643f-4dd7-a085-cf1c16dcd8df` v271. Installed SQLite driver requires `file:` URI and `_pragma`; bare path with query opened an unintended empty database. Trusted-local global watcher status has no project tree and must retain existing empty-project read diagnostics behavior.

Focused repair diagnostic45935 actualexit0:7leafPASS/0FAIL/0SKIP, bothSQL/trustedlocal actual-processCAS/restore and global watcher status. `/tmp/levara-t19-child-diagnostic.jsonl` SHA256 `ba152b0ac1568922c2882d0030d13e77bc737101bc59308782cee9ec756a6638`; same pre/post dirty87bca,249untracked; receipt `007cbde9-c8e9-49de-896a-e20748381e68` v272.

Third native7190 actualexit1:583leafPASS/2FAIL/0SKIP. Atomic whole-byte fixture and trusted-local actual-process CAS failed concurrent lock opening (`openat atomic.lock/alpha.lock: no such file or directory`); bothSQL process controls now passed. `/tmp/levara-t19-native-final-race.jsonl` SHA256 `c55e7d4183c690fb3dddd420cc495fbce157cfd6895eed2b602b5dadf496fad8`; same pre/post dirty87bca,249untracked; receipt `10dd7985-56c5-4856-84c4-3a4885280fc6` v274. No race-data warning or partial-byte oracle failure. Full/core generators42273/60669 actual0, contract2433 actual0 empty log (SHA256 e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855), receipt `89742860-e80c-4fae-8450-ab3d492dd24d` v273. AGENTS byte-identical to preT17 snapshot. Strict94379 actual0. Overall T19 acceptance still pending native-open repair and full gate.

Native-open atomic repeat9283 actualexit1: count100 yielded49 terminalPASS/51FAIL (not last unique test status); log `/tmp/levara-t19-lock-repeat.jsonl` SHA256 `1842403b4ed380fc79879a793291fb44765ce29393f70f2b298a869ed397b6a9`, matching dirty771d6dccef3d5c9996f5db3b5f0b8afc1f42fa5bbb929d1142742aaa57bc64d0,249untracked; receipt `e199d8d3-cba8-4ca8-b3f2-7b9e09db13a2` v275. Original regex omitted new creation test; only atomic repeated. No kernel-level root cause claimed.

Verified exclusive-create protocol repeat95767 actualexit0: correct both-test regex, count100 each atomic whole-byte and24-caller first project lock creation, **200terminalPASS/0FAIL/0SKIP**; log `/tmp/levara-t19-lock-protocol.jsonl` SHA256 `9c509be1c2d62fe651a2cc74ebe6f1c2f5c4ab8bcbcd82c8540861486035f829`, matching dirtyc9fd6206116323f514f75090e9b850ccdaa09ad375de4f1f0628f47d9590a848,249untracked; receipt `9d2f972d-8158-47c9-96e3-f4097f8a044a` v276. Direct native nofollow descriptors remain; absent nonexclusive-create uses one exclusive creator, ONLYErrExist verifies regular winner inode then opens without create, all other errors retained. Independent source review reports no blockers. Final comprehensive native/full/cross-platform checks pending; checkpoint `bac3c67f-b14c-456f-a7f7-fa82c033e097` v277.


## T19 final acceptance — 2026-10-07

Own change accepted 11/11; original roadmap T19 accepted (16/32). Frozen revision `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:8ca1e54259d6b06c0a65ebfaffe2d60381ba26b5f6e067f246f08497671189f1`, 249 untracked; native/full/contract pre/post manifests match.

- Native SQLite/PostgreSQL and trusted-local race: actual exit0, 586 leaf PASS/0 FAIL/0 SKIP; no failed parents. `/tmp/levara-t19-native-protocol-race.jsonl`, SHA256 `a17341af8ea594ab1932ad896dad9d634d3b9de4106bf7c364b1215a1c34cb10`; receipt `c0e35716-56b0-4cb8-8f0d-3885b87e4713`.
- Required make test-commit: actual exit0, 4165 leaf PASS/0 FAIL/2 existing opt-in SKIP, all 11 packages and S0–S4 passed. `/tmp/levara-t19-full-test-commit.jsonl`, SHA256 `16efd8f090a9b2d545a1f0ec1273d2f2a70f394f5a91a24894d78c2d2b72e193`; receipt `e89a047b-8ee3-400d-b6e8-c4d09c23d6db`.
- Full/core generators and final contract-check exit0; empty `/tmp/levara-t19-contract-final-check.log`, SHA256 `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`; receipt `336a5772-ca70-4018-a944-46ec5009756c`.
- Strict OpenSpec and git diff --check exit0. Independent combined-source/contract/log audit accepts all 11 criteria and original T19; receipt `ee698f6d-9cd4-43ef-ab26-3f3341a22371`.
- Lock protocol repeat actual exit0: 200 terminal PASS across 100 runs of atomic writes and concurrent first creation; earlier 49 PASS/51 FAIL retained. Windows/amd64 and Linux/amd64 compile exit0; execution only on native macOS.

Every Go invocation excludes `^TestMemoryREST`. Metadata acceptance edits follow these functional gates; no functional bytes change. External editors remain uncooperative; directory-swap crash recovery belongs to T20. Historical metadata migration I78 and dedicated Neo4j prerequisite I98 remain open. No deployment or production migration/restart performed.


## T20 development gates — 2026-10-07

Change `repair-workspace-generation-recovery` has 10 tasks; strict planning validation exit0. First package gate actual exit1: 24 leaf PASS/5 FAIL/0 SKIP; failed parent branches retained in `/tmp/levara-t20-package-first.jsonl`, SHA256 `3b96331c37d9ebed23826e219cf260d0c47689768dc3a98ff06d3129e744fbee`. This is a development gate without a matched whole-worktree freeze, not acceptance. Clone lost nonnil empty slices; focused fixtures used default chunk minimum and failed to create intended IDs. Clone repaired and explicit positive preconditions/chunking added; rerun pending. Native HTTP generation/recovery/watcher gate is running. All Go excludes `^TestMemoryREST`.


T20 development continuation (not frozen acceptance): package repaired gate actualexit0 29PASS/0FAIL/0SKIP, `/tmp/levara-t20-package-repair.jsonl`, SHA256 `4818362c36cb595062b442f2af6e84306a6cd086bde6af7d8630a8329e3ad8a6`. Initial HTTP build gate actualexit1, zero tests, undeclared collection; `/tmp/levara-t20-native-first.jsonl`, SHA256 `1605f64df8035618a52161fad38d3efe1dc3ae4012ad06ad2b3a7099fffe313d`. Next native development gate actualexit1 57PASS/5FAIL/0SKIP, `/tmp/levara-t20-native-repair.jsonl`, SHA256 `351fcdffafe225b26464f55f3ea29cceb055b500266acc0b8324c48a82413f01`; parents retained. Repaired generation/Task/process gate actualexit0 44PASS/0FAIL/0SKIP, `/tmp/levara-t20-generation-second-race.jsonl`, SHA256 `b190777212ab5e2c7db6f6368e10b9054c6e1d7bcde4b9009fb86a6725417d2d`. Separate watcher gate actualexit1 35PASS/2FAIL/0SKIP, `/tmp/levara-t20-watcher-repair-race.jsonl`, SHA256 `a8d8e4f5512fd8de172e453d5ef267286197a2f32e834e40a54a06d1440cca61`; intentional source-change conflict now asserted then retried. All excluded `^TestMemoryREST`. Subsequent source repairs/new tests require combined frozen race/full gates. Full and core contract generators actualexit0; final contract-check still pending. Independent source review led to Task/service journal integration, null inventory, journal-only watcher bootstrap and explicit dead-letter supersession; no final T20 acceptance yet.


### T20 frozen native gate — repair required

Whole-worktree before/after matched `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:d76372e895998895cf2fd24bac63f406877d442f5a1cbb7eb1d53cf4ecd79b96` (262 untracked). Actual terminal exit1, 625 leaf PASS /5 FAIL /0 SKIP; workspace/MCP PASS, HTTP FAIL. Log `/tmp/levara-t20-final-native-race.jsonl`, SHA256 `db20f79a2c7c51e0872748670354587c1e353e4073a2975fa3e54b485ec85f66`. GC authority bothSQL used obsolete collection-drop oracle; rerank bothSQL forced an obsolete vector-only score. Explicit historical lookup was a real compatibility regression and panicked the old test, aborting remaining HTTP cases. All failed parents remain in raw JSONL. No final acceptance. Root repairs exact historical scope and keeps generic search active-only; independent review additionally required full-active-inventory-only terminal supersession. REST memory owner-spoof tests remain excluded per user instruction.


T20 second frozen native gate: actualexit1, whole pre/post matched `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:1c7f370acecedca8174b2f8160d8734f43b088239356f4ec21c3ecd4b9bd5c37` (263 untracked). Unique leaf parser 688PASS/4FAIL/0SKIP; ALL failed branches retained in JSONL: six independent legacy/latest historical transport failures (slash-named test hierarchy collapses legacy endpoints into apparent parents) plus old full-cycle GC collection-drop oracle. Source workspace/MCP packages PASS, HTTP FAIL. `/tmp/levara-t20-second-final-native-race.jsonl`, SHA256 `9f69a7be18b05b6bd68118b7c1e16896db42dfa8dd0d7e4cb95044e270851dfe`. Actual REST historical body and bothSQL zero-hit transfer controls passed. Root repaired shared MCP protected-tool classification + credential bound and final GC exact-ID oracle; no final acceptance. Independent source review also confirmed per-source historical egress and project evidence including REST status400 paths. Focused earlier repair gate actualexit0 37PASS, `/tmp/levara-t20-last-repair-race.jsonl`, SHA256 `f5e149207c29b72a23bf5854b5c532f681a3905055101acd76fafaed4af7e971`. All Go gates excluded ^TestMemoryREST per user instruction.


# T20 — accepted 2026-10-07

Original roadmap DoD and all 10 change tasks accepted by independent source/evidence review. Active generation is published once for a complete validated batch; failed preparation preserves prior manifest truth. Attempt records, running jobs, watcher digests and restore journals recover under existing authority and process locks. GC removes exact obsolete IDs and preserves shared collections. Explicit retained historical search keeps source-scoped authorization; ordinary search stays active-only. REST and both MCP transports retain authorization through body Close, including empty results.

Frozen tested revision: `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:8a611dac222a80bbbf73eb3b82cbc3b0fd8f76b863be8714d3af31599093c464` (263 untracked files). Whole-content snapshots before/after native, full and contract gates matched.

| Check | Actual result | Log SHA-256 |
|---|---|---|
| Native race, SQLite and dedicated disposable PostgreSQL | exit 0; 692 leaf PASS, 0 FAIL, 0 SKIP; 3 packages PASS | cd91ae3fb87cafb51a68603b45e5a4ab6a95ebdaa5bfc7eab6163894d5c65f1a |
| make test-commit, S0–S4 | exit 0; 4260 PASS, 0 FAIL, 2 existing opt-in SKIP; 11 packages PASS | 2a24778594e3f3cda53145a59fa4db5b4b48e56ca57fcccd3c406bb609620f85 |
| Full/core contract-check after generators | exit 0 | e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855 |
| OpenSpec strict validation | exit 0 | 91391a908874a011972bc4acfbd6557688a6282c1f075c0c3916579ad417b2bb |
| git diff --check and independent review | exit 0; no open acceptance blockers | runtime review receipt d81a2882-f4ae-4221-8ef5-fd00f1cbfd3d |

Commands:
```sh
go test -p=1 -ldflags=-w -count=1 -race -json ./pkg/workspace ./pkg/mcp ./internal/http -run 'Workspace|Confined|Atomic|ProcessLock|Task.*(Executor|Workspace)|Indexer|GCGenerations' -skip '^TestMemoryREST'
GOFLAGS='-p=1 -ldflags=-w -count=1 -json -skip=^TestMemoryREST' make test-commit
GOFLAGS='-p=1 -ldflags=-w' make contract-check
openspec validate repair-workspace-generation-recovery --strict
git diff --check
```

Both SQL dialects used dedicated test stores. Full-gate skips: TestDCDVSALoadBaseline and TestT11NativeRAGLocalQuality. User-excluded REST owner-spoofing reproduction remains excluded.

Corner cases covered: later-file provider failure, same-generation retry, missing/deleted/zero-chunk files, legacy unknown inventory, mid-provider external edit and retry, cross-process completed-job preservation, running-job restart, watcher offline changes and coalescing, journal-only recovery, Task lease/read/CAS recovery, partial/inactive/full publication versus dead-letter ownership, exact GC with foreign shared physical/lexical records, stale historical membership and subsequent generic search, real signed JWT/tenant transport bodies with partial Read and Close, zero-hit metadata after access revocation.

Failed diagnostic history is preserved in [central evidence](decomposition-evidence-2026-10-05.md): initial package 24/5 then repair 29/0; HTTP build failure; development 57/5; generation 44/0; watcher 35/2; first frozen 625/5; focused 37/0; second frozen 688/4 leaf counts with all seven failing branches retained; focused MCP sender repair 22/0; final frozen 692/0. Earlier failures are not acceptance receipts.

Limits: no cross-store or power-loss atomicity claim; process-crash fixtures model persisted intermediate states, not syscall kill barriers. External editors do not join cooperative locks. Empty-result tests exercise the actual verified-credential transfer boundary; REST provider-error branch is source audited, not claimed as a provider-hook test. No production rollout performed.

Acceptance metadata changes only tasks/evidence and the three roadmap documents. Functional content map before these edits: 1482 files, SHA-256 `869c6423cd6e4985f08e7c3aa81583903a64db334b88d7b2fc77b0704f18419d`; final continuity check must match exactly.

## T21 bounded sync/consolidation evidence — 2026-10-07

Original T21 remains open; roadmap remains 17/32. This phase accepts 4/11 tasks: selectors, consolidation revisions, graph/interactions fixed points, and complete/inclusive incremental export. Memory lifecycle/deletion/canonical identity, independent collection status, and whole-change acceptance remain unfinished.

## Current frozen proof

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


## T21 active-memory phase and T22 local backup — 2026-10-07

T21 remains open at5/11; supported local standalone T22 accepted, roadmap18/32. [T21 evidence](../../openspec/changes/repair-independent-sync-convergence/evidence.md), [T22 evidence](../../openspec/changes/repair-verified-backup-inventory/evidence.md).

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


## Combined integration failure history

The first frozen S0 gate exited2 with36 PASS/1 FAIL/0 SKIP because of a broken central-evidence self-link; /tmp/levara-t21-t22-final-test-commit.jsonl, SHA256 fb3c11586973f7bbeb1ef7453609e52f0eb4799b7ff86d17c6739063d85645fc, receipt46e7472d-6e20-4795-ad3d-58d120231042. Only that metadata link was repaired.
The restarted frozen b7000fed gate exited2 with4152 PASS/1 FAIL/2 opt-in SKIP. Only the immediate SQLite concurrent pool InUse oracle failed; S4 did not run. /tmp/levara-t21-t22-final-repair-test-commit.jsonl, SHA256 cd0b5ad71704b8577b1dfba8f1d8190f088101e2f4543d6a3aaae847cfa9dc40, receipt6142c1c5-455c-44c0-8639-2c6c9d85b536. Both whole pre/post maps equal.
The test-only repair reserves the entire native two-connection pool simultaneously and retains two idle connections before workers start, preserving strict final zero-InUse and convergence assertions. database/sql includes pending opens in InUse; raising MaxOpen alone retained the previous MaxIdle ceiling. Actual race count20 exit0:40 dialect leaf executions, no FAIL/SKIP or failed parents; /tmp/levara-t21-pool-oracle-repair-race.jsonl, SHA256 24cc9c02e53b8e7dec87f74505d8c527a0d98de04c95befd72b3f2e8f90ee6c8, receipt17d142d4-512f-4fb1-9f30-39246cc15f87.
Independent source/functional-map review confirmed ONLY sync_convergence_test.go changed: prior native production/backup receipts carry forward by exact byte continuity; no old failed gate is relabeled passing.

The next frozen full command hit the Go HTTP-package aggregate default10m budget: exit2,4125 completed PASS/0 completed FAIL/2 SKIP, but package FAILED and watcher PostgreSQL branch aborted after only1s. /tmp/levara-t21-t22-pool-repair-final-test-commit.jsonl, SHA256 d94be4319384c704817d4fdb27f9dc9bb2edd188fab7a5a62a6466f0ab9b1e74, receipt f4f0f0bc-5d6f-4bcf-8d4c-3780609dd644. Whole before/after513397 revision equal; this is not a passing gate. S0-S2 were successful on that identical revision. Only S3 is rerun with20m aggregate budget, per-case deadlines unchanged, and S4 runs afterward. Acceptance is assembled from actual per-stage commands on the same frozen source, never a fabricated make exit0.


## T23 native experimental acceptance — 2026-10-07

Original T23 accepted, roadmap19/32. [Full criterion mapping, failed histories and bounded limits](../../openspec/changes/repair-native-cluster-recovery/evidence.md).
Frozen native and whole S0–S4 revision3bf5d61d agrees before/after; native race462 PASS/0 FAIL/0 SKIP; actual whole make test-commit exit0,4374 PASS/0 FAIL/2 opt-in SKIP. Native SHA13261879f0acf5d1a0f5e070fefdc3db7b894ea657350b96800f3b08132c338d; whole SHA0ab239ba972f51608654a0e41f9c7dbe8f49e1053cee1a12091f2e5318210bcd. Receipts3b42b68f-2f2b-4fdc-9110-29a924c99915 and7a6bd248-baca-4acc-8132-9005ff3f5793.
Contracts, strict OpenSpec and diff checks exit0; independent read-only review accepts the original bounded criteria. Functional1516-file map SHA1229537889a9e06c795de4f388dc8e0238f5c76d03a7d3676fe3e5509ce3ff5c excludes only nine named acceptance metadata files. Historical failure and interrupted143 evidence retained; neither is relabeled successful. Opt-in skips and user-excluded TestMemoryREST are not passed coverage. Clustering remains experimental; power-loss, snapshot installation/compaction, linearizable reads and all-voters-alive Raft TCP partition are not claimed.

## T26 ledger/leases/completion acceptance — 2026-10-07

Original T26 accepted, roadmap20/32. [Criterion mapping, native evidence, failure history and bounded limits](../../openspec/changes/harden-task-ledger-replay/evidence.md).
Native race684 PASS/0 FAIL/0 SKIP on unchanged2878800f revision; current full S0–S4 actual exit0,4445 PASS/0 FAIL/2 opt-in SKIP on unchanged747b68fa revision. Native SHA0e13265911f5a1bcaeec30e6a9e3fa9e6fa60c9fb552acd2f3e4565cb8e2eab6; whole SHA156a7e9f44afde188bd31dd9c8c7e31c833b66210453fd383c3f41f3cb6fa0c2. Receiptsbe5d7f04-eb55-44c0-82fb-d2dc6272cbab and03272a7d-05a8-4c50-aacc-ea4d03469d21.
Contract full/core generation, final contract-check, strict OpenSpec and diff exit0. Independent review confirms original mapping and actual SQL trigger-backed atomic promotion/rollback. Functional1523-file SHA c4e77fa542f821c51385275dceb29eccb4d2f1c9c669af83f011aafefa221760 excludes only11 acceptance metadata files. First whole integration failed on the exact PostgreSQL lock-mode oracle; focused2/0/0 proves the operation-specific repair preserves NOWAIT ordering. Initial replay/policy/post-hash failures and generated full/core drift retained. Cooperative artifact guard does not claim OS/backend or power-loss atomicity. User-excluded REST owner-spoof tests and two opt-in skips are not passed coverage.

## T27 bounded executor acceptance — 2026-10-07

Original T27 accepted, roadmap21/32. [Actual effect/corner-case matrix and native/whole evidence](../../openspec/changes/prove-workspace-executor-recovery/evidence.md).
Native expanded441 PASS/0 FAIL/0 SKIP and focused6+6 PASS; whole actual S0–S4 exit0,4457 PASS/0 FAIL/2 existing opt-in SKIP on unchangeddc709f2b revision. WholeS4actuallyexecutes193 server leaf tests. NativeSHA696683b7fa2f7a31317587345d4c67d5fb7456f61689b76f41c3c16c8552ef57; wholeSHA2fad1c595c7f780bcecd8d1349dc6e45fdd5eed925a3cd51cfa641823a0d644b; receipts39b87bcb-5b44-452c-abd6-334c327f5413 and4f583631-212b-4900-9974-4c9b217175d9.
Functional1529-file SHAa5049164e03f3173973dcc9434eb3d8aa031b8aa574cee1615aede9c4790c9ed excludes13 acceptance metadata files; contracts/strict/diff actual0 and independent raw audit pass. Viewer/editor, real profile and bytes/receipts/audit, project-lock action deadline, first-manifest-read change and actual SIGKILL-before-receipt/naturalexpiry/CASrecovery verified bothSQL. No production behavior/API/schema/dependency changed. Direct-core/first-read/earlier-action-deadline limits retained; no rawOS/powerloss/shell/network claim. No failing command observed for T27, two opt-in skips/userRESTexclusion aren't passed coverage.



## T21 collection phase acceptance — 2026-10-07

Original T21 remains open; its own change is now6/11 and the roadmap remains21/32. Native receiver-contract guards/stamping, actual independent SQLite OS job status, differing duplicate payloads, long-null chunks and zero-unit accounting passed final race220/0/0; whole make test-commit actually exited0, S0–S4 green,4475/0/2(all11packages). Both opt-in skips remain unclaimed. Source/native/full revision36fb28 and functional1532/c2a347 stayed identical. Final contracts/strict/diff passed; current metadata gates and reviewer receipt are recorded before continuation. [Detailed preserved failures, digests and limits](../../openspec/changes/repair-independent-sync-convergence/evidence.md). I182–I186 closed for this phase, with the unordered-ID oracle distinguished from production bugs. Whole memory lifecycle/delete/aliases and original T21 acceptance remain unfinished.

## T21 full native and whole acceptance — 2026-10-07

All11 change tasks and originalT21 behavioral DoD are accepted; roadmap22/32. Public protocol3 uses external native logical-generation/state-revision/immutable-alias ledgers, whole-memory-envelope SQL/outbox atomicity and canonical logical-lineage ranking. Native Save UPSERT retains the actual persisted incarnation; retirement/revert advances state revision; terminal deletion defeats unseen stale UUIDs; fresh native recreation allocates the next generation independently of wall-clock content timestamps. Historical unresolved original keys are explicitly rejected rather than guessed.

Actual final native race gate exits0,531PASS/0FAIL/0SKIP, all3packages/no failed parents: `/tmp/levara-t21-generation-complete-native.jsonl`, SHA256 `ee42dc1ff3596a75dec36871046c2befe0dc6bee1cc088350f8523e5b0f12a88`, receipt `4872937f-63ec-46f7-9b08-6a2dfaa63486`. Actual whole `make test-commit` exits0/S0–S4green,4539PASS/0FAIL/2existingopt-inSKIP,all11packages/no failed parents: `/tmp/levara-t21-generation-complete-integration.log`, SHA256 `a0f4eb72f703359bf9277f3c02ee1138111f87740474aa521e7b248c2a06516c`, receipt `2e898d75-dd9a-4d4b-958b-ad4a8d44bba5`. Skips are TestDCDVSALoadBaseline and TestT11NativeRAGLocalQuality. Every Go gate excludes `^TestMemoryREST` per user instruction.

Native/full before/after revision is identical: `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:52e1f6e7eb2649340de0b2891958245cdf2456b517b72ff7c0452b7e134d65ba` (334 untracked). Functional1544-file maps agree at `d08b4da50357142858f0f7329532cd74a077208b3cdef67a98014ab89187c492`, using only established13 metadata exclusions.

Current bothSQL proof includes actual native Consolidation Apply/Revert plus public-v3 exchange and real index worker; receiver foreign Revert denied without a journal; stale applied snapshot cannot retire restored sources or resurrect deleted summary; real HTTP response loss after receiver commit followed by SKIP-only retry; fresh generation1 beats future-clock generation0; SKIP aliases persist; both independent roots reopen the exact SQLite file/private PostgreSQL database/schema with fresh native pools/outboxes and unchanged six-table snapshots; missing/cross-scope/canonical-self lineage reports truthful HTTP200/error/all-failed counts and exact rollback. Required auth flow, ordinary-user rejection, header injection, untrusted destination, redirect and local collection credential controls pass in the same current gate. Current collection re-embedding/status/chunk/contract tests pass; earlier actual independent SQLite OS collection-job proof is retained below.

Current `make contract-check`, installed OpenSpec strict validation and `git diff --check` exit0. Standard full/core generators include the three generation-ledger tables and scope/unresolved indexes for both SQL dialects. Independent read-only audit checked raw logs, every current functional byte, originalT21 DoD and required scenarios: no remaining behavioral blocker. Postacceptance bookkeeping/design-status delta and its docs/strict checks are recorded separately below; no compiled source/spec/test mutation is inferred from bookkeeping.

Limits: fresh-pool SQL reopen is not an OS memory crash/power-loss claim. Preserving a live local Revert journal after reverse peer import is not established. These are unclaimed extras, not requirements in originalT21/spec. All prior failed gates below remain historical, not relabeled. I150/I151 and I190–I199 are closed by the current proof.


## T29/T32 current controlled gates — 2026-10-07

Roadmap acceptance remains30/32 while T29 Pi SLO and authoritative Task Runtime audit/receipts remain open. The following are observed gates, not fabricated runtime acceptance. The original user-authorized Frida process was stopped temporarily; its matching respawn was resumed and is running, but originalAPI8081 is still rebuilding WAL/HNSW. Last verified remote Task version457; no successful remote checkpoint/lease/receipt is implied by local evidence.

T29 hardware report and unchanged thresholds: [declared plan and observations](t29-target-load-plan-2026-10-07.md). Mac controlled500RPS audit/mixed/backlog/deadline/restart passed; Pi audit overhead+10.408ms, mixed foreground and original301.866s drain failed. Every acknowledged value/scope and prior audit ID was retained; failed responses are not ACKs. Deterministic embeddings are plumbing/resource controls, not model-quality certification. A1s idle-worker diagnostic is predeclared separately and is not yet executed.

| Current observed gate | Actual result | Log SHA256 |
|---|---|---|
| WholeGo,20m suite budget | 6467leafPASS/0FAIL/7environment-opt-inSKIP; exit0 | 08fac91f5a3279440215eadd7ae8a6925b0cd0c798dd7fc934abe6b3b180438d |
| Final test-commit S0–S4 | exit0; HTTP577.844s | 8361b3c2a5fc325990ca00991be74363e4633f11c01b2399b69a80d4417e075c |
| Final release-candidate, full nativePG16.15 directory | exit0 | dba81793a9ac49e608af4e73e6502c5cba12d56c602d4828415ca68e7c86c6c2 |
| Contracts, profile config/smoke/enterprise, vet, build | all actualexit0 | per-gate outcome manifests under `/tmp/levara-t32-final-corrected-gates-20261007` |
| Final Go lint | exit0; no suppressions | aa04748d02bd7d266043ae712252fac025c8540137fddbac5706afa492605792 |
| WebUI lint/build | both actualexit0; existing cleanup-ref warnings retained | bccfb9273692e0e5f3ef5a66156f3e1e5cc8c007ca2eaf990f41c6667f3b0532 / ce25bdb5e8425929f3bf1c0b6d7d324169504890e37307c127fb87c3b2fe28db |
| WebUI curated |116PASS/0FAIL, exit0 |7ab65935e4b46d525d6f277ab570aade6bf4ce5a51159983508a3d052b7310a7 |
| Native6 actual final archived backend |6PASS/0FAIL, exit0 |b43d57fe0273c780f3af35e87567af673167cd768a81174297fe3bd4a2d30eff |

The fresh native browser workflows use actual authenticated SQLite/API19330: chat owner consent/admin grant/revoke/private retention, workspace CAS/draft preservation, memory room/hall and ID deletion, Task lease/failed-step/foreign-owner denial, document upload/search/exact download/admin grants, graph path/properties/revocation. No route mocks. Backend SHA6287af9eccd12fe0b7d25aa5ed83c1c485c3606f8196f11e3654fe212097549a comes from final archive SHAc78f74c95db694090b39e3a422bdb3960bcfe64c887d7e70f75ea5435e89bad3: exactly six approved product binaries, seven profile presets and LICENSE; no publication or deployment.

403 retry regression was first observed failing on oldcode (graph200→403→automatic200). A single query-provider condition makes401/403 terminal;503 retries and manual recovery remain. Focused403/5032PASS; fullcurated/native matrices pass. Earlier native5PASS/1FAIL runs and their unproven3.82s assertion timing remain historical; passing final results do not establish that old timing cause.

Revision accounting: wholeGo used20b391fa…; corrected combined mandatory gates used36ed95af…; later01db2cd4… differs only in two explicitly verified comment replacements before fresh priority/profile checks, contract/lint and archive build. Exact full revision manifests and observed outcomes are stored in the three dated artifact roots. Postgate evidence bookkeeping and regenerated archive bytes change dirty-state hashes; no unchanged whole-worktree claim is made.

Seven wholeGo SKIPs: host127.0.0.2 bind, absent explicit localLLM, absent book fixture, three absent DeepSeek credential integrations, absent livePotion endpoint. They are not PASS evidence or vendor certification. User-forbidden TestMemoryREST prefix was excluded throughout. Pi full-inference resources, external IdP/HSM/paid-provider production certification and remote-SQL-over-SSH SLO remain unclaimed. Issue ledger I244–I255 preserves failures and residual gaps.

## T29 strict acceptance and T32 current final gates — 2026-10-08

T29 passed the unchanged strict Pi protocol in the fresh owned root `/mnt/nvme/levara-t29-owned-20261007-1035/dispatch-acceptance-workers2-final-20261008`: baseline 498.891 RPS/p95 1.809 ms, audit 499.900 RPS/p95 2.161 ms with zero errors and 0.352 ms overhead; 30 ms-provider mixed load completed with zero errors at heartbeat p95 46.406 ms, recall 154.902 ms and save 127.322 ms. All 16000 acknowledged jobs reconciled to exact SQL values/scope/links and drained in 67.510 s. Three held-provider jobs kept their identities across restart and recovered in 0.284 s; all prior source and durable audit IDs were retained without duplicates. Peak server/recovery RSS was 212664320 bytes. Primary JSONL is explicitly best-effort: 299777/300000 lines with 223 dropped projections; four stable line/byte/mtime samples prove quiescence, while durable SQL retained the complete audit identity set. The accepted final-run bottleneck was eight hard-coded memory-index workers on a two-CPU target; the default is now two with a validated override. This does not relabel the separately recorded historical SQL/WAL/search failures. HNSW defaults 6/48 retained measured top-10 recall 0.900, and identical concurrent searches share only in-flight work.

Current T32 combined gates passed after dependency preflight. `make test-commit`, `make test-release-candidate`, `make contract-check`, profile config/smoke/enterprise E2E and vet outcomes are under `/tmp/levara-t32-final-gates-20261008`; the later lint/build/release-artifact/equivalence repeat is under `/tmp/levara-t32-final-artifact-20261008`. Fresh whole Go evidence `/tmp/levara-t32-final-green-20261008/full-go.log` has 7733 passing test nodes (6504 leaf PASS), 0 FAIL and seven explicit environment/opt-in leaf SKIPs; SHA256 `a9f2e692f001a1ebfaa6cd925c2e5b6c5d5ebdc93af8a1d3fdf969c150a21b5f`. The user-forbidden `TestMemoryREST` prefix was excluded and is not claimed as passing evidence.

WebUI lint/build and all 116 curated scenarios pass under `/tmp/levara-t32-final-ui-20261008`; curated log SHA256 `c343568aff2c638d06ec62ec024057fc6ea94c339b70d5447103b00d3eda719b`. The six native browser workflows pass against the current packaged server under `/tmp/levara-t32-final-native-20261008`; log SHA256 `28145fdc2bff0e1b15c92b3079daa5b639745f14b3da1244bb3cccc198969c07`. Those scenarios exercise authenticated chat owner consent/project administrator grant-revoke-detach, workspace CAS, memory room/hall, Task lifecycle, document ACL and graph revocation. Failed reruns caused by a stale allowed browser origin, omitted SQLite fixture variables and the production auth rate limit remain retained as fixture-orchestration failures; the complete disposable configuration rerun is 6/6.

The release archive `levara-release.tar.gz` has SHA256 `fbb60f49fd08a700835739d5637616720ab04a9acb19d358bba98b4aeda7d3ce`. Its 14-entry allowlist is LICENSE, six product binaries and seven profiles; no source/development entries are present. All six packaged binaries are byte-identical to independent rebuilds with the same flags; proof is `/tmp/levara-t32-final-artifact-20261008/artifact-equivalence.json`. The packaged server SHA256 is `c9cc54a55b60aaf8f6f62cd3d272979d178c582d7bb3d9537b936021ca5e0933`.

External live LLM/DeepSeek/Potion, unavailable book corpus, alternate bind-host check, vendor IdP/HSM/paid-provider production certification and `TestMemoryREST` remain outside the accepted claim. They are explicit gaps, not PASS evidence. No commit, push, publication, deployment or live migration was performed.

Independent review then found that search singleflight copied the result slice but shared nested mutable `json.RawMessage` bytes. The bounded fix deep-copies each record's metadata and extends the coalesced-caller regression to mutate one result while the other remains valid. Post-fix focused race count10 and full `internal/store` pass. All current mandatory gates were repeated at `/tmp/levara-t32-postfix-20261008`: test-commit, release-candidate, contract, profile config/smoke/enterprise, vet, lint, build, release-artifact and current packaged-server native6 all exit0. `gate-outcomes.json` contains their log digests; native6 log SHA256 is `ce01ffdc7ffbd5fde0ab8525e7a751cbb5668d7ebffd34491bff01fde731f3f9`. The rebuilt archive SHA256 is `adf67b9461ca1567aae0a487bebfcf4d2fe5f10ee340f262bc414dc426e1c029`; its 14-entry allowlist and six byte-exact independent binary rebuilds are recorded in `artifact-equivalence.json`, with packaged server SHA256 `97902798b609e9edc2cd4d95cdb22fdde2300db6eb4575c5adf4bd6d44c9ff37`. The strict Pi measurements remain attributed to their measured pre-follow-up binary; the ownership fix is covered by post-fix local gates and is not relabelled as a Pi rerun.

Independent final review verified every selected T29 mirror artifact against its manifest, all post-fix gate log hashes, the rebuilt archive members and binary equivalence, native6, and the nested metadata ownership fix. Verdict: PASS with no remaining implementation or artifact blocker in the declared release scope. I249 stays explicit fail-closed unavailable-classification debt; live vendor/model checks and the user-excluded REST test remain outside the claim.
