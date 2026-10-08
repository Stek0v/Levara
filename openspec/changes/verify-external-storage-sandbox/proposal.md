# Proposal

## Why

S3 multipart and encryption have in-process protocol coverage, but T28 still lacks observed acceptance against an independent S3 implementation. A disposable MinIO sandbox now allows native SDK persistence and recovery to be checked without production credentials.

## What Changes

- Add an explicitly opted-in local external S3 test covering escaped binary objects, range reads, credential denial, multipart checkpoint reconstruction, lost completion acknowledgement recovery, abort and encrypted roundtrip.
- Use the existing local signed KMS protocol fixture with the independent S3 backend.
- Record actual service identity, frozen source, exits and bounded claims before marking acceptance complete.
- Non-goals: AWS vendor certification, corporate credentials, new storage behavior, routes, schema, dependencies or deployment.

## Capabilities

### New Capabilities

None. This is verification of existing behavior.

### Modified Capabilities

None; skip_specs is explicit.

## Impact

One new storage test and this acceptance plan. No public API, SQL dialect, runtime profile, feature flag or production source changes. The test requires an independently running owned loopback S3 server, creates a previously absent owned bucket and cleans only its owned prefix/uploads/bucket. No migration or rollout.
