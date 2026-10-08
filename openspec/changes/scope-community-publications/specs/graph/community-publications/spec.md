# Spec Delta

## Purpose

Keep global community knowledge tied to the exact graph and source publications used to build it, with authorization through completed response delivery.

## ADDED Requirements

### Requirement: Global community admission
Authenticated community consumers SHALL require an active instance administrator without a selected tenant and with read permission. Caller-supplied actor or source hints MUST NOT broaden authority. Trusted anonymous local compatibility SHALL remain explicit.

#### Scenario: Selected tenant cannot read the global aggregate
- **WHEN** an administrator selects a tenant and calls either community search or list
- **THEN** no global summary is returned or submitted to a model

#### Scenario: Unscoped administrator and local compatibility
- **WHEN** an active administrator with no selected tenant reads a provable publication, or a trusted anonymous local caller uses legacy data
- **THEN** the compatible response shape is preserved

### Requirement: Response authority lifetime
Protected responses SHALL stop subsequent reads at the earlier request or verified credential expiry and retain SQL authority until actual response Close. SQL acquisition SHALL remain cancelable. Revocation or demotion before handoff MUST prevent protected text delivery.

#### Scenario: Expiry during partial delivery
- **WHEN** the credential expires after a partial body read
- **THEN** further reads fail and SQL authority remains retained until Close

#### Scenario: Canceled SQL acquisition
- **WHEN** a canceled request waits for the only SQL connection
- **THEN** acquisition terminates without protected bytes or a retained connection

### Requirement: Exact used input provenance
Verified communities SHALL preserve immutable detection inputs and exact source assertions for summary nodes, edges, both endpoints, and incorporated child summaries. A membership list MUST NOT substitute for source identity and publication version. Missing, malformed, unsupported or over-budget proof MUST fail closed for authenticated use.

#### Scenario: Mutable node loses its original source
- **WHEN** a node's current metadata changes after the aggregate was built
- **THEN** validation uses the recorded input proof and cannot adopt the new source to authorize old summary text

#### Scenario: One invalid source in a parent summary
- **WHEN** a source used by a child is retired, revised, unpublished or no longer authorized
- **THEN** the parent summary cannot reach authenticated output or a model

### Requirement: Atomic current publication
Rebuilds SHALL publish memberships, summaries, generation and proof atomically on both SQL backends after proving inputs are still current. Any SQL error or cancellation MUST preserve the prior publication. A late build with stale inputs MUST NOT replace current knowledge.

#### Scenario: SQL failure in replacement
- **WHEN** a later membership or summary insert fails
- **THEN** all replacement effects roll back and the previous publication remains unchanged

#### Scenario: Concurrent source change
- **WHEN** a source changes while summarization is running
- **THEN** stale work cannot be published or accepted by a consumer

### Requirement: SQL authoritative summary retrieval
Vector summary hits SHALL resolve a matching current SQL community generation and validated dependencies before their text is used. Legacy unverified rows MUST NOT become verified automatically. Missing LLM or embeddings SHALL degrade explicitly without publishing partial success.

#### Scenario: Old vector survives a rebuild
- **WHEN** a vector hit references a missing community or old generation
- **THEN** its summary is ignored before model or response use

#### Scenario: Embedding fails after publication
- **WHEN** embeddings fail after an otherwise successful SQL publication
- **THEN** SQL remains authoritative and stale vectors cannot impersonate the new generation
