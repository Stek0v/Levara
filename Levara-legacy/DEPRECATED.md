# Levara-legacy (formerly Cognevra)

This directory holds the **legacy Go implementation** of the vector engine, originally named **Cognevra** and renamed to `Levara-legacy/` on 2026-05-06 as part of the Cognevra → Levara rebrand.

The current production code lives in [`../Levara/`](../Levara/). It is a superset of this module's CLI surface (`server`, `cli`, `benchmark`, `loadtest`) and adds `backup` and `qwen3rerank` subcommands.

## Why this directory still exists

- Historical reference for the Cognevra-era codebase
- `Raspberry/Dockerfile` still builds from here for backwards-compatibility on existing Raspberry Pi deployments; this will move to `../Levara/` in a follow-up PR (Phase 4 of the rebrand)
- Some older tests and documentation may reference paths under this directory

## Status

**Frozen.** No new features land here. Bug fixes only if they affect Raspberry deployments that haven't migrated to the current `Levara/`.

## Module identity

The Go module declaration `module github.com/stek0v/cognevra` (in `go.mod`) intentionally retains the Cognevra name — changing it is a separate breaking change for any external consumers and is tracked alongside the `cognee-plugin/` adapter rename in Phase 4.
