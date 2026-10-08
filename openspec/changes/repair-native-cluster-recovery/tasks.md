## 1. Native baseline

- [x] 1.1 Publish and run separate-process constructor and HTTP missing-history/admission regressions; preserve observed failures.
- [x] 1.2 Repair native Raft lease and prove constructor/quorum/process restart checks under race.

## 2. HTTP correctness

- [x] 2.1 Implement same-stream snapshot/watermark, contiguous validated apply and reconnect recovery.
- [x] 2.2 Fence DirectNode mutations and fanout; handle overflow, duplicate listener cleanup and partial batch failure. Preserve native batch metadata independently of listeners.
- [x] 2.3 Reject unsupported multi-shard HTTP bootstrap and update exact compatibility documentation.
- [x] 2.4 Prove independent-process gaps, persisted WAL reopen and focused protocol corner cases.

## 3. Native recovery safety

- [x] 3.1 Validate WAL before metadata rebuild; repair only feasible partial final writes and prove real interrupted apply followed by acknowledged continuation and another independent reopen. Align writer admission and exact format limits.
- [x] 3.2 Use checked inventory for HTTP/Raft snapshot, checkpoint and rebuild; preserve recoverable nonempty metadata on source failure and support legitimate empty metadata.

## 4. Acceptance

- [x] 4.1 Run native cluster/server/backup, integration/contracts and strict checks on frozen final source; independent review.
- [x] 4.2 Record receipts/issue ledger, experimental boundaries and original T23 status.
