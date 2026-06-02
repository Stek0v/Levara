Сегменты (по позиционированию Levara)

  ┌─────┬──────────────────────────────────────────────────────────────┬─────────────────────────────────────────────────────────────────────────────────────────────┐
  │  #  │                           Сегмент                            │                                  Что у Levara «зацепляет»                                   │
  ├─────┼──────────────────────────────────────────────────────────────┼─────────────────────────────────────────────────────────────────────────────────────────────┤
  │ S1  │ AI-agent devs (Claude Code / Cursor / Codex users)           │ MCP-первый дизайн, room×hall таксономия, per-agent diaries, wake_up budget                  │
  ├─────┼──────────────────────────────────────────────────────────────┼─────────────────────────────────────────────────────────────────────────────────────────────┤
  │ S2  │ Self-hosters / privacy-conscious devs                        │ Single Go binary, own-your-data, sync Mac↔Pi, без облака                                    │
  ├─────┼──────────────────────────────────────────────────────────────┼─────────────────────────────────────────────────────────────────────────────────────────────┤
  │ S3  │ RAG / KG researchers                                         │ Темпоральный граф (valid_until/superseded_by), BEIR-suite, adaptive rerank gate, hybrid RRF │
  ├─────┼──────────────────────────────────────────────────────────────┼─────────────────────────────────────────────────────────────────────────────────────────────┤
  │ S4  │ Стартапы 2-15 человек (заменяют Pinecone/Weaviate/mem0-saas) │ $0 self-hosted, 719 QPS на ноуте, 100% crash recovery, OpenAI-совместимый embedding API     │
  ├─────┼──────────────────────────────────────────────────────────────┼─────────────────────────────────────────────────────────────────────────────────────────────┤
  │ S5  │ Edge / IoT / on-device AI                                    │ Работает на Pi 5 8GB, model2vec/ONNX backends, 256d potion-варианты                         │
  └─────┴──────────────────────────────────────────────────────────────┴─────────────────────────────────────────────────────────────────────────────────────────────┘

  ---
  S1 — AI-agent devs

  1. «Memory Palace for Claude» — гайд + лендинг, как заменить mem0/memorymesh на Levara за 10 минут. CTA: один curl-снипп + claude mcp add. KPI: installs, MCP-добавлений.
  2. /recall Challenge — конкурс: покажи свой самый «спасительный» recall_memory; победители — лонгрид на блоге + Pi-комплект. KPI: UGC + звёзды на GitHub.
  3. «Room×Hall» серия твитов/постов — 7 коротких постов про таксономию (fact/event/decision/preference/advice/discovery). Каждый с примером из реальной сессии. KPI:
  impressions, follower-conv.
  4. Comparison-bench «mem0 vs Levara» — публичный воспроизводимый бенчмарк (latency, recall@k, cost, privacy). KPI: HN-фронт, реддит /r/LocalLLaMA.
  5. Cookbook: Subagent diaries — пример кода «как ревьюер-агент помнит свои прошлые ревью», workflow с diary_write/diary_read. KPI: cookbook-stars, форки.

  S2 — Self-hosters / privacy

  1. «Your memory, your disk» — манифест-пост против vendor-lock SaaS-memory. KPI: shares в /r/selfhosted, /r/homelab.
  2. One-binary install демо — 60-секундный screencast: curl … && ./levara. Везде, где встречается «надо контейнер»: ответ-сравнение. KPI: video views, install conv.
  3. Pi 5 home-server kit — партнёрка с продавцами Pi: «купи Pi → залей Levara + sync». Brand-bundle. KPI: kits sold.
  4. Air-gap labs — серия туториалов: Levara в локалке без интернета, локальные LLM (Ollama), локальные embeddings. KPI: туториал-просмотры, GitHub stars от
  offline-сообщества.
  5. Sync deep-dive — статья + видео про Mac↔Pi sync (CRDT/WAL-tail, не Dropbox). KPI: dev.to / habr.com просмотры, RU-комьюнити подписчиков.

  S3 — RAG / KG researchers

  1. BEIR-leaderboard publish — выложить собственные числа на 6 датасетах, открыто, с воспроизводимыми скриптами (posttests/bier/). KPI: цитирования, mentions в arxiv-sanity.
  2. «Temporal KG в одну строку» — paper-style пост: как whitelist relations + supersede автоматически даёт темпоральную валидность без Neo4j. KPI: academic Twitter/X,
  citations.
  3. Webinar: Adaptive rerank gate (Phase 2.5) — техдемо, как RERANK_SCORE_GAP_THRESHOLD экономит до 60% cross-encoder вызовов. KPI: webinar регистраций, post-конверсия в
  trial.
  4. Open dataset «memory-bench» — собрать публичный датасет «agent conversations + ground-truth recall queries», запушить на HF. KPI: HF downloads, упоминания.
  5. Joint paper / preprint — соавторство с университетом (RU/EU): «KG-aware reranking for personal memory». KPI: arxiv-preprint, цитирования.

  S4 — Стартапы (anti-Pinecone)

  1. TCO-калькулятор — интерактивная страница: введите свои данные → сравнение $/мес Pinecone vs Weaviate-Cloud vs Levara-self-hosted. KPI: leads, sales-call booked.
  2. Migration guide «from Pinecone в 1 час» — скрипт + блог-пост с конкретными API-маппингами. KPI: migrations выполненных (через telemetry opt-in).
  3. «Crash-test» серия — видео где killaем процесс/диск/сеть — Levara восстанавливается со 100% (есть метрика из README). KPI: video shares.
  4. Y Combinator / Indie Hackers AMA — основатель отвечает 1 неделю в /r/startups, Indie Hackers, на HN-launch-day. KPI: HN-фронт, signups.
  5. Free-tier «forever» — community edition + публичный roadmap; pro = managed cloud + support SLA. KPI: free→paid conversion.

  S5 — Edge / IoT

  1. «Levara on $80 hardware» — пост-бенчмарк: Pi 5 8GB, 4 модели (potion/granite/nomic/jina-fp16), RPS, RAM. (Прямо то, что мы сейчас и делаем!) KPI: makers community
  shares.
  2. Партнёрство с Jina / Nomic / minishlab — co-branded benchmark + cross-blog post про их модели на нашем движке. KPI: backlinks, joint announcements.
  3. «Memory at the edge» серия для робототехников — Levara в ROS-стэке, memory для дронов/роботов. KPI: ROS-комьюнити mentions.
  4. OnnxRuntime / model2vec showcase — гайд: как embed_bench из репо — это не только тест, это шаблон для собственного edge-стэка. KPI: репо-форки scripts/load-profiles/.
  5. Hackathon: «Smallest agent that remembers» — спонсорский трек на embedded-AI хакатоне (Pi/Jetson). Приз = Pi-кит + статья. KPI: hackathon teams, demos.

  ---
  Cross-segment лэверы (использовать в любой кампании):
  - Open-source, MIT-style → дёшево распространять
  - Реальные performance-числа уже есть (719 QPS, 2.6ms, 100% recovery) → cred
  - MCP + OpenAI embeddings совместимость → low-switching-cost истории
  - RU-комьюнити (Habr, Pikabu, t.me-каналы) ещё мало затоптано конкурентами по сравнению с EN

  Скажи, какой сегмент раскрутить (расширенные брифы — целевые персоны, каналы, бюджеты, content-calendar), или хочешь сразу spec-doc в docs/marketing/ чтобы было checked-in?

