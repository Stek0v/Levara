# Spec Delta

## Purpose

Provide honest supported static code analysis and publish its derived knowledge with the same immutable source and access lifecycle as other native knowledge.

## ADDED Requirements

### Requirement: Supported static analysis has explicit failures
The code tool SHALL support Go AST extraction and Python heuristic extraction. Unsupported extensions and Go syntax errors MUST return a tool error before persistence; Python extraction MUST NOT claim syntax validation.

#### Scenario: Unsupported or malformed source
- **WHEN** a caller submits JS, TS, an unknown extension or malformed Go
- **THEN** the tool reports an error and creates no source, graph or vector effects

#### Scenario: Supported valid empty graph
- **WHEN** valid supported source contains no extractable declarations or relationships
- **THEN** the tool reports a successful supported analysis, distinct from unsupported input

### Requirement: Code publication uses verified live global authority
Authenticated code requests SHALL require an active instance administrator, no selected tenant and the existing required write authority. Authorization and credential expiry MUST constrain source ingestion, publication and the protected successful response.

#### Scenario: Denied caller
- **WHEN** a non-administrator, selected-tenant administrator, revoked credential or expired credential requests code publication
- **THEN** the request is denied without publishing knowledge

#### Scenario: Revocation during processing
- **WHEN** required authority or source state changes before publication
- **THEN** stale knowledge is not admitted as current and the call reports failure

### Requirement: Static graph has resolvable source-scoped endpoints
Published code graphs SHALL preserve immutable raw source provenance and versioned publication. Every edge MUST reference an emitted endpoint; file modules and external references MUST be explicitly represented, and ambiguous same-name declarations MUST NOT be silently merged.

#### Scenario: Module and external call
- **WHEN** code imports a module or calls an external member
- **THEN** each relationship has real file/declaration/reference endpoints with current source provenance

#### Scenario: Source retirement or replacement
- **WHEN** the indexed source is retired or replaced
- **THEN** prior code knowledge follows the existing source-generation eligibility rules

### Requirement: Code result and failure are honest
Successful code analysis SHALL preserve language/entities/relations/text/details fields and synchronous completion. Omitted collection SHALL select documented code_knowledge. Nil-DB trusted local usage SHALL remain analysis-only. Configured SQL, embedding or pipeline failures MUST NOT be reported as completed success.

#### Scenario: Native no embedding
- **WHEN** SQL is available but embedding is not configured
- **THEN** current source-scoped SQL graph publication succeeds without vector effects or model extraction

#### Scenario: Processing failure
- **WHEN** configured storage or processing fails
- **THEN** the code tool returns an error and does not certify a current successful publication

#### Scenario: Trusted analysis-only use
- **WHEN** a trusted local caller uses no SQL database
- **THEN** supported analysis returns the compatible summary without persistence claims
