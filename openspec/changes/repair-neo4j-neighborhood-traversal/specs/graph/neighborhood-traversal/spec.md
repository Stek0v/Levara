# Spec Delta

## Purpose

Return bounded graph neighborhood context from named seeds with reliable endpoint and type projection.

## ADDED Requirements

### Requirement: Bounded undirected neighborhoods
Names SHALL match case-insensitively. Nonpositive hops SHALL mean one; values above eight SHALL mean eight. Traversal SHALL include forward and reverse incident contexts of nodes within hops minus one of a seed.

#### Scenario: Depth and boundary
- **WHEN** A-B-C-D has an incoming X-A and a boundary C-Y relation
- **THEN** one hop includes A incidents, two includes B incidents and three includes C incidents, without premature boundary edges

#### Scenario: Clamps and cycles
- **WHEN** hop inputs are zero, negative or above eight and the graph has cycles
- **THEN** bounded semantics hold without duplicate source-ID/type/target-ID contexts

### Requirement: Reliable projection
Contexts SHALL use actual endpoints and preserve node types. Different same-name nodes SHALL remain distinct during deduplication. Output SHALL be deterministically ordered and capped at 100.

#### Scenario: Missing edge endpoint metadata
- **WHEN** a relationship has no endpoint properties
- **THEN** its actual nodes supply names and types

#### Scenario: Duplicate names
- **WHEN** multiple seed nodes share a name
- **THEN** each distinct node contributes its reachable contexts

### Requirement: Reader failure propagation
Cancellation and backend iteration errors SHALL return errors without lazy mutation. Empty input or missing seeds SHALL produce no contexts.

#### Scenario: Cancelled context
- **WHEN** the read context is cancelled
- **THEN** the adapter reports an error without altering stored data

#### Scenario: No seeds
- **WHEN** input is empty or no names match
- **THEN** no contexts are returned
