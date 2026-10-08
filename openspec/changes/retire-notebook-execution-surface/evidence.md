# Notebook sunset acceptance — 2026-10-07

Frozen source: `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:2b7591235cf505784cd1ff1a7cb3573317c8a7bf04f03969b731590e760c8500`. Post-native, browser and backup source digest identical;390 nonignored untracked files included. Metadata acceptance follows the gates.

All Go commands use `GOFLAGS='-p=1 -ldflags=-w -count=1 -skip=^TestMemoryREST'`, isolated PostgreSQL DSN; PostgreSQL native tools supplied for backup. Expected focused tests were listed before matrix/backup execution. REST owner-spoofing reproduction stays excluded.

Current broad native race command:
`go test -race ./pkg/auth ./pkg/access ./cmd/server ./internal/http -run 'LDAP|OIDC|SAML|SCIM|Session|ManagedGroup|Identity|Notebook|RESTRouteInventory' -timeout=8m -json`.
Actual exit0,326 leafPASS/0FAIL/0SKIP;all4 packagesPASS.
Log `/tmp/levara-t25-t31-current-native-matrix.jsonl`, SHA256 `48e47614b6e8448ba62fc5149ae54fee2bd7743f23f36fbc392e9fdad612332b`.

Current `make --silent contract-check`, both strict OpenSpec validations and `git diff --check` actual exit0. Contract log `/tmp/levara-t25-t31-current-contract.log` SHA256 `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`. Full/core artifacts regenerated through their standard generators after route inventory changed. Independent read-only reviewer parsed actual logs/digests and source, found no remaining original DoD blocker.

## Preservation and actual recovery

Removed the notebook execution handlers, ten registrations (including the legacy run alias), canonical route inventory and optional WebUI page. Old flag values, including1, cannot restore execution or fail startup. Native SQL DDL remains intact; there is no public read/export shim.

Current native route/preservation branches exercise every former operation under ten old flag values and compare populated SQLite/PostgreSQL history across repeated full schema initialization. Full rows include distinct and legacy-empty owners, Unicode, cell types, source/output, order and fixed timestamps; actual indexes and orphan foreign-key rejection remain checked.

`go test -race ./pkg/backup -run '^TestVerifiedNotebookHistoryRoundTrip$' -timeout=6m -json`: actual exit0,2leafPASS/0FAIL/0SKIP, both SQL.
Log `/tmp/levara-t31-native-backup-restore.jsonl`, SHA256 `d0b2f1ba4ad0d3505bdafbbf73894317000ddc801b07fa0a6a9b260fc103e0a3`.
The test performs actual native CreateVerifiedBackup→hides original roots→VerifyArchive→extracts archive→restores fresh SQL through snapshotSQLForProof. PostgreSQL uses native tools and a fresh private verifier cluster. Complete notebook/cell row proofs, schema/indexes/constraints and orphan-cell rejection match; this is actual administrative API recovery, not a fabricated CLI run.

## Browser and build

- Native backend authenticated browser checks A2/I1/I2: actual exit0,3PASS. Current pages return200; retired /notebooks returns404; navigation/editor/execution controls absent.
  `/tmp/levara-t31-native-browser.log`, SHA256 `2f1923bd3ddbca04cf8cf26f54c0dffe4cd793f5aea085e44a6aa48edeee4bb3`.
- WebUI lint exit0; `/tmp/levara-t31-webui-lint.log`, SHA256 `bccfb9273692e0e5f3ef5a66156f3e1e5cc8c007ca2eaf990f41c6667f3b0532`.
- Current production build exit0; `/tmp/levara-t31-webui-build-clean-dev-cache.log`, SHA256 `22599aa4ecd81540b17d62daa30e65c5d50e876873d35d0689a7269d2e63eeb3`. Route list excludes notebooks.
- First build failed on a stale generated .next/dev/types validator referencing the removed page. Preserved that generated cache in a task-owned /tmp directory and rebuilt; no source/typecheck weakening. Original failed build log retained. Stale I1/I2 browser expectations were caught by source review and replaced before browser execution.

## Compatibility boundary

The current explicit sunset contract replaces the historical F1 planning wait with measured retained-history and recovery gates. No published default-off release interval was observed or claimed. Stored history remains administratively recoverable without resurrecting execution. No production SQL migration, deletion, deployment or publication occurred.

Original T31 accepted; all four bounded tasks complete.
