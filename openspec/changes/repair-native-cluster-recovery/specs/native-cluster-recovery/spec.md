## ADDED Requirements

### Requirement: Native Raft constructor and quorum recovery

The native Raft constructor SHALL use valid lease/heartbeat settings and successful writes SHALL require the configured quorum.

#### Scenario: Independent voters recover after leader death

- **WHEN** the acknowledged leader is killed and two voters remain
- **THEN** a new leader can acknowledge writes and the restarted original root catches up

#### Scenario: Quorum unavailable

- **WHEN** only one of three voters remains
- **THEN** it does not report successful acknowledgement; uncertain timeout outcomes are resolved through later acknowledged state

### Requirement: HTTP snapshot and stream continuity

Every supported HTTP replica connection SHALL begin with a versioned snapshot/watermark fenced with primary mutations and its own listener.

#### Scenario: Offline insert and delete

- **WHEN** primary records change while a replica is disconnected
- **THEN** reconnect restores exact current records without rebroadcast and native WAL reopen retains them

#### Scenario: Admission races and stream faults

- **WHEN** writes overlap connection admission or a stream overflows, has malformed data, skips a sequence, or fails local application
- **THEN** no successful recovery silently skips history and the client retries from a new valid snapshot

### Requirement: Supported deployment boundary

HTTP replication SHALL explicitly reject unsupported multi-shard bootstrap rather than snapshot only a subset.

#### Scenario: Unsupported shard count

- **WHEN** HTTP primary or replica replication is selected with multiple shards
- **THEN** configuration fails before announcing supported replication readiness


### Requirement: Safe native append boundary after interruption

Startup SHALL validate the complete WAL prefix before rebuilding metadata and SHALL repair only a structurally feasible incomplete final physical frame. Public recovery readers SHALL remain nonmutating.

#### Scenario: Interrupted apply followed by acknowledged continuation

- **WHEN** a child process stops inside a native WAL frame and is killed
- **THEN** independent reopen preserves the acknowledged prefix and a new acknowledged write survives another independent reopen

#### Scenario: Visible partial corruption

- **WHEN** available operation, length, vector or location bytes cannot form a valid native frame
- **THEN** startup fails without modifying WAL or metadata

### Requirement: Native writer and recovery admission agree

Native insert, batch and snapshot admission SHALL require nonempty IDs, IDs and serialized metadata at most 1 MiB, and finite vectors of the configured dimension before changing durable metadata.

#### Scenario: Exact native format limits

- **WHEN** both ID and metadata are exactly 1 MiB
- **THEN** acknowledged insert, batch, snapshot and checkpoint remain readable after native reopen

#### Scenario: Invalid or legacy incompatible inventory

- **WHEN** a new record violates admission limits or startup encounters a historical empty-ID or oversized frame
- **THEN** it fails without silently discarding that record or modifying durable files


### Requirement: Snapshot and checkpoint source failures preserve recoverable data

HTTP and Raft snapshot capture, native checkpoint and production rebuild SHALL reject incomplete source reads before publishing replacement state. Legitimate zero-length metadata SHALL remain supported.

#### Scenario: Unreadable nonempty metadata

- **WHEN** the source metadata file is truncated while its original WAL retains acknowledged metadata
- **THEN** snapshot capture and checkpoint fail, the prior replica inventory and source WAL remain intact, and native reopen recovers original metadata

#### Scenario: Legitimate empty metadata

- **WHEN** a record has zero-length metadata
- **THEN** snapshot capture succeeds and native checkpoint/reopen preserves it


### Requirement: Native batch metadata is independent of listener presence

DirectNode batch insertion SHALL preserve native BatchInsert metadata serialization with or without replication listeners.

#### Scenario: Metadata parity across listener admission

- **WHEN** batch values include JSON object/array strings, bytes, RawMessage, maps or ordinary strings
- **THEN** primary, real HTTP replica and native reopen preserve the same serialized bytes as native BatchInsert

#### Scenario: Deterministic marshal failure

- **WHEN** batch metadata cannot be marshaled
- **THEN** that record produces no data or replication event and does not invalidate a healthy listener
