# ASSESSMENT — `levara`

Run date: 2026-10-15. Method: `code-modernization` single-system assess.
Source mapping: `legacy/levara` ≡ repo root `/Users/stek0v/src/levara` (see
`analysis/levara/PREFLIGHT.md`). Analysis engine: gortex daemon graph
(99,797 nodes / 396,381 edges) + live prod telemetry (`:8081/metrics`) +
`git ls-files` inventory. LOC tool: `git ls-files | wc -l` fallback
(`scc`/`cloc` not installed; noted in PREFLIGHT). The three assess
subagents (structure/debt/security) ran as direct gortex queries in the
main thread after the agent quota was exhausted 2026-10-15 — same
analyses, same evidence trail.

## Executive Summary

Levara is a mid-size (255.0 KSLOC Go, 982 files, 11 binaries) local-first
AI memory platform — vector store + knowledge graph + WAL + MCP/REST/gRPC
APIs — with an unusually strong safety net for refactoring: 580 Go test
files, a Python e2e suite, and a fail=0 merge gate across 24 CI jobs.
The dominant modernization value is **in-place structural refactoring**,
not migration: one god-package (`internal/http`, 40k LOC / 111 files)
concentrates the API surface, its hottest methods, and almost all dead
code; one god-main (`cmd/server/main.go`, betweenness 133 — the most
central node in the application graph) hand-wires every subsystem.
Security posture is sound (no real hardcoded secrets; all error-severity
SAST hits are test fixtures or scanner-state noise), leaving an
info-tier hardening backlog (path joins, SSRF candidates, env fallbacks).
Runtime risk is concentrated in one path: memory recall latency variance
(p99/p50 ≈ 1000×) caused by the external embed-server dependency.

## System Inventory

| Metric | Value |
|---|---|
| Go files (tracked) | 982 (580 with tests = 59%) |
| Go LOC (tracked) | 254,979 |
| — `cmd/` | 72 files / 15,949 LOC (11 binaries) |
| — `internal/` | 428 files / 118,187 LOC (7 packages) |
| — `pkg/` | 464 files / 105,857 LOC (49 packages) |
| — `proto/` | 4 files / 11,358 LOC (generated) |
| Largest packages (non-test) | `internal/http` 111 files / 39,992 LOC; `pkg/mcp` 13,266; `pkg/access` 5,009; `internal/store` 4,525; `internal/grpc` 2,944 |
| Direct dependencies | 40 (fiber v2 + fasthttp, grpc v1.83, pgx v5 / ncruces-sqlite3 / neo4j driver, hashicorp/raft, crewjam/saml + go-ldap, gosseract OCR, prometheus, swagger, vek) |
| Build | `go build ./...` green 32s cold; `go vet` clean; test-compile clean (toolchain go1.27.2, directive go 1.26.6) |
| Secondary stacks | Python ops/ML harness (`scripts/`, untracked `.venv-ab`), TS/JS webui (Next.js), pytest e2e (`tests/`) |
| Data stores | SQLite (ncruces) + PostgreSQL (pgx, sqlcompat layer) + Neo4j (optional graph) + WAL; HNSW vector index in `pkg/vectorstore` |
| Integration surfaces | MCP (75+ tools), REST (fiber + swagger), gRPC (36 RPCs, `service.go` + `service_v2.go`), SAML/LDAP SSO, Prometheus `/metrics`, webhooks (`pkg/audit/webhook.go`) |

## Architecture-at-a-Glance

Diagram: `analysis/levara/ARCHITECTURE.mmd`.

| Domain | Packages | Responsibility | Entry points |
|---|---|---|---|
| MCP/HTTP API | `internal/http`, `pkg/mcp`, `pkg/profile`, `pkg/router` | MCP tool dispatch, REST admin, palace memory API, search strategy registry | `cmd/server` |
| gRPC API | `internal/grpc`, `proto/` | 36 RPCs, v1+v2 services | `cmd/server` |
| Search & retrieval | `pkg/bm25`, `pkg/aggregator`, `pkg/graphrank`, `pkg/rerank`, `pkg/vsa`, `pkg/vsamemory` | hybrid lexical+vector+graph search, reranking | via API |
| AI/ML pipeline | `pkg/embed`, `pkg/embcontract`, `pkg/llm`, `pkg/llmcache`, `pkg/llmproxy`, `pkg/classify`, `pkg/chunker`, `pkg/extract`, `pkg/structuredextract`, `pkg/docdetect` | embeddings (gemma 768), LLM calls w/ cache+breaker, chunking, extraction | via API/Ingest |
| Knowledge graph | `pkg/graph`, `pkg/graphdb`, `pkg/graphstore`, `pkg/orchestrator`, `pkg/ontology`, `pkg/taxonomy`, `pkg/temporal`, `pkg/community` | entity/edge extraction, temporal validity, communities | via API |
| Memory system | `pkg/memoryhall`, `pkg/memoryindex`, `pkg/consolidate` | halls, recall ranking, consolidation/supersession | via API |
| Storage & persistence | `internal/store`, `pkg/storage`, `pkg/vectorstore`, `pkg/sqlcompat`, `pkg/backup` | WAL, collections, HNSW, SQLite/Postgres duality, backup | all |
| Ingestion | `pkg/ingest`, `pkg/git`, `pkg/chatimport`, `pkg/fetch`, `pkg/audio`, `pkg/fileio` | doc/git/chat ingestion, URL fetch, audio | via API/Workspace |
| Enterprise & auth | `pkg/access`, `pkg/auth`, `pkg/agenthosts`, `pkg/audit`, `internal/contract` | RBAC, SAML/LDAP, agent hosts, audit, contracts | cross-cutting |
| Platform | `internal/cluster`, `internal/metrics`, `internal/trajectory`, `pkg/governor`, `pkg/runreg`, `pkg/observe` | Raft clustering, telemetry, rate governor, run registry | `cmd/server` |
| Workspace | `pkg/workspace` | file workspaces, index jobs, audit log | via API |
| Clients & ops | `cmd/*` (server, cli, benchmark, loadtest, backup, reconcile, audit, contract, agent-hosts, qwen3rerank), `webui/` (Next.js), `tests/` (pytest e2e) | binaries, dashboard, e2e | — |

Dangling references (dead-code candidates, needs dispatch check before
deletion — see Technical Debt #3): ~35 graph-flagged, concentrated in
`internal/http/mcp.go`, `internal/http/mcp_embed.go`,
`internal/grpc/service.go`.

## Production Runtime Profile

Source: live Mac prod instance `:8081/metrics` (histogram buckets,
2026-10-15). Prometheus server itself not running — direct scrape used.

| Job / tool | Domain | n | p50 | p95 | p99 | p99/p50 |
|---|---|---|---|---|---|---|
| MCP `recall_memory` | Memory recall | 239 | 0.01s | 10s | 10s+ | **≈1000×** |
| MCP `chat_distill` | Chat/AI | 86 | >30s | >30s | >30s | ~1× (uniformly slow) |
| LLM calls | AI/ML | 88 | 30–60s | 60s | 60s+ | ~2× |
| `levara_embed` | AI/ML | 2,236 | ~10s | 10s | 10s | ~1× |
| HTTP api | API | 71 | 0.25s | 5s | >5s | >20× |
| MCP `wake_up`/`save_memory`/`set_context` | Memory | 554 | 0.005–0.05s | ≤0.1s | ≤0.5s | 5–10× |

**Highest-variance domain: memory recall** — `recall_memory` swings from
10ms to 10s+ depending on whether the external embed server (`:9101`,
Python/MPS) answers. This matches the documented 30s embed-timeout
fallback incident class. Telemetry-grounded conclusion: the recall path's
coupling to a separate embed runtime is the top operational risk, ahead
of any static-analysis finding.

## Technical Debt (top 10, ranked by remediation value)

1. **`internal/http` god-package** — 111 files / 39,992 LOC mixing MCP
   dispatch, REST, admin, palace API, search strategy; `mcp.go` alone is
   1,772+ lines; hosts 9 of the hottest methods (`mcpHandler.Q` fan-out
   564) and most dead candidates. Value: every later change gets cheaper.
   Risk: high blast radius — mitigate with characterization tests first
   (the 580-file test suite is the net).
2. **`cmd/server/main.go` god-main** — fan-out 381, betweenness 133
   (highest in app code): all subsystem wiring in one function. Value:
   makes the wiring testable and the server boot auditable.
3. **Dead code in the API layer** — graph flags ~35 unused symbols:
   `internal/http/mcp.go` (CollectionExists:299, CollectionInsert:324,
   CollectionInsertDeferredHook:333, CollectionHasRecord:356,
   OntologyPromptSuffix:465, PipelineFinalizesStatus:476,
   RerankEnabled:551, ListLexicalCollections:616, LogHeartbeat:1772),
   `internal/http/mcp_embed.go` (EmbedBatch:22, EmbedAvailable:29),
   `internal/grpc/service.go` (ListCollections:160, HasCollection:164),
   `internal/store/heap.go` (MaxHeap.Push/MinHeap.Push — dead clone
   pair), `benchmark/memory_eval/run_memory_eval.py:143`. Caveat: MCP
   dispatch may reach methods dynamically — verify against the dispatch
   table and generated contract before deleting.
4. **webui hook boilerplate** — `webui/src/hooks/use-levara.ts`: 23
   near-clone hooks (14 @ 0.946 + 9 @ 0.945 similarity), 9 copies of
   `onSuccess`, 51 files import this one module. Frontend-only refactor,
   low risk, big readability win.
5. **pytest fixture duplication** — `_auth`/`_register` re-implemented
   in 13 e2e files (`tests/test_*.py`) at 0.976 similarity → move to the
   existing `conftest.py`.
6. **Path handling on user input (CWE-22, 143 info hits)** —
   `filepath.Join` with unsanitized input across ingest/workspace —
   systematic traversal review; primary harden target.
7. **SSRF candidates (CWE-918, 4 info hits) + `pkg/fetch`** — verify
   host validation rejects localhost/loopback/private ranges on outbound
   fetch (policy constraint; confirm it is enforced, not assumed).
8. **Insecure env fallbacks (CWE-1188, 26 info hits)** — sensitive
   settings with permissive `os.Getenv` defaults; enumerate and tighten.
9. **gRPC v1/v2 dual surface** — `service.go` + `service_v2.go` coexist
   with v1 methods already dead (#3); plan v1 retirement or document the
   versioning contract.
10. **Hygiene tail** — 755 findings (606 py-print, 149 fmt.Println);
    CLI prints in `cmd/` are legitimate — clean only library-path
    logging. Plus `go-math-rand-for-crypto` (6) and
    `go-xml-decoder-no-strict` (2) spots to fix in place.

Duplication overall: 1,789 clone pairs / 163 clusters; the two big real
ones are listed (#4, #5); the rest are generated gRPC stubs (benign).

## Security Findings

SAST totals: 435 findings; **error tier fully triaged below**. No real
credential values discovered anywhere — nothing to quarantine;
`analysis/.gitignore` still created per protocol.

| Sev | CWE | Finding | file:line | Preview | Fix direction |
|---|---|---|---|---|---|
| P3 | CWE-79 | `dangerouslySetInnerHTML` in root layout | `webui/src/app/layout.tsx:21` | `__html: themeSc…` | Static theme bootstrap idiom — safe as-is; document or replace with nonce'd script |
| P3 | CWE-798 | 6 "hardcoded credentials" in benchmarks | `benchmark/chat_retrieval_selected_path.py:346-361`, `benchmark/mcp_client.py:54` | `CASEID-…`, `BenchPa…` | Synthetic fixtures/ephemeral bench users — false positives; add detector ignore list |
| P3 | CWE-78 | `shell=True` in bench scripts | `benchmark/cron_benchmark.py:111,123,140`, `run_benchmark.py:47,109` | `ps aux \| grep…` | Hardcoded commands, no user input — low risk; switch to argv lists |
| P3 | CWE-327 | SHA1 for fixture doc-ids | `deploy/rerank/eval/corpus_fixture.py:47` | `hashlib.sh…` | Non-crypto use; comment or switch to sha256 |
| — | CWE-95/78 | eval/os.system ×10 | `.mimosa/hook-state/…` | scanner state, **not levarа code** | Exclude `.mimosa/` from indexing/CI scans |
| P2 | CWE-22 | 143 path-join-on-user-input (info) | ingest/workspace paths | — | Systematic traversal-safety pass (harden) |
| P2 | CWE-918 | 4 http-do-with-userdata (info) | fetch paths | — | Verify private/loopback host rejection |
| P2 | CWE-1188 | 26 getenv-sensitive-fallback (info) | config | — | Audit defaults |
| P3 | CWE-330/338 | 6 math-rand-for-crypto (info) | misc | — | Swap to crypto/rand |

Dependency posture: all majors current (grpc 1.83, pgx 5.9, fiber
2.52, x/crypto 0.55); no known-CVE pin observed; `secrets-scan.yml` CI
covers push-time secrets but its scope (what patterns/paths) is not
documented — worth a one-paragraph spec.

## Documentation Gaps (top 5)

1. `internal/http` (largest subsystem, 40k LOC) has no dedicated
   architecture note — MCP dispatch flow, palace API, strategy registry.
2. Formal roadmap doc stale (last consolidated 2026-09-22 per project
   memory); decisions live in `docs/product/*-2026-0*-*.md` snapshots
   instead of a living plan.
3. Dual-instance topology (Mac `:8081`, Pi `:8090/:8080`, embed `:9101`,
   sidecar) is scattered across runbooks — no single ops-topology page.
4. gRPC surface (36 RPCs, v1+v2) undocumented in `docs/api-contract.md`
   (MCP-only) — no versioning/retirement story.
5. `scripts/load-profiles` ML harness partially covered by
   `embed-ops-runbook.md`, but the bench estate (`benchmark/*.py`,
   frida gate, decisions) has no index doc.

## Relative Scale

COCOMO-II basic index = `2.94 × (255.0 KSLOC)^1.10 ≈ 1,305` (inputs
shown; fallback LOC counting via `git ls-files`+`wc`). This is a
**relative complexity/scale index for ranking against other systems,
not a timeline, cost, or duration estimate** — it assumes traditional
human-team productivity, which agentic transformation does not follow.

## Recommended Modernization Pattern

**Refactor — in-place, same stack** → routes to `/modernize-uplift`
(same-stack path; no version-bump target required beyond the available
go1.27.2). Rationale: the stack is current, tests and CI are strong,
debt is structural (god-package, god-main, dead API layer) rather than
technological; runtime risk (recall/embed coupling) is an architecture
constraint to preserve or improve, not a migration driver. Cross-stack
`transform`/`reimagine` are not justified by any finding.

Sequence implied by the evidence: (1) dead-code sweep with dispatch
verification (cheap, shrinks the refactor surface), (2) characterize
current behavior on the API surface (`/modernize-extract-rules`),
(3) split `internal/http` by bounded context, (4) extract server wiring
from `cmd/server/main.go`, (5) harden pass (path joins, SSRF, env
fallbacks) via `/modernize-harden`.
