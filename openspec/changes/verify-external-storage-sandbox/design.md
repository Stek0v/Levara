# Design

## Context

See proposal.md. Existing native S3 storage uses the official SDK, signed multipart checkpoints and exact publication markers/checksums. Existing KMS tests provide a local signed SDK protocol fixture.

## Goals / Non-Goals

Check the supported S3 contract against an independent disposable MinIO process. Test reconstruction preserves endpoint, bucket and checkpoint key material. Do not claim an executable restart, AWS service certification or production readiness from this bounded local check.

## Decisions

- Require LEVARA_TEST_S3_SANDBOX=1 plus explicit exact endpoint, bucket, prefix and static credentials. Endpoint is loopback with an explicit port. Bucket and prefix use levara-sandbox- ownership names, and the bucket must return404 before create. Ordinary tests skip without opt-in; acceptance preflight must verify opt-in rather than accepting a skipped gate.
- Save/List/Load an escaped Unicode and reserved-character binary key, verify a snapshot-pinned range, and assert invalid credentials produce403 without a published object.
- Forward successful operations to MinIO through a loopback reverse proxy retaining the signed Host. Fail one second-part request to preserve a real partial checkpoint, reconstruct storage and parse/resume it. Drop one successfully committed completion response and then its immediate reconciliation HEAD; reconstruction must reconcile the actual published generation from current and older authenticated checkpoints. Fault counters prove the selected paths ran.
- Abort a distinct upload and inspect actual remote upload inventory. Clean tracked uploads and objects under the exact owned prefix, then delete only the bucket created by this test. Foreign content is never deleted.
- Wrap the independent S3 store with existing EncryptedStorage and the local AWSKMS protocol fixture. Inspect stored ciphertext and verify plaintext roundtrip plus actual Encrypt/Decrypt calls. This proves the local protocol subset, not AWS infrastructure.
- Keep source ownership in one new test. A MinIO checksum or SDK compatibility failure must be diagnosed and reported before any production scope expansion; do not weaken assertions to manufacture acceptance.

## Risks / Trade-offs

Independent MinIO checksum support may expose a contract incompatibility → preserve failed logs and request a bounded repair plan. Reverse-proxy fault injection is local test infrastructure → all successes remain actual independent-backend responses. Cleanup failure → fail the gate and leave exact owned resources for root inspection; never broaden deletion.

## Migration Plan

No deployment or migration. Root alone builds/starts the owned pinned-source MinIO process, formats source, runs serialized checks and records evidence. Stop and clean only the owned sandbox after acceptance.
