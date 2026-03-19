# Python gRPC Adapter Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace HTTP/REST transport in VectraDBAdapter with gRPC, add GetByID + HasCollection RPCs, eliminate Python-side caches.

**Architecture:** Extend Go gRPC proto with 2 new RPCs (GetByID, HasCollection), add Go handlers, add Makefile proto generation, then rewrite Python VectraDBAdapter to use grpcio instead of aiohttp. All 9 VectorDBInterface methods map to gRPC calls. TDD: write failing Go tests first, then implement; then rewrite Python adapter and update Python tests.

**Tech Stack:** Go (grpc, protobuf), Python (grpcio, grpcio-tools, protobuf), Protocol Buffers

**Spec:** `docs/superpowers/specs/2026-03-18-python-grpc-adapter-design.md`

---

## Important Context

**Two copies of VectraDBAdapter exist:**
- `cognee-plugin/vectradb_adapter/VectraDBAdapter.py` — standalone plugin (401 lines)
- `cognee/cognee/infrastructure/databases/vector/vectradb/VectraDBAdapter.py` — cognee tree copy (425 lines, has extra `health_check()` method)

Tests load the adapter from the **cognee tree copy** via `conftest.py` (line 268-271). Both files must be updated. The cognee tree copy is the "source of truth" for tests.

**conftest.py uses `importlib.util.spec_from_file_location()`** to load the adapter. The new adapter uses relative imports (`from .generated import vectradb_pb2`) which won't work with this loading strategy. conftest.py must register the generated modules in `sys.modules` before loading the adapter.

---

## File Structure

### New files
- `cognee/cognee/infrastructure/databases/vector/vectradb/generated/__init__.py` — package init
- `cognee/cognee/infrastructure/databases/vector/vectradb/generated/.gitignore` — ignore pb2 files
- `VectraDB/internal/grpc/service_test.go` — Go tests for new RPCs

### Modified files
- `VectraDB/proto/vectradb.proto` — +GetByID, +HasCollection RPCs and messages
- `VectraDB/internal/grpc/service.go` — +GetByID, +HasCollection handlers, fix Search error handling
- `VectraDB/Makefile` — +proto-go, +proto-python targets
- `Makefile` (root) — +proto target
- `cognee-plugin/pyproject.toml` — replace aiohttp/orjson with grpcio/protobuf
- `cognee-plugin/vectradb_adapter/VectraDBAdapter.py` — full rewrite HTTP→gRPC
- `cognee/cognee/infrastructure/databases/vector/vectradb/VectraDBAdapter.py` — same rewrite + health_check
- `tests/conftest.py` — register generated protobuf modules, update adapter loading
- `tests/test_vectradb_adapter.py` — update mocks from HTTP to gRPC stubs

---

## Task 1: Extend Proto with GetByID + HasCollection

**Files:**
- Modify: `VectraDB/proto/vectradb.proto`

- [ ] **Step 1: Add HasCollection and GetByID RPCs to proto**

In `VectraDB/proto/vectradb.proto`, add inside `service VectraDBService` block after line 9 (ListCollections):

```protobuf
  rpc HasCollection(HasCollectionReq) returns (HasCollectionResp);
```

Add after line 21 (after `rpc Info`):

```protobuf
  rpc GetByID(GetByIDReq) returns (GetByIDResp);
```

Add HasCollection messages after `ListCollectionsResp` message (after line 43):

```protobuf
message HasCollectionReq {
  string name = 1;
}

message HasCollectionResp {
  bool exists = 1;
}
```

Add GetByID messages after `SearchResult` message (after line 95):

```protobuf
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
  repeated float vector = 4;
}
```

- [ ] **Step 2: Regenerate Go protobuf stubs**

```bash
cd VectraDB && protoc --go_out=. --go-grpc_out=. proto/vectradb.proto
```

Expected: `proto/pb/vectradb.pb.go` and `proto/pb/vectradb_grpc.pb.go` regenerated.

- [ ] **Step 3: Verify Go compiles (expect error — handlers not yet implemented)**

```bash
cd VectraDB && go build ./... 2>&1 | head -5
```

Expected: compilation error — `Service` does not implement `HasCollection` and `GetByID`. Correct.

- [ ] **Step 4: Commit**

```bash
git add VectraDB/proto/vectradb.proto VectraDB/proto/pb/
git commit -m "proto: add GetByID and HasCollection RPCs"
```

---

## Task 2: Implement Go gRPC Handlers

**Files:**
- Modify: `VectraDB/internal/grpc/service.go`

- [ ] **Step 1: Add imports for gRPC status codes**

Update import block at top of `service.go`:

```go
import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/rupamthxt/vectradb/internal/store"
	"github.com/rupamthxt/vectradb/pkg/chunker"
	pb "github.com/rupamthxt/vectradb/proto/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)
```

- [ ] **Step 2: Add HasCollection handler**

Add after `ListCollections` method (after line 49):

```go
func (s *Service) HasCollection(_ context.Context, req *pb.HasCollectionReq) (*pb.HasCollectionResp, error) {
	return &pb.HasCollectionResp{Exists: s.collections.Has(req.Name)}, nil
}
```

- [ ] **Step 3: Add GetByID handler**

Add after `Delete` method (after line 114):

```go
func (s *Service) GetByID(_ context.Context, req *pb.GetByIDReq) (*pb.GetByIDResp, error) {
	if req.Collection == "" || len(req.Ids) == 0 {
		return &pb.GetByIDResp{}, nil
	}

	db, err := s.collections.Get(req.Collection)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "collection %q not found", req.Collection)
	}

	records := make([]*pb.RecordEntry, 0, len(req.Ids))
	for _, id := range req.Ids {
		_, meta, found := db.Get(id)
		entry := &pb.RecordEntry{Id: id, Found: found}
		if found && meta != nil {
			entry.MetadataJson = string(meta)
		}
		records = append(records, entry)
	}

	return &pb.GetByIDResp{Records: records}, nil
}
```

Note: `db.Get(id)` signature is `func (db *VectraDB) Get(id string) ([]float32, []byte, bool)` at `db.go:267`. We discard the vector (`_`) and use only metadata bytes.

- [ ] **Step 4: Fix Search error handling**

Replace the `Search` method (lines 116-141) with proper error propagation:

```go
func (s *Service) Search(_ context.Context, req *pb.SearchReq) (*pb.SearchResp, error) {
	if req.Collection == "" || len(req.Vector) == 0 {
		return nil, status.Error(codes.InvalidArgument, "collection and vector are required")
	}

	topK := int(req.TopK)
	if topK <= 0 {
		topK = 10
	}

	results, err := s.collections.Search(req.Collection, req.Vector, topK)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "collection %q: %v", req.Collection, err)
	}

	pbResults := make([]*pb.SearchResult, 0, len(results))
	for _, r := range results {
		pbResults = append(pbResults, &pb.SearchResult{
			Id:           r.ID,
			Score:        r.Score,
			MetadataJson: string(r.Data),
		})
	}

	return &pb.SearchResp{Results: pbResults}, nil
}
```

- [ ] **Step 5: Verify Go compiles**

```bash
cd VectraDB && go build ./...
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add VectraDB/internal/grpc/service.go
git commit -m "feat(grpc): add GetByID, HasCollection handlers; fix Search error propagation"
```

---

## Task 3: Go Tests for New RPCs

**Files:**
- Create: `VectraDB/internal/grpc/service_test.go`

- [ ] **Step 1: Check existing test patterns for Cluster initialization**

```bash
grep -r "NewCluster\|NewCollectionManager" VectraDB/internal/store/*_test.go | head -10
```

This reveals how tests create Cluster/CollectionManager. Use the same pattern.

- [ ] **Step 2: Write test file**

Create `VectraDB/internal/grpc/service_test.go`:

```go
package grpc_test

import (
	"context"
	"os"
	"testing"

	grpcSvc "github.com/rupamthxt/vectradb/internal/grpc"
	"github.com/rupamthxt/vectradb/internal/store"
	pb "github.com/rupamthxt/vectradb/proto/pb"
)

func setupService(t *testing.T) *grpcSvc.Service {
	t.Helper()
	dir, _ := os.MkdirTemp("", "grpc-test-*")
	t.Cleanup(func() { os.RemoveAll(dir) })

	// NewCollectionManager signature: (dim int, basePath string)
	cm, err := store.NewCollectionManager(4, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cm.Close() })

	// Pass nil for cluster — GetByID/HasCollection don't use it.
	// Note: Info() RPC cannot be tested without a real Cluster.
	return grpcSvc.NewService(cm, nil, 4)
}

func TestHasCollection(t *testing.T) {
	svc := setupService(t)
	ctx := context.Background()

	resp, err := svc.HasCollection(ctx, &pb.HasCollectionReq{Name: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Exists {
		t.Error("expected false for non-existent collection")
	}

	svc.CreateCollection(ctx, &pb.CreateCollectionReq{Name: "test"})
	resp, err = svc.HasCollection(ctx, &pb.HasCollectionReq{Name: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Exists {
		t.Error("expected true after creation")
	}

	svc.DropCollection(ctx, &pb.DropCollectionReq{Name: "test"})
	resp, err = svc.HasCollection(ctx, &pb.HasCollectionReq{Name: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Exists {
		t.Error("expected false after drop")
	}
}

func TestGetByID(t *testing.T) {
	svc := setupService(t)
	ctx := context.Background()

	svc.CreateCollection(ctx, &pb.CreateCollectionReq{Name: "books"})

	meta := `{"text":"hello world","chapter":1}`
	svc.Insert(ctx, &pb.InsertReq{
		Collection:   "books",
		Id:           "rec-1",
		Vector:       []float32{0.1, 0.2, 0.3, 0.4},
		MetadataJson: meta,
	})

	resp, err := svc.GetByID(ctx, &pb.GetByIDReq{
		Collection: "books",
		Ids:        []string{"rec-1", "rec-missing"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(resp.Records))
	}
	if !resp.Records[0].Found {
		t.Error("rec-1 should be found")
	}
	if resp.Records[0].MetadataJson != meta {
		t.Errorf("metadata mismatch: got %s", resp.Records[0].MetadataJson)
	}
	if resp.Records[1].Found {
		t.Error("rec-missing should not be found")
	}

	// Missing collection → gRPC NotFound error
	_, err = svc.GetByID(ctx, &pb.GetByIDReq{
		Collection: "nonexistent",
		Ids:        []string{"x"},
	})
	if err == nil {
		t.Error("expected error for missing collection")
	}
}

func TestSearchErrorHandling(t *testing.T) {
	svc := setupService(t)
	ctx := context.Background()

	_, err := svc.Search(ctx, &pb.SearchReq{
		Collection: "nonexistent",
		Vector:     []float32{0.1, 0.2, 0.3, 0.4},
		TopK:       5,
	})
	if err == nil {
		t.Error("expected error for missing collection")
	}

	_, err = svc.Search(ctx, &pb.SearchReq{
		Collection: "",
		Vector:     []float32{0.1, 0.2, 0.3, 0.4},
		TopK:       5,
	})
	if err == nil {
		t.Error("expected error for empty collection")
	}
}
```

- [ ] **Step 3: Run Go tests**

```bash
cd VectraDB && go test ./internal/grpc/ -v -count=1
```

Expected: 3 tests PASS. If `NewService(cm, nil, 4)` panics due to nil cluster, wrap it: create a minimal cluster or adjust `NewService` to accept nil gracefully.

- [ ] **Step 4: Commit**

```bash
git add VectraDB/internal/grpc/service_test.go
git commit -m "test(grpc): add TestHasCollection, TestGetByID, TestSearchErrorHandling"
```

---

## Task 4: Makefile Proto Generation

**Files:**
- Modify: `VectraDB/Makefile`
- Modify: `Makefile` (root)
- Create: `cognee/cognee/infrastructure/databases/vector/vectradb/generated/__init__.py`
- Create: `cognee/cognee/infrastructure/databases/vector/vectradb/generated/.gitignore`

- [ ] **Step 1: Add proto targets to VectraDB/Makefile**

Append to end of `VectraDB/Makefile`:

```makefile

# --- Proto Generation ---

.PHONY: proto-go proto-python proto

proto-go:
	@echo "Generating Go protobuf stubs..."
	@protoc --go_out=. --go-grpc_out=. proto/vectradb.proto
	@echo "Done."

proto-python:
	@echo "Generating Python gRPC stubs..."
	@mkdir -p ../cognee/cognee/infrastructure/databases/vector/vectradb/generated
	@python -m grpc_tools.protoc \
		-Iproto \
		--python_out=../cognee/cognee/infrastructure/databases/vector/vectradb/generated \
		--grpc_python_out=../cognee/cognee/infrastructure/databases/vector/vectradb/generated \
		proto/vectradb.proto
	@cp ../cognee/cognee/infrastructure/databases/vector/vectradb/generated/vectradb_pb2.py \
		../cognee-plugin/vectradb_adapter/generated/ 2>/dev/null || true
	@cp ../cognee/cognee/infrastructure/databases/vector/vectradb/generated/vectradb_pb2_grpc.py \
		../cognee-plugin/vectradb_adapter/generated/ 2>/dev/null || true
	@echo "Done."

proto: proto-go proto-python
```

- [ ] **Step 2: Add proto target to root Makefile**

Add after the `benchmark:` target (after line 53):

```makefile
# --- Proto Generation ---

proto:
	$(MAKE) -C VectraDB proto
```

- [ ] **Step 3: Create generated package directories**

Create `cognee/cognee/infrastructure/databases/vector/vectradb/generated/__init__.py` (empty file).

Create `cognee/cognee/infrastructure/databases/vector/vectradb/generated/.gitignore`:
```
vectradb_pb2.py
vectradb_pb2_grpc.py
```

Create `cognee-plugin/vectradb_adapter/generated/__init__.py` (empty file).

Create `cognee-plugin/vectradb_adapter/generated/.gitignore`:
```
vectradb_pb2.py
vectradb_pb2_grpc.py
```

- [ ] **Step 4: Run `make proto` and verify**

```bash
make proto
```

Expected: Go stubs regenerated, Python stubs in both locations.

```bash
ls cognee/cognee/infrastructure/databases/vector/vectradb/generated/vectradb_pb2*.py
ls cognee-plugin/vectradb_adapter/generated/vectradb_pb2*.py
```

Expected: two files in each location.

- [ ] **Step 5: Commit**

```bash
git add VectraDB/Makefile Makefile \
  cognee/cognee/infrastructure/databases/vector/vectradb/generated/__init__.py \
  cognee/cognee/infrastructure/databases/vector/vectradb/generated/.gitignore \
  cognee-plugin/vectradb_adapter/generated/__init__.py \
  cognee-plugin/vectradb_adapter/generated/.gitignore
git commit -m "build: add Makefile proto generation targets (Go + Python)"
```

---

## Task 5: Update Python Dependencies

**Files:**
- Modify: `cognee-plugin/pyproject.toml`

- [ ] **Step 1: Update dependencies (keep build-system section)**

Replace content of `cognee-plugin/pyproject.toml`:

```toml
[build-system]
requires = ["hatchling"]
build-backend = "hatchling.build"

[project]
name = "cognee-vectradb"
version = "0.2.0"
description = "VectraDB adapter plugin for Cognee — high-performance vector search via gRPC"
readme = "README.md"
license = "MIT"
requires-python = ">=3.10"
dependencies = [
    "grpcio>=1.60.0",
    "protobuf>=4.25.0",
]

[project.optional-dependencies]
dev = [
    "pytest>=7.0",
    "pytest-asyncio>=0.21",
    "grpcio-tools>=1.60.0",
]

[tool.hatch.build.targets.wheel]
packages = ["vectradb_adapter"]
```

- [ ] **Step 2: Install updated dependencies**

```bash
cd cognee-plugin && pip install -e ".[dev]"
```

Expected: grpcio, protobuf, grpcio-tools installed.

- [ ] **Step 3: Commit**

```bash
git add cognee-plugin/pyproject.toml
git commit -m "build(plugin): replace aiohttp/orjson with grpcio/protobuf"
```

---

## Task 6: Rewrite VectraDBAdapter (HTTP → gRPC)

**Files:**
- Modify: `cognee/cognee/infrastructure/databases/vector/vectradb/VectraDBAdapter.py` (primary — tests load from here)
- Modify: `cognee-plugin/vectradb_adapter/VectraDBAdapter.py` (sync copy)

- [ ] **Step 1: Write the new adapter in cognee tree**

Replace entire content of `cognee/cognee/infrastructure/databases/vector/vectradb/VectraDBAdapter.py`:

```python
"""
VectraDB adapter for Cognee's VectorDBInterface.

Uses gRPC transport to communicate with VectraDB Go server (port 50051).
Native collections, real delete (HNSW tombstone + WAL), binary protobuf encoding.
"""

import asyncio
import json
import logging
from typing import Any, Dict, List, Optional
from uuid import UUID

import grpc
import grpc.aio

from cognee.infrastructure.databases.exceptions import MissingQueryParameterError
from cognee.infrastructure.engine import DataPoint
from cognee.infrastructure.engine.utils import parse_id
from cognee.modules.storage.utils import get_own_properties

from ..embeddings.EmbeddingEngine import EmbeddingEngine
from ..models.ScoredResult import ScoredResult
from ..vector_db_interface import VectorDBInterface

from .generated import vectradb_pb2 as pb
from .generated import vectradb_pb2_grpc as pb_grpc

logger = logging.getLogger(__name__)


def _serialize_for_json(obj: Any) -> Any:
    """Convert obj to a JSON-serializable structure (handles UUID, Pydantic models)."""
    if isinstance(obj, (str, int, float, bool, type(None))):
        return obj
    if isinstance(obj, dict):
        return {k: _serialize_for_json(v) for k, v in obj.items()}
    if isinstance(obj, list):
        return [_serialize_for_json(item) for item in obj]
    if isinstance(obj, UUID):
        return str(obj)
    if hasattr(obj, "model_dump"):
        return _serialize_for_json(obj.model_dump())
    if hasattr(obj, "__dict__"):
        return _serialize_for_json(vars(obj))
    return str(obj)


class _IndexPoint(DataPoint):
    """Minimal DataPoint used for vector index entries."""

    id: str
    text: str
    metadata: dict = {"index_fields": ["text"]}
    belongs_to_set: List[str] = []


class VectraDBAdapter(VectorDBInterface):
    """
    Cognee VectorDBInterface adapter for VectraDB (gRPC, Go backend).

    Configuration:
        VECTOR_DB_PROVIDER=vectradb
        VECTOR_DB_URL=localhost:50051   (gRPC address, no http://)
    """

    name = "VectraDB"

    def __init__(
        self,
        url: Optional[str],
        api_key: Optional[str],
        embedding_engine: EmbeddingEngine,
        database_name: Optional[str] = None,
    ):
        raw_url = url or "localhost:50051"
        self.url = raw_url.replace("http://", "").replace("https://", "")
        self.embedding_engine = embedding_engine
        self._channel = grpc.aio.insecure_channel(self.url)
        self._stub = pb_grpc.VectraDBServiceStub(self._channel)
        self._embedding_cache: Dict[str, List[float]] = {}
        self._embedding_cache_maxsize = 4096

    # ------------------------------------------------------------------ helpers

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

    async def close(self) -> None:
        """Close the gRPC channel. Must be called on shutdown."""
        await self._channel.close()

    async def health_check(self) -> None:
        """Verify VectraDB is reachable and dimension matches embedding engine."""
        resp = await self._safe_call(self._stub.Info(pb.Empty()))
        server_dim = resp.dimension
        engine_dim = self.embedding_engine.get_vector_size()
        if server_dim and server_dim != engine_dim:
            raise RuntimeError(
                f"DIMENSION MISMATCH: VectraDB server dim={server_dim}, "
                f"embedding engine dim={engine_dim}. "
                f"Fix EMBEDDING_DIMENSIONS or VectraDB -dim flag."
            )
        logger.info("VectraDB dimension check OK: server=%d, engine=%d", server_dim, engine_dim)

    # ------------------------------------------------------------------ embed

    async def embed_data(self, data: List[str]) -> List[List[float]]:
        results: List[Optional[List[float]]] = [None] * len(data)
        uncached_texts: List[str] = []
        uncached_idx: List[int] = []
        for i, text in enumerate(data):
            if text in self._embedding_cache:
                results[i] = self._embedding_cache[text]
            else:
                uncached_texts.append(text)
                uncached_idx.append(i)
        if uncached_texts:
            vecs = await self.embedding_engine.embed_text(uncached_texts)
            for idx, text, vec in zip(uncached_idx, uncached_texts, vecs):
                if len(self._embedding_cache) >= self._embedding_cache_maxsize:
                    del self._embedding_cache[next(iter(self._embedding_cache))]
                self._embedding_cache[text] = vec
                results[idx] = vec
        return results

    # ------------------------------------------------------------------ collections

    async def has_collection(self, collection_name: str) -> bool:
        resp = await self._safe_call(
            self._stub.HasCollection(pb.HasCollectionReq(name=collection_name))
        )
        return resp.exists

    async def create_collection(self, collection_name: str, payload_schema: Any = None):
        resp = await self._safe_call(
            self._stub.CreateCollection(pb.CreateCollectionReq(name=collection_name))
        )
        if not resp.ok:
            raise RuntimeError(f"VectraDB CreateCollection failed: {resp.error}")

    # ------------------------------------------------------------------ data points

    async def create_data_points(self, collection_name: str, data_points: List[DataPoint]):
        texts = [DataPoint.get_embeddable_data(dp) for dp in data_points]
        vectors = await self.embed_data(texts)

        records = []
        for dp, vector in zip(data_points, vectors):
            properties = get_own_properties(dp)
            properties["id"] = str(properties["id"])
            serialized = _serialize_for_json(properties)
            records.append(pb.InsertRecord(
                id=str(dp.id),
                vector=vector,
                metadata_json=json.dumps(serialized, ensure_ascii=False),
            ))

        resp = await self._safe_call(
            self._stub.BatchInsert(pb.BatchInsertReq(
                collection=collection_name,
                records=records,
            ))
        )
        if resp.failed:
            raise RuntimeError(
                f"VectraDB batch insert partial failure: "
                f"{resp.failed} records failed. Errors: {list(resp.errors)}"
            )

    async def retrieve(self, collection_name: str, data_point_ids: List[str]) -> List[ScoredResult]:
        resp = await self._safe_call(
            self._stub.GetByID(pb.GetByIDReq(
                collection=collection_name,
                ids=[str(dp_id) for dp_id in data_point_ids],
            ))
        )
        results = []
        for record in resp.records:
            if record.found:
                payload = json.loads(record.metadata_json) if record.metadata_json else {}
                results.append(ScoredResult(
                    id=parse_id(record.id),
                    payload=payload,
                    score=0.0,
                ))
        return results

    # ------------------------------------------------------------------ search

    async def search(
        self,
        collection_name: str,
        query_text: Optional[str] = None,
        query_vector: Optional[List[float]] = None,
        limit: Optional[int] = 15,
        with_vector: bool = False,
        include_payload: bool = False,
        node_name: Optional[List[str]] = None,
    ) -> List[ScoredResult]:
        if query_text is None and query_vector is None:
            raise MissingQueryParameterError()

        if query_text and not query_vector:
            query_vector = (await self.embed_data([query_text]))[0]

        effective_limit = limit or 15
        fetch_k = effective_limit * 4 if node_name else effective_limit

        resp = await self._safe_call(
            self._stub.Search(pb.SearchReq(
                collection=collection_name,
                vector=query_vector,
                top_k=fetch_k,
            ))
        )

        results = list(resp.results)

        if node_name:
            node_name_set = set(node_name)
            filtered = []
            for r in results:
                meta = json.loads(r.metadata_json) if r.metadata_json else {}
                if any(x in node_name_set for x in meta.get("belongs_to_set", ())):
                    filtered.append(r)
            results = filtered

        results = results[:effective_limit]

        return [
            ScoredResult(
                id=parse_id(r.id),
                payload=json.loads(r.metadata_json) if include_payload and r.metadata_json else None,
                score=1.0 - float(r.score),
            )
            for r in results
        ]

    async def batch_search(
        self,
        collection_name: str,
        query_texts: List[str],
        limit: Optional[int] = None,
        with_vectors: bool = False,
        include_payload: bool = False,
        node_name: Optional[List[str]] = None,
    ) -> List[List[ScoredResult]]:
        query_vectors = await self.embed_data(query_texts)
        return await asyncio.gather(
            *[
                self.search(
                    collection_name=collection_name,
                    query_vector=qv,
                    limit=limit,
                    with_vector=with_vectors,
                    include_payload=include_payload,
                    node_name=node_name,
                )
                for qv in query_vectors
            ]
        )

    # ------------------------------------------------------------------ delete / prune

    async def delete_data_points(self, collection_name: str, data_point_ids: List[UUID]):
        ids = [str(dp_id) for dp_id in data_point_ids]
        await self._safe_call(
            self._stub.Delete(pb.DeleteReq(collection=collection_name, ids=ids))
        )

    async def prune(self):
        resp = await self._safe_call(self._stub.ListCollections(pb.Empty()))
        for col_name in resp.collections:
            await self._safe_call(
                self._stub.DropCollection(pb.DropCollectionReq(name=col_name))
            )
        self._embedding_cache.clear()

    # ------------------------------------------------------------------ indexing

    async def create_vector_index(self, index_name: str, index_property_name: str):
        collection_name = f"{index_name}_{index_property_name}"
        await self.create_collection(collection_name)

    async def index_data_points(
        self, index_name: str, index_property_name: str, data_points: List[DataPoint]
    ):
        collection_name = f"{index_name}_{index_property_name}"
        index_points = [
            _IndexPoint(
                id=str(dp.id),
                text=getattr(dp, dp.metadata["index_fields"][0]),
                belongs_to_set=(dp.belongs_to_set or []),
            )
            for dp in data_points
        ]
        await self.create_data_points(collection_name, index_points)
```

Note: `create_data_points` does NOT call `has_collection` + `create_collection` — the Go `BatchInsert` handler calls `collections.getOrCreate` internally, so the collection is auto-created on first insert. This avoids 2 unnecessary gRPC round-trips per batch.

- [ ] **Step 2: Copy to cognee-plugin (without health_check)**

Copy the file to `cognee-plugin/vectradb_adapter/VectraDBAdapter.py` and remove the `health_check` method (the plugin version doesn't have it). Adjust the relative import paths:

```python
# Change these imports in the cognee-plugin copy:
from ..embeddings.EmbeddingEngine import EmbeddingEngine
from ..models.ScoredResult import ScoredResult
from ..vector_db_interface import VectorDBInterface
```

These should match the current `cognee-plugin` import structure. Verify by checking `cognee-plugin/vectradb_adapter/__init__.py`.

- [ ] **Step 3: Verify syntax**

```bash
python -c "import ast; ast.parse(open('cognee/cognee/infrastructure/databases/vector/vectradb/VectraDBAdapter.py').read()); print('OK')"
```

Expected: `OK`

- [ ] **Step 4: Commit**

```bash
git add cognee/cognee/infrastructure/databases/vector/vectradb/VectraDBAdapter.py \
  cognee-plugin/vectradb_adapter/VectraDBAdapter.py
git commit -m "feat(adapter): rewrite VectraDBAdapter from HTTP/REST to gRPC"
```

---

## Task 7: Update conftest.py for gRPC Imports

**Files:**
- Modify: `tests/conftest.py`

The adapter now uses `from .generated import vectradb_pb2` (a relative import). When conftest.py loads the adapter via `importlib.util.spec_from_file_location`, relative imports fail. We must register the generated modules in `sys.modules` before loading the adapter.

- [ ] **Step 1: Update conftest.py adapter loading section**

Replace the VectraDB adapter loading block (lines 265-281) with:

```python
# ── Load VectraDBAdapter via importlib ────────────────────────────────────────
_vectradb_pkg = _stub("cognee.infrastructure.databases.vector.vectradb")

# Register the generated protobuf modules BEFORE loading the adapter,
# so that `from .generated import vectradb_pb2` resolves correctly.
_generated_dir = (
    _REPO_ROOT / "cognee" / "infrastructure" / "databases" / "vector"
    / "vectradb" / "generated"
)
_generated_pkg_name = "cognee.infrastructure.databases.vector.vectradb.generated"

# Register the generated package
_generated_pkg = _stub(_generated_pkg_name)

# Load vectradb_pb2
_pb2_path = _generated_dir / "vectradb_pb2.py"
if _pb2_path.exists():
    _pb2_spec = importlib.util.spec_from_file_location(
        f"{_generated_pkg_name}.vectradb_pb2", _pb2_path
    )
    _pb2_mod = importlib.util.module_from_spec(_pb2_spec)
    sys.modules[f"{_generated_pkg_name}.vectradb_pb2"] = _pb2_mod
    _pb2_spec.loader.exec_module(_pb2_mod)
    _generated_pkg.vectradb_pb2 = _pb2_mod
else:
    # If generated files don't exist (proto not built), create minimal stubs
    _pb2_mod = _stub(f"{_generated_pkg_name}.vectradb_pb2")
    _generated_pkg.vectradb_pb2 = _pb2_mod

# Load vectradb_pb2_grpc
_pb2_grpc_path = _generated_dir / "vectradb_pb2_grpc.py"
if _pb2_grpc_path.exists():
    _pb2_grpc_spec = importlib.util.spec_from_file_location(
        f"{_generated_pkg_name}.vectradb_pb2_grpc", _pb2_grpc_path
    )
    _pb2_grpc_mod = importlib.util.module_from_spec(_pb2_grpc_spec)
    sys.modules[f"{_generated_pkg_name}.vectradb_pb2_grpc"] = _pb2_grpc_mod
    _pb2_grpc_spec.loader.exec_module(_pb2_grpc_mod)
    _generated_pkg.vectradb_pb2_grpc = _pb2_grpc_mod
else:
    _pb2_grpc_mod = _stub(f"{_generated_pkg_name}.vectradb_pb2_grpc")
    _generated_pkg.vectradb_pb2_grpc = _pb2_grpc_mod

# Now load the adapter (its `from .generated import vectradb_pb2` will resolve)
_adapter_path = (
    _REPO_ROOT / "cognee" / "infrastructure" / "databases" / "vector"
    / "vectradb" / "VectraDBAdapter.py"
)
_spec = importlib.util.spec_from_file_location(
    "cognee.infrastructure.databases.vector.vectradb.VectraDBAdapter",
    _adapter_path,
)
_adapter_mod = importlib.util.module_from_spec(_spec)
sys.modules["cognee.infrastructure.databases.vector.vectradb.VectraDBAdapter"] = _adapter_mod
_spec.loader.exec_module(_adapter_mod)

_vectradb_pkg.VectraDBAdapter = _adapter_mod.VectraDBAdapter
```

- [ ] **Step 2: Verify conftest loads without errors**

```bash
cd tests && python -c "import conftest; print('OK')"
```

Expected: `OK` (requires `make proto` to have been run first).

- [ ] **Step 3: Commit**

```bash
git add tests/conftest.py
git commit -m "fix(tests): update conftest.py for gRPC adapter imports"
```

---

## Task 8: Rewrite Python Unit Tests

**Files:**
- Modify: `tests/test_vectradb_adapter.py`

- [ ] **Step 1: Write updated test file**

Replace entire content of `tests/test_vectradb_adapter.py`:

```python
"""
Unit tests for VectraDBAdapter (gRPC transport).

Stubs for the heavy cognee import chain are set up in conftest.py.
Generated protobuf modules are loaded by conftest.py from the generated/ directory.
"""

import asyncio
import json
import sys
import uuid
from unittest.mock import AsyncMock, MagicMock, patch

import grpc
import grpc.aio
import pytest

from cognee.infrastructure.databases.vector.vectradb.VectraDBAdapter import (
    VectraDBAdapter,
    _serialize_for_json,
)
from cognee.infrastructure.databases.exceptions import MissingQueryParameterError

# Load protobuf types from conftest-registered modules
pb = sys.modules["cognee.infrastructure.databases.vector.vectradb.generated.vectradb_pb2"]
pb_grpc = sys.modules["cognee.infrastructure.databases.vector.vectradb.generated.vectradb_pb2_grpc"]

_DataPoint = sys.modules["cognee.infrastructure.engine"].DataPoint
ScoredResult = sys.modules[
    "cognee.infrastructure.databases.vector.models.ScoredResult"
].ScoredResult


# ─── helpers ──────────────────────────────────────────────────────────────────

def _make_embedding_engine(dim: int = 4) -> MagicMock:
    engine = MagicMock()
    engine.embed_text = AsyncMock(return_value=[[0.1] * dim])
    engine.get_vector_size = MagicMock(return_value=dim)
    return engine


def _make_adapter() -> VectraDBAdapter:
    adapter = VectraDBAdapter(
        url="localhost:50051",
        api_key=None,
        embedding_engine=_make_embedding_engine(),
    )
    # Replace stub with mock for unit testing (no real gRPC server)
    adapter._stub = MagicMock()
    return adapter


class _FakeDataPoint(_DataPoint):
    def __init__(self, text: str = "hello", dp_id=None):
        self.id = dp_id or uuid.uuid4()
        self.text = text
        self.metadata = {"index_fields": ["text"]}
        self.belongs_to_set = []
        self.created_at = 0

    def model_dump(self):
        return {
            "id": self.id,
            "text": self.text,
            "belongs_to_set": self.belongs_to_set,
            "created_at": self.created_at,
        }


# ─── serialization ───────────────────────────────────────────────────────────

class TestSerializeForJson:
    def test_uuid_to_str(self):
        uid = uuid.uuid4()
        assert _serialize_for_json({"id": uid}) == {"id": str(uid)}

    def test_nested_uuid(self):
        uid = uuid.uuid4()
        result = _serialize_for_json({"outer": {"id": uid, "val": 42}})
        assert result["outer"]["id"] == str(uid)

    def test_list_with_uuid(self):
        uid = uuid.uuid4()
        result = _serialize_for_json([uid, "hello", 3])
        assert result == [str(uid), "hello", 3]

    def test_non_serializable_becomes_str(self):
        class WithAttr:
            pass
        result = _serialize_for_json({"w": WithAttr()})
        assert result["w"] == {}

        result2 = _serialize_for_json(int.__add__)
        assert isinstance(result2, str)

    def test_roundtrip_cognee_fields(self):
        dp_id = uuid.uuid4()
        payload = {
            "id": dp_id, "text": "sample",
            "belongs_to_set": ["set_a", "set_b"], "created_at": 1700000000000,
        }
        result = _serialize_for_json(payload)
        assert result["id"] == str(dp_id)
        json.dumps(result)


# ─── collections (gRPC) ─────────────────────────────────────────────────────

class TestCollections:
    @pytest.mark.asyncio
    async def test_has_collection_false(self):
        adapter = _make_adapter()
        adapter._stub.HasCollection = AsyncMock(
            return_value=pb.HasCollectionResp(exists=False)
        )
        assert await adapter.has_collection("col") is False

    @pytest.mark.asyncio
    async def test_has_collection_true_after_create(self):
        adapter = _make_adapter()
        adapter._stub.CreateCollection = AsyncMock(
            return_value=pb.StatusResp(ok=True)
        )
        adapter._stub.HasCollection = AsyncMock(
            return_value=pb.HasCollectionResp(exists=True)
        )
        await adapter.create_collection("col")
        assert await adapter.has_collection("col") is True


# ─── insert and retrieve (gRPC) ─────────────────────────────────────────────

class TestInsertAndRetrieve:
    @pytest.mark.asyncio
    async def test_insert_calls_batch_insert_rpc(self):
        adapter = _make_adapter()
        dp = _FakeDataPoint(text="hello")

        adapter._stub.BatchInsert = AsyncMock(
            return_value=pb.BatchInsertResp(inserted=1, failed=0)
        )
        await adapter.create_data_points("col", [dp])

        adapter._stub.BatchInsert.assert_called_once()
        req = adapter._stub.BatchInsert.call_args[0][0]
        assert req.collection == "col"
        assert len(req.records) == 1
        assert req.records[0].id == str(dp.id)
        assert len(req.records[0].vector) == 4

    @pytest.mark.asyncio
    async def test_retrieve_from_server(self):
        adapter = _make_adapter()
        uid = uuid.uuid4()
        meta = json.dumps({"text": "hi", "id": str(uid)})

        adapter._stub.GetByID = AsyncMock(
            return_value=pb.GetByIDResp(records=[
                pb.RecordEntry(id=str(uid), metadata_json=meta, found=True),
            ])
        )
        results = await adapter.retrieve("col", [str(uid)])
        assert len(results) == 1
        assert str(results[0].id) == str(uid)
        assert results[0].score == 0.0
        assert results[0].payload["text"] == "hi"

    @pytest.mark.asyncio
    async def test_retrieve_not_found_returns_empty(self):
        adapter = _make_adapter()
        adapter._stub.GetByID = AsyncMock(
            return_value=pb.GetByIDResp(records=[
                pb.RecordEntry(id="missing", metadata_json="", found=False),
            ])
        )
        results = await adapter.retrieve("col", ["missing"])
        assert results == []

    @pytest.mark.asyncio
    async def test_insert_retrieve_roundtrip(self):
        """After insert, retrieve returns the same payload."""
        adapter = _make_adapter()
        dp = _FakeDataPoint(text="sync", dp_id=uuid.uuid4())
        dp.belongs_to_set = ["grp_a"]

        adapter._stub.BatchInsert = AsyncMock(
            return_value=pb.BatchInsertResp(inserted=1, failed=0)
        )
        await adapter.create_data_points("col", [dp])

        # Capture what was sent
        req = adapter._stub.BatchInsert.call_args[0][0]
        sent_meta = req.records[0].metadata_json

        adapter._stub.GetByID = AsyncMock(
            return_value=pb.GetByIDResp(records=[
                pb.RecordEntry(id=str(dp.id), metadata_json=sent_meta, found=True),
            ])
        )
        results = await adapter.retrieve("col", [str(dp.id)])
        assert results[0].payload["belongs_to_set"] == ["grp_a"]


# ─── search (gRPC) ──────────────────────────────────────────────────────────

class TestSearch:
    @pytest.mark.asyncio
    async def test_search_returns_results(self):
        adapter = _make_adapter()
        uid = str(uuid.uuid4())
        adapter._stub.Search = AsyncMock(
            return_value=pb.SearchResp(results=[
                pb.SearchResult(id=uid, score=0.9, metadata_json='{"x":1}'),
            ])
        )
        results = await adapter.search("col", query_vector=[0.1]*4, limit=5)
        assert len(results) == 1
        assert str(results[0].id) == uid

    @pytest.mark.asyncio
    async def test_search_score_inverted(self):
        adapter = _make_adapter()
        uid = str(uuid.uuid4())
        adapter._stub.Search = AsyncMock(
            return_value=pb.SearchResp(results=[
                pb.SearchResult(id=uid, score=0.9, metadata_json="{}"),
            ])
        )
        results = await adapter.search("col", query_vector=[0.1]*4, limit=5)
        assert abs(results[0].score - 0.1) < 1e-6

    @pytest.mark.asyncio
    async def test_search_filters_by_node_name(self):
        adapter = _make_adapter()
        uid_a, uid_b = str(uuid.uuid4()), str(uuid.uuid4())
        adapter._stub.Search = AsyncMock(
            return_value=pb.SearchResp(results=[
                pb.SearchResult(id=uid_a, score=0.9,
                    metadata_json=json.dumps({"belongs_to_set": ["x"]})),
                pb.SearchResult(id=uid_b, score=0.8,
                    metadata_json=json.dumps({"belongs_to_set": ["y"]})),
            ])
        )
        results = await adapter.search("col", query_vector=[0.1]*4, limit=10, node_name=["x"])
        assert len(results) == 1
        assert str(results[0].id) == uid_a

    @pytest.mark.asyncio
    async def test_search_no_query_raises(self):
        adapter = _make_adapter()
        with pytest.raises(MissingQueryParameterError):
            await adapter.search("col", limit=5)

    @pytest.mark.asyncio
    async def test_search_payload_excluded(self):
        adapter = _make_adapter()
        uid = str(uuid.uuid4())
        adapter._stub.Search = AsyncMock(
            return_value=pb.SearchResp(results=[
                pb.SearchResult(id=uid, score=0.9, metadata_json='{"x":1}'),
            ])
        )
        results = await adapter.search("col", query_vector=[0.1]*4, limit=5, include_payload=False)
        assert results[0].payload is None

    @pytest.mark.asyncio
    async def test_search_payload_included(self):
        adapter = _make_adapter()
        uid = str(uuid.uuid4())
        adapter._stub.Search = AsyncMock(
            return_value=pb.SearchResp(results=[
                pb.SearchResult(id=uid, score=0.9, metadata_json='{"x":1}'),
            ])
        )
        results = await adapter.search("col", query_vector=[0.1]*4, limit=5, include_payload=True)
        assert results[0].payload == {"x": 1}


# ─── delete / prune (gRPC) ──────────────────────────────────────────────────

class TestDeletePrune:
    @pytest.mark.asyncio
    async def test_delete_calls_server(self):
        adapter = _make_adapter()
        uid = uuid.uuid4()
        adapter._stub.Delete = AsyncMock(
            return_value=pb.DeleteResp(deleted=1, failed=0)
        )
        await adapter.delete_data_points("col", [uid])
        adapter._stub.Delete.assert_called_once()
        req = adapter._stub.Delete.call_args[0][0]
        assert req.collection == "col"
        assert str(uid) in req.ids

    @pytest.mark.asyncio
    async def test_prune_drops_all_collections(self):
        adapter = _make_adapter()
        adapter._stub.ListCollections = AsyncMock(
            return_value=pb.ListCollectionsResp(collections=["a", "b"])
        )
        adapter._stub.DropCollection = AsyncMock(
            return_value=pb.StatusResp(ok=True)
        )
        await adapter.prune()
        assert adapter._stub.DropCollection.call_count == 2


# ─── error handling ─────────────────────────────────────────────────────────

class TestErrorHandling:
    @pytest.mark.asyncio
    async def test_connection_error_on_unavailable(self):
        """UNAVAILABLE gRPC status → ConnectionError."""
        adapter = _make_adapter()

        # Create a mock that raises the right exception type
        mock_error = MagicMock()
        mock_error.code.return_value = grpc.StatusCode.UNAVAILABLE
        mock_error.details.return_value = "server down"

        async def raise_unavailable(*args, **kwargs):
            raise grpc.aio.AioRpcError(
                code=grpc.StatusCode.UNAVAILABLE,
                initial_metadata=None,
                trailing_metadata=None,
                details="server down",
                debug_error_string=None,
            )

        adapter._stub.HasCollection = raise_unavailable
        with pytest.raises(ConnectionError, match="unavailable"):
            await adapter.has_collection("col")

    @pytest.mark.asyncio
    async def test_batch_insert_partial_failure_raises(self):
        adapter = _make_adapter()
        dp = _FakeDataPoint(text="fail")
        adapter._stub.BatchInsert = AsyncMock(
            return_value=pb.BatchInsertResp(inserted=0, failed=1, errors=["disk full"])
        )
        with pytest.raises(RuntimeError, match="partial failure"):
            await adapter.create_data_points("col", [dp])
```

Note: The `grpc.aio.AioRpcError` constructor may vary across grpcio versions. If the constructor fails, replace `raise_unavailable` with:
```python
async def raise_unavailable(*args, **kwargs):
    error = Exception("server down")
    error.code = lambda: grpc.StatusCode.UNAVAILABLE
    error.details = lambda: "server down"
    error.__class__ = grpc.aio.AioRpcError
    raise error
```

- [ ] **Step 2: Verify `import grpc` and `import grpc.aio` are in the imports**

Already included in the code above (lines after `import pytest`). Verify after pasting.

- [ ] **Step 3: Run tests**

```bash
cd tests && python -m pytest test_vectradb_adapter.py -v
```

Expected: All tests PASS. If import errors occur, check:
1. `make proto` was run (generated files exist)
2. conftest.py properly registers generated modules
3. `grpcio` is installed

- [ ] **Step 4: Commit**

```bash
git add tests/test_vectradb_adapter.py
git commit -m "test(adapter): rewrite unit tests for gRPC transport"
```

---

## Task 9: Integration Smoke Test

**Files:**
- No new files

- [ ] **Step 1: Rebuild VectraDB with new proto**

```bash
docker compose down && docker compose up -d --build
```

Wait for startup.

- [ ] **Step 2: Verify gRPC port is open**

```bash
python -c "
import grpc
channel = grpc.insecure_channel('localhost:50051')
try:
    grpc.channel_ready_future(channel).result(timeout=5)
    print('gRPC OK')
except:
    print('gRPC FAILED')
channel.close()
"
```

Expected: `gRPC OK`

- [ ] **Step 3: Run full CRUD cycle**

```bash
python -c "
import asyncio, grpc.aio, sys
sys.path.insert(0, 'cognee/cognee/infrastructure/databases/vector/vectradb/generated')
import vectradb_pb2 as pb, vectradb_pb2_grpc as pb_grpc

async def smoke():
    channel = grpc.aio.insecure_channel('localhost:50051')
    stub = pb_grpc.VectraDBServiceStub(channel)

    r = await stub.CreateCollection(pb.CreateCollectionReq(name='smoke'))
    print(f'Create: ok={r.ok}')

    r = await stub.HasCollection(pb.HasCollectionReq(name='smoke'))
    print(f'Has: exists={r.exists}')

    r = await stub.BatchInsert(pb.BatchInsertReq(
        collection='smoke',
        records=[pb.InsertRecord(id='r1', vector=[0.1]*1024, metadata_json='{\"text\":\"hello\"}')]
    ))
    print(f'Insert: inserted={r.inserted}')

    r = await stub.Search(pb.SearchReq(collection='smoke', vector=[0.1]*1024, top_k=5))
    print(f'Search: {len(r.results)} results')

    r = await stub.GetByID(pb.GetByIDReq(collection='smoke', ids=['r1', 'miss']))
    print(f'GetByID: found={[x.found for x in r.records]}')

    r = await stub.Delete(pb.DeleteReq(collection='smoke', ids=['r1']))
    print(f'Delete: deleted={r.deleted}')

    await stub.DropCollection(pb.DropCollectionReq(name='smoke'))
    await channel.close()

asyncio.run(smoke())
"
```

Expected:
```
Create: ok=True
Has: exists=True
Insert: inserted=1
Search: 1 results
GetByID: found=[True, False]
Delete: deleted=1
```

- [ ] **Step 4: Commit any fixes**

```bash
git add -A && git commit -m "fix: integration smoke test fixes" || echo "nothing to commit"
```

---

## Summary

| Task | Description | Steps |
|------|-------------|-------|
| 1 | Proto: +GetByID, +HasCollection | 4 |
| 2 | Go handlers + Search error fix | 6 |
| 3 | Go tests for new RPCs | 4 |
| 4 | Makefile proto generation | 5 |
| 5 | Python dependencies | 3 |
| 6 | Rewrite VectraDBAdapter (both copies) | 4 |
| 7 | Update conftest.py for gRPC imports | 3 |
| 8 | Rewrite Python unit tests | 4 |
| 9 | Integration smoke test | 4 |
| **Total** | | **37 steps** |

Execution order: strictly sequential 1→2→3→4→5→6→7→8→9.
Tasks 1-3: Go only. Task 4-5: build/deps. Tasks 6-9: Python.

## Deferred: test_vectradb_integration.py

The spec mentions updating `test_vectradb_integration.py` (MockVectraServer → gRPC). This is deferred to a follow-up task because:
1. The integration tests use a MockVectraServer (in-process HTTP mock) that is tightly coupled to HTTP transport
2. Replacing it requires either a mock gRPC server or running against the real Go server
3. The unit tests (Task 8) + smoke test (Task 9) provide sufficient coverage for the adapter rewrite
4. Integration test updates can be done independently after the adapter is stable
