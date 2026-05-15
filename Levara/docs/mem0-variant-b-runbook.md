# mem0 Variant B — runbook

**Status:** Variant B код корректен и intended path для всех writes из mem0.
**Backend (MemoryFS Rust daemon на :7777) — Phase 1 не зашиплен**, см.
`local_net/docs/superpowers/specs/2026-05-10-memory-stack-rca-design.md`
и P0 в [post-phase2.5-roadmap](post-phase2.5-roadmap.md). До Phase 1
ship — этот runbook описывает что есть и как с этим жить.

---

## Что такое Variant B

Поток данных:

```
mem0.add(text)
  → MemoryFSVectorStore.insert()
    → PUT  http://localhost:7777/v1/files/memories/<user>/<id>.md
    → POST http://localhost:7777/v1/workspaces/<ws>/commit
    → (MemoryFS даёт коммит, индексер пишет в Levara gRPC)
mem0.search(query)
  → MemoryFSVectorStore.search()
    → POST http://localhost:8080/api/v1/search   (Levara, read-only)
```

`.md` файл — source of truth. Индекс Levara — disposable derivative,
восстанавливается через `cmd/reconcile` (см. [reconcile-guide.md](reconcile-guide.md)).

`update` и `delete` идут через `POST /v1/workspaces/<ws>/supersede` —
старый файл помечается `status: superseded`, новый/tombstone коммитится
в одной транзакции. **Append-only**, история сохраняется.

---

## Когда использовать какой provider в mem0 config

| Сценарий | provider | Когда |
|---|---|---|
| MemoryFS daemon запущен и Phase 1 готов | `memoryfs` | **target state**, сейчас не выполнимо |
| Daemon не запущен, нужна память | `mem0` (Pi 10.23.0.53:8765) | **сейчас по умолчанию для кросс-проектного** |
| Per-project, нужен local-only stdio MCP | `mfs` (Python bridge) | сейчас для project-specific |
| Прямые writes в Levara минуя MemoryFS | ❌ removed | Variant A удалён (commit `ff4340f`) |

`"levara"` provider в mem0 **больше не существует** — фабрика
(`mem0/utils/factory.py`) и configs (`mem0/vector_stores/configs.py`)
вычищены. Если кто-то поднимает старый config с `provider: levara` —
получит `ValueError: Unsupported vector store provider: levara`. Это
сознательно.

---

## Health check

С 2026-05-16 `MemoryFSVectorStore.__init__` делает `GET /health` на
старте и **падает с понятной ошибкой** если daemon не отвечает:

```
RuntimeError: MemoryFS daemon unreachable at http://localhost:7777
  (ConnectionError: ...). Variant B writes require the MemoryFS Rust
  daemon to be running. Either start it (see Levara/docs/mem0-variant-b-runbook.md)
  or pass require_daemon=False to defer the check to first write.
```

Раньше первый `insert()` падал на bare `ConnectionRefusedError` глубоко
внутри worker thread — тяжело дебажить, особенно если mem0 вызывается
как side-effect другого инструмента. Теперь fail-fast на конструкции.

**Override:** `require_daemon=False` — отложить проверку до первой записи.
Используется только в тестах (где Variant B mock-ается) и при намеренной
конфигурации lazy-init.

---

## Поднять MemoryFS daemon (когда Phase 1 будет готов)

Сейчас бинарь в архиве: `_archive/2026-05-10/LevaraOs/memoryfs-planning/`.
Когда Phase 1 ship выпустит persistence — порядок:

```bash
cd /path/to/memoryfs
cargo build --release
./target/release/memoryfs serve \
    --addr 0.0.0.0:7777 \
    --data ~/.local/share/memoryfs \
    --acl ./acl.yaml
```

Env vars которые ожидает daemon (по дизайн-доку):

| Var | Назначение |
|---|---|
| `MEMORYFS_LEVARA_GRPC` | gRPC endpoint Levara для индексации (`localhost:50051`) |
| `MEMORYFS_LEVARA_JWT` | JWT для gRPC auth |
| `MEMORYFS_DATA_DIR` | Корень `.md` корпуса (default `~/.local/share/memoryfs`) |
| `MEMORYFS_ACL` | Путь к ACL policy yaml |

Health check для smoke-теста:

```bash
curl http://localhost:7777/health      # {"status":"ok","commit":"..."}
curl http://localhost:7777/v1/workspaces
```

---

## mem0 config для Variant B (target state)

`mem0_config.yaml`:

```yaml
vector_store:
  provider: memoryfs
  config:
    collection_name: mem0
    embedding_model_dims: 768
    memoryfs_url: http://localhost:7777
    workspace_id: default
    levara_url: http://localhost:8080
    levara_api_key: ${LEVARA_JWT}
    require_daemon: true     # fail-fast если daemon не отвечает
```

`embedding_model_dims: 768` — выровнено с реальным dim Pi-коллекций
(см. live `/api/v1/collections`). 1024 — старый default, исправлен
2026-05-16.

---

## Troubleshooting

| Симптом | Причина | Фикс |
|---|---|---|
| `RuntimeError: MemoryFS daemon unreachable` на init | daemon не запущен | `cargo run -p memoryfs` или временно `require_daemon=False` + переключиться на `mem0` provider |
| `mem0.add()` молча проходит, но `search()` ничего не находит | индексер MemoryFS не догнал, или Levara dim mismatch | `curl localhost:8080/api/v1/collections \| jq '.[]\|select(.name=="mem0")'` — проверить `embedding_dim` |
| `Connection refused` на :8080 | Levara-сервер не поднят | `docker compose up -d` или `cd Levara && go run cmd/server/main.go` |
| `401` от MemoryFS | `levara_api_key` истёк или не задан | передать `LEVARA_JWT` env |
| Свежий insert не виден в `list(user_id=X)` | работает корректно — `list` теперь читает MemoryFS напрямую (`memories/{user_id}/`), не Levara top-k. Если файла нет в MemoryFS — `insert` не дошёл до commit | проверить логи MemoryFS commit |

---

## Связанные документы

- [reconcile-guide.md](reconcile-guide.md) — восстановить Levara-индекс из MemoryFS корпуса
- [markdown-native-workspace.md](markdown-native-workspace.md) — параллельный markdown слой Levara (не путать)
- [post-phase2.5-roadmap.md](post-phase2.5-roadmap.md) — P0 MemoryFS Phase 1 ship

---

## История изменений

- **2026-05-16** — Создан. Health check в `MemoryFSVectorStore.__init__`,
  `levara.py` default dim 1024→768, фиксация что Variant A удалён
  (`ff4340f`), `"levara"` provider в mem0 больше не существует.
