# Proposal

## Why

DCD reads taxonomy tables that have no production writer. The existing draft lacks an exact seed grammar and native source binding, so populated domain rows alone cannot support an honest routing quality gate.

## What Changes

- Add a bounded Markdown seed grammar and authenticated CLI/REST import, list and remove for an explicit dataset.
- Preserve the existing per-caller catalog: owner is the verified caller; team is the exact verified selected tenant, including empty. Dataset ACL still controls access after the existing selected-tenant dataset-owner membership filter. This resolves the draft's impossible team inheritance from datasets, which have no team column, without broadening private catalogs into shared catalogs.
- Parse the whole seed before one checked SQL transaction; preserve stable natural-key IDs, report alias collisions, and commit a content-free manual provenance journal with mutations. Explicit removal of nonempty hierarchies requires force and preserves source documents.
- Add optional document bindings to existing source DataIDs, distinct from taxonomy IDs and graph provenance. Native DCD ranking matches both dataset and source identity after existing source authorization.
- Measure off/observe/boost on supported graph strategies with actual imported bindings; default routing mode remains unchanged. Automatic proposals and cognify generation remain separate.

## Capabilities

### New Capabilities

- `taxonomy/seed-catalog`: Caller/tenant/dataset-scoped manual taxonomy lifecycle, provenance and authorized native ranking bindings.

### Modified Capabilities

None; the project has no existing main taxonomy spec.

## Impact

Additive SQLite/PostgreSQL manual import journal, concrete parser/store and REST handlers, CLI dispatch, DCD scope/source identity and native VSA metadata. Existing source authorization and publication metadata remain authoritative; no source bytes or graph provenance are rewritten. Root owns schema/routes/generated contracts. No dependencies, production migrations, default boost enablement, shared catalog redesign or automatic proposal application.
