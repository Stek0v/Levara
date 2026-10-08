# Proposal

## Why

Original T23 requires native independent-process replication and quorum recovery. Actual production Raft construction rejects its inherited lease timeout; HTTP replication loses offline writes and snapshot-to-stream writes.

## What Changes

- Align native Raft lease with heartbeat using the installed library.
- Recover HTTP replicas from an ordered snapshot and watermark on the same connection at every reconnect.
- Serialize native primary mutation, snapshot admission and successful fanout; invalidate overflowed streams.
- Prove independent roots, process death, persisted recovery, quorum loss, restart and snapshot restart.
- Preserve experimental status until supported native gates pass; explicitly reject unsupported multi-shard HTTP replication.

## Capabilities

### New Capabilities

- `native-cluster-recovery`: bounded native process and HTTP recovery correctness.

### Modified Capabilities

None.

## Impact

Existing cluster constructor, direct mutation and replication paths, server configuration validation and focused tests. No new dependencies, consensus framework, production restart or deployment.
