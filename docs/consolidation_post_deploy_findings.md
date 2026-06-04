# Consolidation / potion-256 cutover — findings for further analysis

Context: 2026-06-02 deploy on Pi (10.23.0.53:8090). Fix #1 (dim crash guard,
commit `2cffe43`) + Fix #2 (server reconfig to potion-256: `EMBEDDING_ENDPOINT`
→ potion sidecar `/v1/embeddings`, `EMBEDDING_MODEL=potion-code-16M`,
`-dim=256`). Both validated in prod. The items below are open problems and
risks surfaced by testing — none are regressions from the fixes; they are
pre-existing issues the fixes exposed or left for follow-up.

## P1 — High priority

### P1.1 Fix #2 lives only on the Pi, not in version control / IaC — FIXED (2026-06-04)
The prod fix was in the systemd unit (`/etc/systemd/system/levara.service`,
`-dim=256`) and `~/levara/levara.env` (`EMBEDDING_ENDPOINT`/`EMBEDDING_MODEL`).
If the Pi were reprovisioned, the repo IaC (still nomic-768) would restore a
half-migrated state.

**Fixed:** potion-256 baked into the repo deployment artifacts:
- `Raspberry/levara.service` (prod mirror) + `Levara/deploy/raspberry/levara.service`
  (generic template): `-dim=768` → `-dim=256`; both now `Wants`/`After`
  `embed-potion.service` (weak dep — an embedder crash must not take Levara down).
- `Raspberry/levara.env` + `Levara/deploy/raspberry/levara.env`:
  `EMBEDDING_ENDPOINT=http://localhost:9101/v1/embeddings`,
  `EMBEDDING_MODEL=potion-code-16M`; nomic kept as a commented revert fallback.
- **New** `deploy/bench/embed-potion.service`: the prod potion sidecar unit
  (loopback `:9101`, `EMBED_BENCH_MODEL=potion`, sandboxed) so reprovisioning
  brings up the embedder too — without it, Levara would point at a `:9101` that
  doesn't exist. Modeled on the existing `embed-bench.service`.

A fresh checkout now provisions Levara + sidecar at potion-256 with no manual
patching. Backups of the live Pi config remain: `*.bak.20260602-232659`.
Not yet applied to the running Pi (these are repo artifacts; prod already runs
the equivalent live config). Binary `-dim` default stays 128 (generic); every
unit passes `-dim` explicitly, so the default is never the prod value.

### P1.2 Silent error-swallowing hides incompatible collections — FIXED (`01fd0fe`)
`tool_consolidate.go` used to do `if err != nil { continue }`. Now that a dim
mismatch returns an error (Fix #1), a 768 collection produced `clusters=0` with
NO operator-visible signal that the collection is embed-incompatible. Observed
directly in validation check [3] (`local-net`: candidates=28, clusters=0).
**Fix:** `store.ErrDimMismatch` sentinel (Search wraps with `%w`); the
edge-builder detects it once via `errors.Is`, flags
`collectionNeighbors.dimMismatch`, and `ToolConsolidate` emits a visible
warning note + `ConsolidationRuns{dim_incompatible}` metric while still
returning a clean non-error result (sweep/janitor keep going). Regression test
`TestToolConsolidate_SurfacesDimIncompatible`.

### P1.3 Recall quality during the half-migrated window is unaudited — RUNBOOK (2026-06-04)
Before the fix, the server queried at 768 against 256 memory collections. Recall
on those either returned nothing or errored. Question for audit: were any
memory writes/decisions made while recall was silently returning empty? Could
have caused duplicate saves or "memory not found" false negatives.

**Window reconstruction (from local memory + docs, no prod touch):**
- The empty-recall failure mode predates the cutover: `chunksSearch`
  (`api_search.go`) swallowed embed/search errors → `nil` allResults →
  JSON `null` (see `discovery_pi_search_text_null`, observed 2026-05-25).
  After a collection flipped to 256d while the server still embedded queries
  at nomic-768, every recall on it dim-mismatched and returned empty — silently,
  until Fix #1 (dim crash guard) + Fix #2 (server→potion-256) landed 2026-06-02.
- Per-collection risk window = `[collection cutover date → 2026-06-02]`.
  Known cutovers: `_memories_UB-main` 2026-05-27 (first); `uploads`,
  `uploads_chunktest`, `memory-compare` 2026-05-28; "more `_memories_*`"
  same batch (full set must be enumerated on prod). Only **migrated** memory
  collections are at risk, and only **after** their own cutover.
- Note: `_memories_UB-main` already held two textual-duplicate pairs at
  migration time (`prod_admin_credentials`, `prod_server_access` — 4 records,
  2 facts ×2). Those predate the window; do not count them as window damage,
  but they show duplicate-saving is a real pattern in these stores.

**Audit procedure (read-only; run via `levara-pi` MCP, no SSH/secrets):**
1. Enumerate `_memories_*` collections with `embedding_dim` and the timestamp
   they became 256d (the cutover). Yields the at-risk set + each window.
2. For each, `list_memories(collection=X)`; bucket records by `created_at`
   into the window `[cutover, 2026-06-02]` vs outside.
3. Duplicate detection: within each collection, flag window-created records
   whose (room, hall, value) is a near-duplicate of an earlier record — the
   signature of a recall-before-save that returned empty and re-saved.
4. False-negative pass: no durable signal exists for "memory not found" at
   read time; settle for the dup-save proxy above. Record any window record
   that re-states a pre-window fact as a likely false-negative re-entry.
5. Report: per-collection {window record count, suspected dup count, examples}.
   Zero window dups across all migrated stores ⇒ window caused no memory
   damage; close P1.3. Otherwise list the dup keys for manual merge via
   `consolidate` (the dedup tool this whole effort built).

**Status:** runbook ready; prod read not executed (MCP read blocked this
session). Low expected blast radius — migrated memory collections are tiny
(UB-main 4 recs) and the window is ~6 days of low write volume.

## P2 — Medium priority

### P2.1 `model=''` metadata on 3 straggler memory collections — FIXED (code) / cleanup pending
`_memories_consol_sandbox`, `_memories_consol_sb1`, `_memories_local-net` are at
768 with empty `embedding_model`. Empty model metadata means they were created
by a path that didn't record the embedder.

**Root cause:** the lazy auto-create path `CollectionManager.Insert → getOrCreate
→ Create` stamped `embedding_model=''` because the manager had no notion of the
configured embedder. The `_memories_*` sidecars written by `indexMemoryAsync`
take exactly this path, so every memory write into a fresh context minted a
straggler.

**Fixed (code):** `CollectionManager` gained a `defaultModel` field +
`SetDefaultModel`, wired from `EMBEDDING_MODEL` in `cmd/server/main.go`. `Create`
now stamps it, so lazily auto-created collections carry the embedder. Test
`TestLazyCreateStampsDefaultModel`. Explicit `CreateWithDim`/`CreateWithMeta`
callers (which already pass a model) are unaffected.

**Cleanup pending (prod data):** the 3 existing stragglers still carry `''` —
this fix only prevents new ones. They are deletion candidates (2 Phase-A sandbox
leftovers; 1 stale hyphen-dup of the live `_memories_local_net`). Handle on the
next Pi touch.

### P2.2 Base `_memories` store (128 records) can't be consolidated on-demand — FIXED
`memoryCollectionName("")` → `_memories`, but the consolidate tool rejected an
empty `collection` arg. So the largest memory store was unreachable via
on-demand consolidate; only the janitor (`RunOnce`, prefix sweep) touched it.

**Fixed:** callers target the base store explicitly by its vector-collection
name `_memories`. `ToolConsolidate` translates that sentinel to the empty SQL
`collection_name=''` filter (`sqlCollection`), so `sqlStore`, the neighbor
collection, and `Params.Collection` all resolve to the base store rather than a
literal `collection_name='_memories'` (which matches nothing). Empty `collection`
is still rejected (no accidental base-store consolidation); the error and tool
schema now point at `_memories`. Added `baseMemoryCollection` const. Test
`TestToolConsolidate_TargetsBaseStore`.

### P2.3 potion mem0-envelope collapse → false-merge risk — FIXED (`7b7a315`)
Known issue (`discovery_potion_mem0_envelope_collapse`): potion embeddings
converge (cos ≈ 0.9999996) for mem0 records sharing a long common header. Since
consolidation merges mechanically at cosine ≥ TauHigh, envelope collapse could
cause INCORRECT merges of semantically different records (keep newest, supersede
the rest → distinct bodies silently lost). **Fix:** `mergeSafe` content-diff
guard in `Plan` — a cosine-tight cluster is mechanically merged only if every
source's tokens are ≥85% subsumed by the survivor (`MaxMergeLossFraction=0.15`);
otherwise it is downgraded to an LLM abstraction, which preserves every source
via the coverage guard. Token-set based (numbers + words), so a shared header
can't mask a distinct body. Tests `TestPlan_EnvelopeCollapseDowngradesToAbstract`
/ `TestPlan_NearDuplicateStillMerges`.

### P2.4 Latent crash path: `shard.go:126` raw `db.Search` has no dim guard — FIXED
Fix #1 guards `CollectionManager.Search` (the memory/consolidate path). The
sharded path (`shard.go:126` → `s.Search`) calls raw `db.Search`, which still
panicked on a mismatched query. Memory collections aren't sharded today, so it
wasn't hit — but it was a latent DoS if a sharded collection ever got a
mismatched query.

**Fixed:** `Levara.Search` now opens with `if len(query) != db.dim { return nil }`,
matching the existing empty-query/empty-entry guard clauses (Search returns no
error, so a mismatch degrades to an empty result instead of panicking in
`dist`/`vek32.Dot`). Test `TestDBSearchDimMismatchNoPanic` in
`internal/store/db_dimguard_test.go` inserts a 64-dim vector then searches with a
32-dim query and asserts no panic / zero results.

### P2.5 `skipped=1` on `_memories_levara` — skip reason not logged — FIXED
Validation [2]: candidates=25, clusters=1, actions=0, skipped=1. One cluster
was found but skipped, with no recorded reason (coverage-guard rejection? merge
skip? borderline cosine?). Log the skip reason so dry-runs are interpretable.

**Diagnosed 2026-06-03 (reproduced on Pi, `scripts/consol_repro_skip.py`).**
Both swept skips are coverage-guard rejections in `AbstractValue` (run.go
swallows the error as `Skipped++`). Exact reasons:
- `levara`: 14-record cluster (cosine 0.8506..0.9586). 14 distinct technical
  notes over-clustered at TauLow=0.85; DeepSeek truncated at `max_tokens=512`
  and dropped source number `"100"` → reject. The skip *prevented a bad merge*
  — these are not duplicates.
- `localllm`: 3-record cluster (cosine 0.9356..0.9934). Coherent summary, but
  dropped the capitalized token `"REPL"`, which `entityRe` treats as an
  entity → reject. False-positive: `REPL` is not a meaning-bearing entity here.

Two real defects surfaced (both worth fixing, neither is data loss since
dry_run writes nothing) — **all three items below now FIXED:**
1. **`entityRe` too crude** (`\b[A-Z][A-Za-z0-9]+\b`) — flagged any
   capitalized word (`REPL`, `CREATE`, `NULL`, sentence starts) as an entity,
   causing false rejects. **FIXED:** `isEntityToken` gates entity counting
   through a `nonEntityStopwords` set (common English + code/SQL keywords);
   digit-bearing and mixed-case identifiers (DeepSeek, Bm25) always count, real
   acronyms (HNSW) still count. Regression tests
   `TestAbstractValue_IgnoresStopwordCapitalizedTokens` /
   `TestAbstractValue_RealAcronymStillCounts`.
2. **No over-cluster / LLM-input bound** — 14 long records in one abstract
   cluster always overflow `max_tokens=512` and always skip. **FIXED in
   `e4344d5`:** `MaxAbstractSize` cap skips oversized clusters before the LLM
   call with reason "cluster too large for abstraction (N > M)".
3. **Skip reason not surfaced** — **FIXED in `e4344d5`:** `run.go` records
   `res.Skips[]{SourceIDs, Reason}` and `tool_consolidate` renders
   `skip [ids]: reason` in the result text.

## P3 — Hygiene / lower priority

### P3.1 203 live junk 768 test/benchmark collections
`rag_*`, `slide_*`, `rerank_*`, `regr_*`, `pc_*`, `pcs_*`, `mixed_*`, `snap_*`,
`sec_*`, `dedup_*`, `dedrel_*`, `ctx_*`, `test-*`, etc. Now harmless no-ops on
search post-cutover, but they pollute the instance and any future global sweep.
Add a TTL / cleanup policy for test collections.

### P3.2 DeepSeek API key exposure (security)
Key present in chat history, Pi bash history, and `~/levara/levara.env` (mode
600). Rotation recommended; never commit to git.

### P3.3 Deferred enhancements (pre-existing backlog)
- **Per-run LLM budget cap — FIXED (code, 624f9de).** `Config.MaxLLMCalls`
  (default 24) caps abstract Summarizer (DeepSeek) calls per `Run`; abstract
  clusters beyond the cap are recorded as `Skips` with reason
  `"LLM call budget exhausted (N calls)"` instead of being charged/truncated.
  0 = unbounded (back-compat). Oversized-cluster skip precedes the budget check,
  so it never consumes budget. Test: `TestRun_LLMCallBudgetCapsAbstractions`.
- **Skipped-cluster metric — FIXED (code, 3ecf9c6).**
  `levara_consolidation_skipped_total{reason}` with bounded reason categories
  (`oversized`/`llm_budget`/`coverage_guard`/`other`), incremented per skip in
  the consolidate handler; all four eager-init at 0. Makes the budget cap
  observable (`llm_budget` fires when a sweep hits the cap). Categorizer:
  `consolidationSkipCategory`, test `TestConsolidationSkipCategory`.
- **`char_density` metric — FIXED (code).** `levara_consolidation_char_density`
  histogram (label `kind`=merge/abstract) observes survivor-chars / total-
  source-chars per action — a low ratio flags aggressive compression / loss.
  Computed in the engine (`actionCharDensity`, returned via `Result.Densities`
  aligned with `Actions`) and observed in the handler. Buckets 0.1–1.5; both
  kinds eager-init. Tests `TestActionCharDensity`, `TestRun_ReportsCharDensities`.
- **Budget scope — decided per-collection (2026-06-03).** `MaxLLMCalls` is
  scoped to one `Run()` = one collection, not one janitor sweep. A full sweep of
  N collections can therefore make up to N×24 calls. Decision: keep per-collection
  (it bounds a single pathological collection's blast radius; on-demand
  `ToolConsolidate` is always one collection, so per-collection is the right scope
  there too). A sweep-level budget is only meaningful once the janitor is enabled,
  so it is deferred to — and paired with — Phase C janitor enablement rather than
  added now against a risk that does not yet exist.
- Still open: Phase C janitor enablement (`CONSOLIDATION_INTERVAL`) once
  on-demand behavior is trusted; add the per-sweep LLM budget at that time.
  **All P3.3 code items are now done.**

## Full 256 sweep (2026-06-02, dry_run, DeepSeek live)

Swept 12 logical 256-dim memory collections (123 candidates). DeepSeek confirmed
invoked: `external_call{op=complete,result=ok,target=llm}` count=5, sum=14.6s
(~2.9s/call). Server alive throughout; all record counts unchanged (dry_run
wrote nothing).

| collection | cand | clusters | actions | skipped |
|---|---|---|---|---|
| levara | 25 | 1 | 0 | 1 |
| localllm | 13 | 1 | 0 | 1 |
| localllm-e2e-test | 33 | 6 | 6 | 0 |
| (other 9) | 52 | 0 | 0 | 0 |
| **TOTAL** | **123** | **8** | **6** | **2** |

Observations:
- **F-sweep.1 — actions concentrated entirely in test data.** All 6 planned
  actions are in `localllm-e2e-test` (e2e fixtures). Real user memory
  (levara, qvant, yandex_direct, cerbalab.ru, …) produced 0 actionable
  clusters — content is genuinely distinct. Good (no over-eager merging on
  real data) but means the only DeepSeek exercise was on synthetic data;
  re-run on real data periodically as memory grows.
- **F-sweep.2 — 6 clusters from 33 e2e records: verify before any real apply.**
  `localllm-e2e-test` clustered aggressively. Confirm these are true duplicates
  and not potion envelope-collapse false-merges (see P2.3) before running a
  non-dry consolidation on it.
- **F-sweep.3 — skipped clusters (levara, localllm) have no recorded reason.**
  Reinforces P2.5: 2 clusters were found but skipped; dry_run gives no insight
  into why (coverage guard vs merge skip).

## Validation evidence (2026-06-02, post-deploy)
- [1] recall on `_memories_levara` (256) → 200, real records. Embed/search dims match.
- [2] consolidate dry_run on `levara` (256) → candidates=25, clusters=1, server alive (was: crash).
- [3] consolidate dry_run on `local-net` (768) → safe no-op, candidates=28, clusters=0, server alive (was: crash).
- Stability: NRestarts=0, 0 panics, 54 `potion-code-16M status=ok` embeds, running SHA `2cffe43`.

## Heavy quality harness (2026-06-03, `scripts/consol_quality.py`)

The 3-defect fix (commit `e4344d5`) was built fresh for arm64, deployed to the
Pi (binary SHA1 `b659306991aa…`, backup at `levara.bak.20260603-005228`), and
validated end-to-end by a new cross-domain quality harness. **16/16 PASS.**

- **Module A — recall before/after a real `consolidate(dry_run=false)`.** Seeds
  ~18 records with known facts across 5 rooms (auth/deploy/mcp/embed/kg) plus
  corner records (RETRACTED note, code-keywords, number-dense), into a
  throwaway collection, runs 6 hard cross-domain queries with ground truth,
  consolidates for real, re-measures, then tears the collection down. Result:
  2 merge clusters fired, **fact-recall 1.00 → 1.00, ZERO ground-truth facts
  lost**, cross-domain query intact.
- **Module B — coverage-guard corner cases.** B1 exercises the guard function
  deterministically (dropped/invented number → reject; entity fraction at/over
  10% tolerance; code-keyword drop within tolerance). B2 runs adversarial
  clusters through live potion + DeepSeek: tight cluster → MERGE (cos 0.9978),
  oversized 7-record cluster → SKIP("too large"), related pair → abstract
  preserving every number. All pass.
- **Module C — live prod smoke (dry_run).** 10 real collections, no crash,
  server healthy after. The deployed fix is **visibly live**: `levara` →
  `skip: cluster too large for abstraction (12 > 6)`; `localllm` alternately
  `skip: dropped 2/12 source entities (17% > 10%): [REPL Real]` or a clean
  abstract depending on DeepSeek run (guard as designed).

### F-quality.1 — latent embedding mismatch in the consolidation edge-builder — FIXED
`collectionNeighbors.Edges` embedded **`r.Value` only** (`tool_consolidate.go:140`),
but `save_memory` indexes **`embed(key + " " + value)`** (`indexMemoryAsync`). A
long memory `key` therefore polluted the stored vector relative to the value-only
query vector, sinking even near-identical records below `TauLow` and starving the
clusterer. In prod keys are short so the effect was mild, but it meant
consolidation under-clustered when keys carry significant text. Surfaced because
the harness's first fixture used long unique keys → `clusters=0`; shortening keys
restored expected clustering.

**Fix (Variant A):** `Edges` now embeds `r.Key+" "+r.Value`, matching the index,
so edge geometry is symmetric and consistent with both the stored vectors and
`recall`. Regression test `TestEdges_EmbedsKeyPlusValue` asserts the embedded
text equals `key+" "+value` (fails under the old value-only path). Variant B
(value-only on both sides via in-process pairwise cosine, dropping HNSW) was
considered and deferred — cleaner for dedup semantics but a larger change that
shifts the tuned `TauLow/TauHigh` geometry; revisit only if key-pollution is
shown to hurt dedup quality.

**Deployed + re-validated (2026-06-03).** Built arm64, deployed to Pi (Build ID
`740f576fd658…`, backup `levara.bak.20260603-065120`), health 200. Full harness
re-run against the live fixed binary: **16/16 PASS, exit 0.** Module A engaged
(candidates=18, clusters=2, actions=2), fact-recall held 1.00 → 1.00, zero
ground-truth facts lost. Prod clean afterward (0 leftover `qual_fixture` rows),
`NRestarts=0`, service active.
