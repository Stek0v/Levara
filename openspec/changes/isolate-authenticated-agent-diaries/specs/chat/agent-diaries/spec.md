# Spec Delta

## Purpose

Provide isolated agent diaries for authenticated callers and selected tenants while retaining explicitly trusted-local access to historical global agent diaries.

## ADDED Requirements

### Requirement: Authenticated diary identity includes caller and selected tenant
The system SHALL isolate diary reads and upserts by verified caller, selected tenant, normalized agent and selected collection. Tool arguments SHALL NOT override caller or tenant authority.

#### Scenario: Different users choose identical routing labels
- **WHEN** two valid callers use the same agent, collection and key
- **THEN** each reads and updates its own entry without reading or changing the other's canonical row

#### Scenario: One caller selects two tenants
- **WHEN** a caller with both memberships uses the same agent, collection and key in each tenant
- **THEN** each selected tenant retains independent entries

#### Scenario: Spoofed arguments and ambiguous names
- **WHEN** a caller supplies owner or tenant hints, or names contain delimiters
- **THEN** the verified identity governs access and distinct caller/tenant/agent tuples do not collide

### Requirement: Historical global diaries require explicit local authority
The system SHALL preserve anonymous trusted-local global diary behavior and SHALL NOT expose or assign historical global rows to authenticated callers. Authenticated and legacy namespaces SHALL be disjoint for every agent name.

#### Scenario: Historical row has no authenticated owner
- **WHEN** any authenticated caller reads an existing global agent diary
- **THEN** it is absent from the caller's results and remains unchanged

#### Scenario: Trusted-local compatibility
- **WHEN** an explicitly trusted-local anonymous caller reads or updates its global diary
- **THEN** the legacy namespace, filters and canonical identity remain compatible

### Requirement: Diary authority remains live during SQL access
The system MUST verify current credentials, action permission and selected tenant membership under the same SQL authority fence as authenticated access. Errors, cancellation and invalid identity MUST fail closed without partial publication or partial successful reads.

#### Scenario: Read-only or revoked API key
- **WHEN** a read-only key accesses its diary, or the credential is revoked or has empty permissions
- **THEN** the live read-only key may read but may not write, and invalid credentials may neither read nor write

#### Scenario: Cancellation or SQL read failure
- **WHEN** the request is cancelled or a scan/iteration fails
- **THEN** no partial successful result is returned and all SQL resources are released

### Requirement: Diary SQL behavior is portable
The system SHALL support SQLite and PostgreSQL diary writes and reads with stable result shapes, same-owner upsert identity and immutable unrelated rows.

#### Scenario: Native SQL and one-connection pools
- **WHEN** diary access runs on either SQL backend with a one-connection pool
- **THEN** it completes without self-wait, preserves canonical identity on overwrite and leaves other users, agents and collections unchanged

#### Scenario: Empty normalized agent
- **WHEN** an agent is empty or consists only of whitespace
- **THEN** the operation returns an error without mutation
