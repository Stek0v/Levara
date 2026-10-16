# PREFLIGHT — `levara`

Run date: 2026-10-15. Operator: Alexey Stekov. Invoked via
`code-modernization@claude-plugins-official` v0.0.0 in ZCode.

## Setup note (path mapping — read by all downstream commands)

The plugin convention expects source at `legacy/$1`. `levara` is a live,
standalone git repository (branch `v3`, 1 commit ahead of `main`), so the
self-referential `legacy/levara` symlink is deliberately **not** created.
**Path mapping for every downstream command: `legacy/levara` ≡ repository
root `/Users/stek0v/src/levara`.** Analysis artifacts live in
`analysis/levara/`. The source tree is never modified by analysis commands.

## Check 0 — Human answers (verbatim, asked 2026-10-15; UNANSWERED = open item)

1. **Scope** — ANSWERED 2026-10-16 (verbatim): "a1 - go-ядро". Refactor
   scope = Go core (`cmd/`, `internal/`, `pkg/`); webui/scripts/benchmark stay
   out of this pass. API surfaces (MCP/REST) are contract-frozen via
   BUSINESS_RULES.md; no breaking changes to runtime consumers (Mac :8081,
   Pi, embed :9101) without explicit owner go.
2. **Build & test locally** — ANSWERED 2026-10-16 (verbatim): "a2 - ты
   локально можешь запустить в отдельном окружении" — full local test runs
   are permitted in an isolated environment. **Measured 2026-10-16:**
   `go test -count=1 ./...` = **373s wall (~6.2 min)**, 182s user, fully
   self-contained (no env gating, no external services, no fixed ports;
   temp dirs + random ports). 61 packages green + 9 without tests; ONE
   failure: `pkg/community TestLouvain_1K_Performance` — a timing oracle
   (1K nodes < 50ms) that took 94ms under full-suite CPU contention and
   **passes in isolation (0.7s)** — a load flake, not a regression
   (matches the documented timing-oracle flake class). Cost profile:
   `internal/http` alone is 315s = 84% of the suite; `internal/store` 56s,
   `pkg/mcp` 42s. **Verification strategy consequence:** a full-suite
   characterization run per refactor step is affordable (~6 min); per-step
   iteration should use targeted package subsets, with full suite +
   `-race` before merge.
3. **Bespoke build infrastructure** — ANSWERED 2026-10-16 (verbatim):
   "а3 - нет такого чего нет в репо" — nothing exists outside the repo; the
   build/dependency story is fully discoverable from the repository itself.
4. **Prior attempts** — DERIVED FROM REPO 2026-10-16, CONFIRMED BY OWNER
   the decomposition program T01–T24 (started 2026-10-05,
   `docs/product/decomposition-tasklist-2026-10-05.md` + issue ledger
   I01–I83 in `decomposition-issues-2026-10-05.md`) is the relevant prior
   art. It did NOT fail: 80+ scoped fixes landed with RED/GREEN evidence on
   both SQLite and PostgreSQL, race runs, independent review, S0–S4 gates.
   Documented pain points to respect: slow HTTP gate (~340s), full-gate
   flake from Fiber's 1000ms test timeout (I75, cause not attributed),
   cross-store (SQL+vector) atomicity explicitly NOT claimed anywhere,
   historical metadata/vector migration still pending (I78). The refactor
   brief must slot into this same evidence discipline.
5. **Off limits** — ANSWERED 2026-10-16 (verbatim): "нет таких штук нет +
   я уже сказал тебе что все тесты в отдельном окружении и отдельной
   ветке проводить" — no additional off-limits beyond the known generated
   contracts. **Owner-enforced working discipline: ALL test runs happen in
   an isolated environment on a dedicated branch** (never on the working
   branch, never against prod data). This is a hard invariant for
   `/modernize-brief` and every migration phase.

## Check 6 — Scope boundary

**`levara` is a standalone repository** (`module github.com/stek0v/levara`,
no parent workspace). No outbound references outside the root.

**Inbound consumers (runtime/deployment level — the blast radius of any
in-place change):**
- Mac prod instance: per-user launchd service, HTTP `:8081/mcp`
  (long-horizon memory, gemma4:e2b LLM, ~320 collections).
- Pi prod instance: `10.23.0.53` (`:8090` stdio bridge, `:8080` API),
  docker-compose deployments; disk at 99% (7.9Gi) — deploy headroom is
  near zero.
- MCP clients: Claude Code, ZCode (this session), Codex — tool-surface
  compatibility (`docs/api-contract.md`, generated MCP contract) is a
  hard compatibility axis.
- Embed server `:9101` (Python, `scripts/load-profiles/`) — separate
  runtime coupled by embedding dimension/contract (768, gemma).
- `webui/` (TS/JS) + `webui-server` — HTTP API consumers.

Code-level: `internal/` is non-importable; `pkg/` (49 packages) is public
surface but the repo is an application — no known external Go importers.
**Each runtime consumer needs an explicit transition decision in
`/modernize-brief`** (canary/rollback strategy), treated as the gap that
matters most.

## Status table

| # | Check | Status | Found | Fix |
|---|---|---|---|---|
| 0 | Human answers | ⚠️ | 5 questions asked, all currently open | Operator fills in; recorded verbatim above |
| 1 | Stack detect | ✅ | Go 1.26.6 (toolchain go1.27.2 darwin/arm64): 983 `.go` files, ~255k LOC across `cmd/` (11 binaries), `pkg/` (49 pkgs), `internal/` (7), `tests/`, `proto/`. Secondary: Python ops/ML harness in `scripts/` (incl. untracked `.venv-ab` = 24.7k py + 20k h files — local runtime, not system code), TS/JS `webui/`, 5 Dockerfiles + 5 compose files, Makefile, prometheus.yml | — |
| 2 | Analysis tooling | ⚠️ | `scc`, `cloc`, `lizard`, `glow`, `delta` all MISSING. Compensating asset: **gortex daemon has the repo fully indexed** (~110k nodes, call graph, dead code, hotspots) — strictly richer than scc for structural work | `brew install scc cloc glow delta && pip install lizard` (optional; gortex covers structure) |
| 3 | Build toolchain | ✅ | Proven on this tree: `go build ./...` ✅ 32s (475% cpu, cold), `go vet ./cmd/... ./internal/... ./pkg/...` ✅ 0 findings, `go test -run=NONE ./...` (test-compile) ✅. Pinned: go.mod `go 1.26.6`; CI ground truth: `.github/workflows/{go-ci,full-race,memory-quality,lint-prom-rules,secrets-scan}.yml` | — |
| 4 | Source completeness | ✅ | Build+test-compile green from a dirty-tree checkout ⇒ all includes resolve; `go.sum` present; 1642 git-tracked files. Untracked non-system clutter pre-exists: `cascade_probe/`, `levara-release.tar.gz`, `.mimosa/`, logs | None blocking; operator may clean clutter |
| 5 | Runtime evidence | ✅ | Logs at root (`levara.log`, `webui.log`, `webui-server.log`), WAL/collections under `data/`, deploy profiles `scripts/load-profiles/` + `deploy/`, prometheus.yml. Git history meaningful (PR flow, 173+ PRs) | — |
| 6 | Scope boundary | ✅/⚠️ | Standalone repo ✅; runtime inbound consumers listed above ⚠️ (decisions deferred to brief) | `/modernize-brief` must carry per-consumer transition items |

## Verdict per command

| Command | Verdict | Why |
|---|---|---|
| `assess` + `map` + `extract-rules` | **Ready** | Stack fully detected; missing `scc` degrades LOC metrics only (fallback `find`+`wc`; gortex graph adds real structure); Check 4 include count = 0 |
| `brief` | **Ready** | Needs only discovery artifacts + Check 0/6 inputs (open items above must be answered before brief is approved — brief is a human sign-off point anyway) |
| `uplift` (same-stack refactor; Go 1.26.6→1.27 available) | **Ready** | Check 3 green for target: go1.27.2 installed locally ⇒ source and target runtimes both present ⇒ true dual-run/characterization possible; standard `go fix`/`go vet` migration tooling present |
| `transform` + `reimagine` | **Ready** (for Go target) / n-a for other targets until that toolchain exists | — |
| `harden` | **Ready-with-gaps** | No local SAST CLI verified, but compensating: `secrets-scan.yml` CI + mimosa deep security scan MCP available on this machine |

## Overall: **READY-WITH-GAPS**

Blocking nothing. Gaps: (a) five Check-0 answers outstanding — operator
input required before `/modernize-brief` sign-off; (b) optional analyzer
tooling not installed; (c) runtime-consumer transition decisions deferred
to brief by design.

## Next step

`/code-modernization:modernize-assess levara` once Check-0 answers arrive
(assess itself can run before answers — only `brief` gates on them).
