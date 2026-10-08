# Design

## Context

See proposal.md for the mismatch. The exporter uses one scoped SQL query and renders stored provenance; evidence validation belongs to memory publication. Existing callers and saved legacy `verified` records remain supported on both SQL dialects.

## Goals / Non-Goals

Accept the server's current publication label without changing ownership, activity, genre, explicit selection or output shape. Do not revalidate historical Task evidence during export or change stored verification labels, memory writers, schema, history traversal or index behavior.

## Decisions

- Extend the existing native SQL eligibility predicate to both `receipt-validated` and `verified`. Rewriting old rows or dropping legacy eligibility would unnecessarily break existing exports.
- Render the stored label unchanged. Document `receipt-validated` as evidence checked at publication and legacy `verified` as compatibility metadata; neither proves the text factually true.
- Reuse existing evidence fixtures to prove actual validated writes reach the digest, with a PostgreSQL mirror and explicit excluded controls. No new abstraction or dependency.

## Risks / Trade-offs

Legacy labels can lack validated receipts → preserve the displayed label and explain the distinction. Referenced evidence can later change → this is a read-only publication-provenance view, not a fresh truth certification. Keep generated contracts under root's sequential ownership; test owner/shared and selected-ID exclusions without broadening SQL scope. No live data migration or deployment is needed.
