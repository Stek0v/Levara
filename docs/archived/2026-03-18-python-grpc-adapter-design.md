# Python gRPC Adapter for VectraDB

**Date**: 2026-03-18
**Status**: Approved
**Scope**: Replace HTTP/REST transport in VectraDBAdapter with gRPC, add GetByID + HasCollection RPCs

---

## Context

The current `VectraDBAdapter.py` communicates with VectraDB Go server via HTTP/REST (aiohttp). This introduces significant overhead:

- **JSON serialization**: 4 passes per request (Python→JSON→Go→JSON→Python). A dim=1024 vector becomes ~15KB text instead of 4KB binary.
- **HTTP overhead**: ~300-500 bytes headers per request/response, URL routing, middleware.
- **Prefix hack**: No native collections — client-side `{collection}:{id}` prefix with over-fetch (limit × num_collections).
- **Cache-only operations**: `retrieve()` works from Python in-memory cache (lost on restart). `delete()` clears cache only, not server data.

The Go gRPC server (port 50051) is already implemented with native collections, real delete (HNSW tombstone + WAL), and chunking. Benchmarks show 8.4x faster search via gRPC vs HTTP.

## Decision

Full replacement of HTTP transport with gRPC inside the existing `VectraDBAdapter.py`. No fallback to HTTP. Add `GetByID` and `HasCollection` RPCs to eliminate Python-side caches and avoid inefficient ListCollections round-trips.

## Design

### 1. Proto Changes (`VectraDB/proto/vectradb.proto`)

Add two new RPCs and supporting messages:

```protobuf
// Add to service VectraDBService:
rpc GetByID(GetByIDReq) returns (GetByIDResp);
rpc HasCollection(HasCollectionReq) returns (HasCollectionResp);

message GetByIDReq {
  string collection = 1;
  repeated string ids = 2;
}

message GetByIDResp {
  repeated RecordEntry records = 1;
}

message RecordEntry {
  string id = 1;
  string metadata_json = 2;
  bool found = 3;
  repeated float vector = 4;  // optional, for future export/backup use
}

message HasCollectionReq {
  string name = 1;
}

message HasCollectionResp {
  bool exists = 1;
}
```

Design notes:
- `RecordEntry.vector` included for future-proofing (export/backup). The Python adapter will ignore it initially; Cognee's `retrieve()` returns `ScoredResult(id, payload, score)` with no vector.
- `HasCollection` is a dedicated O(1) RPC instead of fetching the full list. `CollectionManager` already has `Has(name) bool` in `collections.go`.

### 2. Go Changes

#### 2.1 `internal/grpc/service.go` — add GetByID + HasCollection handlers

**GetByID** (~30 lines): Calls `collections.Get(collection)` → iterates IDs → calls existing `db.Get(id)` which returns `([]float32, []byte, bool)`. Discards vector, uses metadata bytes for `metadata_json`.

Note: `db.Get(id string) ([]float32, []byte, bool)` already exists in `db.go:267`. No new store method needed — the handler uses the existing `Get()` and discards the vector component.

**HasCollection** (~5 lines): Calls `collections.Has(name)` → returns `HasCollectionResp{Exists: exists}`.

#### 2.2 Search error handling fix

Current `service.go:116-141` silently returns empty `SearchResp{}` when collection is not found or when Search returns an error. Fix: return `codes.NotFound` gRPC status for missing collection, `codes.Internal` for search errors. This allows the Python adapter to distinguish "no results" from "error."

### 3. Build (`Makefile`)

```makefile
.PHONY: proto-go proto-python proto

proto-go:
	protoc --go_out=. --go-grpc_out=. proto/vectradb.proto

proto-python:
	python -m grpc_tools.protoc \
		-Iproto \
		--python_out=cognee-plugin/vectradb_adapter/generated \
		--grpc_python_out=cognee-plugin/vectradb_adapter/generated \
		proto/vectradb.proto

proto: proto-go proto-python
```

Generated Python files go to `cognee-plugin/vectradb_adapter/generated/` and are listed in `.gitignore`.

### 4. Python Adapter Changes (`VectraDBAdapter.py`)

#### 4.1 Transport replacement

Remove:
- `aiohttp` dependency, `orjson` dependency
- `_session`, `_session_lock` (aiohttp session management)
- `_id_cache`, `_id_cache_maxsize` (in-memory payload cache)
- `_collections` (Python-side set)
- `_headers()`, `_open_session()`, `_get_session()`, `_post()`, `_batch_post()`
- `_prefixed_id()`, `_strip_prefix()` (prefix hack)
- `_serialize_payload()` top-level function

Keep:
- `_embedding_cache`, `_embedding_cache_maxsize` — embedding cache is transport-independent and avoids redundant `embedding_engine.embed_text()` calls on re-indexing. Removing it would be a performance regression.

Add:
- `grpc.aio.insecure_channel(url)` — async gRPC channel
- `VectraDBServiceStub(channel)` — generated client stub
- `_safe_call(coro)` — wrapper for gRPC errors

```python
def __init__(self, url, api_key, embedding_engine, database_name=None):
    self.url = (url or "localhost:50051").replace("http://", "")
    self.embedding_engine = embedding_engine
    self._channel = grpc.aio.insecure_channel(self.url)
    self._stub = pb_grpc.VectraDBServiceStub(self._channel)
    # Embedding cache kept: transport-independent, prevents redundant GPU calls
    self._embedding_cache: Dict[str, List[float]] = {}
    self._embedding_cache_maxsize = 4096
```

#### 4.2 Channel lifecycle

```python
async def close(self):
    """Close the gRPC channel. Must be called on shutdown to avoid hanging."""
    await self._channel.close()
```

Replaces current `await self._session.close()`. Same semantic contract.

#### 4.3 Error handling (`_safe_call`)

```python
async def _safe_call(self, coro):
    """Wrap gRPC calls with human-readable errors."""
    try:
        return await coro
    except grpc.aio.AioRpcError as e:
        if e.code() in (grpc.StatusCode.UNAVAILABLE, grpc.StatusCode.DEADLINE_EXCEEDED):
            raise ConnectionError(
                f"VectraDB unavailable at {self.url}: {e.details()}"
            ) from e
        raise RuntimeError(
            f"VectraDB gRPC error ({e.code().name}): {e.details()}"
        ) from e
```

- `UNAVAILABLE` / `DEADLINE_EXCEEDED` → `ConnectionError` (transient, retriable by caller)
- All other gRPC errors → `RuntimeError` (application-level failure)
- No retries in the adapter — retry policy is the caller's responsibility (Cognee pipeline handles this)

#### 4.4 Method mapping (9 VectorDBInterface methods)

| Method | gRPC RPC | Key change |
|--------|----------|------------|
| `has_collection(name)` | `HasCollection(name)` | Dedicated O(1) RPC, not Python set |
| `create_collection(name)` | `CreateCollection(name)` | Real server-side collection with HNSW+WAL |
| `create_data_points(col, dps)` | `embed_data()` → `BatchInsert(col, records)` | No prefix hack, metadata as `metadata_json` string |
| `retrieve(col, ids)` | `GetByID(col, ids)` | From disk via Go, not Python cache |
| `search(col, text, vec, limit, ...)` | `embed_data()` (if text) → `Search(col, vec, limit)` | No over-fetch, native collections |
| `batch_search(col, texts, ...)` | Parallel `search()` via `asyncio.gather` | Same logic, faster transport |
| `delete_data_points(col, ids)` | `Delete(col, ids)` | Real HNSW tombstone + WAL delete |
| `prune()` | `ListCollections()` → `DropCollection()` each | Real server-side data removal |
| `embed_data(texts)` | `self.embedding_engine.embed_text()` | Unchanged, `_embedding_cache` retained |

#### 4.5 Unchanged behavior

- Score conversion: `score = 1.0 - similarity` (VectraDB higher=better → Cognee lower=better)
- `node_name` filtering: client-side, using `json.loads(metadata_json)` from SearchResult. Known trade-off: requires JSON parsing per result for filtered queries. Server-side filtering can be added later as an optimization but is out of scope for this change since `node_name` is used infrequently.
- `create_vector_index()` / `index_data_points()`: wrappers over create_collection/create_data_points

#### 4.6 Application-level error handling

- `StatusResp.ok == False` → `RuntimeError` with server error message
- `BatchInsertResp.failed > 0` → `RuntimeError` (same as current behavior)
- `Search` on missing collection → Go now returns `codes.NotFound` → adapter raises `RuntimeError("Collection not found: ...")`

### 5. Test Changes

#### 5.1 Unit tests (`test_vectradb_adapter.py`)

Update mocks: replace `_post`/`_batch_post` patches with `_stub.*` method patches.

- `adapter._stub.BatchInsert` replaces `adapter._batch_post`
- `adapter._stub.Search` replaces `adapter._post("/api/v1/search", ...)`
- `adapter._stub.HasCollection` replaces `adapter._collections` checks
- `adapter._stub.CreateCollection` replaces `adapter._collections.add()`
- `adapter._stub.GetByID` — new
- `adapter._stub.Delete` — new (previously untested at server level)

#### 5.2 Integration tests (`test_vectradb_integration.py`)

MockVectraServer (HTTP mock) replaced with mock gRPC stub or real Go server.

### 6. Dependencies

Add:
- `grpcio >= 1.60.0` (runtime)
- `grpcio-tools >= 1.60.0` (dev only, stub generation)
- `protobuf >= 4.25.0` (runtime)

Remove from adapter:
- `aiohttp` (may still be used in tests for embed-server)
- `orjson`

## Files Changed

| Layer | File | Action |
|-------|------|--------|
| Proto | `VectraDB/proto/vectradb.proto` | +GetByID, +HasCollection RPCs, +messages |
| Go service | `VectraDB/internal/grpc/service.go` | +GetByID, +HasCollection handlers, fix Search error handling |
| Build | `Makefile` | New: proto-go, proto-python targets |
| Python adapter | `cognee-plugin/vectradb_adapter/VectraDBAdapter.py` | Full rewrite: HTTP→gRPC |
| Python generated | `cognee-plugin/vectradb_adapter/generated/` | New dir, stubs in .gitignore |
| Python tests | `tests/test_vectradb_adapter.py` | Update mocks |
| Python tests | `tests/test_vectradb_integration.py` | Update mock server |

## Definition of Done

1. `make proto` generates Go and Python stubs without errors
2. Go test: `TestGetByID` — insert → getbyid → verify metadata
3. Go test: `TestHasCollection` — create → has(true) → drop → has(false)
4. Go test: `TestSearchErrorHandling` — search on missing collection returns gRPC NotFound
5. All 22 unit tests in `test_vectradb_adapter.py` PASSED (with updated mocks)
6. Integration with real server: insert → search → retrieve → delete cycle via gRPC
7. Search latency via Python gRPC < 1ms (was 2.6ms via HTTP)
8. `aiohttp` is not imported in the adapter

## Known Trade-offs

1. **`node_name` filtering requires client-side JSON parsing**: Each search result's `metadata_json` must be `json.loads()`-ed to extract `belongs_to_set`. This is acceptable because `node_name` filtering is infrequent. Server-side filtering can be added to SearchReq later.
2. **No HTTP fallback**: If gRPC server is down, adapter fails immediately. This is by design — the HTTP server remains running for external consumers but the Cognee adapter uses gRPC exclusively.
3. **`RecordEntry.vector` unused initially**: The proto includes the vector field for future export/backup use cases, but the Python adapter ignores it (Cognee's `retrieve()` only needs metadata).
