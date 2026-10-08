# Spec Delta

## Purpose

Keep workspace search projections aligned with authoritative files across batch failures, job recovery, watcher restarts and garbage collection.

## ADDED Requirements

### Requirement: Publish complete batches
The system SHALL expose a requested generation update only after every selected file and the authoritative manifest publication succeed, preserving the previous searchable state on failure.

#### Scenario: Later file or persistence fails
- **WHEN** the second file fails reading, embedding or vector insertion, or manifest persistence fails
- **THEN** prior active membership and file inventory remain authoritative and prepared records cannot enter search context

#### Scenario: Same logical generation is updated
- **WHEN** an active generation is reindexed with unchanged bytes but changed chunking
- **THEN** prepared records do not overwrite active records before successful publication

### Requirement: Bind workspace search eligibility
The system SHALL admit workspace records into generic search context only when their actual collection and identity match active authoritative manifest membership in every supported mode. Explicit retained historical workspace lookup SHALL preserve existing stale-citation behavior under exact server-verified project, branch, generation and collection scope, with fresh manifest and ACL checks through model and response transfer.

#### Scenario: Unpublished or wrong collection result
- **WHEN** local or authenticated search receives pending, retired or wrong-collection workspace records
- **THEN** those records are excluded before model context or citations

#### Scenario: Explicit retained historical lookup
- **WHEN** an authorized caller selects a retained published old workspace generation
- **THEN** only exact requested historical membership appears with stale citations through actual REST and MCP response transfer, while subsequent generic search remains active-only

### Requirement: Preserve file inventory and deletion scope
The system SHALL record committed file digests even for zero-chunk files, distinguish unknown legacy inventories from committed empty inventories, and apply missing-file deletion only within the requested scope.

#### Scenario: Empty file or last file deletion
- **WHEN** reconciliation processes whitespace-only files or an empty branch
- **THEN** successful inventory matches files and deleted records lose eligibility

#### Scenario: Selected path reconciliation
- **WHEN** missing-file deletion is requested for selected paths
- **THEN** unrelated committed paths remain indexed

### Requirement: Recover jobs under current admission
The system SHALL reload job state under cooperative cross-process project locking before recovering or executing work and SHALL preserve credential, target, retry and terminal-state admission.

#### Scenario: Concurrent live worker
- **WHEN** recovery waits behind another process that completes the job
- **THEN** recovery reloads the terminal state and does not overwrite or duplicate it

#### Scenario: Expired retained credential
- **WHEN** a pending or running job has expired authority
- **THEN** recovery cannot authorize new indexing effects from that credential

### Requirement: Recover and coalesce watcher work
The system SHALL retain pending branch work until committed inventory matches current files, detect offline content edits on restart, and coalesce outstanding branch jobs.

#### Scenario: Offline same-size edit
- **WHEN** a stopped watcher observes a changed file with preserved size and modification time after restart
- **THEN** content digests trigger reconciliation

#### Scenario: Blocked job and further edits
- **WHEN** repeated scans occur while a branch job is pending or running and files change again
- **THEN** outstanding jobs remain bounded and a subsequent reconciliation includes the final edits

#### Scenario: Failed branch recovered by new publication
- **WHEN** an old branch job failed and a complete current inventory is successfully published active
- **THEN** the old failure is marked superseded without rewriting its history and watcher can advance; inactive or partial publication cannot disable its retry, and explicit retry clears the marker

### Requirement: Retire exact derived records safely
The system SHALL retire only recorded obsolete vector IDs, preserve active records across projects sharing a collection, and retain cleanup failures for idempotent retry.

#### Scenario: Shared collection GC
- **WHEN** GC removes an old generation in one project
- **THEN** another project's active records and authoritative files remain intact

#### Scenario: Retirement failure
- **WHEN** vector cleanup fails after successful manifest publication
- **THEN** published state remains authoritative and cleanup can be retried without deleting current IDs
