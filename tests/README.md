# Test Suite

## Quick Start

```bash
# Unit tests (no external services needed)
pytest tests/test_vectradb_adapter.py tests/test_vectradb_integration.py -v

# Full suite (requires VectraDB + embed-server)
docker compose up -d --build
pytest tests/ -v
```

## Test Categories

| Category | Files | Requirements |
|----------|-------|-------------|
| Unit (gRPC mocks) | test_vectradb_adapter.py | None |
| Integration (GrpcMockServer) | test_vectradb_integration.py, test_head_to_head.py | None |
| Dimension validation | test_dimension_validation_*.py | None |
| Book benchmark | test_book_search_benchmark.py | None |
| Real server | test_real_server.py | VectraDB (docker) |
| Comprehensive | test_comprehensive_comparison.py | VectraDB + embed-server |
| Book head-to-head | test_book_head_to_head.py | VectraDB + embed-server |
| RAG cases | test_rag_cases.py | VectraDB + embed-server |
| RAG LLM | test_rag_llm_cases.py | VectraDB + embed-server + Ollama |

## External Services

| Service | Port | Start command |
|---------|------|--------------|
| VectraDB | 8080 (HTTP), 50051 (gRPC) | `docker compose up -d --build` |
| embed-server | 9001 | pplx-embed-context-v1-0.6b (GPU) |
| Ollama | 11434 | `ollama serve` + `ollama pull qwen3.5` |

## Test Architecture

Tests use `conftest.py` to stub Cognee dependencies via `sys.modules` injection.
The VectraDB adapter is loaded via `importlib.util.spec_from_file_location()`.
Generated gRPC protobuf modules are registered before adapter loading.

### GrpcMockServer Pattern

Integration tests use an in-process `GrpcMockServer` class that implements
the same async interface as the real gRPC stub. Wire it via:

```python
adapter._stub = GrpcMockServer()
```

This allows testing all adapter logic without a running VectraDB server.
