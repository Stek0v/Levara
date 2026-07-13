# Levara — Подробная инструкция для начинающих

> Пошаговое руководство: от нуля до работающего AI-сервера памяти.
> Никаких предварительных знаний не требуется.

> **Актуальность:** это подробный учебный текст. Для проверенного фактического
> состояния локального Mac-развёртывания см. `docs/current-state.md`; для
> короткого актуального старта см. `docs/getting-started.md`. Сейчас локально
> используется `:8081`, `standalone-embed`, `potion-code-16M`/256d, PostgreSQL,
> локальный LLM; gRPC/Neo4j/rerank выключены.

---

## Что такое Levara?

Levara — это **сервер памяти** для AI-ассистентов (Claude Code, Cursor). Представьте, что у вашего AI-помощника появляется долговременная память: он запоминает решения, архитектуру проекта, историю изменений — и использует это в каждом разговоре.

**Простая аналогия**: Levara — это как записная книжка для AI, которая ещё и умеет искать по смыслу.

### Что умеет:
- Запоминать ваши решения и предпочтения
- Искать по смыслу, а не только по ключевым словам
- Строить карту связей вашего проекта (кто что вызывает, от чего зависит)
- Анализировать историю коммитов
- Хранить историю разговоров
- Работать с несколькими проектами одновременно (каждый изолирован)

---

## Часть 1: Установка

### Вариант A: На вашем компьютере (macOS / Linux)

#### Шаг 1. Установите Go (если ещё нет)

**macOS:**
```bash
brew install go
```

**Linux (Ubuntu/Debian):**
```bash
sudo apt update && sudo apt install -y golang
```

Проверьте:
```bash
go version
# Должно показать: go version go1.26+ ...
```

#### Шаг 2. Скачайте и соберите Levara

```bash
# Скачайте исходный код
git clone https://github.com/stek0v/levara.git
cd levara

# Соберите сервер и CLI
make build

# Проверьте что серверный бинарь создан
ls -la levara-server
```

#### Шаг 3. Запустите

```bash
# Самый простой запуск (без LLM, только базовый функционал)
./levara-server \
  -profile=standalone \
  -dim=384 \
  -shards=1 \
  -port=8080 \
  -grpc-port=0 \
  -data-dir=./data
```

#### Шаг 4. Проверьте что работает

Откройте **новый терминал** и выполните:
```bash
curl http://localhost:8080/health
```

Вы должны увидеть:
```json
{"health":"healthy","status":"ready","version":"levara-go"}
```

Если видите это — **Levara работает!**

---

### Вариант B: На Raspberry Pi 5

#### Что нужно:
- Raspberry Pi 5 (8ГБ ОЗУ)
- microSD карта (32ГБ+) или NVMe SSD
- Подключение к сети

#### Шаг 1. Установите Ollama (локальная AI-модель)

```bash
# На Pi выполните:
curl -fsSL https://ollama.ai/install.sh | sh

# Скачайте модели (это займёт несколько минут)
ollama pull qwen3:0.6b          # Языковая модель (522МБ)
ollama pull nomic-embed-text     # Модель для поиска по смыслу (274МБ)
```

#### Шаг 2. Скачайте Levara для Pi

На вашем **компьютере** (не на Pi):
```bash
# Скомпилируйте под ARM64 (архитектура Pi)
cd levara
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o levara-arm64 ./cmd/server/

# Скопируйте на Pi (замените IP на ваш)
scp levara-arm64 pi@192.168.1.100:~/levara/levara
```

Или, если исходный код уже на Pi:
```bash
go build -o levara ./cmd/server/
```

#### Шаг 3. Запустите на Pi

```bash
ssh pi@192.168.1.100

# Создайте папку для данных
mkdir -p ~/levara/data

# Критично: обе модели должны быть в памяти одновременно
export OLLAMA_MAX_LOADED_MODELS=2

# Настройте переменные окружения
export DB_PROVIDER=sqlite
export DB_PATH=$HOME/levara/data/levara.db
export EMBEDDING_ENDPOINT=http://localhost:11434/v1/embeddings
export EMBEDDING_MODEL=nomic-embed-text
export LLM_ENDPOINT=http://localhost:11434/v1
export LLM_MODEL=qwen3:0.6b

# Запустите
cd ~/levara
chmod +x levara-server
./levara-server -profile=standalone-embed -dim=768 -shards=1 -port=8080 -grpc-port=0 -data-dir=$HOME/levara/data
```

#### Шаг 4. Проверьте

С вашего компьютера:
```bash
curl http://192.168.1.100:8080/health
# → {"health":"healthy","status":"ready","version":"levara-go"}

curl http://192.168.1.100:8080/health/details
# → покажет статус всех компонентов: embed, llm, database
```

---

## Часть 2: Подключение к Claude Code

### Шаг 1. Добавьте MCP-сервер

В терминале, где вы используете Claude Code:

```bash
claude mcp add --transport http levara http://localhost:8080/mcp
```

> Если Levara на Pi, замените `localhost` на IP вашего Pi:
> ```bash
> claude mcp add --transport http levara http://192.168.1.100:8080/mcp
> ```

### Шаг 2. Проверьте подключение

Запустите Claude Code и спросите:

```
Какие MCP инструменты доступны от levara?
```

Claude должен ответить, что видит MCP-инструменты Levara: cognify, search, save_memory, workspace_* и т.д. Точный список меняется вместе с `pkg/mcp` и контрактом в `docs/api-contract.md`.

### Альтернатива: конфигурация через файл

Создайте файл `.mcp.json` в корне вашего проекта:

```json
{
  "mcpServers": {
    "levara": {
      "type": "http",
      "url": "http://localhost:8080/mcp"
    }
  }
}
```

Этот файл можно закоммитить в git — вся команда будет использовать один и тот же сервер памяти.

---

## Часть 3: Подключение к Cursor

Создайте файл `.cursor/mcp.json` в корне проекта:

```json
{
  "mcpServers": {
    "levara": {
      "url": "http://localhost:8080/mcp"
    }
  }
}
```

Перезапустите Cursor. В настройках (Settings → MCP) вы должны увидеть "levara" в списке подключённых серверов.

---

## Часть 4: Первые шаги — Загрузка проекта

### 4.1. Загрузите документы проекта

Попросите Claude Code:

```
Загрузи README.md этого проекта в Levara для будущего использования
```

Claude вызовет инструмент `cognify` и загрузит содержимое в базу знаний.

Или сделайте вручную через curl:

```bash
# Загрузить README.md
curl -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "id": 1,
    "method": "tools/call",
    "params": {
      "name": "cognify",
      "arguments": {
        "data": "'"$(cat README.md)"'",
        "collection": "my-project"
      }
    }
  }'
```

### 4.2. Сохраните важные решения

```
Запомни: в этом проекте мы используем JWT с httpOnly cookies для аутентификации
```

Claude вызовет `save_memory` и сохранит решение.

### 4.3. Проанализируйте историю Git

```
Проанализируй последние 50 коммитов в этом репозитории
```

Claude вызовет `analyze_commits` — вся история станет доступна для поиска.

---

## Часть 5: Ежедневное использование

### Поиск по проекту

Просто спрашивайте Claude Code как обычно. Если Levara подключена, Claude автоматически будет обращаться к серверу памяти за контекстом:

```
Как работает аутентификация в этом проекте?
```

Claude вызовет `search` → получит из Levara релевантные фрагменты → ответит с учётом знаний проекта.

### Сохранение решений

```
Запомни: мы решили использовать PostgreSQL вместо MySQL из-за поддержки JSONB
```

Это решение сохранится навсегда. Через месяц, в новой сессии:

```
Какую базу данных мы используем и почему?
```

Claude найдёт ваше сохранённое решение и ответит точно.

### Контекст проекта

В начале новой сессии:

```
Дай мне контекст текущего проекта
```

Claude вызовет `get_project_context` и получит: стек технологий, ключевые решения, статистику базы знаний — всё за один запрос.

### Работа с несколькими проектами

Каждый проект = отдельная коллекция. Данные изолированы:

```
Найди информацию об авторизации в проекте topka
```

Поиск идёт только в коллекции "topka" — другие проекты не затрагиваются.

---

## Часть 6: Полный список команд

### 16 MCP-инструментов

| Инструмент | Что делает | Пример использования |
|------------|-----------|---------------------|
| **cognify** | Превращает текст в базу знаний | "Загрузи этот документ в память" |
| **search** | Ищет по базе знаний | "Найди код связанный с авторизацией" |
| **add** | Добавляет сырые данные | "Сохрани этот текст для обработки" |
| **save_memory** | Сохраняет решение/факт | "Запомни что мы используем Redis" |
| **recall_memory** | Вспоминает сохранённое | "Какой кэш мы используем?" |
| **list_memories** | Показывает все сохранённые факты | "Покажи все решения по проекту" |
| **save_chat** | Сохраняет разговор | Автоматически |
| **recall_chat** | Загружает прошлый разговор | "Что мы обсуждали в прошлый раз?" |
| **search_chats** | Ищет по разговорам | "Когда мы обсуждали миграцию?" |
| **analyze_commits** | Анализирует git историю | "Проанализируй последние коммиты" |
| **git_search** | Ищет по коммитам | "Кто менял файл auth.go?" |
| **get_project_context** | Полный контекст проекта | "Дай контекст проекта" |
| **list_data** | Список всех данных | "Что загружено в базу?" |
| **delete** | Удаляет датасет | "Удали старые данные" |
| **prune** | Полный сброс | "Очисти всю базу знаний" |
| **cognify_status** | Статус обработки | "Закончилась ли загрузка?" |

### 7 типов поиска

| Тип | Когда использовать |
|-----|-------------------|
| **HYBRID** (по умолчанию) | Общий поиск — комбинирует смысл и ключевые слова |
| **CHUNKS** | Поиск похожего кода/текста |
| **CHUNKS_LEXICAL** | Точный поиск по словам (имена переменных, ошибки) |
| **RAG_COMPLETION** | Когда нужен готовый ответ, а не ссылки |
| **GRAPH_COMPLETION** | Поиск связей ("что зависит от X?") |
| **TEMPORAL** | Поиск по времени ("что менялось на прошлой неделе?") |
| **SUMMARIES** | Поиск по саммари документов |

---

## Часть 7: Администрирование

### Проверка здоровья сервера

```bash
# Быстрая проверка
curl http://localhost:8080/health

# Подробная проверка (все компоненты)
curl http://localhost:8080/health/details
```

Подробная проверка покажет:
```json
{
  "services": {
    "backend": {"status": "connected"},
    "embed": {"status": "connected", "model": "nomic-embed-text"},
    "llm": {"status": "connected", "model": "qwen3:0.6b"},
    "postgres": {"status": "connected"},
    "collections": {"count": 3, "dimension": 768}
  }
}
```

### Просмотр коллекций

```bash
curl http://localhost:8080/api/v1/collections
```

Покажет:
```json
[
  {"name": "my-project", "record_count": 45, "embedding_dim": 768},
  {"name": "other-project", "record_count": 12, "embedding_dim": 768}
]
```

### Удаление коллекции

```bash
# Удалить все данные одного проекта
curl -X DELETE http://localhost:8080/api/v1/collections/my-project
```

### Статистика кэша

```bash
curl http://localhost:8080/api/v1/cache/stats
```

### Метрики (Prometheus)

```bash
curl http://localhost:8080/metrics
```

---

## Часть 8: Запуск в фоне (как сервис)

### На Linux / Raspberry Pi (systemd)

Создайте файл `/etc/systemd/system/levara.service`:

```ini
[Unit]
Description=Levara Knowledge Engine
After=network.target

[Service]
Type=simple
User=pi
WorkingDirectory=/home/pi/levara
Environment=DB_PROVIDER=sqlite
Environment=DB_PATH=/home/pi/levara/data/levara.db
Environment=EMBEDDING_ENDPOINT=http://localhost:11434/v1/embeddings
Environment=EMBEDDING_MODEL=nomic-embed-text
Environment=LLM_ENDPOINT=http://localhost:11434/v1
Environment=LLM_MODEL=qwen3:0.6b
Environment=OLLAMA_MAX_LOADED_MODELS=2
ExecStart=/home/pi/levara/levara-server -profile=standalone-embed -dim=768 -shards=1 -port=8080 -grpc-port=0 -data-dir=/home/pi/levara/data
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

Активируйте:
```bash
sudo systemctl daemon-reload
sudo systemctl enable levara
sudo systemctl start levara

# Проверить статус
sudo systemctl status levara

# Посмотреть логи
journalctl -u levara -f
```

### На macOS (launchd)

Создайте файл `~/Library/LaunchAgents/com.levara.server.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.levara.server</string>
    <key>ProgramArguments</key>
    <array>
        <string>/usr/local/bin/levara-server</string>
        <string>-profile=standalone</string>
        <string>-dim=768</string>
        <string>-shards=1</string>
        <string>-port=8080</string>
        <string>-grpc-port=0</string>
    </array>
    <key>EnvironmentVariables</key>
    <dict>
        <key>DB_PROVIDER</key>
        <string>sqlite</string>
        <key>DB_PATH</key>
        <string>/Users/YOU/levara/data/levara.db</string>
    </dict>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
</dict>
</plist>
```

Активируйте:
```bash
launchctl load ~/Library/LaunchAgents/com.levara.server.plist
```

---

## Часть 9: Резервное копирование

### Что сохранять

Вся информация хранится в двух местах:
1. **SQLite файл** — память, чаты, граф сущностей
2. **Папка data/** — векторные индексы, WAL-файлы

### Резервное копирование

```bash
# Остановите сервер (или используйте горячий бэкап SQLite)
sudo systemctl stop levara

# Скопируйте данные
cp -r ~/levara/data ~/levara/backup-$(date +%Y%m%d)

# Запустите обратно
sudo systemctl start levara
```

### Восстановление

```bash
sudo systemctl stop levara
rm -rf ~/levara/data
cp -r ~/levara/backup-20240315 ~/levara/data
sudo systemctl start levara
```

---

## Часть 10: Устранение проблем

### Levara не запускается

```bash
# Проверьте порт не занят
lsof -i :8080

# Проверьте права на папку данных
ls -la ~/levara/data/

# Посмотрите логи
cat ~/levara/levara.log
```

### Claude Code не видит инструменты

```bash
# Проверьте что MCP-сервер добавлен
claude mcp list

# Проверьте что Levara отвечает
curl -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}'

# Должен вернуть список из 16 инструментов
```

### Поиск возвращает пустые результаты

Причины:
1. **Данные не загружены** — сначала нужно вызвать `cognify` для загрузки документов
2. **Embed-сервер не настроен** — проверьте `curl http://localhost:8080/health/details` — поле "embed" должно быть "connected"
3. **Другая коллекция** — проверьте что ищете в правильной коллекции

### Embed показывает "unreachable"

```bash
# Проверьте что Ollama запущен
curl http://localhost:11434/api/tags

# Если не запущен:
ollama serve &

# Прогрейте модели
curl -s http://localhost:11434/v1/embeddings -X POST \
  -H "Content-Type: application/json" \
  -d '{"model":"nomic-embed-text","input":"test"}'
```

### Медленный cognify на Pi

Это нормально. На Raspberry Pi 5 обработка одного документа занимает ~25 секунд (модель qwen3:0.6b работает на CPU). Советы:
- Загружайте документы частями (не весь проект сразу)
- Используйте `head -60` для длинных файлов — первые 60 строк обычно содержат основную информацию
- Cognify работает асинхронно — можно проверять статус через `cognify_status`

### Мало свободной памяти на Pi

```bash
# Проверьте использование памяти
free -m

# Если мало — убедитесь что OLLAMA_MAX_LOADED_MODELS=2
# Если всё равно мало — используйте более лёгкую embed модель:
# all-minilm вместо nomic-embed-text (33МБ вместо 274МБ, но dim=384)
ollama pull all-minilm
```

---

## Часть 11: Полезные советы

### Что загружать в Levara

- **README.md** — обзор проекта
- **CLAUDE.md** — инструкции для AI
- **docs/*.md** — документация
- **Ключевые файлы кода** (первые 60 строк — обычно достаточно для понимания)
- **Архитектурные решения** — через `save_memory`

### Что НЕ нужно загружать

- Весь исходный код целиком (AI и так может прочитать файлы)
- Файлы package-lock.json, go.sum и подобные
- Бинарные файлы
- Секреты и пароли

### Лучшие практики

1. **Одна коллекция = один проект**. Не смешивайте данные разных проектов.
2. **Сохраняйте решения сразу**. Приняли решение → `save_memory`. Через месяц поблагодарите себя.
3. **Анализируйте коммиты регулярно**. Раз в неделю `analyze_commits` — и вся история под рукой.
4. **Начинайте сессию с контекста**. `get_project_context` в начале разговора экономит время.

---

## Быстрая шпаргалка

```bash
# Запуск
./levara-server -profile=standalone -dim=768 -port=8080 -grpc-port=0

# Проверка
curl http://localhost:8080/health

# Подключить к Claude Code
claude mcp add --transport http levara http://localhost:8080/mcp

# Подключить к Cursor
echo '{"mcpServers":{"levara":{"url":"http://localhost:8080/mcp"}}}' > .cursor/mcp.json

# Посмотреть коллекции
curl http://localhost:8080/api/v1/collections

# Посмотреть детальный статус
curl http://localhost:8080/health/details
```

---

## Нужна помощь?

- Документация: `docs/` в репозитории
- Подробно о MCP-интеграции: `docs/levara_as_memory.md`
- Многопроектный режим: `docs/per-project-collections.md`
- Русская версия: `docs/ru_levara_as_memory.md`
