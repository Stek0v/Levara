# Agent Host Packaging Examples

These examples wire Claude, Codex, Cursor, and other MCP-compatible hosts to
the same Levara MCP server.

## Files

| File | Use |
|---|---|
| `claude-mcp.json` | Merge into Claude Desktop/Claude Code MCP config. |
| `cursor-mcp.json` | Copy or merge into `.cursor/mcp.json`. |
| `codex-config.toml` | Merge into `~/.codex/config.toml` or workspace Codex config. |
| `workspace-agent-instructions.md` | Append to `AGENTS.md`, `CLAUDE.md`, `.cursorrules`, or project instructions. |

## Environment

```bash
export LEVARA_TOKEN="<jwt-or-api-key>"
```

Some hosts do not expand environment variables inside MCP config files. If so,
use the host’s supported secret/environment-header feature rather than
committing a literal token. The current Codex example uses
`bearer_token_env_var = "LEVARA_TOKEN"`.

## Required Agent Flow

1. `workspace_context`
2. `workspace_search`
3. `workspace_read`
4. optional `workspace_write`
5. optional `workspace_commit`

`workspace_context` is the session-start call. `workspace_read` is mandatory
before answering from any search hit.

## Safe Installer

From the Levara module root:

```bash
go run ./cmd/agent-hosts -host claude -target .mcp.json
go run ./cmd/agent-hosts -host cursor -target .cursor/mcp.json
```

The installer preserves unrelated MCP servers/settings, replaces only the
`levara` stanza, and creates a timestamped backup before writing an existing
file. Add `-dry-run` to print the merged config without writing.

For current Codex configure the [TOML example](codex-config.toml) manually, or
use `codex mcp add levara --url http://127.0.0.1:8080/mcp --bearer-token-env-var LEVARA_TOKEN`.
The repository installer still generates an older Codex headers layout; it
is not the recommended authenticated Codex setup. Structural config tests do
not prove that every installed IDE expands placeholders or loads the same path.

These instructions describe workspace artifacts. Add the separate
[memory workflow](../../docs/memory-workflow-skill.md) for `set_context`,
`wake_up`, room/hall and durable save/recall rules. Merge into existing project
instructions rather than replacing them. See [agent tutorial](../../docs/tutorials/02-agent-integration.md).
