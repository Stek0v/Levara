# Markdown Workspace Capability Parity

Этот документ фиксирует текущий parity-контракт между `REST`, `CLI` и `MCP` для markdown-native workspace. Если capability существует только в одном слое, это должно быть осознанным исключением, а не случайным дрейфом интерфейсов.

## Parity Table

| Capability | REST | CLI | MCP | Status |
|---|---|---|---|---|
| Access preflight | `POST /workspace/access/check` | `not exposed` | `workspace_access_check` | `intentional-gap` |
| Bootstrap context | `GET /workspace/context` | `levara workspace context` | `workspace_context` | `parity` |
| Audit log | `GET /workspace/audit` | `not exposed` | `workspace_audit_log` | `intentional-gap` |
| Context artifacts list | `GET /workspace/context/artifacts` | `not exposed` | `workspace_context_artifacts` | `intentional-gap` |
| Context artifacts reindex | `POST /workspace/context/artifacts/reindex` | `not exposed` | `workspace_reindex_artifacts` | `intentional-gap` |
| Ops status | `GET /workspace/ops/status` | `levara workspace ops-status` | `workspace_ops_status` | `parity` |
| Conflict report | `GET /workspace/conflicts` | `levara workspace conflicts` | `workspace_conflicts` | `parity` |
| Manifest read | `GET /workspace/manifest` | `levara workspace manifest` | `not exposed` | `intentional-gap` |
| Exact read | `GET /workspace/read` | `levara workspace read` | `workspace_read` | `parity` |
| Indexed write | `POST /workspace/write` | `levara workspace write` | `workspace_write` | `parity` |
| Direct index file | `POST /workspace/index` | `levara workspace index` | `not exposed` | `intentional-gap` |
| Delete indexed path | `POST /workspace/delete` | `levara workspace delete` | `not exposed` | `intentional-gap` |
| Reindex paths | `POST /workspace/reindex` | `levara workspace reindex` | `not exposed` | `intentional-gap` |
| Reconcile generation | `POST /workspace/reconcile` | `levara workspace reconcile` | `not exposed` | `intentional-gap` |
| Watch status | `GET /workspace/watch/status` | `levara workspace watch-status` | `not exposed` | `intentional-gap` |
| Run start | `POST /workspace/runs/start` | `levara workspace run start` | `workspace_run_start` | `parity` |
| Run get | `GET /workspace/runs/get` | `levara workspace run get` | `workspace_run_get` | `parity` |
| Commit | `POST /workspace/commit` | `levara workspace commit` | `workspace_commit` | `parity` |
| Log | `GET /workspace/log` | `levara workspace log` | `not exposed` | `intentional-gap` |
| Revert | `POST /workspace/revert` | `levara workspace revert` | `workspace_revert` | `parity` |
| GC / dry-run | `POST /workspace/gc` | `levara workspace gc` | `workspace_gc` | `parity` |
| Search by active generation | `GET /search` plus workspace resolution in server layer | `levara search ...` | `workspace_search` | `functional-parity` |

## Intentional Gaps

- `workspace_access_check` и `workspace_audit_log` не вынесены в CLI, потому что это агентские и ops-oriented capability, а не ежедневный authoring flow.
- `workspace_context_artifacts` и `workspace_reindex_artifacts` сейчас доступны через `REST` и `MCP`, но не обёрнуты в CLI. Это нормальный следующий CLI task, а не пробел в серверной логике.
- `index`, `delete`, `reindex`, `reconcile`, `manifest`, `watch-status`, `log` не имеют отдельных MCP tools, потому что agent-facing contract строится вокруг `workspace_write`, `workspace_search`, `workspace_read`, `workspace_commit`, `workspace_revert`, `workspace_gc`.

## DoD For Future Parity Work

- Новый workspace capability не должен добавляться без явного решения: `REST only`, `CLI only`, `MCP only` или `parity`.
- Если capability помечен как `intentional-gap`, причина должна быть зафиксирована в этом документе.
- Если capability становится ежедневным пользовательским потоком, gap должен быть закрыт отдельной задачей с тестом.
