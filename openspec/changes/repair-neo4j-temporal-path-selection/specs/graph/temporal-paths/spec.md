# Spec Delta

## Purpose

Select shortest graph routes using only relationships visible at the requested temporal snapshot, consistently across supported storage backends.

## ADDED Requirements

### Requirement: Snapshot selects complete visible paths
The system SHALL choose shortest routes from relationships visible at the requested snapshot and SHALL NOT return fragments of temporally invalid routes.

#### Scenario: Expired shortcut
- **WHEN** an expired direct relationship competes with a longer fully visible route
- **THEN** the visible longer route is returned within the requested hop bound

#### Scenario: Invalid path fragment
- **WHEN** every route contains at least one invisible relationship
- **THEN** no relationships are returned

### Requirement: Preserve temporal and pagination compatibility
The system SHALL preserve integer Unix-second snapshots, inclusive validity boundaries, missing validity defaults, unfiltered AsOf0 history, hop bounds, edge pagination and existing input errors.

#### Scenario: Inclusive boundaries and legacy relationships
- **WHEN** a snapshot equals valid_from or valid_until, or a relationship has no validity properties
- **THEN** the relationship is visible according to inclusive bounds and epoch/open-end defaults

#### Scenario: History and page traversal
- **WHEN** AsOf0 is used or visible shortest-route edges are paginated
- **THEN** history remains unfiltered and concatenated pages match the complete ordered edge union without omissions or duplicates

#### Scenario: Bounded or invalid request
- **WHEN** no visible route fits MaxHops or the request cursor is malformed
- **THEN** the result is empty or the existing validation error is returned respectively
