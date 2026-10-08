# T28 acceptance — 2026-10-07

Original T28 is accepted for the supported local sandbox composition: independent MinIO S3, the official AWS SDK with the existing local signed KMS protocol, and durable audit queues with real HTTP receivers in both SQL dialects. No AWS vendor certification, executable restart, deployment or live migration is claimed.

## Independent service

Owned MinIO PID76951 binds loopback19328/19329 with fresh private data under /tmp/levara-t28-minio-build-20261007-1023. Official source tag RELEASE.2025-10-15T17-29-55Z resolves to commit9e49d5e7a648f00e26f2246f4dc28e6b07f8c84a. Downloaded source archive SHA25645521908307306e925c98d629e1c17d78c8b72b6ee242b1bfb1409f7d8ee5841; source build using isolated go1.24.8/GOWORK=off actually exited0. Binary SHA256124ec87a834de8024934c2983d863810122fdef9bfda27305c1ce371d0254820. Binary self-report is DEVELOPMENT.GOGET, not an injected release tag; provenance comes from the pinned source manifest. Project dependencies/toolchain remain unchanged. Archived binary URLs returned410; this failed bootstrap is retained as I225.

Root verified PID argv/listener and ready health200. Test opt-in requires exact loopback endpoint, explicit sandbox credentials, fresh absent bucket and owned prefix. Each actual run created its own bucket; cleanup aborts tracked uploads, removes only owned-prefix objects and deletes only the newly created bucket. Cleanup failures fail the test.

## Observed gates

- Final external gate: actual exit0,1PASS/0FAIL/0SKIP. [Log](/tmp/levara-t28-final-external-race.jsonl), SHA256ac50686d8e55f669b3c2aeaebc0d8492aca13358c45bd46ed979630fa5a5c885.
- Current full race ./pkg/storage ./pkg/audit ./cmd/audit ./cmd/server: actual exit0,356 leafPASS/0FAIL/0SKIP, all four packages PASS. Correct LEVARA_TEST_POSTGRES_DSN selects the disposable native PostgreSQL alongside SQLite; external MinIO gate is opt-in and non-skipped. [Log](/tmp/levara-t28-final-full-race.jsonl), SHA256ab8ebaee0a9da303b7c03ebcec7743b1c3b8d4673bcb84d3bb5c04caf2a6dfa6.
- Frozen full-gate revision before/after: 2eb1dca16b0047918185760b41dcb22dee79090a+dirty:e88cef9f4b3ce3ba3aac1d0e9bbcf5ed9882cc4e2c0c635a7da56639fde53ff7;397 nonignored untracked files. Documentation acceptance changes are subsequent metadata, not part of this source-gate digest.
- make --silent contract-check actual exit0; log /tmp/levara-t28-final-contract.log. Independent source/log review accepted original T28 after the final composed timeout check.

All Go checks retain -skip=^TestMemoryREST as requested. No REST owner-spoofing reproduction ran. The first supplemental filtered matrix used the wrong PG environment variable and had107PASS/1SKIP; it was not accepted as SQL evidence. Correct full gates161/0/0 and final356/0/0 supersede that invocation; logs are retained.

## Corner-case proof

Actual MinIO receives escaped Unicode/reserved-character binary Save/List/Load and snapshot-pinned ranges; invalid credentials produce403 with no object. A real failed part leaves a signed checkpoint; parsed checkpoint and reconstructed storage resume against actual remote parts. Completion commits upstream before the injected lost response; one blocked reconciliation HEAD preserves completing state. Current/older checkpoint reconstruction reconciles the actual object, exact bytes and generation. Fault counters prove selected failures happened. Abort is checked against actual remote upload inventory and object absence.

Actual S3 ciphertext is inspected before plaintext roundtrip. Write-key reference rotation creates a new-key object, retains old bytes and reads both generations. A restricted read-ARN wrapper rejects the old object with nil reader and no KMS call; the allowed new object remains readable. Remote ciphertext corruption returns error and nil plaintext reader, retaining tampered bytes until exact restoration. Provider AccessDenied proves denial/revocation and actual signed Decrypt; it is labelled separately from unavailability. Delayed KMS with a100ms child deadline requires an actual Decrypt, error/nil reader and completion within1s; restored provider reads both objects exactly. Failure paths leave remote old/new ciphertext unchanged.

Full native tests additionally cover envelope/parser corruption, fresh data keys, context binding, allow-list rotation, unavailable/revoked keys, cache expiry without stale outage fallback, cancellation, spool saturation/cleanup and server ingest wiring. BothSQL durable audit cases cover capacity/full queues, expired leases/CAS, rollback, dead-letter/replay, duplicate stable IDs, lost ACK, retries, redirects and slow HTTP receivers without holding SQL transactions; CLI replay/discard remain explicit/idempotent.

## Scope

Encryption applies to newly stored object bytes through the configured encrypted storage wrapper. Existing plaintext is not automatically rewritten. SQL metadata, workspace source files, WAL/vector/BM25/graph indexes, audit JSONL and backup source content are not automatically encrypted by this wrapper. Standalone backup T22 and notebook preservation T31 retain their own evidence; T29 owns actual executable restart/resource/SLO checks. Real AWS KMS/S3 certification and corporate endpoints remain outside the declared local sandbox scope.
