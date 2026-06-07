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
replace `${LEVARA_TOKEN}` with the actual token or use the host's secret
management feature.

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
go run ./cmd/agent-hosts -host codex -target .codex/config.toml
```

The installer preserves unrelated MCP servers/settings, replaces only the
`levara` stanza, and creates a timestamped backup before writing an existing
file. Add `-dry-run` to print the merged config without writing.
