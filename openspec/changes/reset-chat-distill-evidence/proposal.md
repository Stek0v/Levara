# Proposal

## Why

Chat distillation overwrites an existing memory's text while retaining its previous verification label and Task receipts. The new model output can therefore appear receipt-backed even though those receipts describe different text.

## What Changes

- Store newly distilled text as `unverified`, with no originating Task or receipt IDs, for both insertion and overwrite.
- Preserve canonical row identity, owner/collection selection, transcript provenance suffix, dry-run and public result/error shapes.
- Reproduce the overwrite through actual evidence-backed Save on both SQL dialects; verify fresh/legacy/forged/dry-run and namespace controls.

Scope is this evidence boundary. Non-goals: imported transcript ownership, diary namespaces, detached-operation revocation/cancellation, collection session routing and model quality. These remain original T08 work. No schema migration, retrospective backfill, deployment or external provider call is included.

## Capabilities

### New Capabilities

- `chat/distillation`: publication evidence for memories distilled from imported dialogue.

### Modified Capabilities

None; the current main-spec inventory is empty.

## Impact

The existing distillation upsert and its SQL tests; relevant descriptor/guides and generator-owned contract checks. Existing mislabeled rows are corrected on subsequent distillation overwrite; this change does not infer which historical receipts support already stored text. No new dependency or API shape change.
