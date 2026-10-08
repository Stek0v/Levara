# Spec Delta

## Purpose

Preserve graph episode history across retries, target changes and concurrent dataset-scoped writes.

## ADDED Requirements

### Requirement: Current episode identity
The writer SHALL preserve active episode ID/start on tuple retries and create a fresh episode when returning to a closed target.

#### Scenario: Return and retry
- **WHEN** an exclusive relation changes A to B to A and the last A is retried
- **THEN** three historical episodes remain and the retry preserves the latest identity/start

#### Scenario: Legacy retry
- **WHEN** a matching active relationship lacks ID/start
- **THEN** it acquires a stable ID and epoch start without a second active episode

### Requirement: Scoped transitions
Exclusive transitions SHALL close competing targets only for the same source, dataset and case-insensitive relation. Nonexclusive targets SHALL coexist.

#### Scenario: Dataset and case isolation
- **WHEN** mixed-case exclusive writes occur in two datasets alongside nonexclusive targets
- **THEN** each dataset has one current exclusive target and nonexclusive targets remain

### Requirement: Historical preservation
Explicit closed imports SHALL preserve bounds without superseding current assertions. Closed IDs SHALL never reopen or overwrite history.

#### Scenario: Historical retry
- **WHEN** an identical closed ID is retried
- **THEN** identity, envelope, evidence and successor remain unchanged

#### Scenario: Conflicting history
- **WHEN** a closed ID is reused with conflicting tuple, bounds or evidence
- **THEN** the whole batch fails without effects

### Requirement: Valid temporal inputs
Integral numbers, integer strings and RFC3339 timestamps SHALL normalize to Unix seconds. Invalid bounds or transitions predating predecessors SHALL fail atomically.

#### Scenario: Valid representations
- **WHEN** supported representations supply bounds
- **THEN** numeric Unix seconds are persisted

#### Scenario: Invalid bounds
- **WHEN** bounds are malformed, fractional, nonfinite, overflowing, reversed or backdated
- **THEN** the whole batch fails without effects

### Requirement: Atomic concurrency
Overlapping endpoint writes SHALL serialize. Missing endpoints and driver failures SHALL roll back the complete batch with zero success counts.

#### Scenario: Missing endpoint
- **WHEN** node updates accompany an edge with a nonexistent endpoint
- **THEN** all node and edge effects roll back

#### Scenario: Disjoint identical ID requests
- **WHEN** independent endpoint batches concurrently request the same explicit ID for current or closed imports
- **THEN** globally unique current IDs and immutable closed history are preserved, and internal identity reservations do not appear in graph reads

#### Scenario: Concurrent batches
- **WHEN** edge-only or crossed batches compete for overlapping endpoints
- **THEN** no duplicate current exclusive episode or partial batch remains

### Requirement: Internal identity namespace
Caller node labels SHALL NOT enter the internal episode identity namespace. Such input SHALL fail before effects and reservation nodes SHALL remain absent from graph reads.

#### Scenario: Reserved caller label
- **WHEN** a caller supplies the internal identity label as a node type
- **THEN** the whole batch fails with zero effects and no identity orphan

### Requirement: Adapter identity
The adapter SHALL project separate edge IDs and reject conflicting property IDs.

#### Scenario: Explicit ID
- **WHEN** an adapter edge has a separate ID
- **THEN** the relationship preserves it unless a new current episode must avoid an occupied ID

#### Scenario: Conflicting IDs
- **WHEN** separate and property IDs differ
- **THEN** the batch fails before writes
