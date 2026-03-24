# Levara — Per-Project Collections

> Одна папка проекта = одна коллекция. Полная изоляция данных.

## Концепция

```
~/projects/
  topka/        → collection "topka"     (PRD, код, архитектура)
  levara/       → collection "levara"    (Go engine, docs)
  my-startup/   → collection "my-startup" (pitch, plan, финансы)
```

Каждая коллекция — изолированный HNSW индекс на диске. Search в одной коллекции НЕ возвращает данные из другой.

---

## Как загрузить проект

### Через CLI
```bash
# Загрузить и обработать документы проекта
levara cognify "$(cat ~/projects/topka/README.md)" --collection=topka
levara cognify "$(cat ~/projects/topka/docs/architecture.md)" --collection=topka

# Или все файлы разом
for f in ~/projects/topka/docs/*.md; do
  levara cognify "$(head -60 "$f")" --collection=topka
done
```

### Через MCP (Claude Code)
```json
{
  "name": "cognify",
  "arguments": {
    "data": "содержимое CLAUDE.md или README...",
    "collection": "topka"
  }
}
```

### Через REST API
```bash
curl -X POST http://pi:8080/api/v1/cognify \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "texts": ["текст документа..."],
    "collection": "topka"
  }'
```

---

## Как искать

### В конкретном проекте
```bash
levara search "аутентификация" --collection=topka
```

```json
// MCP
{"name": "search", "arguments": {"search_query": "аутентификация", "collection": "topka"}}
```

```bash
# REST API
curl -X POST http://pi:8080/api/v1/search/text \
  -d '{"query_text": "аутентификация", "query_type": "CHUNKS", "collection": "topka", "top_k": 5}'
```

### По всем проектам
```bash
levara search "аутентификация"  # без --collection = все коллекции
```

---

## Как управлять коллекциями

### Список коллекций
```bash
curl http://pi:8080/api/v1/collections
```
```json
[
  {"name": "topka", "record_count": 29, "embedding_dim": 384},
  {"name": "levara", "record_count": 15, "embedding_dim": 384}
]
```

### Удалить коллекцию (все данные проекта)
```bash
curl -X DELETE http://pi:8080/api/v1/collections/topka
```

### Пересоздать (обновить данные)
```bash
# 1. Удалить старую
curl -X DELETE http://pi:8080/api/v1/collections/topka

# 2. Загрузить заново
for f in ~/projects/topka/docs/*.md; do
  levara cognify "$(head -60 "$f")" --collection=topka
done
```

---

## Как сохранять память проекта

### Save memory (привязана к проекту)
```json
// MCP
{
  "name": "save_memory",
  "arguments": {
    "key": "tech_stack",
    "value": "Next.js + Go + PostgreSQL",
    "type": "project",
    "collection": "topka"
  }
}
```

### Recall memory
```json
{
  "name": "recall_memory",
  "arguments": {
    "query": "tech stack",
    "collection": "topka"
  }
}
```

---

## Как очистить данные

### Очистить один проект
```bash
curl -X DELETE http://pi:8080/api/v1/collections/topka
```

### Очистить всё
```bash
# Удалить все коллекции
for coll in $(curl -s http://pi:8080/api/v1/collections | python3 -c "import sys,json; [print(c['name']) for c in json.load(sys.stdin)]"); do
  curl -X DELETE "http://pi:8080/api/v1/collections/$coll"
done

# Или: удалить data директорию и перезапустить
ssh pi 'rm -rf ~/levara/data/node* && systemctl restart levara'
```

---

## Как обновить документы проекта

### Вариант 1: Полная перезагрузка
```bash
# Удалить старую коллекцию
curl -X DELETE http://pi:8080/api/v1/collections/topka

# Загрузить заново
for f in ~/projects/topka/docs/*.md; do
  levara cognify "$(head -60 "$f")" --collection=topka
done
```

### Вариант 2: Добавить новые (без удаления старых)
```bash
# Просто загрузить новый файл — он добавится к существующим
levara cognify "$(cat ~/projects/topka/docs/new-feature.md)" --collection=topka
```

### Вариант 3: Автоматическое обновление через git hook
```bash
# .git/hooks/post-commit
#!/bin/bash
CHANGED=$(git diff --name-only HEAD~1 -- docs/)
for f in $CHANGED; do
  [ -f "$f" ] && levara cognify "$(head -60 "$f")" --collection=$(basename $(pwd))
done
```

---

## Claude Code MCP конфигурация

### claude_desktop_config.json
```json
{
  "mcpServers": {
    "levara": {
      "url": "http://raspberrypi.local:8080/mcp"
    }
  }
}
```

### Как Claude Code использует
```
Claude: → search("архитектура", collection="topka")
Levara: → ищет ТОЛЬКО в topka коллекции
Claude: ← "Next.js frontend, Go backend, PostgreSQL..."

Claude: → save_memory(key="decision", value="выбрали React вместо Vue", collection="topka")
Levara: → сохраняет в topka project memory

Claude: → recall_memory(query="какой фреймворк", collection="topka")
Levara: ← "выбрали React вместо Vue"
```

---

## Мониторинг

### Размер каждого проекта
```bash
curl -s http://pi:8080/api/v1/collections | python3 -c "
import sys, json
for c in json.load(sys.stdin):
    print(f'{c[\"name\"]:20s} {c.get(\"record_count\",0):5d} records')
"
```

### Общий размер на диске
```bash
du -sh ~/levara/data/
```

### Cache hit rate
```bash
curl -s http://pi:8080/api/v1/cache/stats | python3 -m json.tool
```

---

## Лимиты и рекомендации

| Параметр | Рекомендация |
|----------|-------------|
| Проектов (коллекций) | До 50 на Pi 5 (8GB) |
| Записей на проект | До 10K (search <2s) |
| Размер документа для cognify | До 4KB (head -60 файла) |
| Cognify time | ~25s на Pi (qwen3:0.6b) |
| Search time | ~1s на Pi |
| Обновление | Полная перезагрузка коллекции |
