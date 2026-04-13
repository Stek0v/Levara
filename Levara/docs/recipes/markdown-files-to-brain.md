# Рецепт: Markdown-файлы в Brain

## Назначение

Загрузка markdown-документации в Levara для семантического поиска и RAG. Поддерживает два режима:
- **RAG-режим** (быстрый): только chunk + embed, LLM не нужен
- **Full-режим** (полный): извлечение сущностей + граф знаний + chunk + embed

## Предварительные требования

- [ ] Levara-сервер запущен (`curl http://localhost:8080/health`)
- [ ] Embedding-сервис работает (`doctor()` показывает embed_service = "ok")
- [ ] Для full-режима: LLM настроен (`doctor()` показывает llm = "ok")

## Шаги

### Вариант A: RAG-режим (быстрый, без LLM)

1. **Установить контекст**:
   ```
   set_context(collection="docs")
   ```

2. **Загрузить каждый файл** — прочитать содержимое и передать в cognify:
   ```
   cognify(
     data="<содержимое_файла>",
     collection="docs",
     mode="rag",
     room="<тема>",
     tags=["documentation"],
     document_title="<имя_файла>",
     chunk_strategy="merged"
   )
   ```

3. **Проверить**:
   ```
   search(search_query="<известный контент из файлов>", collection="docs")
   ```

### Вариант B: Full-режим (извлечение сущностей)

1. **Установить контекст**:
   ```
   set_context(collection="docs")
   ```

2. **Загрузить с полным пайплайном**:
   ```
   cognify(
     data="<содержимое_файла>",
     collection="docs",
     mode="full",
     room="<тема>",
     tags=["documentation"],
     document_title="<имя_файла>",
     chunk_strategy="merged",
     community_resolution=1.0,
     dedup_threshold=0.95
   )
   ```

3. **Проверить статус** (full-режим асинхронный):
   ```
   cognify_status(run_id="<returned_id>")
   ```

4. **Проверить сущности**:
   ```
   list_communities()
   query_entity(name="<ожидаемая_сущность>")
   ```

## Smoke-тест

```
search(search_query="<уникальная фраза из одного из файлов>", collection="docs")
```

Ожидание: минимум 1 результат с правильным document_title в metadata.

```
search(search_query="<тема>", search_type="HYBRID", collection="docs")
```

Ожидание: гибридный поиск возвращает и семантические, и ключевые совпадения.

## Откат

```
delete(dataset_id="<dataset_id>")
```

Или удалить все данные коллекции docs:
```
prune()  # ВНИМАНИЕ: удаляет ВСЕ данные
```

## Заметки

- **Стратегии чанкинга**: `merged` (по умолчанию, объединяет короткие абзацы), `paragraph`, `sentence`, `sliding` (фиксированное окно с перекрытием)
- **Sliding window** лучше всего для длинных документов: `chunk_strategy="sliding", max_chunk_chars=500, overlap_chars=100`
- **Parent-child** чанкинг (`parent_child=true`) даёт лучшую точность поиска с полным контекстом при извлечении
- **Room/tags** критичны для фильтрованного поиска потом — всегда устанавливай их при загрузке
- Большие файлы: рекомендуется разбивать на секции перед загрузкой (< 50 КБ на вызов)
- **Пакетная загрузка**: для многих файлов сначала вызови `add()` для сохранения сырого текста, потом `cognify` на dataset — это предотвращает потерю данных если пайплайн упадёт на полпути
