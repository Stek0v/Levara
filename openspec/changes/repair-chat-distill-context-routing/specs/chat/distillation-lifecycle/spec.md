# Spec Delta

## Purpose

Keep synchronous chat distillation bounded by its caller and route resulting memories to the collection selected for the request.

## ADDED Requirements

### Requirement: Caller cancellation bounds synchronous work
Distillation SHALL retain the caller's earlier deadline and cancellation through model, SQL and derived indexing work, and MUST reject late results after cancellation.

#### Scenario: Cancellation while the model is running
- **WHEN** the caller cancels while the model is processing a nonempty transcript
- **THEN** the model observes cancellation and no subsequent candidate is saved

#### Scenario: Provider returns a late result
- **WHEN** the provider returns candidates or an empty result after caller cancellation
- **THEN** distillation returns an error and does not publish a successful preview or new candidate

#### Scenario: Cancellation during indexing
- **WHEN** SQL is already committed and the caller cancels during derived indexing
- **THEN** indexing observes cancellation and no later vector publication or candidate save begins
- **AND** already committed SQL remains authoritative

### Requirement: Collection routing matches session context
Explicit nonempty collection SHALL win over the session default. Omitted or empty collection SHALL use the session default when present and retain the empty legacy memory namespace otherwise.

#### Scenario: Legacy session default
- **WHEN** an authenticated legacy client selects a session collection and distills without an explicit collection
- **THEN** resulting memories are stored in that collection

#### Scenario: Latest stateless transport
- **WHEN** an authenticated latest client supplies an explicit collection
- **THEN** the resulting memories use that collection without introducing a server session or set_context support

#### Scenario: Explicit and stateless controls
- **WHEN** an explicit collection is supplied or the session has no default
- **THEN** the explicit collection wins or the empty legacy namespace is retained respectively

### Requirement: Empty content does not invoke a model
A missing, zero-message or whitespace-only transcript SHALL return an error before model invocation and leave memories unchanged. Existing rendering of nonempty content SHALL remain compatible.

#### Scenario: Whitespace-only messages
- **WHEN** all imported message content is whitespace
- **THEN** no model call or memory write occurs

### Requirement: Existing evidence and result contracts remain compatible
Successful distillation SHALL keep canonical identity and ordinary transcript provenance while assigning unverified evidence with empty Task and receipt references. Dry-run SHALL not write and SHALL retain its existing preview shape.

#### Scenario: Existing reset and preview regressions
- **WHEN** a valid transcript is distilled or previewed
- **THEN** the existing evidence-reset, canonical upsert and preview controls pass on both SQL dialects
