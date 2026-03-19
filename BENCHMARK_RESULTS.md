# VectraDB vs LanceDB: Benchmark Results

## Test Environment

- **Hardware**: NVIDIA RTX 3090 (24GB), Intel i7-7700 @ 3.60GHz, Linux 6.8
- **Embedding**: pplx-embed-context-v1-0.6b (dim=1024, FP16/BF16, CUDA)
- **VectraDB**: Go HNSW + WAL, standalone mode, 3 shards, Docker, gRPC :50051
- **LanceDB**: Rust/Python Arrow, in-process, IVF/PQ
- **Data**: book "Ураган" (Janet Edwards), 1430 chunks, 600 chars max
- **LLM**: Qwen 3.5 9.7B (Q4_K_M) via Ollama
- **Tests**: 174 total (127 passed, 47 skipped)

## Current Results (2026-03-19, gRPC + all optimizations)

### Search (VectraDB wins decisively)

| Metric | VectraDB | LanceDB | Delta |
|--------|----------|---------|-------|
| Latency p50 (1.4K vecs) | **2.6 ms** | 12.9 ms | **4.9x** |
| Latency mean (1.4K vecs) | **2.7 ms** | 13.2 ms | **4.9x** |
| Latency p50 (10K vecs) | **2.7 ms** | 18.7 ms | **6.9x** |
| Concurrent QPS | **589** | 109 | **5.4x** |
| Latency growth 1.4K→10K | **+3.7%** | +45% | stable |

### Write (LanceDB wins on throughput)

| Metric | VectraDB | LanceDB | Delta |
|--------|----------|---------|-------|
| Insert dp/s (1.4K) | 591 | **3,911** | 6.6x |
| Insert dp/s (10K) | 697 | **5,226** | 7.5x |

### Quality (real embeddings, GPU)

| Metric | VectraDB | LanceDB |
|--------|----------|---------|
| Keyword hit rate | **100%** | 100% |
| Crash recovery | **100%** | N/A (in-process) |
| Concurrent R+W overhead | +25.8% | N/A |
| Collection isolation | 5% leakage (HTTP prefix) | 0% (native tables) |

### RAG Pipeline (Qwen 3.5)

| Metric | Result |
|--------|--------|
| Grounded answers | 60% (3/5) |
| Hallucination refusal | 100% (5/5) |
| Prompt injection safety | 100% (5/5) |
| Faithfulness (LLM judge) | 10.0/10 |
| Relevancy (LLM judge) | 9.8/10 |

---

## Optimization History

### Phase 1: Python HTTP adapter (baseline, 2026-03-15)

| Metric | VectraDB (HTTP) | LanceDB | Delta |
|--------|----------------|---------|-------|
| Search p50 | 2.5 ms | 8.4 ms | 3.4x |
| Search mean | 2.6 ms | 9.1 ms | 3.5x |
| Concurrent QPS | 719 | 150 | 4.8x |
| Insert dp/s | 741 | 5,067 | **LanceDB 6.8x** |
| Keyword hit rate | 93% | 100% | — |

### Phase 2: gRPC adapter (2026-03-18)

Replaced HTTP/JSON with gRPC/Protobuf. Added GetByID, HasCollection RPCs.

| Transport | Search latency | vs HTTP |
|-----------|---------------|---------|
| Python + HTTP + JSON | 2.6 ms | baseline |
| Go gRPC (cross-process) | 0.31 ms | **8.4x faster** |
| Go in-process (library) | 0.10 ms | **26x faster** |

### Phase 3: Performance optimizations (2026-03-18)

| Optimization | What changed | Measured gain |
|-------------|-------------|--------------|
| JSON marshal before lock | `json.Marshal` moved out of `db.mu` critical section | -3-15ms lock hold time |
| WAL group commit | `fsyncLoop` goroutine coalesces fsyncs | **12.5x fsync reduction** (50 entries → 4 fsyncs) |
| SIMD distance (vek32 AVX2) | `dist()` via AVX2 dot product | **8.1x per call** (557ns → 69ns) |
| HNSW M=20, efMult=10 | Denser graph, wider beam search | Recall 0.93 → **0.994** |
| Embed concurrent batching | `errgroup` semaphore for parallel HTTP | 2-3x batch embed throughput |
| Configurable HNSW params | `--hnsw-m`, `--hnsw-ef-mult`, `--hnsw-ef-min` CLI flags | Tunable without recompile |

### Performance evolution

| Metric | HTTP baseline | gRPC only | + all opts (clean 1K) | Real book (1.4K GPU) |
|--------|-------------|-----------|----------------------|---------------------|
| Search p50 | 2.5ms | 3.3ms* | **1.20ms** | **2.6ms** |
| Concurrent QPS | 719 | 2,524 | **2,756** | **589** |
| Insert dp/s | 741 | 6,264 | **3,976** | 591 |
| Recall@10 | 0.93 | 0.956 | **0.994** | — |

*On loaded server (60K records from previous tests)

---

## Architecture Analysis

### Why VectraDB is faster on search

1. **HNSW graph traversal**: O(log N) average, locality-friendly memory access
2. **SIMD dot product**: AVX2 processes 8 float32s per instruction (8.1x vs scalar)
3. **Go goroutines**: True parallel search, no GIL. RWMutex allows concurrent readers
4. **gRPC binary encoding**: Protobuf 4KB/vector vs JSON 15KB/vector, zero parsing overhead

### Why LanceDB is faster on writes

1. **In-process**: 0ms transport (VectraDB pays gRPC round-trip ~0.3ms)
2. **No fsync**: Arrow files are append-only, no durability guarantee
3. **Columnar batch ops**: Arrow vectorized writes, entire batch in one operation
4. **Lock-free snapshots**: Immutable files, no mutex contention

### VectraDB write bottleneck breakdown

| Bottleneck | Latency | % of write time | Status |
|-----------|---------|----------------|--------|
| WAL fsync | 10-30ms/batch | 40-60% | **Mitigated: group commit (12.5x coalescing)** |
| db.mu lock scope | 5-15ms | 20-30% | **Mitigated: JSON marshal outside lock** |
| gRPC round-trip | 0.3ms | 2-3% | Architectural (microservice tax) |
| Arena + disk I/O | 1-5ms | 10-15% | Acceptable |

---

## When to Use Which

### VectraDB — best for read-heavy production workloads

| Scenario | Why VectraDB | Key metric |
|----------|-------------|------------|
| Real-time RAG API (100+ users) | 5.4x higher QPS, Go goroutines | 589 QPS |
| Latency-critical (SLA < 10ms) | p50 = 2.6ms, p95 = 3.5ms | 4.9x faster |
| Microservice architecture (K8s) | Shared service, gRPC API, Prometheus metrics | Horizontal scale |
| Large-scale (10K+ vectors) | Latency stable (+3.7% vs +45%) | 6.9x at 10K |
| Durability required | WAL + fsync, 100% crash recovery | 100% recovery |

**Ideal profile**: read:write ratio > 100:1, concurrent users, strict SLA.

### LanceDB — best for write-heavy and simple deployments

| Scenario | Why LanceDB | Key metric |
|----------|------------|------------|
| Batch ingestion (100K+ docs) | 6.6x write throughput | 3,911 dp/s |
| Frequent updates (catalog) | Native delete + prune | O(1) delete |
| Local dev / prototyping | pip install, no Docker | Zero-ops |
| Exact search quality | IVF/PQ gives NDCG=1.0 | Perfect recall |
| Single-process pipeline | In-process, 0ms transport | No network |

**Ideal profile**: batch updates, single-process, dev/prototype, CRUD.

## Decision Matrix

```
                    Write-heavy              Read-heavy
                    (ETL, CRUD)              (API, chatbot)
                  +------------------+---------------------+
  Simple deploy   |    LanceDB       |    LanceDB          |
  (single node)   |    (best fit)    |    (good enough)    |
                  +------------------+---------------------+
  Production      |    LanceDB       |    VectraDB         |
  (microservice)  |    (write wins)  |    (best fit)       |
                  +------------------+---------------------+
```

---

## Go Rewrite Components

### Transport

| Transport | Latency | vs Python HTTP |
|-----------|---------|----------------|
| Python + HTTP + JSON | 2.6 ms | baseline |
| Go gRPC (cross-process) | 0.31 ms | **8.4x faster** |
| Go in-process (library call) | 0.10 ms | **26x faster** |

### SIMD Distance (dim=1024, i7-7700 AVX2)

| Implementation | ns/op | Speedup |
|---------------|-------|---------|
| Scalar (4-way unrolled) | 557 | baseline |
| vek32 AVX2 | **69** | **8.1x** |

### Go Chunking (book "Ураган", 1430 chunks)

| Language | Time | Speedup |
|----------|------|---------|
| Python | 50-200 ms | baseline |
| Go | 7.4 ms | **7-27x faster** |

### Component Status

| Component | Files | Tests | Status |
|-----------|-------|-------|--------|
| CollectionManager | `internal/store/collections.go` | 6 | native collections, WAL persistence |
| Delete-by-ID | `internal/store/db.go` | 3 | HNSW tombstone + WAL OpDelete |
| gRPC Server | `internal/grpc/service.go` | 7 | :50051, CRUD + search + chunking + GetByID + HasCollection |
| WAL Group Commit | `internal/store/wal.go` | 4 | fsyncLoop, FlushAsync, 12.5x coalescing |
| SIMD Distance | `internal/store/hnsw.go` | 2 | vek32 AVX2, 8.1x per call |
| HNSW Config | `internal/store/hnsw.go` | — | CLI flags, runtime tunable |
| Text Chunker | `pkg/chunker/` | 5 | paragraph, sentence, merged strategies |
| Embed Client | `pkg/embed/client.go` | 1 | concurrent batching, errgroup |
| Search Pipeline | `pipeline/search.go` | 3 | embed → in-process search |
| Python gRPC Adapter | `VectraDBAdapter.py` | 21 | all 9 VectorDBInterface methods |
| **Total** | **15+ files** | **52 Go + 96 Python** | **all passing** |

### What Remains Python (LLM-bound, no benefit from Go)

- LLM graph extraction (50-300ms/chunk — GPU-bound)
- LLM structured output (instructor + litellm — no Go equivalent)
- Document parsing (unstructured library — 20+ formats)

## Coverage: cases.md

20/20 test cases covered:
- 9 previously covered (adapter, integration, benchmark tests)
- 6 added in test_rag_cases.py (multi-hop, noise, needle-in-haystack, multilingual, typo, chunking)
- 5 added in test_rag_llm_cases.py (grounded, hallucination, relevancy, safety, LLM-as-judge)
