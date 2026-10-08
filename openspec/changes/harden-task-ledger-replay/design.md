# Design

## Context

See proposal.md. The two handlers return an existing ID before comparing payload, and their ON CONFLICT paths also omit equivalence. PostgreSQL and SQLite share these handlers but maintain separate production and fixture DDL.

## Goals / Non-Goals

Goals: immutable exact replay, current-version responses, additive SQL parity, observable process lease and real artifact verification.
Non-goals: task_open behavior changes, new APIs/executor, live migrations, deployment, automatic historical digest reconstruction.

## Decisions

Persist request_digest TEXT NOT NULL DEFAULT '' on receipts and checkpoints. Use standard SHA256 over canonical JSON of normalized actual-applied fields. Include verified owner and effective lease/audit actor; omit base_version, generated UUIDs and time. Object key order is canonical; array order is retained because checkpoint candidates apply sequentially. Unknown ignored fields are excluded.

Compare digest at both early lookup and lost insert-race lookup. SQL errors other than no-row fail closed; loser returns actual current version. Existing transactions and version CAS retain atomicity. Do not put an internal digest in user metadata or create a parallel ledger.

Historical empty digest means equivalence is unknowable: reject explicit replay, retain readable receipts/checkpoints and their original evidence. Reconstructing from rows would guess unstored actor and checkpoint effects.

Both dialects add the same column. Startup verifies column availability after tolerant additive ALTER handling so hidden migration errors cannot produce an apparently healthy task service.

## Risks / Trade-offs

- Historical exact retries become errors → document compatibility, use a new key only for a deliberately new operation; never auto-replay checkpoint effects.
- Canonicalization mistakes → native full-ledger equality controls, nullable exit codes, object ordering and ignored-field tests.
- Process tests can prove only observed scenarios → state direct-core verified context versus transport coverage and natural-expiry timing explicitly.
- Actual native post-hash replacement reproduced false completion → final validation now shares the completion SQL transaction, with retained native workspace project guards through commit/rollback. Existing SQL authority locks, SQLite write reservation and PostgreSQL memory serialization precede project acquisition. Guards release before post-commit DB/index work. This covers cooperating workspace writers; arbitrary OS/backend changes and FS+SQL power-loss atomicity remain outside the guarantee.

## Migration Plan

Deployment remains separately authorized. The additive default preserves all existing rows and permits older readers. Startup fails if required columns cannot be verified. Downgrades must preserve the columns; no historical backfill or row mutation is required. Task feature/profile visibility and public descriptor fields remain unchanged; generated contracts are checked.
