# Spec Delta

## Purpose

Protect authoritative workspace files and snapshots from path redirection, partial publication and destructive restore failures while preserving explicit caller authority.

## ADDED Requirements

### Requirement: Confined workspace filesystem
The system SHALL restrict project and branch operations to their authorized directories, including history, manifests, jobs and context artifacts, and reject redirected namespace directories and unsafe file paths.

#### Scenario: Symlink points outside the authorized branch
- **WHEN** an authorized request encounters a project, branch, parent, file or sidecar symlink redirecting access
- **THEN** it fails without reading or modifying the redirected target or exposing its metadata.

#### Scenario: Denied project operation
- **WHEN** an inactive, expired, foreign or otherwise unauthorized actor requests file or history access
- **THEN** no protected bytes, file metadata or storage mutation is returned or performed.

### Requirement: Exact text read and current digest
The system SHALL preserve supported UTF-8 text bytes and return SHA-256 of actually read bytes as file_digest. Unsupported text encoding SHALL fail explicitly.

#### Scenario: Empty Unicode and CRLF content
- **WHEN** a caller reads empty text or UTF-8 text containing CRLF and Unicode after an external edit
- **THEN** text is exact and file_digest matches the current bytes rather than indexed metadata.

#### Scenario: Invalid UTF-8 file
- **WHEN** text read encounters invalid UTF-8 bytes
- **THEN** it returns an explicit error without silently substituting text.

### Requirement: Cooperative write conflicts and complete publication
The system SHALL distinguish absent files from empty files, reject stale expected_file_digest, and coordinate cooperating processes so only one competing CAS succeeds. Readers SHALL observe complete old or new file bytes.

#### Scenario: Competing processes
- **WHEN** two server processes write different content using the same expected digest, in authenticated or trusted local mode
- **THEN** exactly one publishes and the other reports conflict without truncating published content.

#### Scenario: Canceled or failed preparation
- **WHEN** a write fails or is canceled before publication
- **THEN** existing bytes remain and unpublished temporary files are cleaned.

### Requirement: Verified snapshot restore
The system SHALL verify snapshot identity, canonical paths, duplicate conflicts, regular-file bytes, recorded digests and sizes before modifying the live tree. Restore SHALL preserve exact snapshot bytes, including empty and binary files.

#### Scenario: Corrupt or missing later snapshot file
- **WHEN** a later snapshot file is absent, corrupt or inconsistent with its recorded size
- **THEN** restore fails and all original live bytes remain intact.

#### Scenario: Snapshot publication failure
- **WHEN** preparation is canceled or replacement fails after retaining the previous tree
- **THEN** the previous tree is preserved or restored and the request reports failure.

#### Scenario: Snapshot success and competing reader
- **WHEN** a verified snapshot replaces a branch while cooperating readers run
- **THEN** readers observe a complete tree state and restored bytes match the snapshot.

### Requirement: Honest persistence boundaries
The system SHALL retain SQL authority fences and current generation rules while documenting cooperative-CAS and restore publication limits. It SHALL not claim a transaction with arbitrary external editors or an atomic whole-directory swap.

#### Scenario: External editor
- **WHEN** an external editor changes a file before a new CAS check
- **THEN** the stale expected digest is rejected; simultaneous uncooperative editor scheduling is outside the guarantee.
