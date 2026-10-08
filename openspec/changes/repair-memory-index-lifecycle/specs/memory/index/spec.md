# Spec Delta

## Purpose

Keep derived memory vectors consistent with authoritative SQL retirement and restoration, so indexed recall remains usable after reversible consolidation.

## ADDED Requirements

### Requirement: Repeated publication is claimable
The system SHALL make an identical completed publication claimable when authoritative state requests publication again, while preserving deduplication of pending/running work and rejecting stale claim finalization.

#### Scenario: Restore previously indexed source
- **WHEN** a source vector publication completed, consolidation retired the source, and revert requests the same source digest again
- **THEN** the publication becomes pending and a worker can restore its vector

#### Scenario: Duplicate enqueue before completion
- **WHEN** the same publication is enqueued while pending or running
- **THEN** it retains one job and an active claim is not replaced

#### Scenario: Old claim finishes after reopen
- **WHEN** an obsolete completed claim attempts finalization after republication has been claimed
- **THEN** it cannot alter the new claim's status, attempts or retry deadline

#### Scenario: Revert transaction rolls back
- **WHEN** restoration and requeue occur inside a transaction that fails
- **THEN** both SQL restoration and the job-state change roll back

### Requirement: Vector effects use current SQL truth
The system SHALL publish or delete vectors only when current SQL state permits that effect in the affected namespace. The check and actual effect SHALL be protected from concurrent authoritative changes, including caller cancellation until the effect returns.

#### Scenario: Delayed delete after restoration
- **WHEN** a retirement delete arrives after its source is restored and reindexed
- **THEN** the active source vector remains available

#### Scenario: Delete races with restore
- **WHEN** restoration competes with an already authorized vector deletion
- **THEN** the operations are ordered so the restored source can be republished without a later obsolete delete removing it

#### Scenario: Delayed embedding after source changes
- **WHEN** the source retires or its content or namespace/classification changes during embedding
- **THEN** the obsolete result is not published for that source

#### Scenario: SQL check fails
- **WHEN** the authoritative check cannot execute or acquire its bounded protection
- **THEN** no vector effect occurs and the existing retry mechanism records the failure

#### Scenario: Migration hook network stays outside protection
- **WHEN** memory publication invokes an enabled migration dual-write hook
- **THEN** its network embedding does not hold authoritative memory SQL protection, and a source retired or changed during that embedding is not published into the shadow collection

#### Scenario: Dual-write metadata owner presence
- **WHEN** memory dual-write metadata supplies an owner field
- **THEN** it must be a string exactly equal to current SQL owner, including empty shared owner; null and non-string values are rejected before embedding
- **AND** omitted legacy owner metadata remains supported with current SQL owner included in the captured post-embedding predicate

### Requirement: Reversible consolidation supports indexed recall
The system SHALL preserve scoped indexed recall through apply and revert, without allowing lexical fallback to count as proof of vector publication.

#### Scenario: Abstract then restore
- **WHEN** sources are indexed, consolidation abstracts them, workers drain the committed effects, and the run is reverted
- **THEN** actual source/generated vector presence and a vector-only query show the abstract before revert and restored sources after revert, with foreign namespaces excluded

#### Scenario: Repeat and recover
- **WHEN** identical republication is repeated or an interrupted job is recovered
- **THEN** workers converge on current SQL truth without resurrecting retired records or deleting restored active vectors
