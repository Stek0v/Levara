# Feature audit extractor fix plan

Date: 2026-06-22
Status: implemented

## Problem

The feature-audit workbook currently extracts Web API client bindings from
`webui/src/lib/api.ts` with a broad regular expression. That loses structure
inside the `export const levara = { ... }` object and mis-associates method names
with endpoint strings when a property uses:

- block-bodied arrow functions;
- `async` functions with local `const res = await api(...)`;
- chained `.then(...)` calls;
- template literals with nested `${...}` expressions;
- helper-built query strings such as `URLSearchParams`.

Observed result: the workbook reports 22 Web API client bindings, while the
current `levara` client contains 52 `api<...>` calls after the wrapper function.

## Design

Replace the regexp extractor with a TypeScript AST extractor. This has been
implemented in:

```text
scripts/feature-audit/extract_web_client_bindings.mjs
scripts/feature-audit/extract_web_client_bindings.test.mjs
```

The extractor should:

1. Parse `webui/src/lib/api.ts` with the TypeScript compiler API.
2. Find `export const levara = { ... }`.
3. Iterate only top-level properties of that object.
4. For each property, traverse only that property's initializer subtree.
5. Collect calls to:
   - `api(...)`;
   - optionally direct `fetch(...)` calls if they appear in future client
     bindings, but exclude the wrapper-level `fetch` outside `levara`.
6. Extract one row per call, not one row per property. If a client method ever
   calls more than one endpoint, preserve all calls with `call_index`.
7. Extract:
   - `client`;
   - `call_index`;
   - `method`, defaulting to `GET` when no `method` option exists;
   - `path`;
   - `path_kind`: `static`, `template`, or `dynamic`;
   - `source_line`;
   - `confidence`;
   - `notes`.
8. Preserve template literals as source text when they include interpolation,
   for example `` `/api/v1/datasets/${id}` ``.
9. Decode plain string literals to normal endpoint text, for example
   `/api/v1/search/text`.
10. Mark non-literal or computed paths as `dynamic` with lower confidence
    instead of guessing.

## Implementation tasks

### T1. Move extractor logic out of workbook generation

Create a small reusable module or script for API client inventory extraction.
Preferred shape:

```text
scripts/feature-audit/extract_web_client_bindings.mjs
```

It can export functions for the workbook builder and also run standalone:

```bash
node scripts/feature-audit/extract_web_client_bindings.mjs webui/src/lib/api.ts
```

Standalone output should be deterministic JSON so it can be tested without
opening the workbook.

Status: done.

### T2. Resolve TypeScript dependency explicitly

Because `typescript` is present under `webui/node_modules`, not repo root, the
script should load it from:

```text
webui/node_modules/typescript/lib/typescript.js
```

If missing, fail with a clear message telling the operator to install WebUI
dependencies.

Status: done.

### T3. Replace `fromApiClient`

Update `outputs/feature-audit/build_feature_tracker.mjs` or the promoted
workbook builder to call the AST extractor instead of the regexp.

The `Web Client Bindings` sheet should include the richer columns:

```text
client | method | path | path_kind | source_line | confidence | notes
```

The `User Stories` SDK rows should use `method path` in `Source Evidence`.

Status: done in the current workbook builder.

### T4. Regenerate canonical workbook

Regenerate:

```text
outputs/feature-audit/levara_feature_user_story_tracker.xlsx
```

Expected impact:

- `Web API client bindings` increases from 22 to 52.
- `User stories` increases by the same delta unless multiple calls are later
  grouped intentionally.
- Mis-mapped rows such as `workspaceSearch -> /api/v1/sync/status?...` disappear.

Status: done. The regenerated workbook has 167 user stories and 52 Web API
client bindings.

### T5. Update docs

After regeneration:

- update `docs/feature-audit-tracker.md` counts;
- remove or soften the warning that SDK rows are regexp-derived;
- keep a note that dynamic/template paths are represented as source text;
- mention the standalone extractor command.

Status: done.

## Testing

### Unit/golden tests

Add a focused Node test for the extractor. It should assert:

- `health` maps to `GET /health`;
- `login` maps to `POST /api/v1/auth/login`;
- `datasets` maps to `GET \`/api/v1/datasets?page=${page}&limit=${limit}\``;
- `deleteDataset` maps to `DELETE \`/api/v1/datasets/${id}\``;
- `graphPath` maps to `GET \`/api/v1/graph/path?${q.toString()}\``;
- `workspaceSearch` maps to `POST /api/v1/workspace/search`;
- `syncStatus` maps to `GET \`/api/v1/sync/status?limit=${limit}\``;
- `mcpSessions` maps to
  `GET \`/api/v1/admin/mcp/sessions?limit=${limit}\``.

Also assert there are no duplicate `(client, call_index, path)` rows and no
empty `client` or `path` values.

### Inventory sanity checks

Run the standalone extractor and verify:

```bash
node scripts/feature-audit/extract_web_client_bindings.mjs webui/src/lib/api.ts
```

Expected checks:

- count is at least the number of `api<` calls inside `levara`;
- `workspaceSearch` is not associated with any `/sync/` endpoint;
- `runSync` is associated with `POST /api/v1/sync/run`;
- `mcpSessions` is associated with `/api/v1/admin/mcp/sessions`.

### Workbook checks

After regenerating the XLSX:

- inspect `Summary` and verify the SDK binding count changed;
- inspect `Web Client Bindings` and spot-check workspace/sync/admin rows;
- inspect `User Stories` SDK rows for correct `Source Evidence`;
- render the workbook preview to ensure added columns remain readable.

### Existing gates

Run:

```bash
git diff --check
go test ./docs
npm run lint
npm run build
```

If workbook builder code is promoted under a maintained script path, add its
standalone extractor test to a repeatable command and include that command in
`docs/feature-audit-tracker.md`.

## Non-goals

- Do not rewrite REST or MCP inventory extraction in this fix unless a concrete
  mismatch is found.
- Do not commit `152-fz.md` or `DCD_Design_red_mad_robot.pdf` as part of this
  extractor fix.
- Do not commit generated `node_modules` symlinks or workbook inspection dumps.
