# Spec Delta

## Purpose

Retire legacy notebook execution permanently while preserving stored notebook history for administrative backup and recovery.

## ADDED Requirements

### Requirement: Notebook execution surface stays retired

All ten legacy notebook REST operations SHALL remain unregistered, returning the existing absent-route 404 behavior. The WebUI notebook page SHALL be absent. Legacy LEVARA_NOTEBOOKS values MUST NOT re-enable execution or fail startup.

#### Scenario: Old clients and enabled flags

- **WHEN** an old client requests notebook CRUD, cell CRUD or either run alias with any historical flag value including 1
- **THEN** each operation returns 404 and executes no notebook command or storage mutation

### Requirement: Stored history survives retirement

SQLite and PostgreSQL MUST retain notebook and cell tables, foreign keys, indexes and every existing field, including owner, source, output, ordering and timestamps. Retirement MUST NOT add a deletion, backfill or public read/export endpoint.

#### Scenario: Populated database is reopened and migrated

- **WHEN** an existing populated database is migrated again and old notebook requests are made
- **THEN** complete notebook and cell rows, indexes and foreign-key behavior remain unchanged

### Requirement: Administrative recovery is verified

Acceptance MUST include an actual backup, verification and restore of a populated disposable store through the supported administrative tooling, followed by complete notebook and cell row comparisons. A local artifact MUST NOT count as a published release interval.

#### Scenario: Populated history is restored

- **WHEN** an administrator backs up, verifies and restores a populated disposable store
- **THEN** restored owners, contents, outputs, cell ordering and timestamps equal the original history
