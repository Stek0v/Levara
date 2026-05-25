# Levara Deployment Matrix

Дата: 2026-05-17.

Этот документ фиксирует поддерживаемые режимы развертывания и их границы. Он
должен обновляться вместе с `docker-compose*.yml`, `Raspberry/`, `cmd/server`
flags и ops scripts.

## Matrix

| Mode | Primary use | Required services | Optional services | Storage | HA story | Notes |
|---|---|---|---|---|---|---|
| Standalone local/dev | Development, demos, local agents | Levara server | SQLite, embed, LLM, Neo4j | local `data/` | none | Default `-standalone=true`; fastest feedback loop |
| Full stack | Production-like RAG/KG | Levara, PostgreSQL, Neo4j, embed service | LLM, rerank, Prometheus, S3 | local or S3 | external DB/Neo4j durability | Canonical feature-complete mode |
| Raspberry Pi | Edge/local memory appliance | Levara, SQLite/local storage | embed service, local LLM | local disk | backup scripts | Prefer SQLite and conservative resource settings |
| Primary/replica | Read replica / warm standby | Primary Levara + replica Levara | SQL/Neo4j stack as needed | local per node | WAL stream + snapshot endpoints | Uses `/cluster/wal/stream`, `/cluster/snapshot`, `/cluster/state` |
| Raft shards | Experimental/legacy consensus path | Multiple Levara nodes | Prometheus | local per node | Hashicorp Raft per shard | Separate from CollectionManager canonical path; needs explicit e2e before production use |
| S3/object storage | Shared raw upload storage | Levara + SQL | S3/MinIO/Spaces | `storage://` raw data | backend-dependent | Set `STORAGE_BACKEND=s3` and S3 env vars |

## Canonical API Policy

- New vector and memory features should use collection-aware APIs.
- Legacy vector endpoints `/insert`, `/batch_insert`, `/delete` remain
  compatibility endpoints.
- Unified semantic search should use `/api/v1/search/text` or the MCP `search`
  tool. `/api/v1/search` is retained as a frontend compatibility alias.
- gRPC collection APIs are canonical for backend adapters such as Cognee.
- MCP is canonical for AI-agent memory workflows.

## Production Readiness Checklist

| Area | Required before production |
|---|---|
| Auth | `JWT_SECRET`, API-key policy, `-require-auth=true` where exposed |
| SQL | PostgreSQL migrations verified; SQLite only for local/Pi-style deployments |
| Embeddings | Embedding dimension matches `-dim` and collection metadata |
| Search | ACL-before-rerank invariant tested for any external reranker |
| Graph | Neo4j schema bootstrap decision documented via `NEO4J_BOOTSTRAP_SCHEMA` |
| Storage | Local/S3 raw data access verified, including raw URL/download paths |
| Observability | `/metrics`, `/health/details`, doctor tool, alerting/SLOs wired |
| Backup | Backup/restore tested for SQL, vector data, uploads, and workspace state |

