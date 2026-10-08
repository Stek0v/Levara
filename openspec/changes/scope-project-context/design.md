# Design

## Context

See proposal.md. Existing SQL mirrors provide owner_id, collection_name and superseded_by. The canonical current-memory guard is superseded_by=''. CollectionMeta is actor-free; interactions have no project field; graph collection_id is taxonomy routing, and candidate dataset allowlists are not per-assertion authorization. Existing graph writers do not consistently populate that route field.

## Goals / Non-Goals

**Goals:** identical scoped memories on both SQL backends, atomic success/error response, explicit unsupported sections, sequential reads with pool=1, existing collection/text output.

**Non-Goals:** a new graph aggregation/ACL seam, schema migration for interactions, collection-wide statistics without document authorization, changes to recall or wake_up.

## Decisions

1. Use exact collection plus trusted context owner/shared and canonical active predicate for each SQL read. Do not take owner from tool arguments or treat a supplied collection name as access authority.
2. Withdraw unsafe auxiliary data and explain why each section is unavailable. Filtering graph by collection_id alone is both incomplete and unauthoritative; inventing a fallback would violate the requirement.
3. Use one local read/format routine for primary limit 20 and related limit 3. Close each result set before the next query; check query, scan and iteration errors, discard the assembled response on failure.
4. Missing DB is an explicit tool error: after withdrawing actor-free statistics there is no authoritative vector-only response. Keep malformed-related-item skipping compatible and required nonempty primary collection unchanged.
5. Preserve output schema and profiles; correct only descriptor prose. No generated schema inventory changes are expected, but verify contract drift.

## Risks / Trade-offs

- Consumers of global statistics lose those sections → intentional scope correction, documented unavailable markers and issue-ledger follow-ups.
- Shared empty-owner records are still visible → established memory contract, tested on both dialects.
- Source failures could expose partial data → return an error without the accumulated structured payload.
- Small pool/concurrent reads could stall → sequential row lifecycle and deadline regression; do not introduce nested policy queries.

## Migration Plan

No DDL, migration, dependency or feature flag. Focused RED/GREEN, authenticated legacy/latest transport checks, race, affected package and repository gates, independent review and source-digest evidence. No deployment is authorized by this change. A code rollback restores unsafe behavior and is not a data migration.
