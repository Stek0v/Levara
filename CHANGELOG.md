# Changelog

## [Unreleased]

### 2026-03-19
- docs: rebrand all documentation (VectraDB → Cognevra)
- docs: add "What is Cognevra" sections explaining product identity
- docs: add 100K scale test results
- docs: update BENCHMARK_RESULTS.md with all optimization data

### 2026-03-18 — gRPC Adapter + Performance Optimizations
- feat: Python gRPC adapter replacing HTTP/REST (8.4x transport speedup)
- feat(grpc): add GetByID, HasCollection RPCs
- feat(hnsw): configurable parameters via CLI flags
- perf(store): move JSON marshal outside db.mu lock
- perf(wal): WAL group commit (12.5x fsync coalescing)
- perf(embed): concurrent batch embedding with errgroup
- perf(hnsw): SIMD distance via vek32 AVX2 (8.1x faster)
- perf(hnsw): tune M=20, efMult=10 (recall 0.93→0.994)
- fix(tests): rewrite all test files for gRPC adapter
- fix(docker): expose gRPC port 50051

### 2026-03-17 — Go Rewrite
- feat: Go embedding client + search pipeline (26x faster in-process)
- feat(chunker): Go text chunker with Python parity (7-27x faster)
- feat(cognevra): gRPC server with collection-aware API
- feat(cognevra): native collections, delete-by-ID, WAL delete recovery

### 2026-03-15 — Initial Benchmarks (VectraDB era)
- perf(vectradb): async HNSW indexing
- perf(vectradb): standalone WAL-only mode
- fix(vectradb): cosine distance, WAL recovery, data persistence
- test: comprehensive Cognevra vs LanceDB benchmark
