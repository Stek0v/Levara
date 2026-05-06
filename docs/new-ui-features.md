# Новые UI компоненты Levara

## Обзор

В рамках текущей итерации реализовано 6 новых UI-компонентов для фронтенда Levara. Цель — предоставить полноценный графический интерфейс для функций, которые ранее были доступны только через API: шаринг датасетов, управление онтологиями и коллекциями, поиск по knowledge graph, индикация прогресса обработки и навигация по MCP-статусу. По сравнению с эталонным Cognee Python, Levara опережает по 3 из 6 компонентов и находится в паритете по остальным.

---

## P3: Модаль шаринга датасетов

### Описание

Модальное окно для предоставления доступа к датасету другим пользователям. Позволяет гибко управлять правами на уровне отдельных датасетов.

### Расположение файлов

- `cognee-frontend/src/app/dashboard/ShareDatasetModal.tsx` — компонент модального окна
- `cognee-frontend/src/app/dashboard/DatasetsAccordion.tsx` — интеграция модали в sidebar

### Функционал

- Ввод email пользователя, которому предоставляется доступ
- Выбор типа permission: `read`, `write`, `delete`, `share`
- Отображение списка текущих shares для датасета
- Взаимодействие с API: `POST /v1/permissions/datasets/{principal_id}`

### Как использовать

1. В sidebar открыть popup-меню датасета (иконка с тремя точками)
2. Выбрать пункт "Share"
3. Ввести email получателя и выбрать тип разрешения
4. Подтвердить действие

### API интеграция

```
POST /v1/permissions/datasets/{principal_id}
Content-Type: application/json

{
  "permission_name": "read",
  "dataset_ids": ["uuid"]
}
```

---

## P4: Управление онтологиями

### Описание

Страница `/ontologies` для загрузки и управления OWL/RDF онтологиями. Предоставляет интерфейс для работы с семантическими схемами данных.

### Расположение файлов

- `cognee-frontend/src/app/ontologies/page.tsx`

### Функционал

- Отображение списка загруженных онтологий
- Форма загрузки файлов онтологий (поддерживаемые форматы: `.owl`, `.rdf`, `.xml`, `.ttl`)
- Удаление онтологии из системы
- Взаимодействие с API: `GET /v1/ontologies`, `POST /v1/ontologies`, `DELETE /v1/ontologies`

### Как использовать

1. Перейти на страницу "Ontologies" через header
2. Выбрать файл онтологии через форму загрузки
3. Нажать "Upload" для загрузки
4. Для удаления — использовать соответствующую кнопку рядом с записью

### API endpoints

| Метод | Endpoint | Назначение |
|-------|----------|------------|
| GET | `/v1/ontologies` | Список онтологий |
| POST | `/v1/ontologies` | Загрузка новой онтологии |
| DELETE | `/v1/ontologies` | Удаление онтологии |

---

## P5: Управление коллекциями

### Описание

Страница `/collections` для управления векторными коллекциями. Предоставляет полный CRUD-интерфейс с возможностью миграции embeddings.

### Расположение файлов

- `cognee-frontend/src/app/collections/page.tsx`

### Функционал

- Список коллекций с метаданными: dimension, model, record_count
- Создание коллекции (указание имени и размерности вектора)
- Удаление коллекции
- Re-embed записей — миграция embeddings при смене модели
- Сводная статистика: общее количество коллекций, записей и используемых dimensions

### Как использовать

1. Перейти на страницу "Collections" через header
2. Для создания — заполнить форму (имя + dimension) и подтвердить
3. Для удаления — нажать кнопку удаления рядом с коллекцией
4. Для миграции embeddings — использовать кнопку "Re-embed" у нужной коллекции

### API endpoints

| Метод | Endpoint | Назначение |
|-------|----------|------------|
| GET | `/v1/collections` | Список коллекций |
| POST | `/v1/collections` | Создание коллекции |
| DELETE | `/v1/collections/{name}` | Удаление коллекции |
| POST | `/v1/collections/{name}/reembed` | Миграция embeddings |

---

## P6: Поиск и чат

### Описание

Страница `/search` с чат-интерфейсом для семантического поиска по knowledge graph. Поддерживает несколько типов поиска и сохраняет историю диалога.

### Расположение файлов

- `cognee-frontend/src/app/search/page.tsx` — страница поиска
- `cognee-frontend/src/modules/chat/useChat.ts` — хук управления состоянием чата

### Функционал

- Чат-интерфейс с сохранением истории сообщений в рамках сессии
- 5 типов поиска:
  - **GraphRAG** — поиск по графу знаний с генерацией ответа
  - **RAG** — retrieval-augmented generation
  - **Chunks** — поиск по чанкам документов
  - **Summaries** — поиск по саммари
  - **Feeling Lucky** — случайный релевантный результат
- Настраиваемый параметр Top-K (от 1 до 100)
- Auto-scroll к новым сообщениям
- Отправка: `Enter`, новая строка: `Shift+Enter`

### API интеграция

```
POST /v1/search/
Content-Type: application/json

{
  "query_text": "текст запроса",
  "query_type": "GRAPH_COMPLETION",
  "top_k": 10
}
```

Доступные значения `query_type`:

| Значение | Тип поиска |
|----------|------------|
| `GRAPH_COMPLETION` | GraphRAG |
| `RAG` | RAG |
| `CHUNKS` | Chunks |
| `SUMMARIES` | Summaries |
| `FEELING_LUCKY` | Feeling Lucky |

---

## P7: Индикаторы прогресса cognify

### Описание

Цветовые индикаторы статуса обработки датасетов. Обеспечивают визуальную обратную связь о текущем состоянии pipeline cognify.

### Расположение файлов

- `cognee-frontend/src/ui/elements/StatusIndicator.tsx` — компонент индикатора статуса
- `cognee-frontend/src/ui/App/Loading/CognifyLoadingIndicator/CognifyLoadingIndicator.tsx` — анимированный индикатор загрузки

### Статусы

| Статус | Цвет | Hex | Описание |
|--------|------|-----|----------|
| `DATASET_PROCESSING_STARTED` | Жёлтый | `#ffd500` | Обработка начата |
| `DATASET_PROCESSING_INITIATED` | Жёлтый | `#ffd500` | Обработка инициирована |
| `DATASET_PROCESSING_COMPLETED` | Зелёный | `#53ff24` | Обработка успешно завершена |
| `DATASET_PROCESSING_ERRORED` | Красный | `#ff5024` | Ошибка при обработке |

### Поведение

- Индикатор автоматически обновляется при изменении статуса датасета
- Жёлтый цвет сигнализирует о процессе обработки (started/initiated)
- Зелёный — обработка завершена без ошибок
- Красный — произошла ошибка, требуется внимание пользователя

---

## P8: Навигация MCP Status

### Описание

Quick-nav anchor links на странице `/mcp-status`. Обеспечивают быстрый переход между секциями длинной страницы статуса MCP-интеграции.

### Расположение файлов

- `cognee-frontend/src/app/mcp-status/page.tsx`

### Функционал

- Быстрые ссылки на секции: Services, MCP Protocol, Tools, Integration
- Anchor-based навигация с плавной прокруткой (smooth scroll)
- Корректное позиционирование с отступом `scroll-mt-4` для учёта фиксированного header

### Как использовать

1. Перейти на страницу MCP Status
2. Нажать на любую из ссылок в блоке быстрой навигации
3. Страница плавно прокрутится к соответствующей секции

---

## Навигация (Header)

В header добавлены 3 новых ссылки для доступа к реализованным страницам:

| Ссылка | Маршрут | Назначение |
|--------|---------|------------|
| **Search** | `/search` | Поиск и чат по knowledge graph |
| **Ontologies** | `/ontologies` | Управление онтологиями |
| **Collections** | `/collections` | Управление коллекциями |

Файл: `cognee-frontend/src/ui/Layout/Header.tsx`

---

## Тестирование

### API тесты

Файл: `tests/test_new_ui_features.py` — 38 тестов, покрывающих все новые компоненты:

| Группа | Кол-во тестов | Покрытие |
|--------|---------------|----------|
| Share/Permissions | 8 | Создание, чтение, удаление shares; валидация permissions |
| Collections | 7 | CRUD коллекций, re-embed, статистика |
| Ontologies | 6 | Upload, list, delete; валидация форматов |
| Search API | 5 | Все типы поиска, top_k, пустые запросы |
| MCP | 4 | Статус сервисов, навигация |
| Health | 3 | Healthcheck endpoints |
| Frontend routes | 5 | Доступность маршрутов /search, /ontologies, /collections и др. |

### UI тесты (Playwright)

Файл: `tests/test_new_ui_screenshots.py` — 14 скриншотов всех новых компонентов для визуальной регрессии.

### Запуск

```bash
# API тесты
pytest tests/test_new_ui_features.py -v -s

# UI тесты (требуется Playwright + Chromium)
pytest tests/test_new_ui_screenshots.py -v -s --browser chromium
```

---

## Сравнение с Cognee Python

| Компонент | Cognee Python | Levara | Результат |
|-----------|--------------|----------|-----------|
| Share UI | Нет (только API) | Есть (модаль) | Levara опережает |
| Ontology UI | Нет (только demo-граф) | Есть (upload/list/delete) | Levara опережает |
| Collections UI | Нет (только status) | Есть (CRUD + re-embed) | Levara опережает |
| Search/Chat | Есть (SearchView) | Есть (/search) | Паритет |
| StatusIndicator | Есть | Есть | Паритет |
| CognifyLoadingIndicator | Есть (CSS modules) | Есть (inline Tailwind) | Паритет |

По 3 из 6 компонентов Levara предоставляет функционал, отсутствующий в Cognee Python. Остальные 3 компонента находятся на уровне паритета с различиями в реализации (CSS modules vs Tailwind).
