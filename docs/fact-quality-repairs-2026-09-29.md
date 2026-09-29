# Factual reliability repairs — 2026-09-29

Follow-up to [the original evaluation](fact-quality-results-2026-09-29.md).
These are local repairs and isolated validation results. Production services
were not upgraded or restarted. No schema migration, model download, paid
provider, or production test corpus was used.

## Before and after

| Reproduced defect | Repair | Observed evidence |
|---|---|---|
| Caller could label fabricated evidence as validated | Save and supersede derive status from the common transactional evidence validator; malformed, missing, foreign, failed and stale proof is rejected | Both SQL dialects, negative and valid-evidence controls |
| Evidence-free supersession defaulted to verified | No proof means `unverified`; caller labels are ignored | Public MCP supersession probes pass on SQLite and PostgreSQL |
| Failed or unspecified command exit could certify memory/completion | Command proof requires explicit signed 32-bit integer zero; each candidate's own receipt set is validated in the publication transaction | Exit 7 candidate rejected despite a separate successful criterion receipt; missing exit cannot complete |
| Personal Task promotion could overwrite a shared key | Promotion writes the exact task-owner namespace; explicit shared supersession requires current administrator or trusted-local authority | Private-shadow and current-authorization regressions |
| List/bootstrap omitted provenance | `list_memories` and `wake_up` return status, source Task and receipt IDs | Public probes plus budget tests preserving complete records |
| Current vector hit hid retired historical facts | Audit recall combines scoped literal history, semantic hits and reciprocal same-owner/same-collection predecessors | Frozen historical probe passes; isolation, cycles, limit and SQL failure regressions |
| Breaker cooldown exhausted outbox attempts before recovery | Cooldown rejection defers until retry time, refunds the non-provider attempt and retains the preceding error; stale claims/finishes are fenced | SQLite/PostgreSQL outbox tests and worker recovery with actual vector insertion |
| Shared local inference admitted overlapping calls | Worker uses background admission; local Python backend serializes model calls while health stays independent | Eight-call concurrency/exception test and real-provider bulk runs |
| Publication paths could acquire PostgreSQL locks in opposite order | Save/completion acquire the memory relation write lock before Task row locks | Deterministic red/green PostgreSQL regression for both paths |

The historical read error regression also demonstrated that a successful
literal query followed by failed SQL hydration could return an apparently
successful empty audit. SQL query, scan and iteration errors now propagate.

## Public checks

The unchanged 58-fact fixture SHA-256 is
`9befbbdb333d58a00ae993cd106d6c7677dedb202673fdcdd808697ec7f95acc`.
Both runs used bulk seeding, fresh isolated storage, authenticated full MCP,
the same newly built binary, and a separate offline cached embeddinggemma-300m
provider with 768 dimensions.

| Check | Previous evaluation | Repaired SQLite | Repaired PostgreSQL |
|---|---:|---:|---:|
| Bulk corpus indexing | Two runs failed: 58 and 50 dead-letter jobs | 58/58 completed, attempt 1 | 58/58 completed, attempt 1 |
| Fact retrieval and lifecycle | 46/47 after serial preload | 47/47, no errors/skips | 47/47, no errors/skips |
| All required facts within top 3, answerable queries | 36/36 | 36/36 | 36/36 |
| Returned query rows with identity/value/scope violations | 0/358 | 0/358 | 0/358 |
| Public trust/provenance probes | 3/9 | 9/9 | 9/9 |

The original trust script initially reported **8 pass, 0 fail, 1 error** on
each repaired dialect: it did not recognize the server's specific evidence
rejection phrase. Those reports are retained. The script now recognizes that
phrase, verifies the rejected row is absent, then requires a successful
ordinary write/readback with the exact value and unverified provenance in the
same scope. Self-checks reject database/transport errors, mutation despite
rejection, and invalid positive controls. The final script SHA-256 is
`44398598c1defc3c3986de3e432dbf32d5dbd6fd24b40bb0ea14ffbb8b96672c`.
No fact answers, expected source identities, retrieval thresholds or trust
requirements were relaxed.

## Code validation and evidence

- Focused trust race run: 103 top-level tests, 343 including subtests, no skips.
- Historical/provenance race checks cover SQLite and PostgreSQL.
- Final integration race run across `pkg/mcp`, `pkg/embed`, `pkg/memoryindex`,
  `internal/http` and `cmd/server`: exit 0, all five packages passed,
  1,140 top-level tests / 2,393 including subtests passed. One unrelated opt-in
  `TestDCDVSALoadBaseline` was skipped because `LEVARA_DCD_VSA_LOAD_CASES` was
  unset; there were no hidden skips or skipped PostgreSQL regressions.
- Python backend: 13 existing server tests plus the new concurrency regression
  pass; the existing Starlette/httpx deprecation warning remains.
- Full and core contracts were regenerated and `make contract-check` passed.
- Red results are preserved for trust, provenance, history, lock order,
  worker retry accounting, model concurrency and audit hydration failure.

Local raw evidence is archived under `.git/quality-evidence/20260929-fact-repairs`:
public reports, MCP exchanges, frozen fixtures, gold identities, query rows,
command logs, preflight/configuration, source hashes and cleanup records.
`MANIFEST.json` records file hashes. This local archive is deliberately outside
version control; this report and the runnable regressions are tracked.
The 74-file manifest SHA-256 is
`965d08deda3128e04fb0c6e260d16f86cdf4b9a933c51ae281485c9d95a16bad`.

Test binary SHA-256:
`95c9d390a6a7c216ec86a552462d0873cf3eae14f9607b37110715488e091684`.
The owning Task is `d23f1274-11ac-44e4-aa9c-ac4ee7ea5773`.
Both temporary Levara servers, the isolated embedding process and the isolated
PostgreSQL instance were stopped after validation.

## Compatibility and limits

- A validated receipt establishes an evidence link, not independent truth or
  semantic support. Caller-submitted command receipts do not prove execution.
  Existing stored `verified` labels were not retroactively checked.
- Missing command exit can still be recorded for audit but cannot validate a
  command. Explicit null, strings, fractions and out-of-range values are rejected.
- Personal publication no longer implicitly changes shared memory. Clients
  relying on caller-assigned trust or that shared write behavior must adapt.
- Bootstrap provenance consumes the same token budget, so fewer complete
  memories may fit. It does not silently strip evidence to fit more text.
- Audit history is bounded at 20 records. Unlinked retired records are not
  guaranteed semantic discovery; current-only recall remains unchanged.
- Three unknown questions still return neighbors. Retrieval alone is not an
  abstention policy. The earlier answerer result (22/39 strict passes), entity
  confusion and answer/citation failures were not addressed or regraded here.
- The bulk runs are bounded checks, not a soak test or proof of the native
  Metal crash's root cause. Real provider failures retain the existing retry
  budget; serialization trades model throughput for safe shared inference.
- Document ranking/reranking was not changed; the prior document benchmark's
  limitations remain. No new ranking layer was added.

## Amendment 2026-09-29 evening: deferral budget (fixlist F1)

The deferral described above refunded the non-provider attempt, which let a
permanently unavailable provider park a job forever: load-gate S3 drained
never (`drained=false`, 435 pending). Deferrals no longer refund the claim's
attempt; once attempts reaches the same maxAttempts budget the job transitions
to `dead_letter` with the preceding failure retained (`memory_index_retry`
requeues it after recovery). Unit tests cover both SQL dialects.

The load gate itself was also made honest: S3 asserts "every job completes
exactly once", but its servers could never embed (primary had no embed
endpoint; the secondary requested a model the local backend does not serve),
so the assertion passed only vacuously in CI — failed jobs hid from the drain
check behind future retry times. Both servers now embed through a
deterministic stdlib stub (`--embed-endpoint/--embed-model`), so S3 verifies
1000/1000 completions with zero dead letters and zero duplicate claims.
