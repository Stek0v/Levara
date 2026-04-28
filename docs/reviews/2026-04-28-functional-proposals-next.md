# Levara — следующие функциональные улучшения (после confidence/debug wave)

Дата: 2026-04-28

## 1) Сделать confidence не только retrieval-based, но и evidence-based

Сейчас confidence строится из эвристики top score / gap / coverage и router confidence.
Это хороший baseline, но он не учитывает:
- противоречия между chunk'ами,
- graph support для финальных утверждений,
- качество grounding самого ответа.

**Предложение:**
- добавить post-generation verifier: для каждого ключевого утверждения проверять support в `chunks/context`;
- включить в confidence признаки `supported_claim_ratio`, `conflict_ratio`, `graph_path_support`;
- вернуть в API `confidence_breakdown` (по компонентам).

Ожидаемый результат: меньше "уверенных, но неверных" ответов.

## 2) Перевести abstention с глобального env-порога на policy-профили

Сейчас порог управляется одним env `LEVARA_RAG_ABSTAIN_THRESHOLD`.
Это удобно для старта, но плохо для разных доменов/тенантов/типов поиска.

**Предложение:**
- ввести policy-конфиг на уровне query_type/domain/tenant:
  - `rag_completion.threshold`
  - `graph_completion.threshold`
  - `context_extension.threshold`
- дать runtime override через settings/API, а не только через env.

Ожидаемый результат: адекватная чувствительность к риску для разных use-case.

## 3) Унифицировать debug-блок для всех search стратегий

Сейчас debug metadata прикрепляется в RAG/graph ветках, но не во всех векторных search type.
Для клиентов это делает контракт неоднородным.

**Предложение:**
- добавить единый post-processor response envelope для всех search strategies;
- всегда возвращать `debug.source`, а для routed запросов — `debug.strategy/reason/alternatives`;
- документировать стабильный schema для frontend/SDK.

Ожидаемый результат: проще интеграции и observability на клиенте.

## 4) Добавить explainability артефакты: citations + evidence ids

Сейчас есть `answer` + контекстные массивы, но нет строгой привязки утверждений к источникам.

**Предложение:**
- возвращать `evidence_ids[]` (ids chunks/edges), использованные в ответе;
- опционально `claims[]` с привязкой claim→evidence;
- добавить режим strict-grounded-answer (без evidence — abstain).

Ожидаемый результат: проверяемый GraphRAG, лучше для enterprise.

## 5) Включить feedback loop в confidence/router адаптацию

Сейчас router уже умеет adaptive override, но confidence не калибруется по реальному фидбеку.

**Предложение:**
- писать implicit/explicit feedback в online calibration dataset;
- периодически пересчитывать калибровку confidence и веса router-а;
- ввести safe rollout (shadow -> canary -> full).

Ожидаемый результат: система самоулучшается на реальном продовом трафике.

## 6) Добавить операционные метрики качества ответа (не только latency)

Для production нужна не только скорость, но и контролируемое качество.

**Предложение (минимум):**
- `abstain_rate` по search_type;
- `supported_claim_ratio`;
- `llm_call_skipped_total` (из-за abstain);
- `confidence_histogram` + `error_by_confidence_bucket`.

Ожидаемый результат: можно управлять quality/SLO как инженерной системой.

---

## Короткий практический roadmap

### Sprint 1
- Confidence breakdown + единый response envelope.
- Полное покрытие debug metadata для всех search types.

### Sprint 2
- Evidence ids + strict grounded mode.
- Per-query-type abstain policy (конфиг + runtime override).

### Sprint 3
- Feedback-driven recalibration confidence/router.
- Dashboard quality metrics + alerts.

