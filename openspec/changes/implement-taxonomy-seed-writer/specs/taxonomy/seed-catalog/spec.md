# Spec Delta

## Purpose

Provide a reproducible manual taxonomy catalog with explicit private scope, transactional provenance and authorized source bindings for DCD ranking.

## ADDED Requirements

### Requirement: Bounded complete seed grammar
The system SHALL parse the documented Markdown grammar completely before mutation and reject malformed hierarchy, duplicate normalized siblings, unsupported fields and missing document sources.

#### Scenario: Malformed second domain
- **WHEN** a valid first domain is followed by invalid seed content
- **THEN** import fails without changing taxonomy rows or the provenance journal

#### Scenario: Native document binding
- **WHEN** a document node supplies a title and existing source ID under a collection
- **THEN** import records a distinct taxonomy document identity and source binding

### Requirement: Verified catalog scope
The system SHALL derive catalog owner and exact selected tenant from the verified caller, require dataset write for mutations and dataset read for listing, apply the existing selected-tenant dataset-owner membership restriction before grants, and validate source binding access within the explicit dataset.

#### Scenario: Foreign or read-only dataset
- **WHEN** a caller imports into a dataset without write permission or binds an inaccessible source
- **THEN** the operation is denied without mutation or audit success

#### Scenario: Tenant namespace isolation
- **WHEN** the same caller selects another tenant or no tenant
- **THEN** the previous namespace is neither read nor modified through caller hints

### Requirement: Transactional idempotent import and provenance
The system SHALL commit stable natural-key upserts and a content-free provenance report together, serialize concurrent imports, and replay a supplied request ID only for the identical operation.

#### Scenario: Repeat import
- **WHEN** an authorized seed is imported again with updated descriptions or aliases
- **THEN** existing IDs remain stable and changed fields update without duplicate hierarchy rows

#### Scenario: Audit or late SQL failure
- **WHEN** a later mutation or provenance statement fails
- **THEN** the complete earlier taxonomy and journal state remain unchanged

#### Scenario: Conflicting replay
- **WHEN** an existing request ID is reused for different seed content or action
- **THEN** the request conflicts without applying the new mutation

### Requirement: Explicit safe removal
The system SHALL require force for nonempty domain or collection removal and remove only catalog hierarchy/binding rows in the exact caller scope.

#### Scenario: Nonempty removal
- **WHEN** a caller removes a domain with collections without force
- **THEN** removal conflicts and all rows remain unchanged

#### Scenario: Forced binding removal
- **WHEN** an authorized caller forces removal
- **THEN** taxonomy children are removed while source documents, graph provenance and unrelated catalogs remain intact

### Requirement: Observable CLI and list contract
The system SHALL expose documented import/list/remove commands through existing server authentication, return explicit empty arrays, surface HTTP failures as command failures and report alias collisions without partial import.

#### Scenario: Empty catalog
- **WHEN** an authorized caller lists an empty dataset catalog
- **THEN** the response contains domains as an empty array

#### Scenario: Alias collision
- **WHEN** two imported domains share an alias
- **THEN** import succeeds atomically and reports the collision warning

### Requirement: Authorized native ranking bindings
Native ranking bindings SHALL match both dataset and source document identity after source authorization, preserve separate taxonomy/provenance identities and retain existing legacy route-ID behavior.

#### Scenario: Same source ID in another dataset
- **WHEN** an otherwise similar graph candidate belongs to another dataset
- **THEN** the binding does not boost or authorize that candidate

#### Scenario: Retired source
- **WHEN** a bound source becomes unavailable
- **THEN** its taxonomy binding does not bypass source retirement during search

### Requirement: Truthful observe and boost evaluation
Quality evaluation SHALL exercise supported DCD graph strategies with imported native bindings; observe preserves off-mode behavior, and default boost enablement requires separate release evidence.

#### Scenario: Frozen native comparison
- **WHEN** the same authorized fixture is queried with off, observe and boost
- **THEN** off and observe ordering agree, eligible binding lift is measured, zero-result behavior is reported and denied candidates remain absent
