# Spec Delta

## Purpose

Provide project context whose contents have proven project and caller scope, with explicit unsupported sections instead of global data fallbacks.

## ADDED Requirements

### Requirement: Current accessible memories only
The summary SHALL include only current memories in the exact selected collection owned by the authenticated caller or shared with an empty owner. Related summaries SHALL apply the same rule independently.

#### Scenario: Main and related mixed ownership and history
- **WHEN** Alice requests a main and related project containing Alice, shared, Bob and superseded records
- **THEN** only active Alice/shared records appear in their respective sections

#### Scenario: Forged owner and anonymous scope
- **WHEN** arguments name another owner or an unauthenticated context requests a project
- **THEN** argument identity has no effect, and anonymous scope contains shared records only

#### Scenario: Empty and literal collection names
- **WHEN** a project has no accessible records or its valid name contains whitespace or quotes
- **THEN** the summary reports no accessible memories or matches the literal name without widening scope

### Requirement: Auxiliary scope is explicit
Vector statistics, graph type counts and recent interactions SHALL be marked unavailable until their sources provide proven project and caller access scope. Global values SHALL NOT be returned as fallback.

#### Scenario: Foreign auxiliary data exists
- **WHEN** global graph/interactions or actor-free vector metadata contain foreign project data
- **THEN** none of that data appears and each auxiliary section explains its unavailability

### Requirement: Read failures cannot masquerade as success
Missing database, query, scan and iteration failures SHALL return a tool error without a partial success payload. Each result set SHALL be released before another summary read uses a single-connection pool.

#### Scenario: Database or related read failure
- **WHEN** the database is absent or a read fails after another section has been assembled
- **THEN** the response is an error with no assembled memory summary

#### Scenario: Single connection and cancellation
- **WHEN** multiple related summaries execute with pool size one or the request is cancelled
- **THEN** normal reads finish within the deadline, cancellation errors, and no connection remains held

### Requirement: Transport and output contracts remain discoverable
Successful results SHALL retain collection and text fields and existing profile visibility. Legacy session defaults and explicit overrides SHALL resolve as before; stateless requests SHALL use explicit collection independently of legacy sessions. The descriptor SHALL describe the scoped summary accurately.

#### Scenario: Authenticated transports
- **WHEN** Alice calls via legacy default, legacy override or stateless explicit selection with forged owner arguments
- **THEN** the selected project follows transport routing, only Alice/shared active memories appear, and output matches its declared schema
