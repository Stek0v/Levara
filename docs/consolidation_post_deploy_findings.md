# Consolidation / potion-256 cutover — findings for further analysis

Context: 2026-06-02 deploy on Pi (10.23.0.53:8090). Fix #1 (dim crash guard,
commit `2cffe43`) + Fix #2 (server reconfig to potion-256: `EMBEDDING_ENDPOINT`
→ potion sidecar `/v1/embeddings`, `EMBEDDING_MODEL=potion-code-16M`,
`-dim=256`). Both validated in prod. The items below are open problems and
risks surfaced by testing — none are regressions from the fixes; they are
pre-existing issues the fixes exposed or left for follow-up.

## P1 — High priority

### P1.1 Fix #2 lives only on the Pi, not in version control / IaC
The prod fix is in the systemd unit (`/etc/systemd/system/levara.service`,
`-dim=256`) and `~/levara/levara.env` (`EMBEDDING_ENDPOINT`/`EMBEDDING_MODEL`).
If the Pi is reprovisioned, the binary default (`-dim=768`) and empty embed
config come back → half-migrated state returns. Bake potion-256 defaults into
the deployment config / repo so the fix survives reprovisioning.
Backups taken: `*.bak.20260602-232659` (binary, env, unit).

### P1.2 Silent error-swallowing hides incompatible collections
`tool_consolidate.go:144-146` does `if err != nil { continue }`. Now that a dim
mismatch returns an error (Fix #1), a 768 collection produces `clusters=0` with
NO operator-visible signal that the collection is embed-incompatible. Observed
directly in validation check [3] (`local-net`: candidates=28, clusters=0).
Need: surface "collection dim incompatible with server embedder" as a
warning/metric/run-note, not a silent zero.

### P1.3 Recall quality during the half-migrated window is unaudited
Before the fix, the server queried at 768 against 256 memory collections. Recall
on those either returned nothing or errored. Question for audit: were any
memory writes/decisions made while recall was silently returning empty? Could
have caused duplicate saves or "memory not found" false negatives.

## P2 — Medium priority

### P2.1 `model=''` metadata on 3 straggler memory collections
`_memories_consol_sandbox`, `_memories_consol_sb1`, `_memories_local-net` are at
768 with empty `embedding_model`. Empty model metadata means they were created
by a path that didn't record the embedder. Investigate metadata integrity for
collection creation; these 3 are also cleanup candidates (2 are Phase-A sandbox
leftovers, 1 is a stale hyphen-dup of the live `_memories_local_net`).

### P2.2 Base `_memories` store (128 records) can't be consolidated on-demand
`memoryCollectionName("")` → `_memories`, but the consolidate tool rejects an
empty `collection` arg (`tool_consolidate.go:209`). So the largest memory store
is unreachable via on-demand consolidate; only the janitor (`RunOnce`, prefix
sweep) touches it. Add a way to target the base store explicitly.

### P2.3 potion mem0-envelope collapse → false-merge risk in consolidation
Known issue (`discovery_potion_mem0_envelope_collapse`): potion embeddings
converge (cos ≈ 0.9999996) for mem0 records sharing a long common header. Since
consolidation clusters by cosine ≥ 0.85 / merges at ≥ 0.97, envelope collapse
could cause INCORRECT merges of semantically different records. Watch for
suspiciously large/over-eager clusters on mem0-sourced collections; consider a
content-diff guard before merge.

### P2.4 Latent crash path: `shard.go:126` raw `db.Search` has no dim guard
Fix #1 guards `CollectionManager.Search` (the memory/consolidate path). The
sharded path (`shard.go:126` → `s.Search`) calls raw `db.Search`, which still
panics on a mismatched query. Memory collections aren't sharded today, so it's
not hit — but it's a latent DoS if a sharded collection ever gets a mismatched
query. Consider a guard inside `db.Search` itself (defense in depth).

### P2.5 `skipped=1` on `_memories_levara` — skip reason not logged
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
dry_run writes nothing):
1. **`entityRe` too crude** (`\b[A-Z][A-Za-z0-9]+\b`) — flags any
   capitalized word (`REPL`, `CREATE`, `NULL`, sentence starts) as an entity,
   causing false rejects. Add a stopword list / length-or-frequency gate.
2. **No over-cluster / LLM-input bound** — 14 long records in one abstract
   cluster always overflow `max_tokens=512` and always skip. Raise TauLow for
   long records, cap abstract-cluster size, or scale `MaxTokens` from total
   source length.
3. **Skip reason not surfaced** — return the guard reason in the `consolidate`
   result/run-note; without it this diagnosis required an out-of-band repro.

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
Per-run LLM budget cap for consolidation; `char_density` metrics; Phase C
janitor enablement (`CONSOLIDATION_INTERVAL`) once on-demand behavior is trusted.

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
