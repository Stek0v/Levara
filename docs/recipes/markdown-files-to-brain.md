# Рецепт: Markdown-файлы в Levara

Для исходных документов используйте upload → processing → search. Если нужны
редактируемые truth-файлы и exact read по пути, выбирайте отдельный
[Markdown workspace](../markdown-native-workspace.md) workflow.

## Требования

Запустите SQL-backed сервер и совместимый embedding endpoint по
[getting started](../getting-started.md). Семантическая индексация требует модели;
полный graph extraction дополнительно использует LLM и graph storage.
Файлы должны содержать материал, разрешённый для выбранных providers.

## Загрузить и обработать документ

Из корня репозитория:

```bash
export LEVARA_URL=http://127.0.0.1:8080/api/v1
./levara add --file=./README.md --dataset=docs-example
./levara cognify --dataset=docs-example --collection=docs-example --wait
./levara search 'Levara' --collection=docs-example --type=CHUNKS_LEXICAL --top-k=5
```

Это локальный пример для изолированного tutorial-сервера. Для auth задайте
`LEVARA_TOKEN`. Имя dataset в CLI `add` разрешается для текущего пользователя;
для shared editor target используйте WebUI/API с существующим dataset ID.

`add` сохраняет сырой материал, `cognify` создаёт поисковые производные. Проверяйте
явный успешный terminal status и исходный текст результата, не фиксированный
sleep. Ошибка extraction, неизвестный run ID и failed processing не означают
готовность. Для форматов, повторного запуска и оригиналов:
[document management](../document-management.md).

## Прямой MCP RAG/full вызов

Когда raw-объект уже сохранён отдельно или нужен одноразовый индекс inline-текста,
можно передать содержимое непосредственно:

```text
cognify(data="APPROVED_MARKDOWN_TEXT", collection="docs-example",
        mode="rag", room="docs", tags=["documentation"],
        document_title="example.md", chunk_strategy="merged")
```

`mode="rag"` пропускает graph extraction; `mode="full"` включает полный
настроенный pipeline. Оба режима требуют проверки возвращённого run через
`cognify_status`. Inline cognify не следует считать сохранением оригинального
файла для дальнейшего скачивания. Дополнительные chunking-параметры сверяйте с
[контрактом](../api-contract.md), а улучшение retrieval — с реальным корпусом.

## Пакетный проект и проверка качества

[Project ingest](../project-ingest.md) сканирует проект своими фильтрами, не
`.gitignore`; начните с dry-run и просмотра selected files. После обработки
сравните точную фразу и смысловой вопрос с тем же collection scope. Для ответа
агента проверяйте источник, для workspace — ещё и exact read актуального файла.
[Сценарии документов](../document-workflow-scenarios.md) описывают проверку
ошибок, повторной обработки и доступа двух пользователей.

## Очистка

MCP `delete` принимает `dataset_id`, не `data_id`; операция не гарантирует
физическое удаление всех originals/derivatives любого ingestion flow. Сохраните
созданные IDs и применяйте только документированную операцию к своему dataset.
Глобальный `prune` не является откатом одной collection. Полная стратегия
восстановления: [deployment](../deployment.md#backup-and-recovery).
