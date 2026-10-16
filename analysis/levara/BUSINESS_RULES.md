# BUSINESS RULES — `levara`

Extracted 2026-10-15 via `code-modernization` `modernize-extract-rules`
(Method B lenses — calculations / validations / lifecycle — run in the main
thread; subagent quota exhausted until 2026-10-19). Every High-confidence
citation was verified by reading the cited source lines directly (gortex
`get_symbol_source` / `search_text` transcripts in the session record).
Medium = documented contract + partial code verification; the open question
is listed in "Rules requiring SME confirmation".

Coverage note: this pass prioritizes the refactor blast zone (memory/MCP
recall-save, access, storage/backup, embed, search, graph upsert). Webui,
ingestion detail, LLM-cache internals and governor tuning were not mined —
a follow-up pass can extend the catalog before `/modernize-brief` if those
areas enter scope. **Verified secondary rule source (2026-10-16):** the
decomposition issue ledger
`docs/product/decomposition-issues-2026-10-05.md` (I01–I83, each with
RED/GREEN evidence) pins further behavioral rules already enforced by
tests — notably I06 (REST save derives owner from caller, foreign hints
rejected), I44 (unsupported MCP search labels rejected before effects,
honest AUTO fallback), I45 (vector capabilities advertised only when an
EmbedClient-backed pipeline exists), I47/I48 (zero-match with explicit
filters returns empty — confirms RULE-023 — and metadata filters run
before source-text egress/dedup/ranking), I50 (HYBRID keeps the successful
leg when the other fails, but fails closed on authorization/cancellation).
Treat the ledger as an extension of this catalog when writing the brief.

**Totals: 27 rules — Validation 8, Calculation 6, Lifecycle 6, Policy 7.
P0: 8 (behavior-contract anchors). All SME questions resolved 2026-10-16.**
(2026-10-16 update: former SME Q1/Q5/Q6/Q7 were resolved by direct source
verification and folded into the cards; Q3's matrix is now code-verified and
reduced to a canonical-status confirm. See the SME table for what remains.)

| ID | Name | Cat | Pri | Source | Conf |
|---|---|---|---|---|---|
| RULE-001 | Hall vocabulary is closed | Validation | P0 | `pkg/memoryhall/hall.go:10-17` | High |
| RULE-002 | save_memory requires key and value | Validation | P1 | `pkg/mcp/tool_save_recall_memory.go:70-74` | High |
| RULE-003 | memType defaults when omitted | Validation | P1 | `pkg/mcp/tool_save_recall_memory.go:76-78` | High |
| RULE-004 | pin_priority is optional numeric | Validation | P2 | `pkg/mcp/tool_save_recall_memory.go:86-88` | High |
| RULE-005 | Backup option bounds | Validation | P1 | `pkg/backup/verified.go:107` | High |
| RULE-006 | Backup manifest strict validation | Validation | P1 | `pkg/backup/verified.go:527` | High |
| RULE-007 | RBAC vocabularies are closed sets | Validation | P1 | `pkg/access/policy.go:11,606-617` | High |
| RULE-008 | API-key permissions gate separately from role | Validation | P1 | `pkg/access/policy.go:47,595` | High |
| RULE-009 | gRPC filtered search overfetch factor | Calculation | P1 | `internal/grpc/service.go:1035-1037` | High |
| RULE-010 | REST ACL pre-rerank overfetch | Calculation | P1 | `internal/http/api_search.go:465-468` | High |
| RULE-011 | Embed breaker defaults | Calculation | P1 | `pkg/embed/breaker.go:38-46` | High |
| RULE-012 | Breaker half-open probing | Calculation | P1 | `pkg/embed/breaker.go:50,133` | High |
| RULE-013 | Memory value log truncation | Calculation | P2 | `pkg/mcp/tool_save_recall_memory.go:23,172-180` | High |
| RULE-014 | Vector recall top-K constant | Calculation | P2 | `pkg/mcp/tool_save_recall_memory.go:29,503` | High |
| RULE-015 | Same-key save archives the retired memory | Lifecycle | P0 | `pkg/mcp/tool_save_recall_memory.go:112-114` | High |
| RULE-016 | Superseded memories hidden by default | Lifecycle | P0 | `pkg/mcp/tool_save_recall_memory.go:434,543` | High |
| RULE-017 | Exclusive relationships auto-supersede | Lifecycle | P0 | `pkg/orchestrator/pgupsert.go:129,187-192` | High |
| RULE-018 | Graph upsert: deduped, one transaction, dataset-scoped | Lifecycle | P0 | `pkg/orchestrator/pgupsert.go:38-40` | High |
| RULE-019 | Postgres row lock on memory save | Lifecycle | P1 | `pkg/mcp/tool_save_recall_memory.go:103-106` | High |
| RULE-020 | Index job gated on embed availability | Lifecycle | P1 | `pkg/mcp/tool_save_recall_memory.go:138-170` | High |
| RULE-021 | Recall cascade: vector first, LIKE fallback, embed errors never fail recall | Policy | P0 | `pkg/mcp/tool_save_recall_memory.go:495-563` | High |
| RULE-022 | Vector ranking restored after hydration | Policy | P2 | `pkg/mcp/tool_save_recall_memory.go:557-560` | High |
| RULE-023 | Filters applied pre-rerank; tags ANY/OR; empty filter result is final | Policy | P0 | `docs/search-strategies-guide.md:68`, `internal/http/api_search.go:460` | Medium |
| RULE-024 | Bounded overfetch ≠ exhaustive top-k | Policy | P2 | `docs/search-strategies-guide.md:68` | Medium |
| RULE-025 | wake_up budget and pin priority | Policy | P1 | `AGENTS.md` pin policy; `pkg/mcp` wake_up tool | Medium |
| RULE-026 | Sync excludes vector collections by default | Policy | P1 | `AGENTS.md` Sync section | Medium |
| RULE-027 | Recall LIKE path is owner-scoped | Policy | P0 | `pkg/mcp/tool_save_recall_memory.go:564-566` | High |

No instruction-shaped content was found in any source read during this
extraction (injectionFlags: none).

---

## Validation

### RULE-001: Hall vocabulary is closed
**Category:** Validation
**Priority:** P0
**Source:** `pkg/memoryhall/hall.go:10-17`
**Plain English:** A memory's hall must be exactly one of the six genre values; empty or legacy halls are rejected.
**Specification:**
  Given a `save_memory` call with a non-empty `hall` argument
  When  the value is checked against `hallVocab` by `IsValidHall`
  Then  unknown values are rejected with an error result
  And   empty hall is allowed (treated as unset)
**Parameters:** vocabulary = fact, event, decision, preference, advice, discovery (per AGENTS hall table; code holds `hallVocab`).
**Edge cases handled:** empty string allowed; exact (case-sensitive) membership; legacy halls invalid.
**Confidence:** High — membership semantics verified in source; extending the vocab is a deliberate code change (`internal/http/mcp_palace.go` per AGENTS).

### RULE-002: save_memory requires key and value
**Category:** Validation
**Priority:** P1
**Source:** `pkg/mcp/tool_save_recall_memory.go:70-74`
**Plain English:** A memory without a key or without a value is rejected before any storage access.
**Specification:**
  Given `save_memory` args
  When  `key == ""` or `value == ""`
  Then  return an error ToolResult without touching the database
**Parameters:** none.
**Edge cases handled:** whitespace-only values not trimmed here (delegated downstream).
**Confidence:** High.

### RULE-003: memType defaults when omitted
**Category:** Validation
**Priority:** P1
**Source:** `pkg/mcp/tool_save_recall_memory.go:76-78`
**Plain English:** Omitted memory type gets a default instead of failing.
**Specification:**
  Given `save_memory` without `type`
  When  the arg is missing/empty
  Then  a default type is applied before persisting
**Parameters:** default = `project` (verified: `memType = "project"` at :77-78).
**Confidence:** High.

### RULE-004: pin_priority is optional numeric
**Category:** Validation
**Priority:** P2
**Source:** `pkg/mcp/tool_save_recall_memory.go:86-88`
**Plain English:** Pin priority is parsed only when supplied as a number; absent means unpinned.
**Specification:**
  Given args may contain `pin_priority`
  When  it parses as `float64`
  Then  it is stored with the memory
  And   absent/mistyped values leave the memory unpinned
**Parameters:** documented scale 1-3/5/8/10 (AGENTS pin policy).
**Confidence:** High.

### RULE-005: Backup option bounds
**Category:** Validation
**Priority:** P1
**Source:** `pkg/backup/verified.go:107`
**Plain English:** A verified backup refuses unsafe options before writing anything.
**Specification:**
  Given `VerifiedOptions`
  When  NodeID is not a safe single path segment (must equal its basename; not "." or ".."; no "/" or "\\"), or Shards ∉ [1,64], or Dimension ∉ [1,65536]
  Then  the backup aborts with a validation error
**Parameters:** Shards 1..64; Dimension 1..65536.
**Edge cases handled:** NodeID traversal strings rejected explicitly.
**Confidence:** High.

### RULE-006: Backup manifest strict validation
**Category:** Validation
**Priority:** P1
**Source:** `pkg/backup/verified.go:527`
**Plain English:** Restoring requires a manifest that matches the current format exactly.
**Specification:**
  Given an extracted backup manifest
  When  format ≠ current, or DBProvider ∉ {sqlite, postgres}, or NodeID empty, or Dimension/Shards out of bounds, or roots ≠ 3, or file count ∉ [1, MaxFiles]
  Then  verification fails
**Parameters:** roots = exactly 3 (data/workspace/uploads per VerifiedOptions paths).
**Confidence:** High.

### RULE-007: RBAC vocabularies are closed sets
**Category:** Validation
**Priority:** P1
**Source:** `pkg/access/policy.go:11,619,628`
**Plain English:** Roles and actions come from fixed vocabularies; anything else is normalized or rejected.
**Specification:**
  Given an access request
  When  role/action are resolved
  Then  role ∈ {admin, editor, viewer} (ValidRole) and action ∈ {read, write, delete, share} (normalizeAction)
  And   unknown roles are not silently upgraded
**Parameters (CORRECTED 2026-10-16 — caught by the Phase-1 B3 invariant
tests before contract freeze):** `normalizeAction` (policy.go:628-639)
folds **delete and share into the write class** (deliberate, per finding
M25, 2026-09-03 review: a viewer ACL grant must be denied for
delete/share). The effective `RoleAllows` matrix (policy.go:606-617) is
therefore:

| Role | read | write | delete | share |
|---|---|---|---|---|
| admin | ✅ | ✅ | ✅ | ✅ |
| editor | ✅ | ✅ | ✅ (write-class) | ✅ (write-class) |
| viewer | ✅ | ❌ | ❌ | ❌ |
| other | ❌ | ❌ | ❌ | ❌ |

At the `RoleAllows` level editor == admin in effective permissions; the
admin/editor distinction is enforced elsewhere (superuser flag, share
management via CanGrantDatasetShare/CanManageDatasetShares). Role
comparison is case-insensitive.
**Confidence:** High — matrix verified in code AND pinned by
`pkg/access/apikey_invariant_test.go::TestAuthorizeAPIKeyConjunctionExhaustive`
(112-combination oracle against the live policy). Owner note: the
original B1 confirmation ("канон") was given on the pre-correction matrix
(editor shown without delete/share); the corrected cells need one
re-confirm — see SME table Q10.

### RULE-008: API-key permissions gate separately from role
**Category:** Validation
**Priority:** P1
**Source:** `pkg/access/policy.go:47,595`
**Plain English:** An API key can further restrict what an otherwise-authorized user may do, but never expand it.
**Specification:**
  Given a Decision with `Authenticated` and `Role`
  When  an API key is in play
  Then  `APIKeyAllows` is evaluated and surfaced as `Decision.APIKeyAllowed`
  And   the final `Allowed` reflects both role and key constraints
**Confidence:** High (structure verified; combined-semantics detail is SME Q4).

---

## Calculation

### RULE-009: gRPC filtered search overfetch factor
**Category:** Calculation
**Priority:** P1
**Source:** `internal/grpc/service.go:1035-1037`
**Plain English:** Filtered gRPC search fetches three times the requested top-K (at least 10) so post-filtering can still fill the page.
**Specification:**
  Given a SearchByText request with filters (Phase 2.5 path)
  When  computing the fetch size
  Then  `overfetch = topK * 3`
  And   if `overfetch < 10` then `overfetch = 10`
**Parameters:** factor 3; floor 10.
**Edge cases handled:** tiny top-K requests get the floor.
**Confidence:** High.

### RULE-010: REST ACL pre-rerank overfetch
**Category:** Calculation
**Priority:** P1
**Source:** `internal/http/api_search.go:460`
**Plain English:** The REST chunks-search path also overfetches, then filters to allowed datasets before reranking.
**Specification:**
  Given a chunks search with ACL constraints
  When  candidates are fetched
  Then  an overfetch window is retrieved and filtered by allowed datasets prior to rerank
**Parameters:** `overfetch = req.TopK * 3`, floor 10 — identical to gRPC (RULE-009). Verified :465-468.
**Edge cases handled:** the ordering exists for a security reason (comment :455-461): ACL filtering BEFORE rerank guarantees the rerank sidecar never sees text from unauthorized datasets — this replaced an older `SearchByTextWithRerank` call that leaked forbidden chunks to the reranker.
**Confidence:** High.

### RULE-011: Embed breaker defaults
**Category:** Calculation
**Priority:** P1
**Source:** `pkg/embed/breaker.go:38-46`
**Plain English:** The embed circuit breaker trips after 3 failures and cools down 30 seconds unless configured otherwise.
**Specification:**
  Given a Breaker constructed with threshold < 1 or cooldown <= 0
  When  NewBreaker normalizes
  Then  threshold = 3 and cooldown = 30s
**Parameters:** threshold 3; cooldown 30s.
**Confidence:** High.

### RULE-012: Breaker half-open probing
**Category:** Calculation
**Priority:** P1
**Source:** `pkg/embed/breaker.go:50,133` (Allow/State; fields probing/generation at :31-32)
**Plain English:** An open breaker lets a single probe through after cooldown rather than blocking everything.
**Specification:**
  Given the breaker is open and cooldown has elapsed
  When  Allow() is called
  Then  one probe request is admitted (half-open)
  And   success closes the breaker, failure re-arms the cooldown (generation bump)
**Confidence:** High.

### RULE-013: Memory value log truncation
**Category:** Calculation
**Priority:** P2
**Source:** `pkg/mcp/tool_save_recall_memory.go:172-180`
**Plain English:** The confirmation message echoes only a truncated prefix of the stored value.
**Specification:**
  Given a successful save
  When  composing the result message
  Then  value is truncated to `memoryValueLogMaxLen`
**Parameters:** `memoryValueLogMaxLen = 100` (verified :23).
**Confidence:** High.

### RULE-014: Vector recall top-K constant
**Category:** Calculation
**Priority:** P2
**Source:** `pkg/mcp/tool_save_recall_memory.go:503`
**Plain English:** Recall fetches a fixed number of vector candidates before SQL hydration.
**Specification:**
  Given recall_via_vector path
  When  searching the collection
  Then  top-K = `recallMemoryVectorTopK`
**Parameters:** `recallMemoryVectorTopK = 10` (verified :29).
**Confidence:** High.

---

## Lifecycle

### RULE-015: Same-key save archives the retired memory
**Category:** Lifecycle
**Priority:** P0
**Source:** `pkg/mcp/tool_save_recall_memory.go:112-114`
**Plain English:** Saving a memory under an existing key retires the previous value instead of blind-overwriting it.
**Specification:**
  Given a memory key that already exists for this owner/collection
  When  a new save commits in the same transaction
  Then  `archiveRetiredMemoryKey` moves the old row aside
  And   the transaction fails closed if archiving errors
**Suspected defect:** none — deliberate audit trail.
**Confidence:** High.

### RULE-016: Superseded memories hidden by default
**Category:** Lifecycle
**Priority:** P0
**Source:** `pkg/mcp/tool_save_recall_memory.go:434,543`
**Plain English:** Recall and listing exclude superseded memories unless the caller explicitly asks for history.
**Specification:**
  Given recall/list queries
  When  filters are appended (`appendMemoryFilters`)
  Then  superseded rows are excluded by default
  And   `includeSuperseded` opts in — and on that path, SQL errors surface instead of soft-falling-back
**Confidence:** High.

### RULE-017: Exclusive relationships auto-supersede
**Category:** Lifecycle
**Priority:** P0
**Source:** `pkg/orchestrator/pgupsert.go:35-37` (delegates to `pkg/graph.IsExclusiveRelationship`)
**Plain English:** When a new edge of an exclusive type (assigned_to, role_is, status_is, …) appears for the same source, the prior edge's validity is closed.
**Specification:**
  Given an inserted edge whose relationship is in the exclusive whitelist
  When  another edge with same source+rel already exists
  Then  the prior edge gets `valid_until = now` and `superseded_by = <new id>`
  And   non-exclusive relations (knows, mentions, related_to) coexist
**Parameters:** whitelist: assigned_to, role_is, status_is, located_in, lives_in, works_at, owns, reports_to, current_state, is_a (authoritative list in `pkg/graph.IsExclusiveRelationship`; pgupsert delegates).
**Specification detail (verified 2026-10-16):** the open edge is found by `SELECT id FROM graph_edges WHERE source_id=$1 AND target_id=$2 AND <relationMatch> AND dataset_id=$4 AND valid_until IS NULL ORDER BY id LIMIT 1` (:129); the close is `UPDATE graph_edges SET valid_until=$1, superseded_by=$2, updated_at=$3 WHERE ... AND (valid_until IS NULL)` (:187-192) — only the currently-open edge (valid_until IS NULL) is ever closed, so re-plays are idempotent.
**Confidence:** High.

### RULE-018: Graph upsert: deduped, one transaction, dataset-scoped
**Category:** Lifecycle
**Priority:** P0
**Source:** `pkg/orchestrator/pgupsert.go:38-40`
**Plain English:** Cognify writes land as one all-or-nothing transaction scoped to a tenant dataset.
**Specification:**
  Given extracted nodes and edges
  When  `UpsertGraphToPostgres` runs
  Then  nodes are deduped and written with edges in a single transaction
  And   `datasetID == ""` means legacy/global scope
**Confidence:** High.

### RULE-019: Postgres row lock on memory save
**Category:** Lifecycle
**Priority:** P1
**Source:** `pkg/mcp/tool_save_recall_memory.go:103-106`
**Plain English:** On PostgreSQL, memory saves take a row-exclusive table lock to serialize the archive-then-insert sequence; SQLite commits differently.
**Specification:**
  Given the target DB is PostgreSQL (`!memoryCommitSQLite`)
  When  the save transaction reaches the write phase
  Then  `LOCK TABLE memories IN ROW EXCLUSIVE MODE` is taken
  And   failure to lock aborts the save
**Confidence:** High.

### RULE-020: Index job gated on embed availability
**Category:** Lifecycle
**Priority:** P1
**Source:** `pkg/mcp/tool_save_recall_memory.go:138-170`
**Plain English:** A memory is saved even when the embed server is down; vector indexing is deferred via an index job and reported in the result.
**Specification:**
  Given embed is unavailable (breaker open / server down)
  When  save succeeds
  Then  the memory row commits without a vector
  And   an index job is enqueued only if `deps.EmbedAvailable()`
  And   the result reports `index_status` and `index_job_id` when a job exists
**Confidence:** High.

---

## Policy

### RULE-021: Recall cascade — vector first, LIKE fallback, embed errors never fail recall
**Category:** Policy
**Priority:** P0
**Source:** `pkg/mcp/tool_save_recall_memory.go:495-563`
**Plain English:** Recall tries semantic search first; if embedding or the vector index fails or matches nothing, it silently falls back to a structural SQL LIKE query rather than erroring.
**Specification:**
  Given a `recall_memory` call
  When  `recallViaVectorFiltered` hits an embed error, search error, or empty filtered set
  Then  it returns a soft miss (`(_, false)`)
  And   the caller proceeds to `recallViaSQLLike`
  And   only the `includeSuperseded` path converts SQL errors into tool errors
**Edge cases handled:** embed server down (the documented 1000× latency-variance incident class degrades to LIKE, not failure).
**Confidence:** High.

### RULE-022: Vector ranking restored after hydration
**Category:** Policy
**Priority:** P2
**Source:** `pkg/mcp/tool_save_recall_memory.go:557-560`
**Plain English:** After SQL re-fetches the matched rows, results are re-sorted back into vector-similarity order because SQL IN loses ordering.
**Specification:**
  Given hydrated rows from an `id IN (...)` query
  When  composing results
  Then  rows are sorted by their original vector-rank position (`sort.SliceStable`)
**Confidence:** High.

### RULE-023: Filters applied pre-rerank; tags ANY/OR; empty filter result is final
**Category:** Policy
**Priority:** P0
**Source:** `docs/search-strategies-guide.md:68`; code refs `internal/http/api_search.go:460`, `internal/grpc/service.go:1035`
**Plain English:** Room/tags/ACL filters run before dedup, fusion caps and rerank; matching any one selected tag is enough; and if an explicit filter matches nothing, the answer is an empty result — never an unfiltered fallback.
**Specification:**
  Given a search with explicit room/tags/ACL filters
  When  candidates are post-filtered
  Then  filters precede dedup/fusion/rerank and final authority checks
  And   tags use ANY/OR semantics
  And   zero matches after an explicit filter returns an empty result set
  And   MCP compares tags exactly; REST keeps case-insensitive legacy metadata matching
**Confidence:** Medium — documented contract plus overfetch code verified; exact MCP/REST comparison lines not read (SME Q8).

### RULE-024: Bounded overfetch ≠ exhaustive top-k
**Category:** Policy
**Priority:** P2
**Source:** `docs/search-strategies-guide.md:68`
**Plain English:** The fixed overfetch window bounds candidate recall; the system does not promise a complete filtered top-k for rare filters.
**Specification:**
  Given a rare filter that eliminates most of the overfetch window
  When  the filtered result is short
  Then  the short result is correct-by-contract (no window growth)
**Confidence:** Medium (documented design intent; preserved deliberately in refactor).

### RULE-025: wake_up budget and pin priority
**Category:** Policy
**Priority:** P1
**Source:** `AGENTS.md` (pin policy table); wake_up tool in `pkg/mcp`
**Plain English:** Session wake-up output is token-budgeted; pinned memories are served first within that budget.
**Specification:**
  Given `wake_up(max_tokens)`
  When  composing the briefing
  Then  pinned entries (priority 10 global, 8 infra, 5 active decisions) are included first
  And   the token budget bounds the total
**Parameters:** priority ladder 10/8/5/1-3 (AGENTS pin policy).
**Confidence:** Medium — tool and budget exist; ordering code not line-verified.

### RULE-026: Sync excludes vector collections by default
**Category:** Policy
**Priority:** P1
**Source:** `AGENTS.md` (Sync section)
**Plain English:** Mac↔Pi sync moves memories/interactions/graph but never vector collections unless explicitly opted in, because vectors would need re-embedding.
**Specification:**
  Given a `sync` invocation
  When  types are defaulted
  Then  collections are excluded
  And   opting in requires explicit `types=["collections"]` + named collections
**Confidence:** Medium — documented contract; not line-verified this pass.

### RULE-027: Recall LIKE path is owner-scoped
**Category:** Policy
**Priority:** P0
**Source:** `pkg/mcp/tool_save_recall_memory.go:564-566`
**Plain English:** The structural fallback search is always scoped to the calling owner, so one agent never sees another's unfiltered memories.
**Specification:**
  Given the SQL LIKE fallback path
  When  building conditions
  Then  the owner filter is mandatory (see `appendMemoryFilters` owner arg)
**Confidence:** High.

---

## Rules requiring SME confirmation — RESOLVED 2026-10-16

All owner decisions recorded verbatim below; cards above are final for
this pass.

| Q | Rule | Owner decision (2026-10-16) |
|---|---|---|
| Q2 | RULE-016/021 | PRESERVE the `includeSuperseded` error asymmetry as-is ("согласуется с Rule-021 и общей философией «обычный путь — availability, отладочный — честность»"). Both branches go into equivalence tests. |
| Q3 | RULE-007 | Matrix confirmed CANONICAL ("b1 - канон") — admin all / editor read+write / viewer read / other deny, frozen as-is. |
| Q4 | RULE-008 | Invariant CONFIRMED ("подтвердить инвариант… и закрыть его тестами в первой фазе — до любых перестроек internal/http"): API-key permissions may only narrow, never widen, role access; must be closed with tests in PHASE 1, before any internal/http restructuring. |
| Q8 | RULE-023 | Dual tag matching is a CONTRACT for this pass ("оставить контрактом"); tags unification = separate mini-project AFTER verify, not during the god-package split. |
| Q9 | RULE-015 family | Confirmed as a GAP; behavior FROZEN as-is for this pass; fix — explicit `supersedes_memory_id` should set `superseded_by` — is a SEPARATE post-refactor step with its own acceptance. |
| Q10 | RULE-007 | **NEW (2026-10-16):** the B1 "канон" confirmation was given on a mis-read matrix (editor shown without delete/share). Code truth: `normalizeAction` folds delete/share into the write class (finding M25), so editor effectively holds write+delete+share at RoleAllows level. Re-confirm: is the write-class collapse the intended canon (viewer stays protected, editor == admin at this layer), or should editor be split out in a later behavior-change pass? Phase 1 tests pin the CURRENT semantics either way. |

Resolved 2026-10-16 by source verification (folded into cards): Q1
(default type = `project`), Q5 (REST overfetch ×3/floor 10 identical to
gRPC, with the ACL-leak-avoidance rationale), Q6 (`recallMemoryVectorTopK`
= 10, `memoryValueLogMaxLen` = 100), Q7 (edge-close SQL
`pgupsert.go:129,187-192`; whitelist in `pkg/graph`).
