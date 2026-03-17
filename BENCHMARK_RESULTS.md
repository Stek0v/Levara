# VectraDB vs LanceDB: Benchmark Results

## Test Environment

- **Hardware**: NVIDIA RTX 3090, Linux 6.8
- **Embedding**: pplx-embed-context-v1-0.6b (dim=1024, FP16, CUDA)
- **VectraDB**: Go HNSW + WAL, standalone mode, 3 shards, Docker
- **LanceDB**: Rust/Python Arrow, in-process, IVF/PQ
- **Data**: book "Ураган" (Janet Edwards), 1430 chunks, 600 chars max
- **LLM**: Qwen 3.5 9.7B (Q4_K_M) via Ollama
- **Tests**: 143 total, all passed

## Speed Comparison

### Search (VectraDB wins)

| Metric | VectraDB | LanceDB | Delta |
|--------|----------|---------|-------|
| Latency p50 | **2.5 ms** | 8.4 ms | 3.4x |
| Latency mean | **2.6 ms** | 9.1 ms | 3.5x |
| Concurrent QPS | **719** | 150 | 4.8x |
| Scale 10K latency | **2.6 ms** | 16.4 ms | 6.3x |
| Latency growth 1.4K->10K | **-2.9%** | +80% | stable |

### Write (LanceDB wins)

| Metric | VectraDB | LanceDB | Delta |
|--------|----------|---------|-------|
| Insert dp/s | 741 | **5,067** | 6.8x |
| Insert at 10K | 809 | **6,376** | 7.9x |

### Quality

| Metric | VectraDB | LanceDB |
|--------|----------|---------|
| Keyword hit rate | 93% | 100% |
| Crash recovery | 100% | N/A (in-process) |
| Collection isolation | 0% leakage | 0% leakage |
| Concurrent R+W overhead | +34% | N/A |

### RAG Pipeline (Qwen 3.5)

| Metric | Result |
|--------|--------|
| Grounded answers | 60% (3/5) |
| Hallucination refusal | 100% (5/5) |
| Prompt injection safety | 100% (5/5) |
| Faithfulness (LLM judge) | 10.0/10 |
| Relevancy (LLM judge) | 9.8/10 |

## VectraDB Write Bottlenecks

Root cause of 6.8x write gap (by latency contribution):

1. **WAL fsync** (~10-30ms/batch) -- `wal.go:115 file.Sync()`, one syscall per shard per batch. Cost of durability.
2. **db.mu lock scope** (~20-70ms) -- JSON marshal + arena.Add + disk.Write all under single mutex in `db.go:199-236`.
3. **HTTP round-trip** (~2-5ms/batch) -- microservice architecture tax, LanceDB is in-process (0ms).

These are architectural trade-offs, not bugs. VectraDB pays for durability (WAL fsync) and decoupled deployment (HTTP).

### Possible optimizations (not implemented)

| Optimization | Potential gain | Complexity |
|-------------|---------------|------------|
| JSON marshal before lock | -3-15ms off critical section | Low |
| Group commit (batch fsyncs) | -50-80% fsync cost | Medium |
| FNV hasher reuse (Reset) | -1-2ms per batch | Trivial |
| Lock-free arena (atomic CAS) | -2-5ms per batch | High |

Even with all optimizations, VectraDB will not match LanceDB write speed due to inherent HTTP + fsync overhead.

## When to Use Which

### VectraDB (Cognee) -- best for read-heavy workloads

| Scenario | Why VectraDB | Key metric |
|----------|-------------|------------|
| Real-time RAG API (100+ users) | 4.8x higher QPS, Go goroutines handle concurrency | 719 QPS |
| Latency-critical pipeline (SLA < 10ms) | p95 = 3.5ms, leaves budget for LLM | 3.5x faster |
| Microservice architecture (K8s) | Shared service for N consumers, one instance | Horizontal scale |
| Large-scale search (10K+ vectors) | Latency stable at scale (-2.9% vs +80%) | 6.3x at 10K |
| Durability required (crash recovery) | WAL + fsync, 100% recovery after restart | 100% recovery |

**Ideal profile**: read:write ratio > 100:1, concurrent users, strict SLA.

### LanceDB -- best for write-heavy and simple deployments

| Scenario | Why LanceDB | Key metric |
|----------|------------|------------|
| Batch ingestion (100K+ docs) | 6.8x write throughput, no fsync overhead | 5,067 dp/s |
| Frequent updates (e-commerce catalog) | Native delete + prune, no garbage accumulation | O(1) delete |
| Local dev / prototyping | pip install, no Docker, no infrastructure | Zero-ops |
| Exact search quality needed | IVF/PQ gives NDCG=1.0 vs HNSW approximate | Perfect recall |
| Single-process embedding pipeline | In-process, zero network overhead | 0ms transport |

**Ideal profile**: batch updates, single-process, dev/prototype, CRUD operations.

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

## Coverage: cases.md

20/20 test cases covered:
- 9 previously covered (adapter, integration, benchmark tests)
- 6 added in test_rag_cases.py (multi-hop, noise, needle-in-haystack, multilingual, typo, chunking)
- 5 added in test_rag_llm_cases.py (grounded, hallucination, relevancy, safety, LLM-as-judge)
