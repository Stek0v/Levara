# PILOT NOTES — Phase 1(c) memory REST slice extraction

Completed 2026-10-16 on `refactor/levara-core-1`. This document records
what the pilot recipe actually took, what it surfaced, and what changes
for Phase 2 — per MODERNIZATION_BRIEF.md §3 ("what the pilot surfaces is
expected to revise this brief").

## What moved

| From (internal/http) | To (internal/api/memories) | Notes |
|---|---|---|
| `memories.go` (474 lines: 7 handlers, RegisterMemoryAPI, scanMemoryRows) | `memories.go` (Config + Register + handlers, bodies verbatim) | 5 seams threaded through `Config` as function values: `Q`, `QArgs`, `RequestContext`, `EgressContext`, `DeleteMemory` (was `mcp.DeleteMemory(ctx, &mcpHandler{cfg})`) |
| `memory_events.go` (bus + MemoryEvent) | `events.go` | verbatim; bus used only by this slice |
| `memory_events_test.go`, `memory_events_e2e_test.go` | same names | verbatim, package renamed |
| — | `internal/http/memories.go` (32-line shim) | maps APIConfig → Config, threads closures; `RegisterMemoryAPI` signature unchanged |

## Gates (all green)

| Gate | Result |
|---|---|
| `go build ./...` | green, first pass after the move |
| New package tests | 12/12 (bus fan-out/drop/cancel + SSE wire format, 3-filter precedence) |
| Full `internal/http` suite | **green, 322.4s** (baseline 315.7s — within noise) |
| Contract byte-drift | `docs/contract.json` + `contract-core.json` sha256 **identical to BASELINE.md** |
| `-race` | memories 2.99s green; http Memory|SSE subset 75.5s green |

## What the pilot surfaced (→ brief revisions)

1. **Unexported handler references in a sibling test** —
   `memories_scope_regression_test.go` built `saveMemoryHandler`/
   `getMemoryHandler` directly. Fix: route through the public
   `RegisterMemoryAPI` shim (better: the regression now exercises the
   real wiring). **Lesson: before any Phase-2 sub-move, grep tests for
   direct references to the slice's unexported symbols; adapt them to
   the public registration entry, not to the new package internals.**
2. **The god-type seam is real but narrow** — only ONE call site
   (`deleteMemoryHTTP` → `&mcpHandler{cfg}`) needed the Deps closure.
   For palace/MCP-tool slices (Phase 2) this seam will be wider; the
   closure-threading pattern scales but each method needs its own
   adapter decision.
3. **Mimosa blocks `git mv` on source files** — moves must go through
   write/edit tools (content-identical re-creation + delete via
   batch_edit), which preserves scanning. Slower but compliant.
4. **Dead-set finding already recorded** (BASELINE.md): heap.go is live;
   Phase 1(b) executed zero deletions.
5. **`mcp_palace.go` stays in Phase 2** as planned (methods on
   mcpHandler) — confirmed by recon, not a pilot surprise.

## Phase-2 sub-move recipe (validated by this pilot)

1. Recon: `get_file_summary` + `get_dependencies` on the slice; grep
   tests for unexported references.
2. Enumerate seams; thread as function values in a `Config`; keep
   handler bodies verbatim (no "improvements" inside a move).
3. Shim keeps the exact public registration signature.
4. Gates: build → new-package tests → full `internal/http` suite →
   contract sha256 vs BASELINE → `-race` on touched packages.
5. Record deltas here; commit only the move + adapted tests, nothing else.

## Cost observation

The expensive gate is the full http suite (~5.5 min). For Phase-2's many
small sub-moves, run it at sub-move boundaries only (per brief §Phase-2
risk note); targeted packages during iteration.
