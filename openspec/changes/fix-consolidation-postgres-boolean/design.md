# Design

## Context

See proposal.md. PostgreSQL defines is_pinned as BOOLEAN; the existing SQLite consolidation fixture uses INTEGER. Apply and Revert already use transactions.

## Goals / Non-Goals

Prove cross-dialect mixed-action success, late-action rollback and reversible SQL state. Ownership, shared mutation, synthetic hall policy, async jobs and vector/outbox lifecycle remain open T06 boundaries; this literal fix does not redesign them.

## Decisions

Use FALSE, which both backends accept, instead of integer 0. A per-dialect SQL branch or dependency is unnecessary. Reuse the existing SQLite fixture and isolated PostgreSQL schema helper, preserving the production BOOLEAN type in the PostgreSQL fixture. Force an abstract-column failure after a merge to prove apply rollback; a foreign-key reference to the abstract prevents deletion after reactivation and proves revert rollback.

## Risks / Trade-offs

Existing ownership/hall/index gaps persist → record them explicitly and keep T06 open. Fixture-only failure injection does not measure real LLM quality → make no quality claim. No DDL migration or deployment is required; rollback of the code is one literal, although that restores the PostgreSQL defect.
