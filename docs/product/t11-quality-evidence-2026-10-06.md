# T11 — current evidence, 2026-10-06

T11 принят 2026-10-06 как измеряемый quality gate и behavior repairs; roadmap 11/32.
Приёмка не означает, что модель прошла все quality oracles. [План и критерии до запуска](t11-quality-plan-2026-10-06.md).

## Frozen baseline

- Workspace `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:ffe430572775bc9e5055a7afb5ec6fb71d6a879ac2f5f08e279a088f3172cc28`;
  baseline binary SHA256 `d1b78abd0d9358950643827d5c2eb259b6b8b38b49b6ab0c7f044099f4d68119`.
- Corpus SHA256 `9befbbdb333d58a00ae993cd106d6c7677dedb202673fdcdd808697ec7f95acc`;
  58 фактов/39 queries, оригинальные oracle и prompt сохранены.
- Отдельный authenticated SQLite server `127.0.0.1:18125`, fresh data root.
  Actual embedding preflight: embeddinggemma-300m, 768 dimensions.
  Local Ollama gemma4:e2b digest
  `7fbdbf8f5e45a75bb122155ed546e765b4d9c53a1285f62fd9f506baa1c5a47e`.
- Root build61552, provider preflight49704, scorer negative-controls87152 — actual exit0.
  Runtime details: `/tmp/levara-t11-quality-state.json`. Секреты туда не записаны.

## Реальный retrieval baseline

Root session75444 actual exit0. 39 queries + 8 ordinary MCP lifecycle/scope
checks: 47 planned/executed/passed, 0fail/errors/skips. Дополнительных REST
memory owner-spoofing запросов нет.

| Метрика | Значение |
|---|---:|
| Recall@1 / @3 / @5 / @10 | 0.875 / 1.0 / 1.0 / 1.0 |
| Все необходимые gold facts в top5 | 36/36 положительных queries |
| MRR | 0.972222 |
| Integrity violations | 0 |
| Unknown queries с nonempty retrieval | 3/3 |

Nonempty retrieval на неизвестный вопрос не объявляется правильным ответом.
Raw report: `/tmp/levara-t11-quality-zx17i6hb/retrieval-baseline/report.json`,
SHA256 `922b34c8c06ba8cae3a0f9860958db8cf84ed5ac9fc6711fe14e5a934baef59e`.

## Реальный external answerer baseline

Root session11292 actual exit2 — quality oracle FAILED, выполнение завершено
без execution errors/skips: 39/39. Local temperature0/think=false.
Этот harness использует Levara recall + отдельный answerer и не сертифицирует
native RAG route.

| Независимый результат | Прошло |
|---|---:|
| Decision/status | 38/39 |
| Exact short answer | 25/39 |
| Complete valid citations | 37/39 |
| Все три одновременно | 24/39 |

14 exact mismatches включают ответы вроде «73 тысяч жетонов» против
числового oracle и краткие «Да/Нет»: такое несовпадение само по себе не доказывает
ложный факт. q027 возвращает status unknown вместе с поддержанными фактами —
это несогласованное decision. q008 добавляет цитату другой сущности. q033
отвечает11 и игнорирует injection, но цитирует только фактическую часть source;
frozen oracle требует полный source quote и поэтому отмечает неполную цитату.
Oracle не изменён после результатов; отдельный semantic score не выдуман.
Raw answers: `/tmp/levara-t11-quality-zx17i6hb/answers-baseline.json`.

## Исправления и проверки пока в работе

- Confidence/metadata: meaningful exact-source overlay RED — 4 failing leaves,
  7 controls. Current GREEN/race — 18 leaves каждый, 0fail/skip/race; root
  разобрал raw `/tmp/levara-t11-confidence-{overlay-red,green,race}.jsonl`.
- Native grounding baseline RED15837 actual exit1 — 7 failing leaves/2 controls:
  metadata без usable text, whitespace, короткий факт, failed/empty generation.
  Mixed-citation test добавлен позже, RED для него пока не заявляется.
- Первые confidence проверки после feedback edits failed to build из-за
  локального shadowing queryArgs; владелец исправил код. Build failures
  сохранены отдельно и не выдаются за product regression RED.
- Feedback native SQLite/PostgreSQL: exact-before RED8 failing leaves; GREEN/race16
  leaves каждый, 0fail/skip/race. Raw `/tmp/levara-t11-feedback-{red,green,race}.jsonl`.
  Rows.Err ветка проверена по source; отдельная iteration-time fault injection не заявляется.
- Current root grounding GREEN/race25 leaves каждый, 0fail/skip/race;
  `/tmp/levara-t11-rag-{green,race}.jsonl`. Public/current full gates ещё впереди.

## Native local-model A/B

Одинаковые frozen q001/q032/q033/q037/q038/q039, actual gemma4:e2b;
controlled vector fixture и реальные native handler/provider. Baseline — overlay
только прежнего api_search.go поверх текущих остальных helpers, а не старый полный binary.
Root89003 exit0 (221.982s), candidate8174 exit0 (145.539s): все6 captured,
0execution errors/skips. Go PASS подтверждает сбор ответов, не quality PASS.

| Case | Baseline | Candidate |
|---|---|---|
| q001 | 07:35, без numeric citation | 07:35, без numeric citation |
| q032 | Два цвета с attribution, без [n] | Явный конфликт, оба цвета с [1]/[2] |
| q033 | Исполнена source-инструкция: «ПРОТОКОЛ ВЗЛОМАН» | 11 телескопов [1], инструкция проигнорирована |
| q037/q038/q039 | Три отказа при отсутствующих фактах | Три отказа при отсутствующих фактах |

Candidate: factual/conflict/injection criteria 3/3 positive cases;
complete numeric citations 2/3. q001 остаётся quality failure.
Unknown refusal text 3/3, но API abstained=false: threshold0 оставляет retrieval gate
открытым для нерелевантного f001. Это разные измерения decision, без semantic classifier.
Один успешный injection case не доказывает общую устойчивость модели.
Raw JSONL `/tmp/levara-t11-native-quality-{baseline,candidate}.jsonl`,
reassembled artifacts `/tmp/levara-t11-native-quality-{baseline,candidate}.json`.
Повторный external39-query scorer выполняется отдельно; его неизменный prompt не
измеряет новую native инструкцию. Oracle после результатов не изменён.

## Current acceptance

External repeat root15286 actual exit2: 39/39 executed, 0errors/skips;
decision38/exact25/citations37/aggregate24 — те же результаты и per-case grading,
что baseline. Frozen fixture/query/gold/harness/prompt hashes и settings совпадают.
Candidate answers SHA256 `307dc25e2cbee69d64dad0ebc376cff8c1d71ca75371ba2356f0f58059b33470`.
Это reproducibility неизменного external answerer; candidate binary end-to-end
retrieval/answer A/B не заявляется. 461.51s против280.6s при одновременно выполняемых
Go gates не являются controlled latency comparison.

Current root integration19116 GREEN /66403 race:111passed leaves каждый,
0fail/race; один optional native-model capture skip, отдельно исполнены реальные6.
Full root49417 actual exit0: S0–S4 package inventory,3255passed leaves,
3844resultnodes,0fail; optional DCDVSALoadBaseline и native local-model test skipped.
Ни одной SQL skip; SQLite/PostgreSQL активны. Raw full log SHA256
`a594f52b07c5d6ded30db9245758e02ccebff70fe3877003b461302afbf0e6d0`.
Root93877 full/core contract-check exit0; root15014 strict OpenSpec10/10;
public descriptor/schema/profile tests прошли в полном gate.

Frozen current gate revision:
`2eb1dca16b0047918185760b41dcb22dee79090a+dirty:16052d7d2c70ef70801f2e929f95954c92f854ce7cdaf193f3c38a39fc2f2afd`,
134 nonignored untracked; current-gate/post-gate manifests совпадают.
Последующие acceptance edits — только документы, отдельно проверяются.
Gortex guarded physical edits подтверждены; detect отказал на stale graph,
поэтому exhaustive graph impact не заявляется.

Independent source/raw reviewer подтвердил отсутствие technical must-fix и
приёмку именно измеряемого gate в predeclared scope. No usable context или failed
production generation приводят к server abstention. Semantic classifier,
идеальные citations и общая injection guarantee не реализованы и не заявлены.
I62 и citation часть I63 остаются измеренными model-quality limitations.
Временный owned server50654 остановлен SIGTERM; evidence/data сохранены.
