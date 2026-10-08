# Tasks

## 1. Confined filesystem and cooperative locking

- [x] 1.1 Add concrete stdlib root/subdirectory/regular-read/atomic-write helpers, preserving existing path formatting; test namespace, parent/leaf/history symlinks, special files, unique temporary cleanup and complete visibility with `go test ./pkg/workspace -run 'Confined|Atomic' -skip '^TestMemoryREST'` (no external service), and document supported filesystem boundary.
- [x] 1.2 Add cancellation-aware native per-project file locking using existing platform packages; verify same-process and actual child-process exclusion/release/cancellation and explicit unsupported-platform errors with `go test ./pkg/workspace -run 'ProcessLock' -skip '^TestMemoryREST'` (OS child processes), documenting per-project serialization ceiling and cooperative limits.

## 2. Workspace editing and sidecars

- [x] 2.1 Adopt confined manifest serialization, jobs JSON and artifacts enumeration/read without changing identity, retry or ACL policy; test redirected sidecar/branch paths and registry/job no-effects failures with `go test ./pkg/workspace ./internal/http -run 'Workspace.*(Manifest|Job|Artifact|Confined)' -skip '^TestMemoryREST'` (dedicated PostgreSQL preflight) and update storage guide.
- [x] 2.2 Adopt confined read/write/run/reindex/reconcile access and locks; add actual read file_digest and explicit UTF-8 contract, preserving empty/absent CAS and retained authority; test Unicode/CRLF/empty/invalid text, external edits, stale digests and complete file visibility with `go test ./internal/http -run 'Workspace.*(Read|Write|Run|Confined)' -skip '^TestMemoryREST'` (dedicated PostgreSQL preflight) and update API examples.

## 3. Byte-preserving history

- [x] 3.1 Prepare and publish complete commits with actual copied size/digest, safe records and error cleanup; verify empty/binary content and malformed/corrupt records with `go test ./internal/http -run 'Workspace.*(Commit|History)' -skip '^TestMemoryREST'` (dedicated PostgreSQL preflight), documenting snapshot scope.
- [x] 3.2 Verify all restore metadata/files before effects, stage complete tree and retain/rollback previous tree; test missing/corrupt second snapshot file, duplicate/path conflict, cancellation, second-rename failure and exact successful bytes with `go test ./internal/http -run 'Workspace.*(Revert|Restore|History)' -skip '^TestMemoryREST'` (dedicated PostgreSQL preflight), documenting two-renames and T20 crash-recovery boundary.

## 4. Native authority and process controls

- [x] 4.1 Cover real REST/MCP denied callers, current credentials, tenant/project collisions, pool1 and actual retained response without nested SQL acquisition with `go test -race ./internal/http -run 'Workspace.*(Authority|Fence|Confined|History)' -skip '^TestMemoryREST'` (dedicated PostgreSQL preflight); preserve all former authority cases.
- [x] 4.2 Add actual independent-process competing CAS and readers during restore for native SQLite/PostgreSQL and trusted local mode; observe one winner/one conflict, complete states and cancellation cleanup with `go test -race ./internal/http -run 'Workspace.*Process' -skip '^TestMemoryREST'` (OS child processes plus dedicated PostgreSQL preflight); document external-editor limitation.

## 5. Public contract

- [x] 5.1 Update workspace guides/read result description and generated full/core contracts via existing generators only; verify examples, backward-compatible fields and `make contract-check` (no external service).

## 6. Integration acceptance

- [x] 6.1 Freeze combined revision, run native both-SQL targeted race and required `make test-commit`, excluding `^TestMemoryREST`; record actual terminal statuses, all failed parents, hashes and matched pre/post manifests (dedicated PostgreSQL preflight).
- [x] 6.2 Independently audit combined source/contracts/DoD and observed logs, run strict OpenSpec and `git diff --check`, reconcile original T19 acceptance, roadmap/issues/evidence and Task checkpoint; T20 generation/crash recovery remains separate.
