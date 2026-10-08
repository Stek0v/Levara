## ADDED Requirements

### Requirement: Valid explicit synchronization selection
The system SHALL reject unknown types, malformed lists and a collections selection without valid collection names before remote effects. Omitted or empty types SHALL preserve the default memories, interactions and graph selection; vector collections SHALL require explicit opt-in.

#### Scenario: Unsupported selector
- **WHEN** a caller selects an unknown type or collections without names
- **THEN** the request fails and no remote request occurs

#### Scenario: Default non-vector sync
- **WHEN** types are omitted or empty
- **THEN** memories, interactions and graph are selected and vector collections are excluded

### Requirement: Deterministic lifecycle-aware memory convergence
Independent stores SHALL converge after repeated exchanges of the same logical memory identity, including equal-version conflicts, retirement, supersession and deletion. Absence from an export SHALL NOT imply deletion. An accepted import SHALL preserve actual persisted identity and reconcile native indexing with SQL lifecycle.

#### Scenario: Equal-version divergent values
- **WHEN** independent stores contain the same logical key with equal update times and different payloads
- **THEN** both stores reach the same deterministic state and another exchange causes no mutation

#### Scenario: Deleted or retired source
- **WHEN** a memory is deleted, retired or superseded before synchronization
- **THEN** its lifecycle evidence is transmitted and repeated exchanges do not resurrect it as active

### Requirement: Truthful repeated synchronization
Declared non-vector types SHALL reach a fixed point on independent roots. Counters and status SHALL describe actual committed imports, skips and failures. Lost acknowledgements SHALL report failure even if remote commit occurred; retries SHALL be idempotent. Export limits SHALL NOT silently omit eligible records.

#### Scenario: Lost acknowledgement
- **WHEN** the remote commits and its acknowledgement is lost
- **THEN** the first operation reports failure and a retry accounts for existing records without duplication

#### Scenario: Repeated graph exchange
- **WHEN** the same graph payload is exchanged repeatedly
- **THEN** temporal validity remains consistent and unchanged records count as skipped

### Requirement: Explicit vector collection imports
Vector collection synchronization SHALL remain opt-in and asynchronous results SHALL identify the actual import job and terminal committed results for the owning instance.

#### Scenario: Import begins
- **WHEN** an explicit collection import is accepted
- **THEN** status indicates running until terminal import evidence is available and another instance cannot claim its job

### Requirement: Safe incremental timestamp boundary
The system SHALL compare parsed timestamp instants consistently across native SQL dialects and SHALL include the supplied boundary. Replayed unchanged rows SHALL be skipped. Mixed native and RFC3339 timestamp formats SHALL NOT silently omit chronologically eligible records.

#### Scenario: Native SQLite timestamp after RFC3339 boundary
- **WHEN** a native timestamp with a space separator is chronologically after a boundary with a T separator
- **THEN** the record is exported

#### Scenario: Equal boundary and timezone-equivalent values
- **WHEN** the stored timestamp equals the boundary instant
- **THEN** the record is included and an unchanged replay is counted as skipped

### Requirement: Receiver embedding identity
Fresh collection import vectors SHALL carry the receiver's complete native embedding contract. The importer SHALL refuse an incompatible existing target before inserting units, and SHALL preserve source records and business metadata. Missing or null metadata SHALL be treated as an empty object; unsupported nonobject unit metadata remaining after chunk expansion SHALL count as failed without insertion. Existing oversized-document wrapping SHALL remain compatible.

#### Scenario: Native source uses another model
- **WHEN** a native source record is re-embedded into a compatible receiver collection
- **THEN** both reserved metadata fields describe the actual receiver vector space and source metadata remains unchanged

#### Scenario: Same dimension but incompatible existing target
- **WHEN** the receiver encoder differs from the existing target contract despite equal dimensions
- **THEN** the job fails with zero processed units and retains the existing target data and contract

### Requirement: Explicit memory lifecycle wire version
Memory synchronization SHALL transmit a versioned object envelope with separate memory and deletion arrays. Unsupported versions, legacy arrays, and missing arrays SHALL fail before any receiver SQL or indexing effect. An acknowledgement SHALL include the accepted memory protocol version and account for all transmitted units. Binary version warnings SHALL NOT substitute for validation of the actual memory payload.

#### Scenario: Legacy peer cannot decode lifecycle history
- **WHEN** a lifecycle-capable node exchanges memories with a peer using the legacy array contract
- **THEN** memory synchronization reports incompatibility without importing deletions as active rows, while other explicitly selected types retain their own results

#### Scenario: Deletion-only exchange and stale replay
- **WHEN** a native deletion journal is exchanged without live memories
- **THEN** the receiver commits the exact scoped deletion and native retirement intent, and replay of that known deleted physical ID cannot recreate the row

#### Scenario: Lifecycle snapshot under clock skew
- **WHEN** a memory exchange supplies a wall-clock since boundary
- **THEN** its lifecycle envelope includes deletion history and referenced memory records independently of that boundary until a durable revision cursor exists

### Requirement: Logical memory generations and canonical aliases
Memory lifecycle identity SHALL use exact owner, collection and captured original logical key. Native insertion SHALL allocate a durable generation only for the actual canonical inserted UUID, without registering attempted UUIDs from an UPSERT update. Retirement and restoration SHALL use integer state revisions; terminal deletion SHALL prevent recreation of the same generation even with a previously unseen physical UUID. A native fresh creation after known retirement or deletion SHALL use a new generation. Higher imported generations SHALL require predecessor lifecycle history independently of wall-clock timestamps.

#### Scenario: Independent UUID after native deletion
- **WHEN** two native stores independently create generation zero for the same scoped logical key and one deletes its canonical record
- **THEN** importing the other store's stale UUID cannot recreate that generation or publish a new active index intent

#### Scenario: Supersession across independent canonical identities
- **WHEN** the source supersedes its record and the receiver has another canonical UUID for the original logical key
- **THEN** the receiver retires its own predecessor, retains that predecessor ID, imports a distinct successor generation, and remaps both lineage directions and index intent to receiver canonical IDs

#### Scenario: Native UPSERT and lifecycle rollback
- **WHEN** a native scoped-key UPSERT retains an existing canonical record, or its transaction rolls back
- **THEN** attempted UUIDs do not become aliases or generations, and rollback preserves memory, generation head, incarnation, aliases, deletion evidence and index intent together

### Requirement: Versioned complete identity history
The generation-aware memory protocol SHALL require explicit incarnation and alias arrays in addition to live memory and deletion arrays. Canonical aliases SHALL remain immutable across replay, losing content updates and physical deletion. An equal or losing import SHALL persist validated aliases without publishing changed content or index intent. The generation-aware memory envelope SHALL commit as one existing native memory-fenced transaction or report no committed imports.

#### Scenario: Missing or ambiguous history
- **WHEN** the peer omits required history, rebinds a physical ID to another logical generation, or historical original identity cannot be verified
- **THEN** the exchange fails before effects rather than reconstructing identity from archive-key spelling or timestamps

#### Scenario: Restoration and remove-wins ties
- **WHEN** a retired retained incarnation is restored with a greater state revision
- **THEN** restoration may become active without reviving a physically deleted incarnation; equal revision conflicts retain the more restrictive lifecycle state
