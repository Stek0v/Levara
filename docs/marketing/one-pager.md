# Levara — One-Pager

**Levara gives AI agents persistent, searchable, governed memory — without
forcing teams into a SaaS memory silo.**

_Русская версия ниже._

---

## The problem

AI agents are powerful inside one context window and forgetful outside it.
Chat history is noisy, vector search alone loses provenance, and shared agent
workspaces need explicit access, audit, and recovery semantics. Switching
agents or models means starting over.

## What Levara does

One Go binary, local-first:

- **Agent memory** under a project-specific `room × hall` taxonomy — facts,
  decisions, events, preferences, advice, discoveries — with wake-up briefings
  and filtered recall.
- **Hybrid search**: semantic + BM25 keyword fusion, temporal knowledge graph,
  optional reranker.
- **Markdown workspace**: human-readable files stay the source of truth;
  indexes are disposable derivatives.
- **Long-horizon tasks** (alpha): Definition-of-Done criteria, leases,
  immutable receipts, deterministic validation.
- **Governance**: RBAC, tenant isolation, audit log, auth on every surface
  (REST, MCP, gRPC, WebUI).
- **Sync**: bidirectional Mac ↔ server ↔ Pi, last-write-wins with atomic
  conflict resolution.

## Why it's different

| | SaaS memory | Vector DB alone | Levara |
|---|---|---|---|
| Data location | vendor cloud | your infra | your infra |
| Agent-agnostic (MCP) | ✗ | — | ✔ |
| Provenance + taxonomy | partial | ✗ | ✔ |
| Workspace files humans can read | ✗ | ✗ | ✔ |
| Access control & audit | partial | ✗ | ✔ |
| Works offline | ✗ | — | ✔ |

The agent is replaceable. The model is replaceable. The project's context
stays.

## Verified (September 2026)

- **Security review**: 54 findings → 52 fixed, 2 documented design choices;
  all critical and high severity closed with regression tests.
- **CI, 19 green checks**: lint, vet, race detector, govulncheck (0 reachable
  vulnerabilities), npm audit (0 vulnerabilities), contract drift check,
  Postgres integration shards.
- **Load-tested**: 50 concurrent agents — save p95 22 ms, recall p95 238 ms,
  zero cross-agent leaks; 3,200 background jobs on two servers sharing one
  database — zero duplicate executions.

## Profiles: same engine, four operating models

`personal` (local SQLite) → `solo_pro` (multi-device sync) → `team` (shared
Postgres + auth) → `enterprise` (tenant isolation, audit export, identity and
storage adapter seams).

## Get started

```bash
git clone https://github.com/Stek0v/Levara.git && cd Levara
make build
cp deploy/profiles/personal.local.env.example .env
./levara-server -config-check && ./levara-server -profile=standalone -port=8080
```

MIT licensed · https://github.com/Stek0v/Levara

---

# Levara — одна страница

**Levara даёт ИИ-агентам долговременную память, поиск и рабочее пространство,
которые можно держать локально — без SaaS-силы.**

## Проблема

ИИ-агенты сильны внутри одного контекстного окна и забывчивы за его пределами.
История чатов шумная, чистый векторный поиск теряет происхождение данных, а
общим рабочим пространствам агентов нужны доступ, аудит и восстановление.

## Что делает Levara

Один Go-бинарник, local-first: память агентов (таксономия room × hall),
гибридный поиск (семантика + BM25 + временной граф знаний), Markdown-workspace
(файлы читаемы человеком — источник истины), долгие задачи с DoD-критериями и
неизменяемыми квитанциями, RBAC/tenant/аудит, двунаправленная синхронизация
Mac ↔ сервер ↔ Pi.

## Чем отличается

Агента можно заменить. Модель можно обновить. Контекст проекта остаётся.

## Проверено (сентябрь 2026)

- Аудит кода: 54 находки → 52 исправлено, все критические и высокие — с
  регрессионными тестами.
- CI: 19 зелёных проверок, включая govulncheck и npm audit (0 уязвимостей).
- Нагрузка: 50 параллельных агентов — запись p95 22 мс, recall p95 238 мс,
  0 утечек; 3 200 фоновых задач на двух узлах — 0 дублей.

## Профили

`personal` → `solo_pro` → `team` → `enterprise` — один движок, четыре модели
эксплуатации.

MIT · https://github.com/Stek0v/Levara
