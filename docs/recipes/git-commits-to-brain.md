# Рецепт: Git-коммиты в Levara

`analyze_commits` доступен только активному instance administrator и собирает
сообщения и диффы из репозитория на файловой системе **сервера**. Это отдельная
интеграция, а не загрузка локального репозитория клиента. Содержимое выбранных
коммитов может передаваться настроенным моделям.

## Требования

Нужен доступный серверу git repo. Без embeddings инструмент возвращает текстовый
preview; для поискового индекса нужен embedding endpoint, для извлечения графа —
LLM и поддерживаемое graph storage. Настройки: [интеграции](../integrations.md).
Используйте отдельное тестовое окружение для первой обработки.

## Выполнить ограниченный анализ

MCP-аргументы:

```text
analyze_commits(repo_path="/absolute/server/path/to/repo",
                since="2026-01-01", limit=20)
```

Текущая реализация направляет результат в фиксированную collection
`git_commits`; `set_context` не меняет этот target. Перед запуском worker сервер
создаёт или повторно использует внутренний dataset и точный document source,
фиксирует revision/hash и только затем показывает run. В embedding-режиме
pipeline асинхронный. Его run ID находится в `summary`; отдельного поля `run_id`
в результате `analyze_commits` пока нет.

```text
cognify_status(run_id="ID_FROM_SUMMARY")
git_search(query="authentication middleware changes")
```

Дождитесь успешного завершения и проверьте конкретный найденный commit/text.
`git_search` использует свою фиксированную коллекцию; обычный `search` без
правильного scope не доказывает, что коммиты обработались туда, куда ожидалось.
`git_search` — административный поиск напрямую по фиксированной collection. Он
может вернуть и старые записи `git_commits`, у которых ещё нет document
publication lineage. Повторный `analyze_commits` обновляет выбранный диапазон;
для пользовательской document ACL используйте обычный document/search flow.

## Проверить результат

Сопоставьте найденный текст с исходным commit. Graph entities и имена relations
зависят от LLM; не обещайте `AUTHORED`/`MODIFIED` edges для каждого запуска.
Preview без модели и завершение pipeline с частичными возможностями — разные
результаты. Ограничивайте `since` и `limit`, прежде чем обрабатывать всю историю.

## Повторный запуск и очистка

Внутренний dataset/document принадлежит серверу, но его ID не является частью
публичного результата `analyze_commits`, поэтому симметричный rollback через
`delete(dataset_id=...)` не документирован. Выполняйте эксперименты в отдельном
data/SQL окружении. Повторное чтение того же диапазона не является доказанной
exactly-once incremental ingestion. Не используйте глобальный `prune` для
отмены анализа одного репозитория.

Для управляемой загрузки документов используйте [document management](../document-management.md),
для индексирования файлов проекта — [project ingest](../project-ingest.md),
для проверки поведения — [testing](../testing.md).
