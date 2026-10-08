# Manual taxonomy import

The catalog is private to the verified caller and exact selected tenant (including no tenant), inside an explicit dataset. Import/remove require dataset write access; listing requires read access. A document binding additionally requires read access to its source in that dataset. For a nonempty selected tenant, the dataset owner must belong to that tenant before dataset grants are considered. Catalog hints never grant source access. Administratively managed sharing of personal chats is a separate audience contract.

## Seed grammar

UTF-8 input is limited to 1 MiB and 1000 domain/collection/document nodes. Leading BOM, CRLF and blank lines are accepted. Other nonblank lines must follow this grammar:

```markdown
# Domains
## auth
Описание: Authentication and session lifecycle
Алиасы: authentication, login
### Collections
#### sessions
Описание: Session documents
##### Документ: Session policy
Источник: existing-source-data-id
Алиасы: session rules
```

`# Domains` is required once. Domains contain an optional `### Collections` section; collections belong to the current domain. Documents belong to the current collection. Each node accepts optional `Описание:` and comma-separated `Алиасы:` once. Documents require exactly one `Источник:`. Duplicate siblings after Unicode lowercasing and trimming, unsupported fields, control characters and malformed hierarchy are rejected before any mutation. Shared domain aliases are warnings. Imported source IDs and taxonomy document IDs remain distinct.

## CLI

Use the existing CLI server/auth configuration:

```sh
levara taxonomy import seed.md --dataset DATASET_ID --revision v1
levara taxonomy list --dataset DATASET_ID
levara taxonomy remove auth --dataset DATASET_ID --collection sessions
levara taxonomy remove auth --dataset DATASET_ID --force
```

Removal can select `--collection` and then `--document`; document selection requires a collection. Nonempty domain/collection removal requires `--force`. Removal deletes taxonomy bindings only, preserving source documents and graph data. Missing targets return zero removals. Failed HTTP requests exit unsuccessfully.

## REST and transaction reports

- POST `/datasets/:id/taxonomy/import`: `seed`, optional `source_name`, `source_revision`, `request_id`. Result: `run_id`, `domains`, `collections`, `documents`, `created`, `updated`, `warnings` (array).
- GET `/datasets/:id/taxonomy`: nested `domains` array with collections and documents; an empty catalog returns `{"domains":[]}`.
- DELETE `/datasets/:id/taxonomy`: `domain`, optional `collection`, `document`, `force`, `request_id`. Result: `run_id`, `removed`.

Unknown body fields are rejected. Owner/tenant come from verified authentication. Import is additive: existing natural-key IDs remain stable, declared fields update, absent nodes remain until explicit removal. The whole seed and a content-free journal report commit together. The journal stores request hashes, source label/revision and reports, never raw seed/source bytes. Replaying a supplied request ID requires identical action and body hash; a mismatch conflicts. Omitted request IDs create fresh runs. Errors distinguish invalid input (400), unauthenticated (401), denied (403), conflict (409) and unavailable storage (503).

Native DCD boost matches authorized dataset and source identity. Off/observe ordering is preserved and the routing default remains unchanged. Automatic proposals, cognify generation and shared catalog audiences are outside this manual lifecycle.
