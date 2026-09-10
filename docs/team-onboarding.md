# Repeatable team onboarding

`levara team apply` provisions ordinary local-password accounts, one read-write
API key per account, caller-owned datasets and individual viewer/editor/admin
grants. It verifies login and access for the users named in the plan. It does
not provision LDAP/SSO identities, administrators, tenants or group membership.

Use an authenticated SQL-backed server and HTTPS, or loopback HTTP for local
setup. The API URL includes `/api/v1`. This command does not use the global CLI
token: each account authenticates with its own password from an environment
variable. The example names are placeholders; use accounts and dataset names
dedicated to the intended team.

## Plan and dry-run

Build the CLI and copy the [example plan](../examples/team-onboarding/plan.json):

```bash
go build -o levara ./cmd/cli
cp examples/team-onboarding/plan.json team.json
./levara team apply --plan=team.json --dry-run
```

Dry-run validates JSON, references, duplicate email/name entries, roles and
limits. It contacts no server, reads no passwords and creates no journal. It
does not claim that remote resources are absent or that credentials work.
Unicode is preserved; emails are never merged or changed to another identity.
Case-insensitive email duplicates within the plan are rejected conservatively.

The plan supports 2–8 users, up to 16 datasets and 32 individual grants. User
`name` values are local references; dataset `name` values are the exact remote
names. `password_env` names an environment variable, not a password. Do not put
tokens or passwords in the JSON plan.

For a tenant-enforced server, all accounts must already exist and be enrolled
in the intended tenant. Set `"existing_users_only": true` in the plan to prohibit
registration. Tenant creation/enrollment and selection are prerequisites; this
command never attempts an administrative bootstrap. Multiple memberships may
require deployment-specific tenant setup before the command can succeed.

## Apply and verify

Supply the passwords through your shell or secret manager. For example, in Bash:

```bash
read -r -s -p 'Alice password: ' LEVARA_TEAM_ALICE_PASSWORD
printf '\n'
read -r -s -p 'Bob password: ' LEVARA_TEAM_BOB_PASSWORD
printf '\n'
export LEVARA_TEAM_ALICE_PASSWORD LEVARA_TEAM_BOB_PASSWORD

./levara --url=https://levara.example/api/v1 team apply \
  --plan=team.json --state="$HOME/.config/levara/team-state.json"
```

The command prints a bounded JSON report with `created`, `reused`, `verified`,
`failed`, `unknown` and `blocked` steps. Exit zero means all planned operations
and access checks completed. The default total deadline is two minutes;
`--timeout=5m` changes it, up to ten minutes. Each HTTP request is also bounded.
Redirects are rejected. A 429 response stops the apply with an error report;
respect the server's auth limits before resuming rather than weakening them.

The checks establish that each dataset owner can read its known ID and other
planned users initially receive 403. After grants are applied, access is checked
against the plan with both JWTs and API keys. A viewer must be unable to change
grants. An expired credential, a server error or an unrelated 404 does not count
as a successful denial. Login sessions used by the command are logged out on
completion where the server remains reachable.

The viewer check sends an actual grant mutation expected to be denied. If a
broken server accepts it, the command reports failure; inspect that grant before
using the deployment. These checks cover the planned accounts and datasets,
not all users or every REST/MCP/gRPC permission surface.

## Resume without duplicates

Repeat the same command with the same plan, URL and journal. The journal binds
account IDs, dataset IDs, key IDs and initial private-access evidence to that
plan. Existing accounts must accept the exact supplied credentials; existing
datasets must belong to the expected account. Conflicting owners, grants or
identities stop execution. An existing grant without the journal's earlier
private-access evidence is not silently adopted.

The journal contains API-key secrets. It is written atomically with mode `0600`,
file/directory synchronization and an exclusive sibling lock. Keep it outside
Git and protect it like a credential file. Reports contain no passwords, keys,
JWTs, admin tokens or raw response bodies. Do not run the same plan concurrently
with different journals; the server does not enforce unique API-key names.

No cross-request transaction is claimed. A server failure can leave earlier
users, keys or datasets created; the report identifies completed and blocked
steps. Repeating a successful or partially completed apply reconciles recorded
resources. If key creation loses its response, its journal entry stays `unknown`
and the command will not create another key, even when a subsequent list appears
empty. Resolve that key through the account's key-management API and recover the
original journal deliberately; automatic key rotation/revocation is not part of
this command. Likewise, remove a stale `.lock` only after confirming no apply is
still running.

## Optional vector collection

Collections are omitted by default. An explicit plan entry may request:

```json
{
  "name": "team-index",
  "user": "alice",
  "embedding_dim": 768,
  "embedding_model": "your-configured-model",
  "distance_metric": "cosine"
}
```

Place entries in the top-level `collections` array. The command compares actual
dimension, model and distance metric with the request, including on repeat. It
does not adopt an existing global collection absent from the journal. Collection
creation does not start an embedding model or index documents. Raw collections
are global resources; individual grants and the A/B checks here apply to
datasets, not to raw collection endpoints.

For server configuration use [deployment](deployment.md). For ingestion and
document-specific access use [document management](document-management.md).
