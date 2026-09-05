# Tutorial 02 — Connect Your Agent

Start a server with [getting started](../getting-started.md), then connect your
MCP host to `http://127.0.0.1:8080/mcp`. Shared deployments need an individual
credential and HTTPS or a trusted local tunnel. Tool availability depends on
`LEVARA_MCP_TOOLSET` and feature flags; product profiles are a separate setting.

## Merge the configuration

The repository includes a config-merging helper. From its root, inspect a dry
run before writing an existing client config:

```bash
go run ./cmd/agent-hosts -host claude -target .mcp.json \
  -server-url http://127.0.0.1:8080/mcp -dry-run
go run ./cmd/agent-hosts -host cursor -target .cursor/mcp.json \
  -server-url http://127.0.0.1:8080/mcp -dry-run
```

For current Codex, configure it directly:

```toml
[mcp_servers.levara]
url = "http://127.0.0.1:8080/mcp"
bearer_token_env_var = "LEVARA_TOKEN"
```

Or use `codex mcp add levara --url http://127.0.0.1:8080/mcp --bearer-token-env-var LEVARA_TOKEN`.
The repository installer still generates an older Codex headers layout; its
structural tests are not proof that current Codex reads that authentication field.
Omit the bearer setting only for the deliberately no-auth loopback tutorial.

Choose the target your installed client actually reads. Remove `-dry-run` only
for that chosen file; the helper preserves unrelated settings and backs up an
existing target. It does not validate the live client's authentication behavior.

[Host examples](../../examples/agent-hosts/README.md) contain JSON/TOML templates.
A minimal JSON configuration for the no-auth loopback tutorial is:

```json
{"mcpServers":{"levara":{"url":"http://127.0.0.1:8080/mcp"}}}
```

For an authenticated server configure a valid `Authorization: Bearer ...` or
`X-API-Key` using the client's secret/environment-header support. The checked-in
`${LEVARA_TOKEN}` placeholders are templates; some hosts send them literally.
Do not copy a token into a committed project file. The [memory skill guide](../memory-workflow-skill.md)
shows a Codex environment-header example; verify it with your installed client.

## Give the agent the right workflow

For durable project memory use [memory-workflow-skill](../memory-workflow-skill.md)
and a project memory contract like [AGENTS.md](../../AGENTS.md): choose a stable
collection, `set_context`, `wake_up`, recall relevant decisions before research,
and save only verified durable outcomes with room/hall.

For Markdown artifacts append the relevant rules from
[workspace-agent-instructions.md](../../examples/agent-hosts/workspace-agent-instructions.md):
`workspace_context → workspace_search → workspace_read`, followed by guarded
writes and commits when needed. That file teaches the workspace workflow; it
is not the complete memory playbook. Merge instructions into existing project
rules instead of replacing the whole `AGENTS.md`.

## Verify with a real session

Ask the agent to report its selected collection and recall an existing project
fact. Check the tool calls and source record, then repeat in a fresh session.
When a real decision is reached, verify it is saved once with its reason and
correct scope. You do not need a dummy memory write to prove the connection.

For shared documents test with two accounts and a known denied dataset as well
as an allowed one: [document scenarios](../document-workflow-scenarios.md).
Client tool approval, server authentication and dataset permissions are separate
controls. An MCP toolset is not an authorization boundary.

## Troubleshooting

| Symptom | Check |
|---|---|
| No tools after reconnect | `LEVARA_MCP_TOOLSET`, relevant feature flag, client refresh of `tools/list` |
| Authenticated call fails | Actual transmitted header, token validity and resource grants; legacy MCP may deliberately return 404 |
| Stateless transport rejects a request | Required headers and metadata in [API guide](../api-reference.md); normal host examples use `/mcp` |
| Context changes between curl calls | Sessionless calls need an explicit collection each time |
| Agent searches but does not recall decisions | Install the memory instructions as well as the transport config |

Continue with [knowledge base](03-knowledge-base.md),
[workspace recipes](../markdown-workspace-deployment-recipes.md) and
[Team deployment](04-team-deploy.md).
