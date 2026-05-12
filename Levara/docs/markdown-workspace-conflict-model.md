# Markdown Workspace Conflict Model

## Purpose

This contract defines how Levara resolves drift between Markdown filesystem truth,
the active indexed generation, and concurrent team activity.

The baseline rule is strict:

- `filesystem_truth_wins`
- writes are `last-writer-wins` until `workspace_commit`
- optimistic locking is available through `expected_file_digest`
- search answers must not be trusted when `workspace_conflicts.has_conflicts=true`

## State Model

Levara tracks three separate states for a project branch:

1. Filesystem truth: current `.md` files under the workspace root.
2. Active generation: the currently published vector/BM25 generation.
3. Operational state: watcher status and indexing jobs.

A branch is considered conflicted when any of the following is true:

- the branch has no `active_generation`
- a file digest differs from the active generation digest
- a Markdown file exists in filesystem truth but is absent from the active generation
- the active generation references a path missing from filesystem truth
- watcher branch status is `pending`
- watcher branch status has `last_error`
- indexing jobs contain `failed` or `dead_letter`

## Conflict Categories

### Dirty Paths

`dirty_paths` means the file exists both in Markdown truth and the active
generation, but the digest differs. This is the canonical signal for
`edit happened after the last active generation`.

### Unindexed Paths

`unindexed_paths` means a Markdown file exists locally but has never been
published into the active generation.

### Missing Indexed Paths

`missing_indexed_paths` means search still points at a path that has already been
deleted or removed from Markdown truth.

### Operational Drift

Even when file digests match, the branch must still be treated as conflicted if:

- watcher status is still pending
- watcher recorded a branch error
- reconcile jobs are `failed`
- reconcile jobs are in `dead_letter`

This prevents Codex/Claude from treating stale search results as authoritative.

## Write and Commit Semantics

### Single Writer

- `workspace_write` updates filesystem truth immediately.
- Without `expected_file_digest`, the most recent write wins.
- `workspace_commit` snapshots filesystem truth, not the active generation.

### Team Write Safety

For team edits, the required safe sequence is:

1. `workspace_read`
2. capture `file_digest`
3. `workspace_write(expected_file_digest=...)`
4. if conflict: reread and merge
5. `workspace_commit`
6. `workspace_reconcile` or `workspace_write(index=true)`

If `expected_file_digest` is stale, Levara must reject the write with a conflict
response and leave the file unchanged.

## Revert and Reconcile

`workspace_revert` restores Markdown truth to a historical commit. It does not,
by itself, republish a fresh active generation.

That means the expected sequence after revert is:

1. `workspace_revert`
2. `workspace_conflicts`
3. `workspace_reconcile(activate_generation=true)`

Until reconcile completes, `dirty_paths` or `missing_indexed_paths` are expected.

## Answering Contract For Agents

Before answering from indexed search, agents should use:

1. `workspace_context`
2. `workspace_conflicts`
3. `workspace_search`
4. `workspace_read`

If `workspace_conflicts.has_conflicts=true`, the agent must either:

- wait for watcher/reconcile to drain, or
- explicitly reconcile and then rerun search

Answers should still follow the citation contract from
`markdown-workspace-answer-contract.md`, including:

- `path`
- `heading`
- `generation`
- `freshness`
- `source_uri`
- `stale`
- `potentially_stale`

## Recommended Operator Actions

The minimum operator responses are:

- missing generation: `workspace_reconcile`
- dirty/unindexed/missing paths: fresh reconcile with `activate_generation=true`
- watcher pending: wait for drain or reconcile manually
- watcher error: fix root cause, then reconcile
- failed jobs: inspect failed entries and rerun
- dead-letter jobs: inspect root cause, retry or enqueue a fresh generation

## DoD

- Conflict policy is documented and matches `workspace_conflicts.Policy`.
- REST and MCP both expose `dirty_paths`, `unindexed_paths`,
  `missing_indexed_paths`, watcher status, job status summary, and
  `recommended_actions`.
- Tests cover:
  - missing active generation
  - stale `expected_file_digest`
  - revert causing drift until reconcile
  - watcher pending / watcher error
  - failed and dead-letter jobs
  - conflict-free steady state
