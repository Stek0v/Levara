# Feature audit tracker

Date: 2026-06-22
Status: working QA artifact

This document explains the local feature/user-story audit artifacts produced for
the app-wide behavior review. The spreadsheet is the canonical tracker for the
current pass; this document records how to interpret it, what was verified, and
what should happen next.

## Canonical artifact

The canonical workbook is:

```text
outputs/feature-audit/levara_feature_user_story_tracker.xlsx
```

It currently tracks:

| Area | Count |
|---|---:|
| User stories | 167 |
| Web UI pages | 16 |
| REST routes | 130 |
| MCP tools | 67 |
| Web API client bindings | 52 |
| Existing Playwright tests | 74 |

The workbook sheets are:

- `Summary`
- `User Stories`
- `Test Runs`
- `REST Inventory`
- `MCP Inventory`
- `WebUI Pages`
- `Web Client Bindings`
- `Existing E2E Tests`

## Verification status

The latest full local automated pass for the feature audit completed with:

| Gate | Result |
|---|---|
| Go commit gate | `make test-commit` passed S0-S4 |
| WebUI lint | `npm run lint` passed |
| WebUI build | `npm run build` passed |
| WebUI E2E | `LEVARA_API_URL=http://127.0.0.1:8081 npx playwright test` passed, 74 tests |
| Server build | `make build` passed |
| Commands | `go test ./cmd/...` passed |

During the first E2E pass, three logistical test failures were found and fixed:

- Auth setup waited for a full `/login` load and could time out before token
  seeding.
- The screenshot sweep tried to visit 11 pages inside the default 30 second
  test budget.
- The notebook badge check read body text immediately after navigation instead
  of waiting for visible badge locators.

The fixed E2E files are:

- `webui/e2e/helpers.ts`
- `webui/e2e/upload-flow.spec.ts`
- `webui/e2e/all-scenarios.spec.ts`

## Interpretation rules

- The workbook is a QA tracker, not an API reference.
- REST and MCP inventories are generated from code and are suitable for coverage
  planning.
- Web UI stories are based on page surfaces plus expected user behavior visible
  in the code and E2E suite.
- Web API client binding rows are generated from the TypeScript AST under
  `export const levara = { ... }`. Template paths are preserved as source text,
  for example `` `/api/v1/sync/status?limit=${limit}` ``.
- A story marked as passed means the current automated gates passed after fixes;
  it does not prove every negative, accessibility, performance, or live external
  integration behavior.

## Local files reviewed

The following untracked files are local source/research artifacts, not part of
the current app feature audit unless a follow-up explicitly makes them examples
or fixtures:

| File | Assessment |
|---|---|
| `152-fz.md` | Full Russian personal-data law text; useful for compliance/RAG testing, but should not be committed as product docs without source/licensing review. |
| `DCD_Design_red_mad_robot.pdf` | 14-page paper on Domain-Collection-Document RAG design; useful research input for router/workspace architecture, but should stay out of git unless added as a cited research asset. |

Generated support files under `outputs/feature-audit` are intentionally separate
from source. The local `node_modules` symlink and `.inspect.ndjson` dump are
ignored and should not be committed.

## Recommended next work

1. Add explicit E2E coverage for `/workspace`, `/sync`, `/admin`, dataset detail,
   and structured extraction happy/error states.
2. Add accessibility smoke checks for primary forms, navigation, dialogs, and
   table-heavy pages.
3. Add visual regression baselines for the screenshot sweep instead of only
   saving screenshots.
4. Decide whether `152-fz.md` and the DCD PDF should become a documented sample
   corpus. If yes, add license/source notes and ingest/retrieval test scenarios.
5. Promote the workbook builder into a maintained script once the output path
   policy is settled.

## Extractor validation

The SDK binding extractor is now AST-based and can be checked independently:

```bash
node scripts/feature-audit/extract_web_client_bindings.mjs webui/src/lib/api.ts
node --test scripts/feature-audit/extract_web_client_bindings.test.mjs
```

The current extractor sanity checks verify representative auth, dataset,
workspace, sync, graph, and admin bindings, and guard against empty or duplicate
rows.

## Documentation impact

`docs/levara-codex-user-admin-guide.html` has been expanded locally to cover:

- workspace knowledge plane usage;
- `workspace_search -> workspace_read` citation discipline;
- durable indexing jobs, watcher status, conflicts, commit/revert, and GC;
- structured PDF extraction sidecar workflow;
- memory consolidation and `reconcile_memory`;
- operational monitoring and production environment notes.

Before publishing that HTML guide, run a browser render pass and verify the long
tables remain readable on desktop and mobile widths.
