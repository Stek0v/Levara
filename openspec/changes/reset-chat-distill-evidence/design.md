# Design

## Context

See proposal.md for motivation. The distillation upsert changes value/room/hall but omits three evidence fields. Ordinary Save derives its label from validated publication evidence; distillation has only a transcript and model proposal. Existing SQLite-only distill test DDL omits those fields.

## Goals / Non-Goals

Make the three evidence fields explicit in both native INSERT and conflict-update branches. Keep the existing upsert and return contract. Broader T08 authorization and quality work remains separate.

## Decisions

- Use constant `unverified`, empty Task ID and serialized empty receipt list in the existing SQL statement. Carrying old evidence would attest unrelated text; a new validation subsystem or transcript-as-receipt would invent authority.
- Retain normal textual transcript provenance, which identifies origin without asserting verification. Preserve owner/collection identity and canonical ID on overwrite.
- Add only the missing evidence columns to the existing lightweight test DDL. The new regression uses actual evidence-backed Save/Task receipt fixtures on SQLite and isolated PostgreSQL, preserving the dialect-aware dependency wrapper and native PostgreSQL timestamps where applicable.

## Risks / Trade-offs

Historical mislabeled rows are not silently rewritten; selected re-publication requires its own authority. Stub model tests establish storage boundaries, not factual quality or imported-session authorization. Retained unrelated owner/shared/collection control rows must remain byte-for-byte unchanged.

## Migration Plan

Existing columns suffice. Future distillation writes reset obsolete evidence automatically; no live migration or rollout is performed in this change. No API/profile/feature-flag membership change is planned.
