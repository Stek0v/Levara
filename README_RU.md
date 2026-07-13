# Levara

> Локальная и корпоративная память для AI-агентов: поиск, граф знаний, MCP, workspace и управляемые профили в одном Go-проекте.

[![Go](https://img.shields.io/badge/Go-1.26+-00ADD8.svg)](https://go.dev/)
[![Profile Gate](https://img.shields.io/badge/profiles-personal%20%7C%20solo%20%7C%20team%20%7C%20enterprise-brightgreen.svg)](docs/profile-presets.md)
[![License](https://img.shields.io/badge/license-MIT-green.svg)](LICENSE)

Levara помогает агентам сохранять решения, факты, рабочий контекст и результаты поиска так, чтобы новая сессия могла быстро восстановить состояние проекта. Внутри: HNSW, BM25, граф знаний, WAL, Memory Palace, Markdown workspace, MCP-инструменты и профили развёртывания от локального одиночного разработчика до enterprise-команды.

<Callout type="info">
Русская версия является кратким вводным документом. Полное MDX-описание проекта, архитектуры и команд см. в `README.md`.
</Callout>

<Callout type="warning">
Фактическое локальное состояние Mac-развёртывания описано в `docs/current-state.md`: сейчас Levara работает через launchd на `:8081`, профиль `standalone-embed`, embedding `potion-code-16M`/256d на `:9101`, PostgreSQL и локальный LLM подключены, gRPC/Neo4j/rerank выключены.
</Callout>

## Для кого

| Профиль | Целевая аудитория | База | Auth | Хранилище | Enterprise gate |
|---|---|---|---|---|---|
| `personal` | Один разработчик и локальные AI-агенты | SQLite | Выключен по умолчанию | Файловая система | Нет |
| `solo_pro` | Индивидуальная работа с бэкапами и sync | SQLite | Опционально | Локально + S3-совместимое | Нет |
| `team` | Команда с workspace и audit | Postgres | Обязателен | Управляемое shared-хранилище | RBAC + tenant checks |
| `enterprise` | Корпоративные команды | Postgres | Обязателен | KMS/BYOK + внешние sinks | SSO, SCIM, SIEM, retention |

## Что умеет

| Слой | Возможности |
|---|---|
| Core engine | HNSW, WAL, BM25, graph search, temporal edges, cognify pipeline |
| Agent memory | MCP, Memory Palace, wake-up briefings, pinned facts, per-agent diaries |
| Workspace plane | Markdown source of truth, manifest, citations, audit log, async index jobs |
| Identity/access | JWT, API keys, RBAC policy service, tenant membership, profile validation |
| Enterprise adapters | OIDC/SSO proposal surface, SCIM, KMS/BYOK, audit sinks, object storage contracts |

## Быстрый старт

```bash
go run ./cmd/server \
  -profile=standalone \
  -dim=768 \
  -port=8080 \
  -grpc-port=0
```

Текущий локальный Mac-запуск использует другой набор флагов:

```bash
./levara-server \
  -profile=standalone-embed \
  -dim=256 \
  -port=8081 \
  -grpc-port=0 \
  -data-dir=/Users/stek0v/src/levara/data \
  -embed-endpoint=http://127.0.0.1:9101/v1/embeddings \
  -embed-model=potion-code-16M \
  -llm-upstream=http://localhost:11434/v1
```

Для проверки продуктовых профилей:

```bash
make profile-config-check
```

Для локального smoke-набора:

```bash
go test ./docs ./pkg/profile ./pkg/access ./pkg/storage
```

## Основные документы

| Документ | Назначение |
|---|---|
| `README.md` | Полное MDX-введение, структура репозитория, команды и API surfaces |
| `docs/current-state.md` | Проверенное фактическое состояние локального Mac runtime |
| `docs/getting-started.md` | Актуальный быстрый старт и команды проверки |
| `docs/deployment.md` | Реальные launchd/systemd/Docker рецепты |
| `docs/product-ladder.md` | Продуктовая лестница Personal → Solo Pro → Team → Enterprise |
| `docs/profile-presets.md` | Правила конфигурации и fail-fast gates для профилей |
| `docs/security/enterprise-readiness-checklist.md` | Security checklist перед Team/Enterprise rollout |
| `docs/marketing/` | Маркетинговые материалы по сегментам |

## Разработка

```bash
go test ./...
make contract-check
make profile-config-check
```

Корневой `Makefile` запускает основные команды напрямую из корня репозитория.

## Лицензия

MIT. Подробности в `LICENSE`.
