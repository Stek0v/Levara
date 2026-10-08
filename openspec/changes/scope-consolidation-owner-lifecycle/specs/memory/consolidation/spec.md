# Spec Delta

## Purpose

Consolidate redundant memories without crossing private namespaces, losing classification or overwriting later changes, with reversible SQL effects and honest asynchronous outcomes.

## ADDED Requirements

### Requirement: Verified consolidation namespace
Consolidation SHALL select only the verified caller's exact owner namespace and explicit collection. Explicit shared selection MUST require live administrator or trusted-local authority. Identity arguments MUST NOT grant authority.

#### Scenario: Forged identity and mixed owners
- **WHEN** a caller supplies forged owner/actor arguments while private, foreign and shared memories occupy one collection
- **THEN** only the caller's private memories are candidates, including for dry runs, and foreign text never reaches providers

#### Scenario: Explicit shared selection
- **WHEN** a caller selects shared memories
- **THEN** a live administrator or trusted-local caller can process the empty-owner namespace and an ordinary caller receives an error without effects

### Requirement: Homogeneous classification
Consolidation SHALL group only memories with equal owner, collection, type, room and hall, and derived abstracts SHALL retain that classification. Invalid legacy halls MUST cause abstraction to skip before provider use.

#### Scenario: Cross-axis similarity
- **WHEN** similarity edges join records differing on any classification axis
- **THEN** those edges cannot cause mixed clusters or mixed provider inputs

#### Scenario: Legacy hall
- **WHEN** an abstraction cluster contains an empty or unknown hall
- **THEN** the result reports a skip, no abstract is written, and no summarizer is called

### Requirement: Guarded atomic apply
Apply SHALL validate captured source and survivor identities, complete row state, active status and classification under live authority before mutation. Memory changes, run evidence and configured derived-index effects MUST commit atomically.

#### Scenario: Stale or forged plan
- **WHEN** a source/survivor is missing, changed, newly pinned, retired, foreign or absent from captured candidates
- **THEN** apply fails without partial retirement, abstract, journal or outbox effects

#### Scenario: Transaction failure or revoked credential
- **WHEN** a constraint, index enqueue or live credential check fails
- **THEN** all authoritative changes roll back

#### Scenario: Missing required index outbox
- **WHEN** vector indexing is configured but its SQL outbox is unavailable
- **THEN** consolidation reports a diagnostic without committing untracked index effects

### Requirement: Safe reversible run
Revert SHALL authorize the run namespace and validate every affected row against its persisted after-state before restoring retirement fields or deleting generated abstracts. It MUST preserve unrelated later edits and provide idempotent authorized repeated revert.

#### Scenario: Mixed merge and abstraction
- **WHEN** an unchanged journaled run is reverted
- **THEN** source retirement fields are restored, generated abstracts are deleted, the merge survivor is retained and derived-index effects are atomic

#### Scenario: Changed survivor or provenance
- **WHEN** any recorded row changes after apply, including pins or provenance without updated timestamp changes
- **THEN** revert fails with no partial effects

#### Scenario: Foreign or legacy run
- **WHEN** a caller requests another owner's run or a run without valid journal evidence
- **THEN** revert returns an explicit error without exposing or changing that run

### Requirement: Verified detached execution
Asynchronous consolidation SHALL retain verified request authority while using a bounded independent deadline. Each pending job MUST be claimed once, and status SHALL be visible only to its exact submitting owner.

#### Scenario: Request cancellation
- **WHEN** the HTTP request ends after enqueue
- **THEN** detached work retains the original verified actor and cannot broaden its namespace

#### Scenario: Duplicate runner and foreign status
- **WHEN** multiple runners claim one pending job or another user requests its status
- **THEN** only one runner performs consolidation and the other user receives no job data

### Requirement: Honest recovery and maintenance
Recovery SHALL close reads before writes and MUST NOT reconstruct credentials from saved arguments or owner IDs. Interrupted authenticated jobs SHALL report unknown outcome and require resubmission. Trusted-local maintenance MUST isolate owner namespaces.

#### Scenario: Authenticated restart with one connection
- **WHEN** pending or running jobs are recovered without live actor proof on a single-connection SQL pool
- **THEN** recovery returns without deadlock, does not execute private consolidation and records an unknown-outcome/resubmit failure

#### Scenario: Local maintenance across owners
- **WHEN** trusted-local maintenance processes multiple owner namespaces
- **THEN** no cluster, provider input or abstract crosses those namespaces

### Requirement: Dialect and transport parity
The lifecycle SHALL preserve SQLite/PostgreSQL parity and expose accurate MCP schemas and errors through direct and HTTP execution.

#### Scenario: Both persistence backends
- **WHEN** the same authorized, stale-plan, rollback and async scenarios run on SQLite and PostgreSQL
- **THEN** their authoritative outcomes agree and no unavailable-service skip counts as acceptance
