# Design

## Confirmed contract

Sync remains authorized for an active instance administrator, with write permission for server API credentials. The configured credential travels only to its exact configured base URL; redirects stay disabled. Empty types preserve memories/interactions/graph defaults. Vector collections require explicit type and nonempty names. Unknown or malformed selectors fail before network effects. Binary version mismatch continues to warn.

Valid graph and interaction identities use deterministic canonical payload precedence, with timestamp instants normalized before comparison. Graph retirement dominates legacy active state; latest known validity start and earliest known end join independently of content, and incompatible intervals fail. Export and comparison use the same native confidence precision. Conflicting graph dataset/source identity fails explicitly instead of moving a record across scopes. Per-record transactions use existing dialect-specific write admission; identical and losing payloads count as skipped. Exports keep the compatible complete-array shape and remove silent truncation under bounded request contexts.

Incremental memory and interaction export compares parsed instants and includes the timestamp boundary for safe replay. It scans under the existing request deadline because SQLite timestamp text is not chronologically comparable; a normalized indexed revision is the upgrade path for large corpora.

Consolidation Apply and Revert publish fresh memory update revisions. Retirement time remains the actual current time even when the prior logical update revision is in the future. Revert validates the complete prior snapshot, including old revision where present, then publishes a new revision; it never restores an old update timestamp. Legacy journals retain their validation path.

## Historical memory lifecycle integration requirements

Scoped-key conflict retains target SQL identity. Remote IDs therefore cannot independently identify local deletion or supersession, and archive keys encode predecessor IDs. The required implementation, now accepted below, must preserve canonical IDs, map remote incarnations explicitly, retain physical deletion evidence separately from ephemeral index jobs, and enqueue accepted changes through the existing guarded native outbox in the same transaction. Absence from export is never deletion evidence. Lifecycle capabilities must be explicit so old peers cannot silently downgrade lifecycle state.

These historical acceptance requirements are fulfilled by the generation-aware implementation below. Independent persistent roots now prove conflict, delete, recreate, supersede, consolidate/revert and retry behavior; all11 tasks and originalT21 are accepted.

## Verification

Use actual independent SQLite files and disposable PostgreSQL databases, pool size one, authenticated HTTP exchanges and persistent state comparisons after repeat. Cover lost acknowledgement after actual remote commit, invalid acknowledgements, version warning, exact destination credentials, retirement versus stale active state, over-limit exports and explicit collection terminal results. Root serializes Go, generators and integration checks. User-excluded REST memory owner-spoofing reproductions remain excluded.

## Active-memory conflict and publication

Active records compare parsed UTC timestamps at native microsecond precision, then a deterministic rank over value/type/room/hall/pin metadata. The exact logical key remains key/owner/collection; persisted local IDs and creation timestamps are immutable under replacement. Physical ID collisions with a different logical tuple fail before mutation. Export normalizes timestamp spelling so native PostgreSQL offsets do not change wire identity.

Accepted mutations and native canonical-ID outbox intent commit together. An enabled index without its outbox fails explicitly. Remote value changes invalidate previous local Task/receipt proof; same-value metadata changes preserve the existing value-bound verification contract. Remote verification is never promoted.

A type-only change can reuse the running key/value digest job because embedding input contains only key and value. Final native SQL fencing rechecks immutable identity/key/value/activity and reads the current type for vector metadata. Late changed-value embeddings remain ineligible.

This active-memory phase rejects a winning replacement of a retired target. It does not infer missing incoming lifecycle fields or absence as deletion; lifecycle aliases, durable physical deletion and compatibility admission were required before wholeT21 acceptance and are implemented by the generation-aware protocol below.

## Receiver-local collection contracts

Freshly re-embedded units use the exact receiver contract selected by native collection creation, including configured tokenizer/pooling/normalization. Before writing, reject existing targets with incompatible model, dimension, metric or full fingerprint; never relabel existing data. Stamp both reserved embedding fields only after successful local embedding, preserving source/business metadata. Missing/null metadata becomes an object; scalar/array metadata fails that unit. Native insertion retains collection identity and contract admission through its actual write, preventing concurrent replacement from selecting a different vector space. Duplicate/job ownership and collection status verification subsequently passed; collection3.3 and fullT21 acceptance are recorded in evidence.md. This paragraph describes the earlier narrow receiver-contract repair.

## Generation-aware memory exchange

Protocol3 requires explicit incarnations and immutable aliases with memories/deletions. Its full snapshot and import use the existing native memory fence; the whole memory envelope is atomic, while selected types retain independent results. Protocol2 bounded known-ID evidence remains historical and is explicitly incompatible with the new required history.

External head/incarnation/alias tables preserve existing full-row consolidation hashes. Actual AFTER INSERT allocates a tuple generation atomically; UPSERT attempts never register discarded UUIDs. Native state flips advance integer revisions and deletion is terminal within a generation. Imported validated identities/state are preregistered in the same transaction so triggers do not mint another generation or revision.

Original logical keys are captured before native archive renames. A changed-key successor starts its own logical slot; predecessor provenance alone does not allocate a successor generation. Native first creations join generation zero across independent nodes. New generations require predecessor retirement/deletion history. State revisions order restoration, restrictive state wins ties, and timestamps rank content without granting resurrection.

Every foreign physical ID maps through immutable scoped generation identity to a receiver canonical ID, including aliases from losing or equal content imports. Canonical predecessor IDs survive supersession and receive retirement intents before successor publication. Historical original keys that cannot be verified remain explicitly unresolved; migration/import never parses archive suffixes as identity evidence.

Implementation and required native independent-root lifecycle/consolidation/retry gates pass on SQLite and PostgreSQL: native531/0/0, wholeS0–S4 4539/0/2. All11 tasks and originalT21 are accepted on2026-10-07; exact frozen evidence, historical failures, final bookkeeping checks and unclaimed limits are recorded in evidence.md.
