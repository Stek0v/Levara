# Spec Delta

## Purpose

Keep the origin of distilled memories distinct from validated publication evidence so a newly generated statement cannot inherit proof for earlier text.

## ADDED Requirements

### Requirement: New distilled text has no inherited verification

The system SHALL store newly distilled text with verification status `unverified`, an empty originating Task ID and an empty receipt list, on both insertion and overwrite. Caller or model-provided evidence hints MUST NOT elevate these fields.

#### Scenario: Receipt-backed statement is overwritten

- **WHEN** distillation replaces an owned selected memory whose previous statement has validated Task receipts
- **THEN** the new text is unverified with no Task or receipts, while its canonical identity and namespace remain unchanged

#### Scenario: Legacy verification is overwritten

- **WHEN** distillation overwrites a legacy verified statement
- **THEN** the new statement does not retain that verification or its old evidence fields

#### Scenario: Fresh statement and forged evidence hints

- **WHEN** distillation inserts a new statement with caller or model evidence hints
- **THEN** it is stored as unverified without Task or receipt provenance

### Requirement: Evidence reset preserves existing write selection

The evidence reset SHALL affect only the existing owner/key/collection selection. Unrelated owners, shared rows and sibling collections MUST retain their existing values and evidence.

#### Scenario: Same key in other namespaces

- **WHEN** distillation overwrites a selected owned statement while the same key exists for another owner, shared scope or another collection
- **THEN** the selected statement loses obsolete evidence and all control rows remain unchanged

### Requirement: Preview retains stored evidence

A dry-run SHALL return proposed candidates without changing stored memory text or evidence. Ordinary transcript origin SHALL remain distinguishable from Task receipts without changing existing result/error shapes.

#### Scenario: Preview of a previously validated statement

- **WHEN** a dry-run proposes replacement text for an evidence-backed memory
- **THEN** the candidates retain ordinary transcript origin and the stored memory and evidence remain unchanged
