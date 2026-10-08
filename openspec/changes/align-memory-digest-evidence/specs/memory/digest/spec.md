# Spec Delta

## Purpose

Export explicitly selected eligible memory decisions and discoveries as a scoped read-only Markdown view, preserving their publication provenance and verification label.

## ADDED Requirements

### Requirement: Current and legacy verification eligibility
The system SHALL include active explicitly selected decisions/discoveries labelled `receipt-validated` or legacy `verified`, while excluding unverified or retired records and preserving the original label.

#### Scenario: Validated publication reaches digest
- **WHEN** an owned memory is saved with valid current source evidence and selected for a digest
- **THEN** it appears with its unchanged receipt-validated label, content, freshness and task/receipt provenance

#### Scenario: Legacy compatibility
- **WHEN** an eligible explicitly selected legacy verified memory exists
- **THEN** it remains exportable with the legacy label without claiming server receipt validation

#### Scenario: Caller cannot certify publication
- **WHEN** a caller saves a memory with a verification hint but no validated evidence
- **THEN** that unverified memory does not appear in the digest

### Requirement: Scoped read-only selection
The system SHALL retain the collection, caller/shared-owner, active-state, genre and explicit-ID boundaries of digest selection, without modifying SQL, index, Git or workspace state.

#### Scenario: Excluded controls
- **WHEN** selected IDs include foreign-owner, sibling-collection, retired, unverified or other-genre records
- **THEN** these records are absent while eligible own/shared records remain available

#### Scenario: Invalid or duplicate selection
- **WHEN** IDs are malformed, empty or exceed the existing maximum
- **THEN** the existing error contract is retained
- **AND** duplicate valid selected IDs do not duplicate exported records
