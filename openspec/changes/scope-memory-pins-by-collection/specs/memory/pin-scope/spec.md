# Spec Delta

## Purpose

Let agents change a project's wake-up briefing without modifying memories with the same key in another project, while retaining explicitly documented legacy selection behavior.

## ADDED Requirements

### Requirement: Exact nonempty collection selection
Pin and unpin SHALL apply a nonempty collection selector to the mutation as an exact match. A request SHALL NOT change another owner's rows; the existing caller-plus-shared ownership rule SHALL remain in force.

#### Scenario: Identical keys across collections and owners
- **WHEN** Alice pins or unpins key K in collection A and K also exists for Alice/shared in B and for Bob in A
- **THEN** only Alice/shared rows for K in A change, with all other rows retaining their pin, priority, and timestamp

#### Scenario: Unknown selected collection
- **WHEN** a valid request selects a collection with no matching accessible row
- **THEN** pin returns its existing missing-match error and unpin returns its existing idempotent success without changing any row

### Requirement: Compatible absent and empty selectors
An omitted or empty collection selector with no session default SHALL retain historical key-and-owner selection. A legacy session default SHALL select its collection; an explicit nonempty selector SHALL override that default.

#### Scenario: Legacy session default
- **WHEN** a legacy session has collection A selected and calls pin or unpin without a nonempty collection
- **THEN** the operation changes matching accessible rows in A only

#### Scenario: Explicit override
- **WHEN** a legacy session defaults to A but the request selects B
- **THEN** the operation changes matching accessible rows in B only

#### Scenario: No session default
- **WHEN** a request has no default and omits collection or supplies an empty string
- **THEN** historical key-and-owner selection is preserved across collections

### Requirement: Malformed selectors cannot widen a mutation
A present collection value that is not a string SHALL produce an error before any row changes, including null, number, array, and object values.

#### Scenario: Invalid collection type
- **WHEN** pin or unpin receives a nonstring collection value
- **THEN** the response is an error and every memory row remains unchanged

### Requirement: Discoverable transport-compatible selector
Both pin tools SHALL advertise an optional string collection selector. Their result schemas, existing required key, default pin priority, and tool-profile visibility SHALL remain compatible. Stateless MCP SHALL use the explicit selector without relying on a legacy session.

#### Scenario: Descriptor and stateless invocation
- **WHEN** a client discovers either tool and invokes it on stateless MCP with collection A
- **THEN** discovery declares the optional string selector, the invocation affects A only, and the response matches the existing status/error contract

#### Scenario: Default priority and repeated unpin
- **WHEN** a selected memory is pinned without priority, then unpinned twice
- **THEN** pin uses priority 1 and both unpin calls succeed with the selected rows unpinned at priority 0

### Requirement: Equivalent SQL outcomes
SQLite and PostgreSQL SHALL produce equivalent selection, mutation, error, and no-effect results for these scenarios. A database failure SHALL remain an error rather than successful evidence.

#### Scenario: Both persistence backends
- **WHEN** the same scoped, legacy, foreign-owner, absent-row, and malformed-selector cases execute on either backend
- **THEN** the same rows change and the same success/error decisions are returned
