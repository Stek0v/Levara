# Spec Delta

## Purpose

Preserve immutable task evidence and recovery state across retries, competing processes and completion checks on authoritative native SQL storage.

## ADDED Requirements

### Requirement: Exact request replay
Receipt and checkpoint admission SHALL return the original ID and current task version for an exact normalized request replay, without changing authoritative ledger state.

#### Scenario: Retry after lost response
- **WHEN** an authorized caller retries the same normalized request and key with a stale base version
- **THEN** the original ID and current version are returned and no events or checkpoint effects are added

### Requirement: Changed request rejection
Receipt and checkpoint admission SHALL reject the same key with different normalized evidence, identity or checkpoint effects as an explicit idempotency conflict.

#### Scenario: Evidence or recovery-state mismatch
- **WHEN** status, revision, digest, criteria, observation, actor, step, blocker or candidate data differs for an existing key
- **THEN** admission fails and all ledger rows remain unchanged

#### Scenario: Concurrent conflicting requests
- **WHEN** different requests compete for one key
- **THEN** at most one payload is persisted and the loser cannot report its different payload as accepted

### Requirement: Historical replay safety
Historical evidence SHALL remain readable, but replay without stored request identity SHALL fail explicitly instead of claiming unprovable equivalence.

#### Scenario: Legacy digest absent
- **WHEN** a request reuses a historical receipt or checkpoint key without a stored digest
- **THEN** admission reports an unverifiable idempotency conflict and preserves historical bytes

### Requirement: Native lease exclusivity
Independent processes SHALL have at most one live claim winner. Natural expiry and reclaim SHALL prevent the former lease actor from mutating execution evidence.

#### Scenario: Winner dies
- **WHEN** the winning process is killed and its persisted lease subsequently expires naturally
- **THEN** a new process reclaims once and the stale actor cannot renew, pass or record an execution receipt

### Requirement: Actual artifact verification
Completion SHALL reject evidence whose actual authorized artifact bytes no longer match the immutable receipt.

#### Scenario: Independent artifact replacement
- **WHEN** another process replaces artifact bytes after a passing receipt
- **THEN** validation and completion fail without changing task, receipt or promotion state

### Requirement: Retained completion guards
Completion SHALL retain authority and cooperating workspace artifact guards through commit or rollback, with bounded cancellation and credential expiry.

#### Scenario: Writer arrives after artifact hash
- **WHEN** a cooperating process attempts to replace workspace evidence after its real hash has passed
- **THEN** it cannot replace those bytes before completion commits or rolls back

#### Scenario: Completion authority ends
- **WHEN** completion is cancelled or its credential expires
- **THEN** task and promotion state remain unchanged and SQL/project guards are released

#### Scenario: Terminal replay under revoked authority
- **WHEN** an otherwise unexpired credential has been revoked before a completed-task replay
- **THEN** the replay fails admission without changing immutable task state
