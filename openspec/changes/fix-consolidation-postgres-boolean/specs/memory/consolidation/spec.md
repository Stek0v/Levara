# Spec Delta

## Purpose

Make memory consolidation SQL changes atomic and reversible on both supported SQL backends.

## ADDED Requirements

### Requirement: Abstract records are unpinned on both SQL backends

Consolidation SHALL create its generated abstract record with pin disabled on SQLite and PostgreSQL.

#### Scenario: Mixed merge and abstract apply

- **WHEN** a valid run applies a merge and an abstract action
- **THEN** both changes are committed and the generated abstract is unpinned

### Requirement: Failed apply does not leave a partial run

Consolidation SHALL roll back preceding mutations if a later action fails during the same apply.

#### Scenario: Abstract insertion fails after a merge

- **WHEN** a merge succeeds inside the transaction and the later abstract INSERT fails
- **THEN** no source retirement or generated record from the failed run is committed

### Requirement: Revert restores the SQL memory set

Reverting an applied run SHALL reactivate its retired sources and remove its generated abstract atomically.

#### Scenario: Revert mixed applied run

- **WHEN** the mixed merge/abstract run is reverted
- **THEN** the original sources are active, its abstract is absent and unrelated control rows are unchanged
