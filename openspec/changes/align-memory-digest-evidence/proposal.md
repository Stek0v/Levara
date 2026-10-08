# Proposal

## Why

Server-validated memory writes now persist `receipt-validated`, while Markdown digest selects only legacy `verified`. A memory can therefore pass source-evidence validation and still disappear from its explicit digest selection. Reproduce this T07/I09 mismatch before changing eligibility.

## What Changes

- Include active explicitly selected `receipt-validated` decisions/discoveries alongside compatible legacy `verified` records.
- Preserve original labels, source task/receipts, freshness, owner/shared scope and read-only export semantics.
- Add SQLite/PostgreSQL writer-to-digest regressions and exclusion controls; update the descriptor and generated documentation through the existing generator.

## Capabilities

### New Capabilities

- `memory/digest`: scoped explicit Markdown export with evidence-label eligibility and unchanged provenance.

### Modified Capabilities

None; no main specs are registered yet.

## Impact

Bounded changes to the existing digest handler/tests, its MCP descriptor and guides/generated contracts. No schema migration, dependency, new tool, writable export, import, or deployment. Input/result/error shapes and the legacy label remain compatible; receipt validation establishes evidence provenance, not factual truth. History attribution, recall candidate coverage and general evidence validation are separate T07 work.
