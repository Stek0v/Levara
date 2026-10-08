# Spec Delta

## Purpose

Make repository analysis obey request cancellation and expose empty, repeated and failed outcomes accurately through the supported administrator interface and CLI.

## ADDED Requirements

### Requirement: Git parsing obeys caller cancellation
Repository subprocesses SHALL use the caller context. Invalid repositories MUST report errors; a valid repository with no commits and a valid filter with no matches SHALL return the existing successful zero-commit result.

#### Scenario: Canceled repository request
- **WHEN** the caller cancels Git analysis before or during the subprocess
- **THEN** the operation terminates with a cancellation error and starts no ingestion

#### Scenario: Empty repository or filter
- **WHEN** an initialized repository has no commits or the selected filter matches none
- **THEN** analysis returns a successful zero-commit result

### Requirement: Repeated analysis retains its documented source semantics
Repeated commit analysis SHALL not claim commit-hash deduplication. The existing immutable-source ingestion and versioned pipeline rules MUST remain authoritative, and previews MUST report the selected commit count.

#### Scenario: Same commit analyzed again
- **WHEN** the same repository selection is analyzed twice
- **THEN** both calls report the selected commit count under documented source/reprocessing semantics without an exactly-once claim

### Requirement: Git transport and CLI preserve failure
Git MCP operations SHALL retain verified active administrator/no-selected-tenant admission and protected responses. CLI Git commands MUST exit unsuccessfully for transport, HTTP, malformed JSON, JSON-RPC and MCP tool errors.

#### Scenario: MCP error in HTTP success
- **WHEN** the server returns HTTP200 with result.isError=true
- **THEN** the CLI exits unsuccessfully and displays the tool error

#### Scenario: Ordinary caller
- **WHEN** a non-administrator requests repository analysis or Git search
- **THEN** the existing server repository admission denies the request
