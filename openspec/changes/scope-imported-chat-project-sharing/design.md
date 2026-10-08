# Design

## Context

See proposal.md for motivation. Projects are datasets.id; dataset_shares supplies viewer/editor/admin roles. Collection names are not project ACL identities. Current raw message and derivative keys use platform/session; run IDs are also global. Some local source-daemon writes bypass REST.

## Goals / Non-Goals

Use one additive session registry and one run registry, retaining raw messages as authoritative storage and existing derivative machinery. Preserve anonymous operation only where the transport explicitly permits trusted local mode. No new ACL framework, group system, import merging, synchronization extension or automatic administrator inspection of private transcripts.

## Decisions

### Scoped physical keys

Derive canonical internal chat and run keys from an unambiguous serialized tuple containing owner, exact tenant and source identifier (also platform for chats). Store the original session/run selector in registries. Existing raw UNIQUE and derivative keys then remain usable without destructive table reconstruction. Registries have UNIQUE identity tuples; owner and tenant are immutable. Authenticated clients cannot create arbitrary physical IDs by submitting an identifier that looks canonical.

Legacy unregistered rows remain local-only. New local rows retain legacy IDs for compatibility and are explicitly marked local. Since arbitrary legacy IDs may equal a derived authenticated key, scoped registration must reject pre-existing raw rows or ledgers without its matching registry. Reverse local writes must reject authenticated registered IDs. Check collisions inside the writing transaction; never adopt content based only on physical ID. Source IDs repeated by different owners or tenants remain different chats.

### Private default and owner consent

Authenticated import always starts private. A chat owner with current project write authority may attach their chat to that project; this is the owner's explicit sharing choice. Audience and roles are managed by project administrators using existing dataset shares. Administrators may detach a chat already in their project, but cannot attach or read another person's private chat without their sharing choice. This avoids both a second consent-workflow system and implicit private-content disclosure.

One nullable project ID per chat is sufficient for the requested project sharing. Removing the association restores owner-only visibility. Project deletion or missing project ACL denies non-owner reads rather than deleting personal source data.

### Exact tenant and live permissions

Every authenticated access verifies active credentials, exact selected tenant and live tenant membership before resolving a chat. Private read requires owner; shared read additionally permits current explicit project owner/share read authority. Empty-owner legacy public project semantics do not make personal chats public. Administrative operations require actual project owner/admin role, never generic ActionShare (which currently normalizes to editor write).

Use existing fenced read/write transaction hooks for checks and mutations. Close rows before additional queries on a one-connection pool. Fail closed on SQL, scan, cancellation or credential errors. Do not hold a SQL cursor across providers.

### Selectors and import runs

Responses expose chat_id plus original platform/session_id. Exact chat_id resolves only after permission checks. Legacy platform/session lookup selects the caller's own chat first, otherwise a unique authorized shared chat; ambiguous shared matches return an error, never an arbitrary first row. Scoped run keys prevent foreign ledger collisions; retrying a returned run ID must not scope it a second time or adopt someone else's run. Run listings, source paths, warnings and counters remain owner/exact-tenant-only: sharing one chat never grants access to its potentially mixed private/project run.

### Derivatives and daemon

RAG and distillation registries use canonical chat keys. Source reads and publication recheck current access. Personal and shared source content must not be published into an unrestricted global collection. Reuse existing scoped dataset ingestion where it carries equivalent authority; otherwise keep imported RAG publication local-only until a scoped publication target exists, returning an explicit unsupported result instead of leaking content. Private distillation remains caller-owned and unverified; a collection name alone never grants project sharing.

Daemon imports are explicit local imports unless configured with a verified scoped service identity. No ambient impersonation of the first user. Auth-required startup disables unauthenticated loopback RAG and distillation while retaining local raw ingestion. Reindex/retry preserves registry identity.

A successfully committed distilled memory is a personal copy owned by the caller. Revoking source project access prevents future reads, model requests and checked publication; it does not retroactively delete that copy. The primary vector insert/readback holds a current source and memory fence after embedding. Deferred migration hooks run only after releasing that fence to avoid nested SQL deadlock with a one-connection pool. Cancellation before invoking the hook prevents invocation; an already invoked context-free migration callback retains its existing lifetime contract and mirrors the personal SQL copy.

## Risks / Trade-offs

- Revocation during a provider call → check before provider egress and subsequent publication; already sent content cannot be recalled.
- Legacy ownership unknown → local-only retention; no guessed migration.
- Project ACL deletion → shared access denied; private owner copy retained.
- Global-key compatibility → authenticated APIs expose original selectors plus canonical chat_id; CLI handles both.

## Migration Plan

Add registries through dialect-neutral SchemaStatements/EnsureSchema, idempotently on each actual database; do not rely on process-wide sync.Once. Existing tables are not rebuilt and existing content is not reassigned. Test old populated databases and repeated migration on SQLite/PostgreSQL. Rollback must not run an older globally readable authenticated server against scoped data; disable authenticated imported-chat surfaces before rollback. Local raw imports remain readable through explicit local mode.
