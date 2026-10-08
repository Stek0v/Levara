# T12 — versioned ingest/publication, 2026-10-06

Принята область авторизованного ingestion с metadata coordinator. Продуктовый
код уже выполнял DoD; нового подтверждённого дефекта нет, переписывания нет.
Roadmap 12/32. Trusted-local путь без metadata сохраняет отдельную совместимость
частичной записи; whole-batch rollback для него не заявляется.

## Native SQL и fault/race coverage

Root23153 GREEN /67004 race actual exit0: по476 passed leaves,594 result nodes,
0fail/race. Один посторонний optional `TestRateLimit_IntegrationPerSourceIP`
пропущен; SQL skips нет. SQLite и отдельная PostgreSQL16.15/port53350.
Команда: `go test -p=1 -ldflags=-w -count=1 -timeout=12m -json
./pkg/ingest ./pkg/access ./internal/http ./internal/grpc
-run 'Ingest|Publication|Lineage|Cognify|Source|MetadataAuthorized|StructuredArtifact|StructuredUpload|ReplaceAuthorized'
-skip '^TestMemoryREST'`; второй запуск с `-race`.

Логи `/tmp/levara-t12-native-baseline-{green,race}.jsonl`, SHA256:

- GREEN `93b7584154f4ec4bc9298663b3b2c65903201eda479c699ed3330941ea0f6740`.
- Race `f19839a3c3bad5a9b51b6c2fc0c4f3c22aaa9b0fd07f2fcc76180d900567502f`.

Проверены invalid second item/duplicates до Save и sidecar, partial storage
failure и cleanup journal, noncooperative Save/cancellation, source CAS и
concurrent replacement, A→B→A/delete-recreate, exact repeat/fault rollback,
stale attempt, inherited-source lineage, atomic COMPLETED/publication,
partial batch terminal truth, failure finalization/retry и реальные gRPC
транспортные вызовы при pool=1 на обеих SQL.

## Actual embedding smoke

Текущий binary root87907 build exit0, SHA256
`f395e0582ebbc87510847e22354acd39826d08d36d29e82d1fb4a444621c3008`.
Сначала зафиксированы два синтетических текста; hash
`05188672a939f7466dbd1f01868bd89c03db3bdd0ef4b0689331faab78d43ff6`.
Свежие authenticated SQLite/vector roots, mode rag, локальный
embeddinggemma-300m/768; graph/LLM extraction в этом smoke не проверяются.

Root47842 actual exit0; report
`/tmp/levara-t12-live-8u7zdd4m/report.json`, SHA256
`fb5b7cf3d85a64a9c5086b10801f5848409c6105ca3ea17164b4e8d8656c4893`.
Точный stdlib harness сохранён рядом как `harness.py`, hash
`7c273aa2774d3e4689fac6e99afcbc50c49ef0125b66dc764d2b2d1dc951446a`.
Проверки:

1. Второй whitespace-only текст → HTTP400, ноль source/claim/publication rows
   и ноль новых обращений к provider.
2. Два валидных текста → COMPLETED; SQL snapshot связывает current source
   revision/hash, claim и publication с точным run attempt; distinct generations,
   независимые chunk counts и соответствующая source lineage.
3. Два native запроса с `query_type: CHUNKS` → HTTP200, document/dataset/generation
   каждого результата совпадают с опубликованным snapshot.
4. Retry по document refs → новый attempt и новые поколения, прежние source
   revision/hash. Остановленный SQLite содержит2sources/2COMPLETED/2publications,
   `lineage_verified=1`, ноль pending journal.

Девять actual proxy embedding requests имеют dimension768; отдельно actual
preflight тоже768. Это здоровый сквозной путь, а не model answer-quality или
нагрузочный benchmark. Owned server завершился с exit0; data/evidence сохранены.

Неуспешные попытки сохранены: первый startup readiness probe превысил10s
(`/tmp/levara-t12-live-5jpg4u_e`), затем исправлены harness assumptions о plain-text
HTTP400, SQL `chunks` и bare-list CHUNKS response (`fofud81p`, `e6oyi9vb`, `o8lh_cae`).
Это не product RED. Предыдущий successful `ah415lra` был AUTO→HYBRID из-за
нечитаемого REST параметра `search_type`; его нельзя объявлять CHUNKS proof.

## Приёмка и границы

Read-only independent reviewer проверил actual source, оба native logs,
report/harness hashes и stopped SQL snapshot: original T12 DoD выполнен в
авторизованной области, technical must-fix нет. Гипотеза credential expiry после
fence не подтверждена и не объявляется дефектом.

Pre/post smoke revision совпадают:
`2eb1dca16b0047918185760b41dcb22dee79090a+dirty:b1c0fcd10bf92dea5bb2f6f32ef1df1141c90a6e37b8cf35ce9119dfec169149`,
134 nonignored untracked. Product source после frozen T11 S0–S4/3255 gate и
contracts не менялся; T12 дополняет его ingestion/gRPC matrix и реальным M smoke.
Последующие acceptance edits только в документах, static/docs проверяются отдельно.
