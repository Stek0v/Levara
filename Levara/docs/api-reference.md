# Levara API Reference

Levara exposes three interfaces: HTTP REST, gRPC, and MCP (Model Context Protocol).

## HTTP REST API

Base URL: `http://localhost:8080`

### Health

```
GET /health
```

Response:
```json
{
  "status": "ok",
  "vectors": 1234,
  "uptime": "2h15m30s"
}
```

### Metrics

```
GET /metrics
```

Prometheus-format metrics.

---

### Collections

#### Create Collection

```
POST /api/v1/collections
```

```json
{
  "name": "my_collection",
  "dimension": 768
}
```

#### List Collections

```
GET /api/v1/collections
```

#### Delete Collection

```
DELETE /api/v1/collections/{name}
```

---

### Vectors

#### Insert / Upsert

```
POST /api/v1/insert
```

```json
{
  "id": "doc_1",
  "vector": [0.1, 0.5, 0.9, ...],
  "metadata": {
    "text": "Document content",
    "source": "file.pdf",
    "page": 3
  },
  "collection": "my_collection"
}
```

#### Batch Insert

```
POST /api/v1/batch-insert
```

```json
{
  "records": [
    {
      "id": "doc_1",
      "vector": [0.1, 0.5, ...],
      "metadata": {"text": "First document"}
    },
    {
      "id": "doc_2",
      "vector": [0.3, 0.7, ...],
      "metadata": {"text": "Second document"}
    }
  ],
  "collection": "my_collection"
}
```

#### Search

```
POST /api/v1/search
```

```json
{
  "vector": [0.1, 0.5, 0.8, ...],
  "k": 5,
  "collection": "my_collection",
  "filter": {
    "source": "file.pdf"
  }
}
```

Response:
```json
{
  "results": [
    {
      "id": "doc_1",
      "score": 0.95,
      "metadata": {
        "text": "Document content",
        "source": "file.pdf"
      }
    }
  ],
  "latency_ms": 2.6
}
```

#### Rerank (Phase 2, default-on)

When `RERANK_ENDPOINT` is configured server-side, the unified text search
endpoint (`POST /api/v1/search` with `query_text` body) reranks results
through a cross-encoder by default. Clients control this via a tri-state
`rerank` field:

| value | meaning |
|---|---|
| field omitted / `null` | server default — on iff `RERANK_ENDPOINT` is set |
| `true` | force on (still requires endpoint) |
| `false` | explicit opt-out |

Phase 1.5 default model: `cross-encoder/mmarco-mMiniLMv2-L12-H384-v1`,
ONNX INT8 (Pi 5 winner; see `docs/phase2-rerank-default-design.md`).

Latency budget cap: `RERANK_BUDGET_MS` (default 1500). On overrun the
search returns the vector-order ranking and emits
`levara_rerank_invocations_total{outcome="budget"}`. Other outcomes:
`ok`, `error`, `disabled`, `no_text` (Phase 2.1 — candidates returned
but none carried a `text`/`name` field in metadata, so the rerank pass
had nothing to score; distinct from `error` to surface data-shape gaps).

#### Get by ID

```
GET /api/v1/records/{id}?collection=my_collection
```

#### Delete

```
DELETE /api/v1/records/{id}?collection=my_collection
```

---

### Datasets

#### Add Data

```
POST /api/v1/datasets/{name}/add
```

```json
{
  "text": "Raw text to process",
  "source": "manual"
}
```

Or upload a file:

```bash
curl -X POST http://localhost:8080/api/v1/datasets/my_project/add \
  -F "file=@document.pdf"
```

#### Cognify (Process Data)

```
POST /api/v1/datasets/{name}/cognify
```

```json
{
  "wait": true
}
```

#### Search Dataset

```
POST /api/v1/datasets/{name}/search
```

```json
{
  "query": "What is the main architecture?",
  "type": "GRAPH_COMPLETION",
  "k": 5
}
```

Supported search types: `VECTOR`, `GRAPH_COMPLETION`, `HYBRID`, `TEMPORAL`, `COT`, `NL_CYPHER`, `ENTITY`, `KEYWORD`, `SUMMARY`, `CLASSIFICATION`, `ONTOLOGY`, `PROVENANCE`, `AGGREGATE`, `STRUCTURED`, `GIT`.

#### List Datasets

```
GET /api/v1/datasets
```

#### Delete Dataset

```
DELETE /api/v1/datasets/{name}
```

#### Prune Dataset

```
POST /api/v1/datasets/{name}/prune
```

---

### Git Analysis

#### Analyze Repository

```
POST /api/v1/git/analyze
```

```json
{
  "repo_path": "/path/to/repo",
  "since": "2024-01-01",
  "dataset": "my_project"
}
```

#### Diff Summary

```
POST /api/v1/git/diff-summary
```

```json
{
  "repo_path": "/path/to/repo",
  "branch": "main"
}
```

---

## gRPC API

Port: `50051`

Proto file: `proto/levara.proto`

### Service: LevaraService

```protobuf
service LevaraService {
  rpc CreateCollection(CreateCollectionRequest) returns (CreateCollectionResponse);
  rpc ListCollections(ListCollectionsRequest) returns (ListCollectionsResponse);
  rpc DeleteCollection(DeleteCollectionRequest) returns (DeleteCollectionResponse);
  rpc Upsert(UpsertRequest) returns (UpsertResponse);
  rpc BatchUpsert(BatchUpsertRequest) returns (BatchUpsertResponse);
  rpc Search(SearchRequest) returns (SearchResponse);
  rpc Get(GetRequest) returns (GetResponse);
  rpc Delete(DeleteRequest) returns (DeleteResponse);
}
```

### Examples with grpcurl

```bash
# Create collection
grpcurl -plaintext -d '{"name":"my_collection"}' \
  localhost:50051 levara.v1.LevaraService/CreateCollection

# Insert
grpcurl -plaintext -d '{
  "collection": "my_collection",
  "id": "doc_1",
  "vector": [0.1, 0.5, 0.9],
  "payload": {"text": "hello"}
}' localhost:50051 levara.v1.LevaraService/Upsert

# Search
grpcurl -plaintext -d '{
  "collection": "my_collection",
  "vector": [0.1, 0.5, 0.8],
  "k": 3
}' localhost:50051 levara.v1.LevaraService/Search

# Delete
grpcurl -plaintext -d '{"collection":"my_collection","id":"doc_1"}' \
  localhost:50051 levara.v1.LevaraService/Delete
```

---

## MCP (Model Context Protocol)

Endpoint: `http://localhost:8080/mcp`

### Available Tools (15)

| Tool | Description |
|------|-------------|
| `add` | Add text or file to a dataset |
| `cognify` | Process dataset into knowledge graph |
| `search` | Search across datasets (all 15 search types) |
| `datasets_list` | List all datasets |
| `dataset_delete` | Delete a dataset |
| `health` | Check server health |
| `prune` | Remove stale data from a dataset |
| `status` | Get processing status |
| `git_analyze` | Analyze a git repository |
| `git_diff_summary` | Get diff summary for a branch |
| `classify` | Classify text |
| `summarize` | Summarize text or document |
| `extract_entities` | Extract named entities |
| `ontology_build` | Build ontology from data |
| `graph_query` | Query the knowledge graph (Cypher) |

### Configuration

```json
{
  "mcpServers": {
    "levara": {
      "url": "http://localhost:8080/mcp"
    }
  }
}
```

---

## Error Responses

All endpoints return errors in a consistent format:

```json
{
  "error": "collection not found: my_collection",
  "code": 404
}
```

| HTTP Code | Meaning |
|-----------|---------|
| 400 | Bad request (invalid parameters) |
| 404 | Resource not found |
| 409 | Conflict (duplicate ID) |
| 500 | Internal server error |
