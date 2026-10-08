# Design

## Context

See proposal.md. Existing tables already have owner/team/dataset and document source IDs; DCD currently matches only taxonomy IDs against separate graph route columns. Native graph provenance stores source DataID in JSON properties. Dataset rows have no team column, and the current resolver isolates catalogs by caller owner. Existing retrieval benchmark strategies do not invoke DCD.

## Goals / Non-Goals

**Goals:** authenticated manual seed lifecycle, exact existing private namespace, native SQL transactions/audit, usable authorized source bindings, truthful bounded graph-strategy quality evidence.

**Non-Goals:** shared catalog audience redesign, anonymous writer, automatic proposals/generation/application, default boost rollout, bulk graph updates, source deletion, live migrations or deployment. Original T15/Neo4j prerequisite remains visible.

## Decisions

- Preserve caller-owned catalogs instead of dropping the resolver owner predicate: derive owner from verified caller and team from exact verified selected tenant. Empty tenant is an exact namespace in authenticated mode; trusted anonymous legacy resolver behavior remains separate. Dataset write permits import/remove; dataset read permits listing one's catalog. Source document bindings additionally require source read access in that dataset. For a nonempty selected tenant, reuse the existing dataset-owner membership restriction before dataset grants on the same transaction reader. No body owner/team hint grants authority.
- Use stdlib line parsing: UTF-8 Markdown up to 1 MiB and 1000 nodes. Blank lines, CRLF and a leading BOM are allowed. Require `# Domains`; domains use `## <name>`, collections `### Collections` then `#### <name>`, document bindings `##### Документ: <title>`. `Описание:` and comma-separated `Алиасы:` are optional once per node; `Источник:` is required exactly once for document nodes. Reject other nonblank lines, invalid hierarchy, duplicate normalized siblings and missing sources. Normalize natural keys with Go Unicode lowercasing and trimming, avoiding dialect-dependent SQL lower semantics. Alias collisions across domains are reported as warnings.
- The public contract is POST `/datasets/:id/taxonomy/import`, GET `/datasets/:id/taxonomy`, DELETE `/datasets/:id/taxonomy`. Import body: `seed`, optional `source_name`, `source_revision`, `request_id`; remove body: `domain`, optional `collection`, `document`, `force`, `request_id`. Document removal requires a collection selector. Unknown fields are rejected. CLI: `levara taxonomy import <file> --dataset <id> [--revision <value>]`, `list --dataset <id>`, `remove <domain> --dataset <id> [--collection <name>] [--document <title>] [--force]`. Existing auth/server configuration applies.
- Import is additive upsert of listed nodes, with stable existing IDs; absent nodes are not implicitly removed. Parse all input before mutation. Reuse the existing metadata write fence/checked transaction to serialize concurrent imports on PostgreSQL and SQLite; close materialized rows before nested policy reads. Match normalized keys in Go within the exact scope/parent and reject ambiguous legacy duplicates. This avoids unsafe unique-index rollout onto legacy data. Existing global fence is the deliberate throughput ceiling; add per-catalog locking/indexed canonical keys only after measured contention.
- Add `knowledge_taxonomy_runs` mirrored SQL journal: id, owner_id, team_id, dataset_id, request_id, action, request_sha256, source_name, source_revision, report_json, created_at, UNIQUE(owner_id,team_id,dataset_id,request_id). It stores hashes and reports, never raw seeds/source bytes. A supplied request ID replays only the same action/body hash; mismatch conflicts. Omission creates a fresh ID. Data mutation and journal commit together, with credential/cancellation recheck immediately before commit. No operation is claimed successful when audit SQL fails.
- Response shapes: import report contains run_id, domains, collections, documents, created, updated and warnings (array); list contains domains (array with nested collections/documents); remove report contains run_id and removed (count). Errors preserve explicit 401/403/409/400/503 distinctions. Empty list is `{"domains":[]}`. Missing remove target is idempotent zero removal. Nonempty domain/collection removal requires force; source bytes, graph rows and unrelated namespaces are never removed.
- Keep taxonomy document IDs and source DataIDs distinct. Resolver candidates gain source_document_id; native VSA candidates carry SourceDocumentID parsed from authoritative SQL graph properties. Native boost requires nonempty equal dataset IDs plus exact nonempty source IDs. Existing legacy route-ID matching remains compatible. Source authorization precedes ranking; a taxonomy binding grants no read permission. Do not overwrite graph source identity, publication generation or content revision.
- Freeze quality fixtures before comparison. Exercise graph completion strategies that invoke DCD; off and observe must have identical ordering/results, boost must improve an eligible bound case without increasing zero-result rate or admitting foreign/retired sources. Local native fixture evidence is reported separately from external corpus/model or hardware performance evidence. Default DCD mode remains unchanged.

## Risks / Trade-offs

- Private catalog semantics differ from the old draft's dataset-owner/team inheritance → document the resolved contract; shared catalogs need a separate audience design.
- Legacy duplicate names or malformed hierarchy → refuse ambiguous writes without repairing user data automatically.
- Global metadata lock and namespace scans can limit throughput → bounded seed and existing indexes first, measurements before new lock abstractions.
- Ranking hints can be stale → they never grant source access; retirement/revision gates remain authoritative. Imported document removal removes bindings only.
- Quality fixtures can accidentally bypass DCD → prove observe diagnostics and matching native source metadata in actual supported graph requests.

## Migration Plan

Add the manual journal to both schema inventories and exercise fresh/populated/repeated initialization in disposable native fixtures. Preserve existing taxonomy rows, IDs and routing defaults. Root generates REST/schema contract artifacts after source stabilization; no handwritten generated files. Rollback removes new application entry points while leaving existing taxonomy and journal data intact; no automatic DROP.
