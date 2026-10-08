# Proposal

## Why

The public code tool reports successful empty analysis for unsupported languages, hides storage failures and writes graph edges whose endpoints do not match its node IDs. Its HTTP wrapper also lacks the existing administrator and source-publication boundary used by Git analysis.

## What Changes

- Reject unsupported source extensions and malformed Go through a checked analyzer; describe Python as heuristic extraction, preserving the legacy internal analysis adapter.
- Publish static code graphs through existing immutable source ingestion and native versioned publication, with resolved endpoints and no model extraction.
- Require verified instance administrator authority with no selected tenant for the instance-wide code tool, retaining live authorization through effects and responses. **BREAKING**: previously admitted non-administrator code writes are rejected.
- Propagate pipeline/storage errors, preserve synchronous successful JSON fields and nil-DB analysis-only behavior, and document omitted collection selection.
- Make Git parsing honor caller cancellation, define empty repositories and repeated commit analysis honestly, and propagate MCP/HTTP errors to CLI failure.

## Capabilities

### New Capabilities
- `code-knowledge-publication`: supported static analysis, explicit failure and authoritative source-scoped graph publication.
- `git-analysis-lifecycle`: cancelable repository analysis, empty/repeated results and administrator transport/CLI behavior.

### Modified Capabilities
None; the main capability inventory is currently empty.

## Impact

Affected packages: extract, git, orchestrator and MCP; HTTP MCP admission/response lifetime; existing CLI Git commands; generated full/core contracts and documentation. Reuse current SQLite/PostgreSQL schema and publication guards; no new dependency or migration. No JS/TS analyzer, Python syntax validator, repository-wide semantic resolution, commit deduplication, production rollout or Neo4j parity claim is included. Original roadmap T18 retains its external T15 dependency separately.
