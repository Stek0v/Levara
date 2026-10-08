# Notebook sunset

The optional notebook execution surface and WebUI page are retired. All ten former CRUD/cell/run operations are unregistered and return404, including the legacy run alias. Every old LEVARA_NOTEBOOKS value is ignored without a startup error and cannot restore execution.

## Retained history

Existing notebooks and notebook_cells tables, indexes, foreign keys and stored fields remain unchanged in SQLite and PostgreSQL. No DROP, data backfill or deletion is part of this change. The main memory, search and workspace workflows are unchanged. There is no new public legacy read/export endpoint.

## Administrative recovery

Use the existing verified local backup tooling on an offline owned instance, retaining SQL inventory and stored notebook/cell relationships. `levara-backup verified` produces an archive and independently restores/verifies it in fresh temporary resources; `levara-backup verify --input ARCHIVE` repeats that verification. The operator must keep access to the archive and its receipt. Restoration to a separately prepared destination follows existing administrative procedures; the retired HTTP/UI execution interface is not restored by setting an environment variable.

Acceptance requires populated histories with distinct owners, cell types, source/output, order and timestamps to compare exactly after repeated schema initialization and actual backup→verify→restore. Evidence is recorded in the retire-notebook-execution-surface change after the commands run; this guide itself is not a passing receipt.

## Release boundary

The historical F1 plan requested at least one published release with notebooks off. No evidence of that published interval was found. The current T31 sunset contract explicitly replaces that planning wait with retained SQL history and the mandatory preservation/recovery gates above; it does not claim the historical interval occurred. Production deployment or publication is a separate authorized action.
