# Design

## Context

See proposal.md for motivation. SQL is authoritative; Neo4j is a rebuildable projection. Existing path reads use Unix-second envelopes. Generic gRPC forwards JSON properties; graphstore has a separate edge ID.

## Goals / Non-Goals

Atomic episode transitions and preserved history. No public route, flag, SQL schema, ACL, live migration or dependency changes. One internal Neo4j identity constraint is added. Neighborhood traversal is separate.

## Decisions

- Share the exclusive-type vocabulary in the existing graph package and preserve the orchestrator exported wrapper.
- Validate edge metadata before effects. Integral Go/JSON numbers, integer strings and RFC3339Nano timestamps normalize to int64 Unix seconds; reject malformed, fractional, nonfinite, overflowing or reversed bounds. Nil means absent. SQL microseconds stay unchanged.
- Missing current start uses one server time after locks. Active tuple retries preserve ID/start and update ordinary evidence; conflicting explicit start errors.
- Explicit closed imports never supersede current state. Missing closed start means epoch zero. Identical closed-ID retry is a no-op; conflicting tuple/envelope/evidence errors. Closed IDs never reopen.
- Identity includes source, target, dataset and type; exclusive types compare case-insensitively, nonexclusive exactly. New current caller IDs are hints; occupied IDs receive a fresh UUID suffix. Adapter separate/property ID conflicts error.
- Reserve candidate IDs using internal __EpisodeIdentity__ nodes with a unique id constraint; these are never __Node__ or graph-visible. Reserve in the same transaction as relationships, locking known caller/default candidates in sorted order before endpoints. This covers disjoint tuples requesting one ID; endpoint-only locks do not. Check/adopt accessed legacy IDs without a global history rewrite.
- Ensure the identity constraint before edge effects through a small writer mutex/success flag; retry initialization after failures. Consume DDL before proceeding; read-only writer construction performs no DDL. Include it in explicit EnsureSchema too.
- Lock ordered batch nodes and required endpoints, then apply edges in input order. Missing endpoints abort; never create phantom nodes. Close predecessors at successor start and reject backdating before a predecessor start.
- Preserve closed properties/successor links. Use existing APOC locking, creation and type replacement. Legacy matching active relationships missing ID/start acquire stable ID and epoch start under lock.

## Risks / Trade-offs

- Equal second boundaries → retain inclusive path semantics without claiming SQL subsecond parity.
- Overlapping endpoint serialization → correctness before throughput, no lock framework.
- Managed retries → generate clock/IDs inside the transaction after locks.

## Migration Plan

No live migration. Use owned disposable Neo4j/APOC and SQLite/PostgreSQL checks, independent review and receipts. Code rollback cannot undo persisted history; deployment is outside this change.
