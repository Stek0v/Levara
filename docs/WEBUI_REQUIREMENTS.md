# Levara WebUI — Backend API + Пользовательские сценарии

## Часть 1: Backend API (118 endpoints)

### Здоровье и статус
| Метод | Путь | Что делает |
|-------|------|-----------|
| GET | `/health` | Базовая проверка здоровья |
| GET | `/health/details` | Детальный статус всех сервисов (postgres, neo4j, embed, llm) |
| GET | `/metrics` | Prometheus метрики |
| GET | `/api/v1/info` | Информация о БД (размерность, шарды, коллекции) |
| GET | `/api/v1/cache/stats` | Статистика LLM кеша (попадания, промахи) |
| GET | `/api/v1/errors` | Последние ошибки |

### Авторизация
| Метод | Путь | Что делает |
|-------|------|-----------|
| POST | `/api/v1/auth/login` | Вход (email+пароль → JWT) |
| POST | `/api/v1/auth/register` | Регистрация |
| GET | `/api/v1/auth/me` | Текущий пользователь |
| POST | `/api/v1/auth/keys` | Создать API-ключ |
| GET | `/api/v1/auth/keys` | Список API-ключей |
| DELETE | `/api/v1/auth/keys/:id` | Отозвать ключ |

### Датасеты и данные
| Метод | Путь | Что делает |
|-------|------|-----------|
| GET | `/api/v1/datasets` | Список датасетов |
| POST | `/api/v1/datasets` | Создать датасет |
| DELETE | `/api/v1/datasets/:id` | Удалить датасет |
| GET | `/api/v1/datasets/:id/data` | Записи в датасете |
| GET | `/api/v1/datasets/:id/data/:dataId/raw` | Сырые данные записи |
| PATCH | `/api/v1/datasets/:id/data/:dataId` | Обновить запись |
| DELETE | `/api/v1/datasets/:id/data/:dataId` | Удалить запись |
| GET | `/api/v1/datasets/status` | Статус обработки |

### Загрузка файлов
| Метод | Путь | Что делает |
|-------|------|-----------|
| POST | `/api/v1/add` | Загрузить файлы (PDF, DOCX, CSV, TXT, изображения) |
| POST | `/api/v1/ocr` | Извлечь текст из изображения (OCR) |

### Обработка данных (Cognify / Memify)
| Метод | Путь | Что делает |
|-------|------|-----------|
| POST | `/api/v1/cognify` | Запуск пайплайна: чанкинг → извлечение сущностей → граф |
| GET | `/api/v1/cognify/:runId/status` | Статус пайплайна |
| GET | `/api/v1/cognify/:runId/stream` | SSE поток прогресса в реальном времени |
| POST | `/api/v1/memify` | Обогащение графа + community detection |
| GET | `/api/v1/memify/:runId/status` | Статус memify |
| GET | `/api/v1/memify/:runId/stream` | SSE поток memify |

### Поиск
| Метод | Путь | Что делает |
|-------|------|-----------|
| POST | `/api/v1/search/text` | Основной поиск (16 режимов: CHUNKS, HYBRID, GRAPH_COMPLETION, RAG, COT...) |
| POST | `/api/v1/search/dual` | Поиск по коллекциям с разными моделями/размерностями |

### Коллекции
| Метод | Путь | Что делает |
|-------|------|-----------|
| GET | `/api/v1/collections` | Список коллекций с метаданными |
| POST | `/api/v1/collections` | Создать коллекцию |
| GET | `/api/v1/collections/:name/meta` | Метаданные коллекции |
| PUT | `/api/v1/collections/:name/meta` | Обновить метаданные |
| DELETE | `/api/v1/collections/:name` | Удалить коллекцию |

### Ре-эмбеддинг (миграция моделей)
| Метод | Путь | Что делает |
|-------|------|-----------|
| POST | `/api/v1/reembed` | Запуск миграции на другую embed-модель |
| GET | `/api/v1/reembed/:runId/status` | Статус миграции |

### Граф знаний
| Метод | Путь | Что делает |
|-------|------|-----------|
| GET | `/api/v1/visualize` | HTML-страница визуализации графа (D3.js) |
| GET | `/api/v1/datasets/:id/graph` | Граф датасета (nodes + edges JSON) |

### Память (Memories)
| Метод | Путь | Что делает |
|-------|------|-----------|
| POST | `/api/v1/memories` | Сохранить факт/memory |
| GET | `/api/v1/memories` | Список всех memories |
| GET | `/api/v1/memories/:key` | Получить memory по ключу |
| DELETE | `/api/v1/memories/:key` | Удалить memory |

### Сессии и диалоги
| Метод | Путь | Что делает |
|-------|------|-----------|
| POST | `/api/v1/interactions` | Сохранить вопрос-ответ |
| GET | `/api/v1/interactions` | История диалогов |
| GET | `/api/v1/interactions/:sessionId` | Диалог по ID сессии |

### Обратная связь
| Метод | Путь | Что делает |
|-------|------|-----------|
| POST | `/api/v1/feedback` | Оценка результата (1-5) |
| GET | `/api/v1/feedback` | Список оценок |
| GET | `/api/v1/feedback/stats` | Статистика (средняя оценка, худшие запросы) |

### Онтологии
| Метод | Путь | Что делает |
|-------|------|-----------|
| GET | `/api/v1/ontologies` | Список онтологий |
| POST | `/api/v1/ontologies` | Загрузить онтологию (RDF/OWL) |
| DELETE | `/api/v1/ontologies/:id` | Удалить онтологию |

### Notebooks (интерактивные)
| Метод | Путь | Что делает |
|-------|------|-----------|
| GET | `/api/v1/notebooks` | Список notebooks |
| POST | `/api/v1/notebooks` | Создать notebook |
| GET | `/api/v1/notebooks/:id` | Notebook с ячейками |
| PUT | `/api/v1/notebooks/:id` | Обновить notebook |
| DELETE | `/api/v1/notebooks/:id` | Удалить notebook |
| POST | `/api/v1/notebooks/:id/cells` | Добавить ячейку |
| PUT | `/api/v1/notebooks/:id/cells/:cellId` | Обновить ячейку |
| DELETE | `/api/v1/notebooks/:id/cells/:cellId` | Удалить ячейку |
| POST | `/api/v1/notebooks/:id/cells/:cellId/run` | Выполнить ячейку |

### Пользователи и настройки
| Метод | Путь | Что делает |
|-------|------|-----------|
| GET | `/api/v1/users/me` | Профиль пользователя |
| PUT | `/api/v1/users/me` | Обновить профиль |
| PUT | `/api/v1/users/me/password` | Сменить пароль |
| GET | `/api/v1/settings` | Настройки |
| PUT | `/api/v1/settings` | Обновить настройки |

### RBAC и шаринг
| Метод | Путь | Что делает |
|-------|------|-----------|
| GET | `/api/v1/datasets/:id/shares` | Список доступов к датасету |
| POST | `/api/v1/datasets/:id/shares` | Дать доступ |
| DELETE | `/api/v1/datasets/:id/shares/:shareId` | Отозвать доступ |
| GET | `/api/v1/permissions/me` | Мои права |

### Мультитенантность
| Метод | Путь | Что делает |
|-------|------|-----------|
| POST | `/api/v1/tenants` | Создать тенант |
| GET | `/api/v1/tenants` | Список тенантов |
| GET | `/api/v1/tenants/mine` | Мои тенанты |
| POST | `/api/v1/tenants/select` | Выбрать активный |
| POST | `/api/v1/tenants/:id/users` | Добавить пользователя |
| DELETE | `/api/v1/tenants/:id/users/:uid` | Удалить пользователя |

### Синхронизация (между инстансами)
| Метод | Путь | Что делает |
|-------|------|-----------|
| GET | `/api/v1/sync/manifest` | Манифест (коллекции, счётчики) |
| GET/POST | `/api/v1/sync/export|import/memories` | Экспорт/импорт memories |
| GET/POST | `/api/v1/sync/export|import/interactions` | Экспорт/импорт диалогов |
| GET/POST | `/api/v1/sync/export|import/graph` | Экспорт/импорт графа |
| GET/POST | `/api/v1/sync/export|import/collection/:name` | Экспорт/импорт коллекции |

### MCP Server
| Метод | Путь | Что делает |
|-------|------|-----------|
| POST | `/mcp` | JSON-RPC 2.0 (25+ инструментов для ИИ-агентов) |
| GET | `/mcp` | SSE поток серверных событий |
| DELETE | `/mcp` | Завершить MCP-сессию |

---

## Часть 2: 40 пользовательских сценариев

### Категория A: Первое знакомство

**A1. Регистрация и вход**
Пользователь открывает WebUI → видит форму входа → регистрируется или входит → попадает на Dashboard.
`POST /auth/register` → `POST /auth/login` → `GET /users/me`

**A2. Просмотр статуса системы**
После входа пользователь видит Dashboard: сколько коллекций, документов, здоровье сервисов.
`GET /health/details` → `GET /info` → `GET /collections` → `GET /cache/stats`

**A3. Просмотр своих данных**
Пользователь видит список своих датасетов с количеством записей и датой создания.
`GET /datasets` → для каждого `GET /datasets/:id/data`

### Категория B: Загрузка и обработка данных

**B4. Загрузка одного файла**
Перетаскивание PDF/DOCX в зону upload → система показывает прогресс → файл появляется в датасете.
`POST /add` (multipart) → `GET /datasets`

**B5. Загрузка пачки файлов**
Выбор нескольких файлов или папки → массовая загрузка с прогресс-баром → все попадают в один датасет.
`POST /add` (multiple files) → `GET /datasets/:id/data`

**B6. Просмотр содержимого документа**
Клик на документ в датасете → превью текста, метаданные, размер.
`GET /datasets/:id/data/:dataId/raw`

**B7. Запуск Cognify (извлечение знаний)**
Выбор датасета → кнопка "Обработать" → выбор стратегии (авто/rag/full) → прогресс-бар в реальном времени → уведомление о завершении.
`POST /cognify` → `GET /cognify/:runId/stream` (SSE) → `GET /cognify/:runId/status`

**B8. Мониторинг обработки**
Страница с активными задачами: cognify, memify, reembed. Каждая показывает стадию, процент, время.
`GET /cognify/:runId/status` × N

**B9. Удаление данных**
Выбор датасета → подтверждение → удаление всех данных и связей.
`DELETE /datasets/:id`

### Категория C: Поиск

**C10. Простой текстовый поиск**
Строка поиска → ввод запроса → результаты с подсветкой → клик на результат → полный текст.
`POST /search/text` (query_type: AUTO)

**C11. Выбор типа поиска**
Dropdown рядом с поисковой строкой: Dense / Sparse / Hybrid / RAG / Graph. Меняет query_type.
`POST /search/text` (query_type: CHUNKS | CHUNKS_LEXICAL | HYBRID | RAG_COMPLETION | GRAPH_COMPLETION)

**C12. Поиск с фильтром по коллекции**
Checkbox-ы или dropdown для выбора коллекций → поиск только в выбранных.
`POST /search/text` (collection: "selected")

**C13. Поиск с фильтром по домену**
Выбор домена (медицина, наука, финансы) → поиск по коллекциям этого домена.
`POST /search/text` (domain: "medical")

**C14. RAG-ответ на вопрос**
Задать вопрос → получить сгенерированный ответ с цитатами из источников.
`POST /search/text` (query_type: RAG_COMPLETION)

**C15. Многоходовый поиск (граф)**
Сложный вопрос → система разбивает на подвопросы → показывает цепочку рассуждений.
`POST /search/text` (query_type: GRAPH_COMPLETION_COT)

**C16. Поиск по Cypher-запросу**
Продвинутый пользователь вводит Cypher-запрос → видит результаты из графа.
`POST /search/text` (query_type: CYPHER, cypher_query: "MATCH ...")

**C17. Оценка результата поиска**
Рядом с каждым результатом — кнопки оценки (1-5 звёзд) → feedback отправляется → адаптивный роутер учится.
`POST /feedback` (query, result_id, rating)

**C18. Dual-search (мульти-модельный)**
Поиск одновременно по коллекциям с разными embedding-моделями → объединённые результаты.
`POST /search/dual` (collections: ["sci_articles", "general"])

### Категория D: Граф знаний

**D19. Визуализация графа**
Интерактивный граф: узлы-сущности, рёбра-связи. Масштабирование, перетаскивание, фильтры по типу.
`GET /visualize` или `GET /datasets/:id/graph`

**D20. Поиск сущности в графе**
Ввод имени сущности → подсветка на графе → список связей.
`POST /search/text` (query_type: GRAPH_COMPLETION) → `GET /datasets/:id/graph`

**D21. Просмотр сообществ (communities)**
Граф раскрашен по сообществам → клик на сообщество → саммари группы.
`POST /search/text` (query_type: COMMUNITY_GLOBAL)

**D22. Временная шкала событий**
Временная ось с событиями из графа → фильтр по дате.
`POST /search/text` (query_type: TEMPORAL)

### Категория E: Память и контекст

**E23. Просмотр сохранённых фактов**
Таблица: ключ, значение, тип (fact/decision/event), дата. Фильтры и поиск.
`GET /memories` → фильтр по type

**E24. Сохранение нового факта**
Форма: ключ, значение, тип, комната → сохранить.
`POST /memories` (key, value, type, room)

**E25. Просмотр истории диалогов**
Список сессий → клик → полная цепочка вопросов и ответов.
`GET /interactions` → `GET /interactions/:sessionId`

**E26. Чат-интерфейс (RAG)**
Полноценный чат: ввод вопроса → ответ с цитатами → сохранение в сессию → контекст переносится.
`POST /search/text` (session_id, query_type: RAG_COMPLETION) → `POST /interactions`

### Категория F: Управление коллекциями

**F27. Создание коллекции с параметрами**
Форма: название, embed-модель, размерность, метрика расстояния, домен.
`POST /collections` (name, embedding_model, embedding_dim, distance_metric)

**F28. Миграция на новую embed-модель**
Выбор коллекции → выбор новой модели → запуск ре-эмбеддинга → прогресс.
`POST /reembed` → `GET /reembed/:runId/status`

**F29. Проверка drift (дрейф модели)**
Сравнение текущей embed-модели коллекции с серверной → предупреждение о рассинхроне.
MCP tool: `check_drift`

### Категория G: Онтологии и схемы

**G30. Загрузка медицинской онтологии**
Загрузка файла UMLS/MeSH RDF → система знает медицинские термины → cognify извлекает правильные сущности.
`POST /ontologies` (multipart file)

**G31. Просмотр активных онтологий**
Список загруженных онтологий: имя, количество классов, дата.
`GET /ontologies`

### Категория H: Notebooks

**H32. Создание интерактивного notebook**
Jupyter-подобный интерфейс: ячейки кода и markdown → выполнение → результаты.
`POST /notebooks` → `POST /notebooks/:id/cells` → `POST /notebooks/:id/cells/:cellId/run`

**H33. Запуск поискового эксперимента в notebook**
Ячейка: search("запрос", type="HYBRID") → результаты → анализ.
`POST /notebooks/:id/cells/:cellId/run` (содержит search-запрос)

### Категория I: Администрирование

**I34. Управление пользователями**
Список пользователей → роли → добавление/удаление.
`GET /users` → `POST /tenants/:id/users`

**I35. Управление правами доступа**
Датасет → вкладка "Доступ" → добавить пользователя (viewer/editor/admin).
`POST /datasets/:id/shares` → `GET /datasets/:id/shares`

**I36. Создание API-ключа**
Настройки → API-ключи → создать → скопировать → использовать в приложении.
`POST /auth/keys` → `GET /auth/keys`

**I37. Мультитенантность**
Переключение между организациями → данные изолированы.
`GET /tenants/mine` → `POST /tenants/select`

### Категория J: Синхронизация и экспорт

**J38. Синхронизация с удалённым Levara**
Кнопка "Синхронизировать" → выбор направления (push/pull) → прогресс → результат.
`GET /sync/manifest` → `POST /sync/export/*` или `POST /sync/import/*`

**J39. Экспорт графа знаний**
Кнопка "Экспорт" → скачать граф (nodes + edges) как JSON/CSV.
`GET /sync/export/graph`

**J40. Экспорт коллекции с embeddings**
Выбор коллекции → полный экспорт (vectors + metadata) для бэкапа или переноса.
`GET /sync/export/collection/:name`

### Категория K: Продвинутый поиск и аналитика

**K41. Сравнение результатов разных режимов поиска**
Ввод запроса → система одновременно выполняет Dense, Sparse, Hybrid → три столбца результатов рядом → пользователь видит что нашёл каждый метод.
`POST /search/text` × 3 (query_type: CHUNKS, CHUNKS_LEXICAL, HYBRID)

**K42. Поиск с reranking**
Включить тумблер "Reranking" → результаты переранжированы кросс-энкодером → badge "reranked" на каждом результате.
`POST /search/text` (rerank: true)

**K43. Поиск по тегам**
Выбрать теги из облака тегов → результаты фильтруются по metadata.tags.
`POST /search/text` (tags: ["medical", "2024"])

**K44. Автодополнение поискового запроса**
Пользователь печатает → выпадают подсказки из graph_nodes (сущности) и прошлых запросов.
`GET /memories` (type: query_history) + `POST /search/text` (top_k: 5)

**K45. Поиск похожих документов**
Клик "Найти похожие" на результате → поиск по вектору этого документа.
`POST /search/text` (вектор из metadata)

**K46. Фасетный поиск**
Боковая панель: фильтры по дате, автору, типу документа, коллекции → результаты обновляются динамически.
`POST /search/text` + клиентская фильтрация по metadata

**K47. Сохранение поискового запроса**
Кнопка "Сохранить запрос" → запрос с параметрами сохраняется в memories → можно повторить позже.
`POST /memories` (key: "saved_query_...", value: {query, type, collection, tags})

**K48. История поисковых запросов**
Выпадающий список последних запросов → клик повторяет поиск.
`GET /interactions` (фильтр по текущему пользователю)

**K49. Экспорт результатов поиска**
Кнопка "Экспорт" → CSV/JSON с результатами, scores, metadata.
Клиентская генерация из результатов `POST /search/text`

**K50. Поиск с указанием минимального порога релевантности**
Слайдер "Минимальный score" → результаты ниже порога скрываются.
`POST /search/text` + клиентская фильтрация по score

### Категория L: Граф знаний — продвинутое

**L51. Фильтрация графа по типу сущностей**
Чекбоксы: Person ✓, Organization ✓, Location ☐ → граф показывает только выбранные типы.
`GET /datasets/:id/graph` + клиентская фильтрация

**L52. Фильтрация графа по типу связей**
Dropdown: показать только WORKS_AT, KNOWS, CITES → граф перестраивается.
`GET /datasets/:id/graph` + клиентская фильтрация

**L53. Поиск кратчайшего пути между сущностями**
Выбрать две сущности на графе → подсветить кратчайший путь между ними.
`POST /search/text` (query_type: CYPHER, cypher_query: "MATCH p=shortestPath(...)")

**L54. Просмотр соседей сущности**
Клик на узел графа → раскрытие 1-hop соседей → инфопанель со свойствами.
`POST /search/text` (query_type: GRAPH_COMPLETION) или `GET /datasets/:id/graph`

**L55. Граф с временной осью**
Узлы расположены по горизонтали по дате → видно эволюцию знаний во времени.
`POST /search/text` (query_type: TEMPORAL) + `GET /datasets/:id/graph`

**L56. Тепловая карта графа**
Узлы окрашены по степени связности (degree) → самые связанные = яркие.
`GET /datasets/:id/graph` + клиентский расчёт degree

**L57. Экспорт графа в Gephi/Neo4j format**
Кнопка "Экспорт для Gephi" → GEXF файл.
`GET /sync/export/graph` + клиентская конвертация

**L58. Детальная карточка сущности**
Клик на узел → панель: имя, тип, описание, источник (из какого документа), дата извлечения, все связи.
`POST /search/text` (query_type: GRAPH_COMPLETION, queryText: entity_name)

**L59. Редактирование сущности в графе**
Двойной клик на узел → редактирование имени, типа, описания → сохранение в Neo4j.
`PATCH /datasets/:id/data/:dataId` (обновление metadata)

**L60. Ручное создание связи**
Drag от одного узла к другому → выбор типа связи → создание edge.
`POST /search/text` (query_type: CYPHER, cypher_query: "MATCH ... CREATE ...")

### Категория M: Чат и RAG — продвинутое

**M61. Мультиколлекционный чат**
Чат с выбором нескольких коллекций как источника → ответы цитируют конкретные коллекции.
`POST /search/text` (query_type: RAG_COMPLETION) по нескольким коллекциям

**M62. Чат с прикреплением файла**
Перетаскивание файла прямо в чат → автоматическая индексация → ответ по содержимому.
`POST /add` → `POST /cognify` → `POST /search/text` (RAG_COMPLETION)

**M63. Чат с режимом "цепочка рассуждений"**
Тумблер "Chain of Thought" → ответ показывает шаги рассуждения (sub-questions, found context).
`POST /search/text` (query_type: GRAPH_COMPLETION_COT)

**M64. Чат с историей контекста**
Новое сообщение учитывает предыдущие → система помнит о чём говорили.
`POST /search/text` (session_id: "current_session")

**M65. Переключение LLM-модели в чате**
Dropdown в чате: DeepSeek / GPT-4 / Ollama local → следующий ответ генерируется выбранной моделью.
`POST /search/text` + серверный LLM provider routing

**M66. Цитирование источников в ответе**
Каждый фрагмент ответа помечен [1], [2] → внизу список источников с ссылками на документы.
Парсинг из `POST /search/text` response: chunks[] → metadata → document title

**M67. Редактирование ответа ИИ**
Пользователь исправляет ответ → исправленная версия сохраняется как feedback.
`POST /feedback` (query, result_id, rating: 5, comment: "corrected answer")

**M68. Экспорт чата**
Кнопка "Экспорт" → Markdown/PDF с полной историей диалога.
`GET /interactions/:sessionId` → клиентская генерация

**M69. Шаблоны запросов**
Библиотека предустановленных запросов: "Summarize this topic", "Compare A and B", "Find contradictions".
`GET /memories` (type: query_template) → подстановка параметров → `POST /search/text`

**M70. Голосовой ввод запроса**
Кнопка микрофона → speech-to-text в браузере → текст отправляется как запрос.
Web Speech API → `POST /search/text`

### Категория N: Данные и документы — продвинутое

**N71. Предварительный просмотр PDF**
Встроенный PDF-viewer → подсветка чанков, из которых извлечены сущности.
`GET /datasets/:id/data/:dataId/raw` + PDF.js

**N72. Просмотр чанков документа**
Список чанков конкретного документа → каждый с текстом, индексом, embedding-размерностью.
`GET /datasets/:id/data` (фильтр по document_id)

**N73. Ручное добавление текста**
Текстовое поле → вставить текст → система создаёт запись в датасете.
`POST /add` (text content)

**N74. Импорт из URL**
Ввод URL → скачивание страницы → извлечение текста → добавление в датасет.
`POST /add` (url parameter)

**N75. Bulk-действия над записями**
Чекбоксы на записях → "Удалить выбранные" / "Переместить в другой датасет".
`DELETE /datasets/:id/data/:dataId` × N

**N76. Статистика по датасету**
Панель: количество записей, средний размер, распределение по типам файлов, дата последней загрузки.
`GET /datasets/:id/data` + клиентская агрегация

**N77. Поиск внутри датасета**
Строка поиска на странице датасета → фильтрация записей по содержимому.
`POST /search/text` (collection: dataset_collection)

**N78. Версионирование данных**
Просмотр истории изменений записи: кто, когда, что менял.
`GET /datasets/:id/data/:dataId` (включая updated_at, version)

**N79. Дедупликация данных**
Кнопка "Найти дубликаты" → система показывает похожие записи (cosine > 0.95) → объединить/удалить.
MCP tool: semantic dedup → клиентское отображение

**N80. Drag-and-drop сортировка датасетов**
Перетаскивание датасетов для ручной сортировки на Dashboard.
Клиентский state + `PUT /settings` (dataset_order)

### Категория O: Память и факты — продвинутое

**O81. Категоризация memories по комнатам (rooms)**
Вкладки: auth / deploy / infra / project → каждая показывает свои memories.
`GET /memories` (фильтр по room через metadata)

**O82. Категоризация memories по жанрам (halls)**
Фильтры: fact / decision / event / preference / advice / discovery.
`GET /memories` (фильтр по hall через metadata)

**O83. Timeline фактов**
Горизонтальная шкала → факты расположены по дате сохранения → клик раскрывает детали.
`GET /memories` + сортировка по дате

**O84. Pinned facts (закреплённые)**
Раздел "Важное" вверху страницы → показывает pinned memories → badge с приоритетом.
MCP: `wake_up` → отображение pinned

**O85. Редактирование факта**
Клик на memory → inline-редактирование value → сохранение.
`DELETE /memories/:key` → `POST /memories` (обновлённый)

**O86. Bulk-импорт фактов**
Загрузка CSV/JSON с фактами → массовое создание memories.
`POST /memories` × N

**O87. Конфликт фактов**
Система обнаруживает противоречие (два факта об одном — разные значения) → предлагает выбрать актуальный.
`GET /memories` + клиентское сравнение по ключу

**O88. Дневник агента (diary)**
Просмотр diary записей конкретного агента (reviewer, planner) → изолированный namespace.
MCP: `diary_read(agent="reviewer")`

**O89. Поиск по памяти**
Строка поиска → semantic search по memories → результаты ранжированы по релевантности.
MCP: `recall_memory(query="...")`

**O90. Экспорт всех memories**
Кнопка "Выгрузить всё" → JSON файл со всеми фактами, решениями, событиями.
`GET /sync/export/memories`

### Категория P: Cognify pipeline — продвинутое

**P91. Выбор стратегии чанкинга**
Dropdown при запуске cognify: merged / sliding / sentence / paragraph / code / parent-child / section / auto.
`POST /cognify` (chunk_strategy: "sliding")

**P92. Настройка размера чанка**
Слайдеры: min_chunk_chars (50-500), max_chunk_chars (200-2000).
`POST /cognify` (min_chunk_chars, max_chunk_chars)

**P93. Выбор режима cognify**
Переключатель: RAG (быстро, только embed) / Full (с извлечением сущностей + граф).
`POST /cognify` (mode: "rag" | "full")

**P94. Custom extraction prompt**
Текстовое поле для кастомного системного промпта → LLM извлекает сущности по вашим правилам.
`POST /cognify` (custom_prompt: "Extract only medical entities...")

**P95. Просмотр извлечённых сущностей**
После cognify → таблица: имя сущности, тип, описание, confidence, из какого документа.
`GET /datasets/:id/graph` (nodes)

**P96. Просмотр извлечённых связей**
После cognify → таблица: source → relationship → target, confidence.
`GET /datasets/:id/graph` (edges)

**P97. Ре-запуск cognify с другими параметрами**
Кнопка "Переобработать" → выбор новых параметров → повторный запуск на том же датасете.
`POST /cognify` (collection: existing, overwrite: true)

**P98. Cognify с привязкой к онтологии**
Dropdown: выбрать онтологию → cognify использует её для ограничения типов сущностей.
`POST /cognify` + автоматическое обогащение промпта из `GET /ontologies`

**P99. Мониторинг LLM вызовов во время cognify**
Панель: количество LLM вызовов, средний latency, cache hit rate, стоимость.
`GET /cache/stats` + `GET /cognify/:runId/status` (entities, edges, elapsed_ms)

**P100. Сравнение стратегий чанкинга**
Один датасет → два запуска cognify с разными стратегиями → сравнение количества чанков, сущностей, качества.
`POST /cognify` × 2 → сравнение `GET /cognify/:runId/status`

### Категория Q: Аналитика и мониторинг

**Q101. Dashboard с метриками в реальном времени**
Виджеты: QPS, latency p50/p95, cache hit rate, ошибки за час → обновляются каждые 5 сек.
`GET /metrics` (Prometheus) + WebSocket/polling

**Q102. График качества поиска по времени**
Линейный график: средняя оценка feedback по дням → тренд улучшения/ухудшения.
`GET /feedback/stats` + `GET /feedback` (с группировкой по дате)

**Q103. Топ-10 худших запросов**
Таблица: запросы с самой низкой оценкой → клик → переход к результатам.
`GET /feedback` (sort: rating ASC, limit: 10)

**Q104. Распределение типов поиска**
Pie chart: сколько запросов по каждому query_type (CHUNKS, HYBRID, RAG...).
`GET /metrics` → `search_requests_by_type` counter

**Q105. Использование коллекций**
Bar chart: количество записей / поисковых запросов по каждой коллекции.
`GET /collections` + `GET /metrics`

**Q106. Latency histogram**
Гистограмма распределения latency поисковых запросов → выявление аномалий.
`GET /metrics` → `search_duration_seconds` histogram

**Q107. Errors timeline**
Временная шкала ошибок → группировка по типу → drill-down в детали.
`GET /errors` → клиентская визуализация

**Q108. LLM usage & costs**
Таблица: модель, количество вызовов, tokens consumed, estimated cost.
`GET /cache/stats` + логи из `GET /metrics`

**Q109. Storage usage**
Визуализация: сколько места занимают коллекции, WAL, uploads.
`GET /info` + `GET /collections` (record counts × dim × 4 bytes)

**Q110. Active users**
Список активных пользователей за последний час/день/неделю.
`GET /interactions` (group by user_id, sort by date)

### Категория R: Совместная работа

**R111. Комментарии к результатам поиска**
Рядом с результатом — кнопка "Комментировать" → комментарий виден всем с доступом.
`POST /feedback` (comment field)

**R112. Общие датасеты команды**
Вкладка "Команда" → датасеты с общим доступом → роли (viewer/editor/admin).
`GET /datasets` (shared_with_me) + `GET /datasets/:id/shares`

**R113. Уведомления**
Иконка колокольчика → "Cognify завершён", "Новый комментарий", "Датасет расшарен".
WebSocket + `GET /cognify/:runId/status` polling

**R114. Общий notebook команды**
Notebook с совместным доступом → несколько пользователей видят результаты.
`GET /notebooks/:id` + `POST /notebooks/:id/cells/:cellId/run`

**R115. Аудит действий**
Лог: кто, когда, что сделал (загрузил файл, удалил датасет, запустил cognify).
`GET /interactions` + `GET /feedback` + server logs

### Категория S: Мобильные и edge-сценарии

**S116. Responsive мобильный интерфейс**
Поиск и чат доступны с телефона → адаптивная верстка → быстрый ответ.
Все API те же, клиентский responsive design

**S117. Оффлайн-поиск (cached)**
Последние результаты кешируются в браузере → доступны без сети.
Service Worker + IndexedDB (клиентский кеш)

**S118. Push-уведомления о завершении cognify**
Cognify запущен → телефон заблокирован → push "Обработка завершена".
`GET /cognify/:runId/stream` (SSE) → Web Push API

**S119. QR-код для доступа к датасету**
Кнопка "Поделиться" → генерация QR-кода с ссылкой + временным API-ключом.
`POST /auth/keys` (expiry: 24h) → клиентская генерация QR

**S120. Keyboard shortcuts**
`/` — фокус на поиск, `Ctrl+K` — command palette, `Ctrl+Enter` — отправить, `Esc` — закрыть.
Клиентская обработка hotkeys

### Категория T: Интеграции

**T121. Webhook на завершение cognify**
Настройки → URL для webhook → при завершении cognify — POST на внешний URL.
`POST /cognify` (hooks: {on_complete: "https://..."})

**T122. Интеграция с Slack**
Кнопка "Отправить в Slack" → результат поиска форматируется и отправляется в канал.
Клиентский Slack webhook

**T123. Embed-виджет для сайта**
Генерация `<iframe>` кода для встраивания поисковой строки на внешний сайт.
`POST /search/text` через CORS-enabled endpoint

**T124. REST API documentation (Swagger/OpenAPI)**
Страница /docs → автогенерированная документация всех endpoints → "Try it" кнопки.
Клиентский Swagger UI, spec генерируется из кода

**T125. GraphQL endpoint**
Альтернативный доступ через GraphQL для фронтенд-разработчиков.
GraphQL-прослойка поверх REST API

**T126. Python SDK playground**
Встроенный Python-editor → pip install levara-client → пример кода → выполнение.
`POST /notebooks/:id/cells/:cellId/run` (type: "python")

### Категория U: Безопасность и compliance

**U127. Двухфакторная аутентификация**
Настройки → включить 2FA → QR-код для Google Authenticator → код при входе.
`POST /auth/login` (totp_code: "123456")

**U128. Аудит доступа к данным**
Кто и когда открывал какой документ → журнал доступа.
`GET /interactions` + middleware logging

**U129. Маскировка чувствительных данных**
В результатах поиска PII (email, телефон, ИНН) автоматически маскируются.
Клиентский regex-фильтр на отображении

**U130. Политики ретеншена данных**
Настройки → "Удалять данные старше N дней" → автоматическая очистка.
`POST /prune/data` (retention_days: 90)

**U131. IP whitelist**
Настройки → список разрешённых IP → доступ только с них.
Серверный middleware (не API)

**U132. GDPR-экспорт данных пользователя**
Кнопка "Мои данные" → скачивание всех данных пользователя (memories, interactions, feedback).
`GET /sync/export/memories` + `GET /sync/export/interactions` (фильтр по user_id)

### Категория V: Персонализация и UX

**V133. Тёмная/светлая тема**
Переключатель в header → тёмный/светлый режим → сохраняется в настройках.
`PUT /settings` (theme: "dark")

**V134. Язык интерфейса**
Dropdown: English / Русский → все подписи переводятся.
`PUT /settings` (locale: "ru") + i18n

**V135. Настраиваемый Dashboard**
Drag-and-drop виджетов: поиск, последние документы, быстрые действия, метрики.
`PUT /settings` (dashboard_layout: [...])

**V136. Закладки на результаты**
Кнопка "Закладка" на результате поиска → раздел "Мои закладки".
`POST /memories` (key: "bookmark_...", value: {doc_id, query, score})

**V137. Недавние действия**
Sidebar: последние 10 действий (поиск, загрузка, cognify) → быстрый доступ.
`GET /interactions` (limit: 10, sort: desc)

**V138. Onboarding tutorial**
При первом входе → пошаговое руководство: "Загрузите файл → Запустите Cognify → Ищите".
Клиентский guided tour (tooltips)

**V139. Command palette (Ctrl+K)**
Открывается окно быстрого доступа → поиск по действиям, коллекциям, запросам.
Клиентский fuzzy search по actions + `POST /search/text` (top_k: 5)

**V140. Пользовательские ярлыки**
Настройки → "При вводе /sum → выполнить search с query_type: SUMMARIES".
`PUT /settings` (shortcuts: {"/sum": {type: "SUMMARIES"}})

### Категория W: Продвинутые пайплайны

**W141. Scheduled cognify (по расписанию)**
Настройки датасета → "Обрабатывать каждые 24 часа" → cron-задача.
`POST /cognify` (trigger: cron, schedule: "0 0 * * *")

**W142. Webhook-triggered cognify**
Внешняя система отправляет POST → Levara запускает cognify автоматически.
`POST /api/v1/add` + `POST /api/v1/cognify` (auto_cognify: true)

**W143. Pipeline builder (визуальный)**
Drag-and-drop: Source → Chunk → Extract → Embed → Index. Кастомные пайплайны.
UI-only → генерация JSON-конфига → `POST /cognify` (pipeline_config: {...})

**W144. A/B тест поисковых стратегий**
Два варианта search config → 50/50 трафик → сравнение feedback scores.
`POST /search/text` + routing по experiment_id + `GET /feedback/stats` (group by experiment)

**W145. Data lineage (происхождение данных)**
Для каждого результата поиска → показать: из какого документа, какой чанк, какая сущность, через какой путь в графе.
`POST /search/text` → metadata.source_chunk + metadata.document_id → `GET /datasets/:id/data/:dataId`

**W146. Batch processing UI**
Загрузка CSV с запросами → массовый поиск → результаты в таблице → экспорт.
`POST /search/text` × N → клиентская агрегация

**W147. Knowledge gap detection**
Система анализирует неуспешные запросы (low feedback) → подсвечивает "пробелы в знаниях" → рекомендует загрузить данные по теме.
`GET /feedback` (rating < 3) → группировка по тематике

**W148. Auto-tagging при загрузке**
При загрузке файла → LLM классифицирует тему → автоматически присваивает теги.
`POST /add` → `POST /cognify` (auto_tags: true)

**W149. Cross-collection analytics**
Дашборд: сравнение качества поиска между коллекциями → графики NDCG, latency, usage.
`GET /collections` + `GET /feedback/stats` (per collection) + `GET /metrics`

**W150. Rollback cognify**
Кнопка "Откатить" → удалить все сущности/связи добавленные последним cognify run.
`POST /prune/data` (run_id: "...") или `POST /search/text` (CYPHER: "MATCH ... WHERE extracted_at > ...")

### Категория X: AI-ассистент и automation

**X151. Smart suggest при загрузке**
После загрузки файла → система рекомендует: стратегию чанкинга, нужную онтологию, целевую коллекцию.
`POST /add` → анализ content type → рекомендации

**X152. Auto-summary датасета**
Кнопка "Суммаризировать" → LLM создаёт краткое описание всего датасета.
`POST /search/text` (query_type: COMMUNITY_GLOBAL, collection: dataset_collection)

**X153. Вопросы к конкретному документу**
Выбрать документ → чат только по нему → ответы только из этого документа.
`POST /search/text` (collection: document_specific, session_id: doc_session)

**X154. Генерация FAQ из датасета**
Кнопка "Сгенерировать FAQ" → LLM создаёт 10-20 типичных вопросов и ответов.
`POST /search/text` (query_type: RAG_COMPLETION, query: "Generate FAQ...") → `POST /memories` × N

**X155. Детектор противоречий**
Кнопка "Проверить противоречия" → система находит факты которые противоречат друг другу в графе.
`POST /search/text` (query_type: GRAPH_COMPLETION, query: "Find contradictions...")

**X156. Auto-link между датасетами**
Система обнаруживает общие сущности между датасетами → предлагает объединить графы.
`GET /datasets` → для каждого `GET /datasets/:id/graph` → клиентское сравнение entities

**X157. Рекомендации "Вам может быть интересно"**
На основе истории запросов → система рекомендует документы которые пользователь ещё не видел.
`GET /interactions` → `POST /search/text` (on past queries, exclude seen IDs)

**X158. Batch fact-checking**
Загрузка списка утверждений → система проверяет каждое по базе знаний → отчёт: подтверждено/опровергнуто/не найдено.
`POST /search/text` (query_type: RAG_COMPLETION) × N → scoring

**X159. Knowledge graph diff**
Сравнение графа до и после cognify → показать добавленные/удалённые/изменённые сущности.
`GET /datasets/:id/graph` (snapshot before) vs (snapshot after)

**X160. Smart notifications**
Система замечает что новые данные противоречат старым → уведомление: "Факт X обновлён, проверьте".
Background job → `POST /memories` (type: alert) → push notification

---

## Часть 3: Группировка сценариев по экранам WebUI

| Экран | Сценарии | Ключевые API |
|-------|---------|-------------|
| **Login / Register** | A1, U127 | auth/login, auth/register |
| **Dashboard** | A2, A3, B8, Q101, Q105, V135, W149 | health, info, collections, datasets, metrics |
| **Datasets** | B4-B6, B9, I35, N71-N80, R112 | datasets, add, shares |
| **Cognify / Pipeline** | B7, B8, P91-P100, W141-W143 | cognify, cognify/stream |
| **Search** | C10-C18, K41-K50, W144, W146 | search/text, search/dual, feedback |
| **Chat (RAG)** | E26, C14-C15, M61-M70, X153-X154 | search/text (RAG), interactions |
| **Graph Viewer** | D19-D22, L51-L60 | visualize, graph, search (GRAPH/TEMPORAL) |
| **Memories** | E23-E24, O81-O90 | memories |
| **History** | E25, K48 | interactions |
| **Collections** | F27-F29, X156 | collections, reembed, check_drift |
| **Ontologies** | G30-G31, P98 | ontologies |
| **Notebooks** | H32-H33, R114, T126 | notebooks, cells, run |
| **Analytics** | Q101-Q110, W147 | metrics, feedback/stats, errors |
| **Settings** | I34, I36, I37, V133-V134, V140, U128-U132 | users, auth/keys, tenants, settings |
| **Sync / Export** | J38-J40, O90, N76 | sync/* |
| **Notifications** | R113, S118, X160 | SSE, WebSocket, push |
| **Integrations** | T121-T126 | webhooks, Swagger, embed widget |

---

## Часть 4: Задачи для полноценного WebUI (из анализа deep-research-report)

Ниже — задачи, которые НЕ покрыты пользовательскими сценариями, но необходимы для production-ready WebUI. Сгруппированы по блокам. Приоритеты: P0 = must-have для MVP, P1 = нужно до релиза, P2 = после релиза.

### Блок 1: Скоуп и Definition of Done

| # | Задача | Приоритет | Оценка | Описание |
|---|--------|:---------:|:------:|----------|
| T1 | Определить цели и не-цели WebUI | P0 | Medium | Что WebUI делает, а что — нет. Например: "WebUI не заменяет MCP для AI-агентов" |
| T2 | Definition of Done для релиза | P0 | Medium | Чеклист: все P0-сценарии работают, NFR выполнены, security baseline пройден |
| T3 | North Star метрика | P0 | Small | 1-2 метрики успеха: "время от загрузки документа до первого ответа < 60 сек" |
| T4 | Границы ответственности WebUI vs Backend | P1 | Small | Что делает фронт, что — бэкенд. Например: валидация файлов — фронт, chunking — бэкенд |

### Блок 2: Персоны и роли

| # | Задача | Приоритет | Оценка | Описание |
|---|--------|:---------:|:------:|----------|
| T5 | Описать 5 персон | P0 | Medium | Админ, аналитик, разработчик, viewer, интегратор (API). Для каждой: топ-5 задач, частота, критичность |
| T6 | Матрица ролей → экраны → операции | P0 | Medium | Кто что видит и может делать. Viewer не удаляет датасеты, аналитик не управляет тенантами |
| T7 | User journeys для каждой персоны | P1 | Medium | Путь от входа до решения задачи. Например: аналитик: вход → загрузка → cognify → поиск → экспорт |
| T8 | Jobs-to-be-done карта | P1 | Medium | "Когда [ситуация], я хочу [действие], чтобы [результат]" для каждой персоны |

### Блок 3: Функциональные спецификации (edge cases)

| # | Задача | Приоритет | Оценка | Описание |
|---|--------|:---------:|:------:|----------|
| T9 | UI-состояния для каждого экрана | P0 | Large | Loading / Empty / Error / Partial / Success. Для каждого экрана из Части 3 |
| T10 | Обработка ошибок API | P0 | Medium | Unified error model: код, message, traceId, retryable. Маппинг на UI (тост/модал/inline) |
| T11 | Пагинация/сортировка/фильтры | P0 | Medium | Стандарт для всех таблиц: cursor vs offset, лимиты, sort fields, filter syntax |
| T12 | Идемпотентность операций | P0 | Medium | Cognify/Memify/Reembed: защита от двойного запуска. Кнопка disabled после клика |
| T13 | Отмена/повтор/ретрай | P1 | Medium | Cancel cognify run, retry failed upload, undo delete (soft delete) |
| T14 | SSE reconnection policy | P0 | Medium | При обрыве: exponential backoff, resume from last event, таймаут 30 сек |
| T15 | Offline/degradation UX | P1 | Medium | Что показывать если embed-server/LLM/Neo4j недоступен. Graceful fallback |
| T16 | Bulk operations UX | P1 | Medium | Select all / select page / confirm destructive bulk actions |

### Блок 4: NFR (нефункциональные требования)

| # | Задача | Приоритет | Оценка | Описание |
|---|--------|:---------:|:------:|----------|
| T17 | Performance budget | P0 | Medium | LCP < 2.5s, FID < 100ms, CLS < 0.1. Измерение через Lighthouse CI |
| T18 | SLO для UI-операций | P0 | Medium | Search < 500ms, Upload < 5s (для файлов < 10MB), Cognify start < 1s |
| T19 | Масштабируемость | P1 | Medium | Целевое: 50 параллельных пользователей, 100K документов в коллекции |
| T20 | Browser support matrix | P0 | Small | Chrome/Edge/Firefox/Safari последние 2 версии. Mobile: iOS Safari, Chrome Android |

### Блок 5: Design System

| # | Задача | Приоритет | Оценка | Описание |
|---|--------|:---------:|:------:|----------|
| T21 | Design tokens | P0 | Medium | Colors (light/dark), spacing (4/8/12/16/24/32), typography (Inter/mono), radii, shadows |
| T22 | Component library | P0 | Large | Button, Input, Select, Table, Modal, Toast, Card, Badge, Tabs, Dropdown, Tooltip, Progress, Empty State, Error State |
| T23 | Паттерны страниц | P0 | Medium | List (table+filters+pagination), Detail (tabs+actions), Form (validation+submit), Dashboard (widgets grid) |
| T24 | Storybook | P1 | Medium | Каталог компонентов с вариантами, состояниями, a11y checks |
| T25 | Dark/Light theme | P1 | Medium | CSS variables / tokens для обеих тем, переключатель, persist в settings |
| T26 | Responsive breakpoints | P0 | Medium | Mobile (< 768), Tablet (768-1024), Desktop (> 1024). Sidebar collapse на mobile |
| T27 | Visual regression tests | P2 | Medium | Chromatic или Percy для ключевых компонентов |

### Блок 6: Accessibility (a11y)

| # | Задача | Приоритет | Оценка | Описание |
|---|--------|:---------:|:------:|----------|
| T28 | WCAG 2.2 AA baseline | P0 | Large | Семантический HTML, ARIA-паттерны для кастомных виджетов, contrast ratio 4.5:1 |
| T29 | Keyboard navigation | P0 | Medium | Все основные потоки (поиск, загрузка, навигация) доступны без мыши |
| T30 | Focus management | P0 | Medium | При открытии модалки — фокус внутрь, при закрытии — возврат. Skip links |
| T31 | Screen reader testing | P1 | Medium | VoiceOver (Mac), NVDA (Windows) на критичных потоках |
| T32 | Automated a11y в CI | P1 | Medium | axe-core / pa11y в CI pipeline на каждый PR |

### Блок 7: i18n (интернационализация)

| # | Задача | Приоритет | Оценка | Описание |
|---|--------|:---------:|:------:|----------|
| T33 | Определить локали | P0 | Small | MVP: ru + en. Fallback: en. BCP 47 tags |
| T34 | Система переводов | P0 | Medium | i18next / FormatJS. Все строки через ключи, нет конкатенации |
| T35 | Форматирование дат/чисел | P0 | Medium | Intl API: даты по локали, числа с разделителями, валюты |
| T36 | Псевдолокализация | P1 | Medium | Тест на длинные строки (немецкий +30%), RTL-проверка (если нужен арабский) |
| T37 | Extraction workflow | P1 | Medium | Автоматический сбор новых ключей из кода → файлы переводов |

### Блок 8: Архитектура фронтенда

| # | Задача | Приоритет | Оценка | Описание |
|---|--------|:---------:|:------:|----------|
| T38 | Выбор фреймворка | P0 | Small | React (Next.js) / Vue (Nuxt) / Svelte (SvelteKit). Решение зависит от команды |
| T39 | SSR/SSG/CSR стратегия | P0 | Medium | Dashboard: CSR. Public docs: SSG. Auth pages: SSR |
| T40 | State management | P0 | Medium | UI state (local), server state (TanStack Query / SWR), global state (Zustand / Pinia) |
| T41 | API client генерация | P0 | Medium | OpenAPI spec → TypeScript client (openapi-typescript-codegen). Нет ручного дрейфа |
| T42 | Routing и навигация | P0 | Medium | File-based routing, breadcrumbs, sidebar menu, deep linking |
| T43 | Form library | P0 | Medium | React Hook Form / Formik / VeeValidate. Validation rules, error display |
| T44 | Data fetching + caching | P0 | Medium | TanStack Query: stale-while-revalidate, optimistic updates, prefetch |
| T45 | SSE client | P0 | Medium | EventSource wrapper: auto-reconnect, backoff, event parsing, typing |

### Блок 9: CI/CD и developer workflow

| # | Задача | Приоритет | Оценка | Описание |
|---|--------|:---------:|:------:|----------|
| T46 | Monorepo или отдельный репо | P0 | Small | Решение: WebUI в `levara-webui/` отдельный repo или `webui/` monorepo |
| T47 | Linting + formatting | P0 | Medium | ESLint + Prettier + EditorConfig + Stylelint. Обязательные checks в CI |
| T48 | PR pipeline | P0 | Medium | lint → typecheck → unit → build → (preview deploy). GitHub Actions |
| T49 | Staging environment | P0 | Large | Auto-deploy PR → preview URL. Main → staging. Tag → production |
| T50 | Release process | P1 | Medium | SemVer + Conventional Commits + semantic-release + CHANGELOG |

### Блок 10: Тестирование

| # | Задача | Приоритет | Оценка | Описание |
|---|--------|:---------:|:------:|----------|
| T51 | Тест-пирамида | P0 | Medium | Unit (компоненты) → Integration (страницы) → E2E (критические потоки) |
| T52 | E2E framework | P0 | Medium | Playwright (multi-browser). Critical User Journeys: login → upload → cognify → search → feedback |
| T53 | Unit/component tests | P0 | Medium | Vitest + Testing Library. Coverage > 70% для бизнес-логики |
| T54 | API contract tests | P1 | Medium | Проверка что фронт и бэкенд не разъехались. Prism (OpenAPI mock) |
| T55 | Smoke suite | P0 | Medium | 5-10 E2E тестов на критичные потоки, запуск перед каждым деплоем |

### Блок 11: Observability и мониторинг

| # | Задача | Приоритет | Оценка | Описание |
|---|--------|:---------:|:------:|----------|
| T56 | Frontend error reporting | P0 | Medium | Sentry: unhandled errors, API failures, с контекстом (user, page, action) |
| T57 | TraceId сквозной | P1 | Medium | UI генерирует traceId → передаёт в API headers → коррелируется с backend логами |
| T58 | Performance monitoring | P1 | Medium | Core Web Vitals → dashboard. Lighthouse CI на каждый PR |
| T59 | User analytics | P2 | Medium | Анонимная аналитика: какие экраны используются, частота поиска, воронки |
| T60 | Dashboards + алерты | P1 | Medium | Grafana: JS errors/hour, API latency p95, active users. Alert on spike |

### Блок 12: Безопасность

| # | Задача | Приоритет | Оценка | Описание |
|---|--------|:---------:|:------:|----------|
| T61 | Threat model | P0 | Medium | Активы (данные пользователей, API ключи), акторы, trust boundaries |
| T62 | Auth architecture | P0 | Large | JWT в httpOnly cookie vs localStorage. Refresh tokens. Logout = invalidate |
| T63 | CORS policy | P0 | Small | Whitelist origins. Credentials mode. No wildcard в production |
| T64 | Security headers | P0 | Medium | CSP, X-Frame-Options, X-Content-Type-Options, HSTS, Referrer-Policy |
| T65 | XSS prevention | P0 | Medium | Никакого dangerouslySetInnerHTML. Sanitize user input. CSP nonce для inline scripts |
| T66 | CSRF protection | P0 | Medium | SameSite=Strict cookies. Double-submit token для API mutations |
| T67 | Dependency scanning | P0 | Medium | Dependabot / Renovate. Автоматические PR на уязвимые зависимости |
| T68 | Rate limiting UI | P1 | Medium | Показывать 429 красиво: "Слишком много запросов, подождите N сек" |
| T69 | Secrets management | P1 | Medium | Никаких API keys в клиентском коде. Env vars через build-time injection |
| T70 | Security audit checklist | P1 | Medium | OWASP ASVS L1 для MVP, L2 для production |

### Блок 13: Приватность и compliance

| # | Задача | Приоритет | Оценка | Описание |
|---|--------|:---------:|:------:|----------|
| T71 | Data inventory | P0 | Large | Какие данные собираем, где храним, зачем, сроки хранения |
| T72 | Privacy notice | P1 | Medium | Ссылка в footer. Что собираем, как обрабатываем, права пользователя |
| T73 | Cookie consent | P1 | Medium | Баннер для non-essential cookies (аналитика). Согласие до активации |
| T74 | Right to deletion | P1 | Medium | Кнопка "Удалить мой аккаунт" → удаление всех данных (GDPR Art. 17) |
| T75 | Data export (GDPR) | P1 | Medium | Кнопка "Скачать мои данные" → JSON/CSV со всеми данными пользователя |
| T76 | PII masking in logs | P0 | Medium | Не логировать email, пароли, токены ни на фронте, ни на бэкенде |

### Блок 14: Документация и онбординг

| # | Задача | Приоритет | Оценка | Описание |
|---|--------|:---------:|:------:|----------|
| T77 | Docs site | P1 | Medium | Docusaurus / VitePress. Разделы: tutorials, how-to, reference, concepts (Diátaxis) |
| T78 | User quickstart | P0 | Medium | "5 минут до первого поиска": регистрация → загрузка файла → поиск |
| T79 | Developer quickstart | P0 | Medium | git clone → npm install → npm dev → открыть localhost:3001 |
| T80 | API reference | P0 | Medium | Swagger UI по OpenAPI spec. Interactive "Try it" |
| T81 | Troubleshooting FAQ | P1 | Small | Топ-20 вопросов: "Cognify зависает", "Поиск возвращает пусто", etc. |
| T82 | Contributing guide | P1 | Small | PR process, code style, test requirements, release flow |

### Блок 15: Maintenance и roadmap

| # | Задача | Приоритет | Оценка | Описание |
|---|--------|:---------:|:------:|----------|
| T83 | Browser support policy | P0 | Small | Последние 2 версии major browsers. Обновление раз в квартал |
| T84 | Dependency update cadence | P0 | Medium | Dependabot weekly. Major upgrades — раз в квартал с тестированием |
| T85 | Feature flag system | P1 | Medium | Для постепенного раскатывания новых фич. LaunchDarkly / Unleash / env vars |
| T86 | Public roadmap | P2 | Small | GitHub Projects / Linear board. Что в следующем релизе |
| T87 | Deprecation policy | P2 | Small | 2 минорных версии warning → удаление. Changelog entries |

---

## Часть 5: Статистика

- **Пользовательских сценариев**: 160 (A1-X160)
- **Инженерных задач**: 87 (T1-T87)
- **Backend endpoints**: 118
- **Экранов WebUI**: 17
- **Всего требований**: 247
- **Категорий сценариев**: 21 (A-X)
- **Блоков задач**: 15 (Блоки 1-15)
