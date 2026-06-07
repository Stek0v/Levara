# Markdown Workspace User Scenarios

Этот документ фиксирует `30` базовых пользовательских сценариев для markdown-native workspace поверх Levara. Цель документа: связать реальные пользовательские потоки с уже существующими acceptance/regression тестами и явно показать, какие corner cases покрыты автоматически.

## Solo Workflows

### S01. Инициализация workspace и первый контекст
Mode: `solo`
Goal: разработчик открывает новый проект и получает actionable bootstrap-контекст.
Flow: вызывает `workspace_context`, видит статус проекта, guidance по `workspace_write`, `workspace_search`, `workspace_commit`.
Expected: пустой проект не падает, corrupt manifest отражается как статус, а не как panic.
Автотесты: `internal/http/workspace_test.go::TestWorkspaceContextBootstrapActiveAndInitGuidance`, `internal/http/workspace_test.go::TestWorkspaceContextReportsCorruptManifestPerProject`
Corner cases: пустой workspace, битый manifest, проект без active generation.

### S02. Первая запись Markdown и немедленный read/search
Mode: `solo`
Goal: сохранить новый `.md` и сразу получить его через retrieval.
Flow: `workspace_write` -> reindex/activate generation -> `workspace_read` -> `workspace_search`.
Expected: filesystem truth становится source of truth, search возвращает ровно текущий контент.
Автотесты: `internal/http/workspace_test.go::TestWorkspaceAPIWriteReadAndReindexUseFilesystemTruth`, `internal/http/workspace_test.go::TestWorkspaceAPIIndexDeleteAndManifest`
Corner cases: повторная запись того же path, пустой файл, отсутствующий chunk после reindex.

### S03. Expected digest для безопасной правки
Mode: `solo`
Goal: защититься от случайной перезаписи свежего файла устаревшим агентским состоянием.
Flow: `workspace_read` получает digest -> `workspace_write.expected_file_digest` -> сервер валидирует optimistic lock.
Expected: запись с устаревшим digest отклоняется детерминированной ошибкой.
Автотесты: `internal/http/workspace_test.go::TestWorkspaceWriteExpectedDigestRejectsConflictingWrite`
Corner cases: digest отсутствует, digest не совпадает, параллельная запись в тот же path.

### S04. Reconcile из текущего Markdown без ручной чистки индекса
Mode: `solo`
Goal: пересобрать generation из текущего tree state.
Flow: разработчик меняет несколько `.md`, затем вызывает `workspace_reconcile`.
Expected: active generation строится из текущего markdown tree, stale vector state не протекает в ответы.
Автотесты: `internal/http/workspace_test.go::TestWorkspaceAPIReconcileBuildsGenerationFromCurrentMarkdown`
Corner cases: удалённые path, пустой список путей, смена branch.

### S05. Поиск с цитатами и deterministic read
Mode: `solo`
Goal: агент отвечает только после retrieval и точного чтения источника.
Flow: `workspace_search` возвращает citation contract -> `workspace_read` по path/heading -> ответ пользователю.
Expected: search hit содержит `path`, `heading`, `generation`, `freshness`, snippet; read подтверждает exact source.
Автотесты: `internal/http/workspace_test.go::TestWorkspaceMCPSearchResolvesActiveCollectionAndFreshness`, `internal/http/workspace_test.go::TestWorkspaceMCPToolsDispatch`
Corner cases: stale generation, missing active generation, ambiguous collection binding.

### S06. Commit, log и revert как truth-layer операции
Mode: `solo`
Goal: зафиксировать рабочее знание и откатиться к предыдущему решению.
Flow: `workspace_commit` -> `workspace_log` -> `workspace_revert --reindex`.
Expected: rollback меняет markdown truth layer и возвращает retrieval к старому состоянию.
Автотесты: `internal/http/workspace_test.go::TestWorkspaceAPICommitLogAndRevertSnapshotsTruthLayer`, `cmd/cli/workspace_e2e_test.go::TestWorkspaceCLIFullCycleWriteSearchCommitRevert`
Corner cases: revert без reindex, несколько commits подряд, поиск по старому и новому generation.

### S07. Run artifacts как durable knowledge
Mode: `solo`
Goal: хранить результат запуска задач рядом с knowledge surface.
Flow: `workspace_run_start` создаёт `/runs/<id>/prompt.md`, `stdout.md`, `result.md`; затем `workspace_read` читает их как обычный markdown.
Expected: run artifacts видимы в workspace, индексируются и читаются как first-class документы.
Автотесты: `internal/http/workspace_test.go::TestWorkspaceAPIRunArtifactsAreDurableMarkdown`
Corner cases: metadata в run artifacts, повторный run id, чтение после restart.

### S08. Watcher auto-reconcile после локального save
Mode: `solo`
Goal: получать быстрый search после обычного редактирования файла на диске.
Flow: пользователь сохраняет `.md` -> watcher debounce -> reconcile -> generation activation.
Expected: watcher не делает burst reindex на каждое сохранение, но быстро обновляет active generation.
Автотесты: `internal/http/workspace_test.go::TestWorkspaceWatcherCanEnqueueAsyncReconcile`, `internal/http/workspace_test.go::TestWorkspaceWatcherDebouncesAndReconcilesFilesystemChanges`
Corner cases: burst saves, debounce window, async reconcile без немедленной активации.

### S09. Persisted watch-status после рестарта
Mode: `solo`
Goal: не терять operational state watcher между процессами.
Flow: watcher пишет `.kb/watch-status.json`, затем состояние читается после нового старта.
Expected: enabled/scan/reconcile counters и branch status поднимаются из persisted state.
Автотесты: `internal/http/workspace_test.go::TestWorkspaceWatcherStatusPersistsAndLoads`, `internal/http/workspace_test.go::TestWorkspaceWatcherStatusTracksBranches`
Corner cases: malformed status file, несколько веток, статус без active watcher.

### S10. Context artifacts: OpenAPI, DDL, ADR, runbooks
Mode: `solo`
Goal: включать non-code knowledge как first-class retrieval artifacts.
Flow: правится `.kb/context-artifacts.json` -> `workspace_context_artifacts` -> `workspace_reindex_artifacts` -> `workspace_search`.
Expected: artifact registry показывает include targets, reindex подтягивает их в retrieval plane.
Автотесты: `internal/http/workspace_test.go::TestWorkspaceContextArtifactsRegistryListsAndReindexes`
Corner cases: битый registry, пустой registry, удалённый artifact, смешанные типы артефактов.

### S11. Ops status для self-serve диагностики
Mode: `solo`
Goal: понять здоровье workspace без чтения внутренних файлов руками.
Flow: `workspace_ops_status` или `kb workspace ops-status`.
Expected: пользователь видит jobs, watcher lag, audit summary, metrics rollup.
Автотесты: `internal/http/workspace_test.go::TestWorkspaceOpsStatusReportsJobsWatcherAuditAndMetrics`, `internal/http/workspace_test.go::TestWorkspaceOpsStatusHandlesInvalidJobTimestamp`
Corner cases: malformed audit rows, invalid timestamps, project scope по нескольким веткам.

### S12. GC dry-run перед очисткой старых generations
Mode: `solo`
Goal: безопасно удалить pending/stale generations без surprise deletions.
Flow: `workspace_gc` в dry-run режиме -> проверка плана -> реальный GC.
Expected: dry-run показывает collections/BM25 manifests к удалению, реальный GC чистит их idempotently.
Автотесты: `internal/http/workspace_test.go::TestWorkspaceAPIGCRemovesPendingGenerationCollectionsAndBM25`
Corner cases: pending generation без активной, повторный GC, mixed vector/BM25 cleanup.

### S13. Retrieval quality regression gate
Mode: `solo`
Goal: убедиться, что dense/lexical/hybrid retrieval не деградировал на реальном corpus.
Flow: corpus индексируется -> прогоняется evaluation suite -> сравниваются recall/MRR thresholds.
Expected: `CHUNKS`, `CHUNKS_LEXICAL`, `HYBRID` держат минимальные пороги качества.
Автотесты: `internal/http/workspace_eval_test.go::TestWorkspaceRetrievalQualityEval`, `internal/http/workspace_eval_test.go::TestWorkspaceEvalMetricsEmpty`
Corner cases: запрос без expected hit, heading mismatch, пустая метрика.

### S14. CLI full-cycle без прямого HTTP
Mode: `solo`
Goal: пройти весь поток через CLI и убедиться, что parity с API сохранён.
Flow: `kb workspace write` -> `kb search` -> `kb workspace commit` -> `kb workspace revert`.
Expected: CLI использует те же серверные semantics, что и REST/MCP.
Автотесты: `cmd/cli/workspace_e2e_test.go::TestWorkspaceCLIFullCycleWriteSearchCommitRevert`
Corner cases: commit/revert через CLI, смена generation names, watcher status через CLI.

### S15. Host config merge для одного человека
Mode: `solo`
Goal: быстро подключить Claude/Codex/Cursor к одному Levara workspace.
Flow: merge/install host config -> добавить `Authorization` header -> проверить MCP endpoints.
Expected: существующие host-config поля сохраняются, Levara section заменяется предсказуемо.
Автотесты: `pkg/agenthosts/install_test.go::TestMergeMCPJSONPreservesExistingServersAndFields`, `pkg/agenthosts/install_test.go::TestMergeCodexTOMLPreservesOtherSectionsAndReplacesLevara`, `docs/markdown_workspace_docs_test.go::TestMarkdownWorkspaceAgentHostExamples`
Corner cases: invalid JSON/TOML, dry-run install, backup existing config.

## Team Workflows

### S16. Viewer может читать, но не писать
Mode: `team`
Goal: обеспечить read-only доступ без риска mutation.
Flow: viewer вызывает `workspace_read` и `workspace_search`, затем пытается `workspace_write`.
Expected: read/search разрешены, write запрещён без утечки чувствительных деталей.
Автотесты: `internal/http/workspace_test.go::TestWorkspaceAPIViewerCanReadButCannotWrite`, `internal/http/workspace_test.go::TestWorkspaceSearchHonorsProjectRBAC`
Corner cases: denied write на существующий path, denied read на чужой проект, mixed-role token.

### S17. Editor может писать в рамках проекта
Mode: `team`
Goal: отделить editor permissions от owner/admin.
Flow: editor пишет markdown, выполняет reindex, читает результат.
Expected: editor может мутировать content внутри разрешённого project scope.
Автотесты: `internal/http/workspace_test.go::TestWorkspaceAPIEditorCanWrite`
Corner cases: editor вне project scope, editor без read path, запись в несуществующую ветку.

### S18. Preflight access check до тяжёлого запроса
Mode: `team`
Goal: агент заранее понимает, что ему можно делать в проекте.
Flow: `workspace_access_check` вызывается перед `workspace_search`, `workspace_write`, `workspace_commit`.
Expected: preflight возвращает deterministic role/capability map.
Автотесты: `internal/http/workspace_test.go::TestWorkspaceAccessCheckPreflightHonorsRoles`, `internal/http/workspace_test.go::TestWorkspaceMCPAccessCheckAndAuditLog`
Corner cases: anonymous actor, project not found, mixed room/project membership.

### S19. Access denied без утечки filesystem truth
Mode: `team`
Goal: запретить side-channel через ошибки API/MCP.
Flow: пользователь без прав бьёт `workspace_read`, `workspace_search`, `workspace_context`.
Expected: denied response не раскрывает path existence, active generation и содержимое файлов.
Автотесты: `internal/http/workspace_test.go::TestWorkspaceAPIAccessDeniedDoesNotRevealFilesystemState`, `internal/http/workspace_test.go::TestWorkspaceMCPSearchAccessDeniedDoesNotLeak`, `internal/http/workspace_test.go::TestWorkspaceMCPAccessDeniedForForeignProject`
Corner cases: path существует, project существует, generation существует, но доступ запрещён.

### S20. Audit trail по успехам и отказам
Mode: `team`
Goal: видеть, кто читал, писал и где получил deny.
Flow: агенты выполняют read/write/search/access-check; затем ops читает `workspace_audit_log`.
Expected: audit содержит события успеха и отказа без сохранения чувствительного контента.
Автотесты: `internal/http/workspace_test.go::TestWorkspaceAuditLogRecordsSuccessAndDenialWithoutContent`, `internal/http/workspace_test.go::TestWorkspaceMCPAccessCheckAndAuditLog`
Corner cases: malformed audit row, denied request, mixed REST/MCP sources.

### S21. Team bootstrap видит только разрешённые проекты
Mode: `team`
Goal: не показывать сотруднику чужие проекты в стартовом контексте.
Flow: `workspace_context` вызывается пользователем с ограниченным ACL.
Expected: context list фильтруется по project scope, corrupt чужой manifest не всплывает.
Автотесты: `internal/http/workspace_test.go::TestWorkspaceContextRespectsACLProjectList`
Corner cases: несколько проектов, corrupt manifest в недоступном проекте, пустой scope.

### S22. Конфликты между файловой системой и active generation
Mode: `team`
Goal: понять, есть ли drift между markdown truth layer и retrieval plane.
Flow: один участник меняет файл руками, другой агент вызывает `workspace_conflicts`.
Expected: endpoint показывает dirty/deleted/stale generation сигналы.
Автотесты: `internal/http/workspace_test.go::TestWorkspaceConflictsDetectsFilesystemDrift`
Corner cases: deleted file, unindexed change, branch drift, несколько конфликтных path.

### S23. Coalescing duplicate indexing jobs
Mode: `team`
Goal: не взорвать очередь одинаковыми reconcile/reindex запросами от нескольких агентов.
Flow: несколько одинаковых payload попадают в очередь jobs.
Expected: дубликаты коалесцируются, queue pressure контролируется.
Автотесты: `internal/http/workspace_test.go::TestWorkspaceEnqueueIndexJobCoalescesDuplicatePayload`
Corner cases: одинаковый project/branch/generation, разный generation, burst enqueue.

### S24. Worker обрабатывает pending jobs end-to-end
Mode: `team`
Goal: отдельный indexing worker стабильно подхватывает queued jobs.
Flow: job создаётся API или watcher-ом -> worker поднимает pending -> completion сохраняется в state.
Expected: pending -> running -> completed проходит детерминированно.
Автотесты: `internal/http/workspace_test.go::TestWorkspaceIndexWorkerProcessesPendingJob`
Corner cases: worker restart, empty queue, completion без active generation.

### S25. Retry, backoff и dead-letter
Mode: `team`
Goal: не терять индексирующие операции при временных ошибках.
Flow: worker получает failing job -> backoff/retry -> dead-letter после лимита.
Expected: retries ограничены, dead-letter виден в ops status и может быть повторно запущен.
Автотесты: `internal/http/workspace_test.go::TestWorkspaceIndexWorkerBackoffAndDeadLetter`, `internal/http/workspace_test.go::TestWorkspaceIndexJobsRecordSuccessFailureAndRetry`
Corner cases: transient fail, permanent fail, retry exhausted, повторная постановка dead-letter job.

### S26. Shared search учитывает RBAC и branch freshness
Mode: `team`
Goal: два участника команды получают актуальные search results только в разрешённых ветках и проектах.
Flow: `workspace_search` и `workspace_search` через MCP используют active generation + watcher freshness.
Expected: результаты отфильтрованы по ACL и маркированы по freshness/branch.
Автотесты: `internal/http/workspace_test.go::TestWorkspaceSearchHonorsProjectRBAC`, `internal/http/workspace_test.go::TestWorkspaceMCPSearchFreshnessUsesProjectBranchWatcherStatus`
Corner cases: stale branch, shared project, foreign project.

### S27. Явный stale generation и ambiguous collection handling
Mode: `team`
Goal: агент не должен тихо использовать устаревшую или неоднозначную коллекцию.
Flow: пользователь указывает generation/collection явно или опускает их при нескольких кандидатах.
Expected: stale generation помечается, ambiguous binding требует явного выбора.
Автотесты: `internal/http/workspace_test.go::TestWorkspaceMCPSearchMarksExplicitOldGenerationStale`, `internal/http/workspace_test.go::TestWorkspaceMCPSearchRequiresExplicitCollectionForAmbiguousGeneration`, `internal/http/workspace_test.go::TestWorkspaceMCPSearchMissingActiveGenerationIsActionable`
Corner cases: несколько collections на branch, explicit old generation, отсутствие active generation.

### S28. Командный full-cycle через MCP
Mode: `team`
Goal: агент-хост проходит write/search/commit/revert/GC без ручного HTTP.
Flow: MCP tools `workspace_write`, `workspace_search`, `workspace_commit`, `workspace_revert`, `workspace_gc`.
Expected: весь цикл работает через единый tool surface.
Автотесты: `internal/http/workspace_test.go::TestWorkspaceMCPFullCycleSearchCommitRevertGC`, `internal/http/workspace_test.go::TestWorkspaceMCPToolsDispatch`
Corner cases: commit после search, revert с reindex, GC после rollback.

### S29. Team retrieval evaluation как release gate
Mode: `team`
Goal: команда не принимает деградацию retrieval quality перед merge/release.
Flow: evaluation corpus гоняется в CI, thresholds фиксируют recall/MRR.
Expected: regression в lexical/dense/hybrid поиске блокирует release.
Автотесты: `internal/http/workspace_eval_test.go::TestWorkspaceRetrievalQualityEval`, `internal/store/hnsw_race_test.go::TestHNSW_ReinsertDeletedEntryRefreshesEntryLayer`
Corner cases: corpus пополнен новыми artifact types, heading-level regression, hybrid rank drift.

### S30. Team install/update host configs без потери локальных настроек
Mode: `team`
Goal: массово подключать Claude/Cursor/Codex без поломки существующих конфигов разработчиков.
Flow: `agent-hosts` install/merge обновляет host config и делает backup.
Expected: сторонние MCP servers и unrelated sections сохраняются, Levara block заменяется атомарно.
Автотесты: `pkg/agenthosts/install_test.go::TestInstallWritesBackupAndPreservesExistingConfig`, `pkg/agenthosts/install_test.go::TestInstallDryRunDoesNotWrite`, `pkg/agenthosts/install_test.go::TestParseHost`
Corner cases: dry-run, backup path, invalid host name, preexisting Levara config.

## Coverage Notes

- Сценарии `S01-S30` опираются на реальные acceptance/regression тесты, уже живущие в `internal/http`, `internal/store`, `pkg/agenthosts`, `cmd/cli` и `docs`.
- Неавтоматизированных сценариев в этом списке сознательно нет: каждый сценарий привязан минимум к одному существующему тесту.
- Operational warnings из GitHub Actions не относятся к функциональному контракту workspace, но должны закрываться отдельным CI-hygiene блоком.
