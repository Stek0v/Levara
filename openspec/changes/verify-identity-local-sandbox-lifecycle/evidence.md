# Identity local sandbox acceptance — 2026-10-07

Frozen source: `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:2b7591235cf505784cd1ff1a7cb3573317c8a7bf04f03969b731590e760c8500`. Post-native, browser and backup source digest identical;390 nonignored untracked files included. Metadata acceptance follows the gates.

All Go commands use `GOFLAGS='-p=1 -ldflags=-w -count=1 -skip=^TestMemoryREST'`, isolated PostgreSQL DSN; PostgreSQL native tools supplied for backup. Expected focused tests were listed before matrix/backup execution. REST owner-spoofing reproduction stays excluded.

Current broad native race command:
`go test -race ./pkg/auth ./pkg/access ./cmd/server ./internal/http -run 'LDAP|OIDC|SAML|SCIM|Session|ManagedGroup|Identity|Notebook|RESTRouteInventory' -timeout=8m -json`.
Actual exit0,326 leafPASS/0FAIL/0SKIP;all4 packagesPASS.
Log `/tmp/levara-t25-t31-current-native-matrix.jsonl`, SHA256 `48e47614b6e8448ba62fc5149ae54fee2bd7743f23f36fbc392e9fdad612332b`.

Current `make --silent contract-check`, both strict OpenSpec validations and `git diff --check` actual exit0. Contract log `/tmp/levara-t25-t31-current-contract.log` SHA256 `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`. Full/core artifacts regenerated through their standard generators after route inventory changed. Independent read-only reviewer parsed actual logs/digests and source, found no remaining original DoD blocker.

## Observed shared-store lifecycle

`go test -race ./cmd/server -run '^TestIdentityLocalSandboxLifecycle$' -timeout=6m -json`: actual exit0,2leafPASS/0FAIL/0SKIP, SQLite/PostgreSQL.
Log `/tmp/levara-t25-cleanup-fixed-local-lifecycle.jsonl`, SHA256 `52b34e935f898053061a21a7db1dc5ccdca0560ddf9a55a103b256dd76eb75c2`.
Focused source digest was `3331e11bfb856fee8dfc94199628da085b49dd4664a24259d22c2ac60545f818` and rechecked identical; later broad gate includes final inventory/browser deltas.

Real loopback TLS listener and native verified-session routes share the full SQL store. Public SCIM provisions two immutable identities; an explicit administrator fixture action admits them to the configured tenant. Signed OIDC/SAML provider responses with different email claims bind the same immutable subjects. Native group-protected document bytes allow/deny after membership and grant removal/restoration; rename retains SQL owner. Concurrent same-ETag group PATCH gives one200/one412. Logout revokes the selected session; deactivation denies retained sessions and both new provider logins. Pending OIDC/SAML callbacks fail after auth-object reconstruction with retained SQL/JWT/SP keys, while persisted completed sessions survive until revocation.

The broad matrix separately passed local LDAP TLS/BER, certificate/account/cancellation/stable-identity failures, issuer/email collision, PKCE/nonce/state and signed assertion replay controls.

## Historical failures and limits

- First actual focused run:0PASS/2FAIL, SCIM group400 because the fixture omitted separate tenant admission. Log `/tmp/levara-t25-first-local-lifecycle.jsonl`, SHA256 `07c6aeaf5c065d7224cd468073cacf8720918f9599ac67ee5a4b79e75c4ece15`; test precondition corrected, production authorization unchanged.
- Second run reached SQLite lifecycle observations but stalled during unbounded TLS cleanup. Verified owned test PID was terminated with SIGQUIT; actual exit1 and stack retained in `/tmp/levara-t25-corrected-local-lifecycle.jsonl`. Corrected cleanup closes client idle connections before bounded shutdown; subsequent full both-SQL run passes.
- Object reconstruction proves process-local pending-state semantics, not executable restart. Local signed protocol providers do not certify AD/Entra or vendor deployment. No deployment, migration or production data change.

Original T25 accepted; all three bounded tasks complete.
