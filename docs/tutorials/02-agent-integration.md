# Tutorial 02 — Connect Your Agent (10 minutes)

Levara is an MCP server, so any MCP-capable coding agent can use it as its
memory layer. Verified against Claude Code, Cursor, and Codex CLI configs on
2026-09-03 (the JSON/TOML snippets here are covered by doc tests in
`docs/markdown_workspace_docs_test.go`).

Prerequisite: a running Levara server — see
[01-first-memory.md](01-first-memory.md) step 1 if you don't have one.

## Claude Code

Add Levara to your project's `.mcp.json` (or `~/.claude.json` for all
projects):

```json
{
  "mcpServers": {
    "levara": {
      "url": "http://localhost:8080/mcp",
      "headers": {
        "Authorization": "Bearer ${LEVARA_TOKEN}"
      }
    }
  }
}
```

Running without auth (dev default)? Keep the header — set
`LEVARA_TOKEN=dev` in your shell, or drop the `headers` block entirely.
With `-require-auth`, the token is a Levara API key or JWT.

## Cursor

`~/.cursor/mcp.json` — same shape:

```json
{
  "mcpServers": {
    "levara": {
      "url": "http://localhost:8080/mcp",
      "headers": {
        "Authorization": "Bearer ${LEVARA_TOKEN}"
      }
    }
  }
}
```

## Codex CLI

`~/.codex/config.toml`:

```toml
[mcp_servers.levara]
url = "http://localhost:8080/mcp"
```

## Give the agent its operating rules

Copy `examples/agent-hosts/workspace-agent-instructions.md` into your
repo as `AGENTS.md` (or append it). It teaches the agent when to call
`save_memory` (every decision, discovery, preference — immediately, not at
session end), when to `recall_memory` (before researching something
unfamiliar), and what never to store (code paths, git history, secrets).

## Verify the loop

Ask your agent:

> Save a memory: in room "onboarding", hall "preference", key
> "reply-style", value "terse replies, no emoji".

Then open a **new** session and ask:

> What do you remember about my reply style?

The agent should call `recall_memory` and answer from Levara, not from the
chat history. That's the whole point: the memory outlives the conversation.

## Trouble-shooting

| Symptom | Fix |
|---|---|
| Client shows the server but no tools | Check the profile: tool visibility depends on `LEVARA_PROFILE` and feature flags (e.g. task tools need `LEVARA_LONG_HORIZON_RUNTIME=1`) |
| 401 on every call | Server runs with `-require-auth`; set a valid `Authorization: Bearer <key>` |
| 404 on `/mcp/2026-07-28` | Stateless transport requires the strict header set; use the session transport `/mcp` (what the configs above use) or add `MCP-Protocol-Version` + `Mcp-Method` headers |
| Agent saves nothing | It needs instructions — see previous section |

## Next

- [03-knowledge-base.md](03-knowledge-base.md) — beyond memory: documents,
  hybrid search, the knowledge graph

_Last verified: 2026-09-03 (main; config snippets are doc-tested)._
