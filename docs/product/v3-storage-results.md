# V3 storage evaluation results

Status: reproducible isolated evidence and a reviewed default-off implementation
on branch `v3`. All startup figures are process-cold; host-cold validation is
still required before rollout.

## Safety boundary

- Fixtures are deterministic synthetic WALs under capability-scoped temporary
  roots. Production native collections were never opened by a benchmark.
- The harness refuses a run that would exceed 4 GiB or leave less than 12 GiB
  free. That rule initially stopped the 50k S0 run; after moving reproducible
  model caches and APFS space reclamation, the guarded local 100k run fit.
- S1/S2 exact snapshot and ordered-tail loading are implemented behind the
  default-off `-hnsw-snapshots` flag. The S3 lazy prototype remains available
  only with the `v3bench` build tag.
- `TestMemoryREST` and its prefixes were not run.

## Startup and search measurements

Configuration unless stated otherwise: 768 dimensions, `M=16`, `M0=32`, ten
queries, top 10, three fresh child processes. The synthetic oracle contains 100
relevant result slots per run. Historical rows use the legacy
`EfConstruction=0` behavior, whose construction candidate width is `M`/`M0`.

| Records | Mode | HNSW search ef | Open/readiness | Load | Recall@10 | Peak RSS | Snapshot |
|---:|---|---:|---:|---:|---:|---:|---:|
| 1k | S0 WAL rebuild | 60 | 1.68–1.73 s | same | 0.93 | 38–39 MB | — |
| 1k | S1 exact snapshot | 60 | 26.8–33.6 ms | same | 0.94 | — | 257 KB |
| 1k, 256d | S0 WAL rebuild | 48 | 0.75–0.80 s | same | 0.96 | 36–37 MiB | — |
| 1k, 256d | S1 exact snapshot | 48 | 36.0–42.2 ms | same | 0.96 | 46 MiB | 251 KB |
| 10k | S0 WAL rebuild | 60 | 25.18–25.70 s | same | 0.29 | 162 MB | — |
| 10k | S1 exact snapshot | 400 | 267–302 ms | same | 0.90 | 254–255 MB | 2.99 MB |
| 10k | S1 exact snapshot | 500 | 264–316 ms | same | 0.95 | 254–255 MB | 2.99 MB |
| 10k | S1 exact snapshot | 1000 | 273–305 ms | same | 0.98 | 253–255 MB | 2.99 MB |
| 10k | S2 snapshot + one WAL-tail insert | 500 | 279–296 ms | same | 0.95 | 254–255 MB | 2.99 MB |
| 10k | S3 lazy exact snapshot | 500 | 0.015–0.019 ms | 265–285 ms | 0.95 | 253–255 MB | 2.99 MB |
| 50k | S1 exact snapshot | 1000 | 1.929 s, one run | same | 0.65 | 973 MB | 16.83 MB |
| 100k | S0 WAL rebuild | 48 | 298.41 s, one run | same | 0.10 | 833 MiB | — |
| 1k across 326 dirs | S0 WAL rebuild | 48 | 1.57–1.78 s | same | 1.00 | 82–96 MiB | — |

The 50k result failed the quality gate and therefore stopped after one child.
It remains valid startup, memory, disk, and negative-quality evidence. Raising
only `efSearch` does not repair the default `M=16` graph at that scale.

An intentionally aggressive 10k probe with `M=32`, `M0=64`, and `ef=1000`
reached Recall@10 1.00. It enlarged the snapshot to 5.34 MB and took 83.7 s for
fixture plus graph construction. It is a quality ceiling, not a selected
production configuration.

At 10k, `M=16` exposes the useful search boundary directly:

| ef | Recall@10 | Search p50 |
|---:|---:|---:|
| 250 | 0.76 | 5.35 ms |
| 400 | 0.90 | 7.22–11.29 ms |
| 500 | 0.95 | 7.89–7.92 ms in the stable runs |
| 1000 | 0.98 | 10.25–10.61 ms in the stable runs |

One ef=500 and one ef=400 child showed scheduler outliers. The result digest
and recall stayed exact; query latency needs longer sampling before an SLO is
set.

### Linux `base` scale runs

These runs used the same deterministic harness on Linux/amd64 with about 61
GiB RAM and a disposable source/data root. They are kept separate from the Mac
table because CPU architecture, Go toolchain, page cache, and storage differ.

| Records | HNSW | Construction | Search ef | Open | Recall@10 | Search p50/p95 | Peak RSS | Snapshot |
|---:|---|---:|---:|---:|---:|---:|---:|---:|
| 50k | M32/M064 | legacy (`0`) | 1000 | 5.72–5.83 s | 0.93 | 14.5–14.9 / 16.6–16.9 ms | 1.89 GiB | 29.18 MiB |
| 100k | M32/M064 | legacy (`0`) | 1000 | 12.21 s, one run | 0.71 | 23.34 / 31.29 ms | 3.77 GiB | 59.66 MiB |
| 50k | M16/M032 | 200 | 1000 | 4.84 s, one run | 0.78 | 9.63 ms p50 | 1.81 GiB | 16.37 MiB |
| 100k | M32/M064 | 200 | 1000 | 12.29 s, one run | 0.78 | 20.95 / 23.40 ms | 3.73 GiB | 60.28 MiB |
| 100k | M32/M064 | 400 | 1000 | 12.04 s, one run | 0.85 | 21.57 / 22.98 ms | 3.71 GiB | 60.49 MiB |
| 100k | M32/M064 | 800 | 1000 | 12.18 s, one run | 0.85 | 25.04 / 39.05 ms | 3.69 GiB | 60.60 MiB |

The failed 100k legacy row proves that increasing graph degree and query breadth
alone does not preserve quality as the corpus grows. The failed 50k
`EfConstruction=200` row also shows that wider construction cannot compensate
for an undersized M16/M032 graph at this scale. At 100k, construction breadth
200 improves M32/M064 recall by seven points, and breadth 400 adds another
seven points. Breadth 800 took 34.4 minutes to build the fixture and graph yet
remained at Recall@10 0.85. Full-message HNSW construction is therefore rejected
at this scale; no wider construction probe is justified.

## Scale observations and extrapolation

The earlier Mac S1 observations at 10k and 50k fit `t = a * n^1.18`; their 100k
planning point was about 4.4 seconds with a chosen ±40% band. It was not a
statistical error bound and described a quality-failing M16 graph. A Linux
M32 snapshot later measured about 12 seconds at 100k; cross-host extrapolation
is therefore not used as rollout evidence.

Snapshot size is approximately 300–337 bytes per record for `M=16`, giving a
30–34 MB 100k estimate and roughly 270–303 MB at 900k. Peak RSS increased from
about 254 MB at 10k to 973 MB at 50k; a linear fit estimates about 1.87 GB at
100k and 16.2 GB at 900k. The latter is unacceptable for RPi5 and means the
snapshot solves repeated graph construction time but does not solve resident
vector/graph memory.

The first 50k S0 attempt was skipped by the conservative preflight estimate
before fixture construction because it would have left about 11.24 GiB free,
below the 12 GiB floor. After caches were moved to external storage and APFS
reclaimed space, the guarded 100k S0 run became safe and measured 298.41
seconds for the process-cold rebuild. Fixture construction plus the measured
child took 617.5 seconds. The observed full host-cold baseline remains 41m01s
for 326 collections; it is the authoritative current-system symptom.

## Other storage variants

| Variant | Evidence | Result |
|---|---|---|
| S4 PostgreSQL + pgvector | PostgreSQL 16.15 queried read-only | `vector` is unavailable; `pg_trgm` 1.6 is available but not installed. Excluded from v3 startup implementation. |
| S5 SQLite vector | Python SQLite 3.54 compile/runtime probe | FTS5 is enabled; `vec0` is unavailable. Excluded until a portable Mac/Linux/ARM64 extension is deliberately shipped. |
| S6 contiguous brute force | NumPy 2.2.6, 100k×768 float32, 10 scans | 307.2 MB per scan; mean 7.59 ms, p50 5.59 ms, p95 25.61 ms, peak RSS 342 MB. Linear 900k scenario: 68 ms mean, 230 ms p95, 2.76 GB vectors read per query. Pi and concurrent-load suitability remain unmeasured hypotheses. |
| S7 archive legacy v1 | Read-only inventory | v1 occupies 8.3 GiB and v2 9.4 GiB. Do not archive v1 until source/session/segment coverage and sealed quality are proven; current metadata for v1 is stale (`record_count=0`) and cannot be trusted as coverage evidence. |
| S8 SQL truth + compact semantic tier | Q7–Q12 developer calibration | Semantic-only retrieval failed on the long-message case even though the retained derived excerpt contained its tail evidence; lexical+semantic hybrids recovered it. Keep raw SQL/FTS as truth and treat semantic derivatives as replaceable indexes. |

A repeatable-read, read-only PostgreSQL inventory observed 545 imported sessions,
311,538 imported messages, 545 RAG tracking rows, and 464 distillation rows. These
counts explain why SQL truth plus a session/segment semantic tier can be much
smaller than the 900,108-record v2 message index, but tracking-row counts are
not vector counts and are not used as a storage estimate.

The same snapshot contains 782,763,375 UTF-8 message bytes. Grouping by session
and dividing by the renderer's 200 KiB part cap gives a lower-bound planning
count of 4,164 parts before renderer metadata and boundary effects. One 768d
vector per part would occupy about 12.2 MiB, compared with about 2.58 GiB for
900,108 vectors. A 200 KiB part is larger than the encoder context and therefore
cannot be embedded losslessly as-is: this estimate applies only to a validated
summary/derivative per part. The actual renderer count and selected-path quality
run are required before it can become a migration promise.

## Retrieval calibration

The deterministic 14-query developer corpus covers identifiers, code, date,
version, number, Russian, English, cross-language, multi-message facts, a
238,040-byte message, correction/staleness, negation, abstention, duplicate
fusion, and private/shared/revoked/foreign collisions. All Q0–Q12 variants pass
authorization, exact provenance, and active-stale gates. Semantic digest:
`3fdeaac4bf91e9863420e5c0b3b9bb784efa62041ec04f39b94d3ed9e76df32e`.

| Variants | Recall@5 / MRR@10 / all facts | Interpretation |
|---|---|---|
| Q0 normalized LIKE | 0.462 / 0.462 / 0.462 | Diagnostic only; phrase scan misses paraphrase. |
| Q1/Q2 lexical calibration | 1.000 / 1.000 / 1.000 | Strong exact/raw baseline on this synthetic set. Q1 is SQLite FTS5 emulation, not PostgreSQL evidence. |
| Q3 dense surrogate | 1.000 / 1.000 / 1.000 | Developer calibration only. |
| Q4/Q5 dense/rerank surrogate | 0.923 / 0.923 / 0.923 | Semantic retrieval misses the long-message case. |
| Q6 lexical+dense | 1.000 / 1.000 / 1.000 | Hybrid repairs dense miss. |
| Q7–Q9 derived semantic only | 0.923 / 0.923 / 0.923 | Semantic retrieval misses the long case despite retained tail evidence. |
| Q10–Q12 lexical+derived | 1.000 / 1.000 / 1.000 | Raw lexical retrieval repairs the semantic miss. |

These results cannot satisfy the sealed gate: production encoders, PostgreSQL
FTS, reranker/generator artifacts, and at least 120 hand-verified queries are
still required. The calibration is useful for rejecting unsafe designs: a
semantic derivative must not replace raw searchable chat text.

### Actual selected-path calibration

The final synthetic run launched and stopped its own loopback embedding server.
It bound the process to the exact venv entry point, server-source digest, and
1,269,643,611-byte `embeddinggemma-300m-full-v1` model tree before and after the
run. Health and every embedding response reported that exact alias. It used
exact cosine search, real PostgreSQL 16.15 full-text search, and real SQLite
3.54 FTS5. The inputs contain 132 development cases, a separate 24-case
calibration set, and 24 disjoint final-validation cases. Roles and gates were
frozen before validation. This remains synthetic selected-path evidence rather
than a sealed production corpus.

| Row | Final Recall@5 / all facts / session | Unknown nonempty | Frozen result |
|---|---:|---:|---|
| Q0 normalized LIKE | 0.00 / 0.00 / 0.00 | 0 | diagnostic only |
| Q1 PostgreSQL `simple` + `plainto_tsquery` | 0.00 / 0.00 / 0.00 | 0 | diagnostic only; development recall 0.698 |
| Q2 SQLite FTS5 + rare-token gate | 1.00 / 1.00 / 1.00 | 0 | **pass** |
| Q4 exact Gemma dense | 1.00 / 1.00 / 1.00 | 0.333 | fail abstention |
| Q6 PostgreSQL FTS + Q4 RRF | 1.00 / 1.00 / 1.00 | 0.333 | fail abstention; non-inferior to Q4 |
| S1 compact head/tail dense | 1.00 / 1.00 / 1.00 | 0.167 | fail abstention |
| S2 PostgreSQL FTS + S1 RRF | 1.00 / 1.00 / 1.00 | 0.167 | fail abstention |

All rows had zero unauthorized, stale, or duplicate hits. Across 156 scored
queries in the final run, Q2 p50/p95 was 0.15/0.91 ms, Q4 was 34.84/52.06 ms,
and Q6 was 35.35/52.63 ms. Corpus embedding took 77.8 s. The harness correctly exited 2:
the selected Q6 path failed its predeclared zero-unknown gate. The final
re-auditable artifact SHA-256 is
`301eb20a342113121994c1d89a0866c540072546b65022c8fb618e16de7e1edb`.
Its gate inputs reject missing, non-finite, non-numeric, and out-of-range values. The
managed server inherited a listener bound by the parent before process launch,
so a different process could not win the selected port. Endpoint identity was
required and passed, while retrieval quality still failed as reported. Failed
startup/readiness also terminates the child and closes its log before the error
returns to the caller.

The earlier unbound-endpoint artifact remains useful only as historical
calibration. Its SHA-256 is
`090297a8cfa37839bb39a7486c80c6c82b346e61e9eb5e43ea398075fb799c99`.

The current product forwarder at `127.0.0.1:9102` could not complete the same
run: it returned persistent HTTP 500 responses after 12 single-document
requests. It forwards to `10.23.0.64:9101`, whose model bytes were not
independently identified, so no product-path quality artifact is claimed.

## V3 decision

| Area | Decision | Evidence |
|---|---|---|
| Native collection restart | Implement an opt-in exact graph snapshot bound to dimension, HNSW config, checksum, and an exact WAL prefix. Replay an ordered valid tail. Any missing, corrupt, forged, stale, symlinked, or mismatched snapshot uses the existing full rebuild. | S1 reduced 100k open from 298.41 s to about 12.2 s; corruption/tail/race tests pass. |
| Listener lazy load | Do not wire S3 into production in this change. | It makes the listener ready immediately but moves the same load to the first read; the public API has no complete loading-state contract yet. |
| Full-message HNSW | Reject as the imported-chat primary index. Keep existing collections readable; do not migrate more messages into this shape. | At 100k, M32/M064 `EfConstruction=800` took 34.4 minutes, 3.69 GiB RSS, and Recall@10 remained 0.85. |
| Imported-chat truth | Keep complete raw messages and access scope in Levara SQL. Treat every search structure as a deletable derivative. | This preserves provenance, project sharing, revocation, and messages larger than an encoder context. |
| First lexical candidate | SQLite FTS5 with the frozen rare-token gate is the only current candidate that passed all synthetic gates. Keep it isolated until the import-search API and a sealed corpus are ready. | Q2 passed recall, all-facts, session, abstention, and safety gates at 0.15/0.91 ms p50/p95. |
| Dense, hybrid, compact semantic | Do not enable Q4/Q6/S1/S2. Do not tune their thresholds against this validation set. | Known-query quality was 1.0, but Q4/Q6 answered 2/6 unknowns and S1/S2 answered 1/6. |
| Existing v1/v2 data | Do not delete or archive it in v3. | Coverage parity and sealed retrieval quality are not yet proven. |
| RPi5 | Keep SQL/lexical operation; do not deploy the full semantic HNSW corpus. | The measured 100k graph already needs about 3.7 GiB RSS; 900k is outside the Pi budget. |

The implementation scope is deliberately limited to the restart optimization
that passed its gates. It does not change ranking, authorization, imported-chat
ownership, or the source-of-truth database.

With `-hnsw-snapshots`, the first start without a usable snapshot performs the
existing WAL rebuild and best-effort atomic publication. A later start accepts
the snapshot only when its dimension, HNSW configuration, payload, topology,
and exact WAL prefix validate, then replays any ordered valid tail. Missing,
stale, corrupt, forged, or checkpoint-invalidated snapshots fall back to the
authoritative WAL rebuild and are republished best-effort. The flag remains off
until host-cold rollout validation is complete.
