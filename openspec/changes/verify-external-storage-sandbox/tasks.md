# Tasks

## 1. Independent local S3 lifecycle

- [x] 1.1 Add the opt-in native external S3 fixture and document its scope and environment. Verify `go test -race ./pkg/storage -run '^TestS3ExternalSandbox$' -count=1 -timeout=5m` after root preflight confirms the owned pinned-source MinIO loopback process, explicit LEVARA_TEST_S3_SANDBOX=1, exact LEVARA_TEST_S3_ENDPOINT, absent LEVARA_TEST_S3_BUCKET named levara-sandbox-..., LEVARA_TEST_S3_PREFIX named levara-sandbox-.../ and LEVARA_TEST_S3_ACCESS_KEY/LEVARA_TEST_S3_SECRET_KEY static sandbox credentials. DoD: actual non-skipped escaped binary/list/range, invalid credential403/no object, checkpoint reconstruction/resume, one real lost completion ACK plus HEAD recovery, remote abort inventory and ciphertext with local signed KMS roundtrip; cleanup succeeds. Record service/source/log digests and actual exit in evidence.md before checking this task.

## 2. Current acceptance

- [x] 2.1 Run the current storage package matrix and independently review the frozen fixture, exact fault counters and owned cleanup. Verify `go test -race ./pkg/storage -count=1 -timeout=8m` with the same owned sandbox preflight, `openspec validate verify-external-storage-sandbox --strict`, and `git diff --check`. DoD: current observed exit0 receipts, no skipped external gate accepted, no production source or public/generated contract drift, explicit local S3/KMS and reconstruction limits in evidence.md. No AWS vendor certification or executable restart claim.

Accepted 2026-10-07: final current native356/0/0 including opt-in independent MinIO and bothSQL audit; standalone external1/0/0; contract/strict/diff exit0 and independent review. See [evidence.md](evidence.md) for digests, failure history and scope. No AWS vendor certification or executable-restart claim.
