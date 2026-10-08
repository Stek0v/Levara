# Design

## Context

See proposal.md for motivation. Notebook handlers currently contain CRUD and command execution, enabled only by LEVARA_NOTEBOOKS. Stored history is in SQL and remains authoritative. The 2026-09-27 F1 plan requested one release with notebooks off; there is no observed published-release evidence. The current authorized T31 contract replaces that planning wait with explicit preservation and recovery gates, without claiming the old interval occurred.

## Goals / Non-Goals

Goals: retire the optional execution and UI surface permanently while retaining recoverable history in both SQL backends.

Non-goals: a replacement notebook UI, public read/export endpoints, automatic data migration or deletion, publication, deployment, or proof of an earlier published release.

## Decisions

Delete the notebook handlers and registration, rather than retaining a flag or tombstone handler. Fiber's unregistered-route 404 preserves the established default-off contract; ignored old environment values cannot resurrect execution and do not fail startup. Remove the optional UI page; navigation already excludes notebooks.

Keep schema DDL and indexes unchanged. Preservation tests populate distinct owners, empty owner, Unicode/source/output, multiple cell types/order and fixed timestamps; re-run migration and all old routes, then compare complete stored rows and schema constraints/indexes in SQLite and PostgreSQL. Use existing SQL fixtures rather than another abstraction.

Recovery stays administrative through existing backup/verify/restore tooling on owned disposable data. Root integration must record actual restored-row comparisons, update route inventory/generated contracts and sunset documentation, and run WebUI and backend gates. No insecure legacy read/export shim is retained.

## Risks / Trade-offs

[An enabled old client loses execution] → publish the explicit 404 sunset contract with release documentation; history remains in SQL and recoverable by administrators.

[Stored rows silently change during startup or recovery] → require populated before/after SQL snapshots and real backup/verify/restore evidence before accepting T31.

[Historical release wait is mistaken for verified evidence] → record that the new authorized contract replaces the old planning condition; no published-release claim.

## Migration Plan

No SQL migration is added. Verify a populated disposable backup/restore before accepting the change. A later explicitly authorized deployment removes the routes and UI; old environment values are ignored. Rollback to a prior binary is an administrator decision and must not imply new safe public export behavior.
