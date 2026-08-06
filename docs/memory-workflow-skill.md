# Automatic memory workflow skill

The `levara-memory-workflow` skill gives MCP-capable coding agents a
conservative default workflow for Levara memory. It restores relevant context
when substantial work starts, consults prior decisions before recommending a
change, and preserves only verified outcomes that are likely to matter in a
future session.

The skill is stored at:

```text
.agents/skills/levara-memory-workflow/
```

## What it does

For coding, debugging, architecture, review, and operational investigations,
the skill directs the agent to:

1. derive a stable project collection and call `set_context`;
2. call `wake_up` with a bounded context budget;
3. recall relevant decisions or discoveries before new research;
4. verify recalled claims against current authoritative sources;
5. save durable decisions, discoveries, facts, preferences, advice, and dated
   events with `collection`, `room`, and `hall`;
6. search before saving, skip duplicates, and supersede stale memories;
7. exclude secrets, raw transcripts, temporary task state, code locations,
   and unverified conclusions.

The workflow is deliberately selective. It is intended to improve future
agent sessions, not archive every step of the current one.

## Requirements

- A running Levara MCP endpoint.
- The `core`, `memory`, `workspace`, or `full` MCP toolset. `core` is the
  smallest compatible profile.
- An agent client that supports Agent Skills or equivalent reusable
  instructions.

At minimum, the server must expose `levara_instructions`, `set_context`,
`wake_up`, `recall_memory`, and `save_memory`.

## Install for Codex

Copy the complete skill directory into the personal Codex skills directory:

### Linux and macOS

```bash
mkdir -p "${CODEX_HOME:-$HOME/.codex}/skills"
cp -R .agents/skills/levara-memory-workflow \
  "${CODEX_HOME:-$HOME/.codex}/skills/"
```

### Windows PowerShell

```powershell
$codexSkills = if ($env:CODEX_HOME) {
  Join-Path $env:CODEX_HOME 'skills'
} else {
  Join-Path $env:USERPROFILE '.codex\skills'
}
New-Item -ItemType Directory -Force -Path $codexSkills | Out-Null
Copy-Item -Recurse -Force '.agents\skills\levara-memory-workflow' $codexSkills
```

Restart Codex after copying the directory. The bundled `agents/openai.yaml`
allows implicit invocation for substantial project work. Invoke the skill
explicitly with `$levara-memory-workflow` when testing or when a client does
not select it automatically.

When a repository already contains this skill under `.agents/skills`, a
client with project-skill discovery may use it directly without a personal
copy.

## Connect the Levara MCP server in Codex

The following example uses a local Levara endpoint and an API key supplied by
an environment variable:

```toml
[mcp_servers.levara]
url = "http://127.0.0.1:8080/mcp"
enabled = true
env_http_headers = { "X-API-Key" = "LEVARA_MCP_API_KEY" }
default_tools_approval_mode = "writes"
```

Set `LEVARA_MCP_API_KEY` outside the configuration file. Omit
`env_http_headers` when authentication is intentionally disabled. Use HTTPS
for endpoints accessed across an untrusted network.

Configure the smallest required tool surface on the server:

```bash
export LEVARA_MCP_TOOLSET=core
```

Restart both Levara and Codex after changing their configuration.

## Other clients

For clients that implement Agent Skills, copy the same directory into the
client's supported skill location. For clients without skill discovery, use
`SKILL.md` as project instructions and preserve the linked
`references/memory-policy.md` next to it.

The workflow depends on standard MCP tool calls, not on Codex-specific APIs.
Only installation and implicit invocation metadata are client-specific.

## Verify the installation

Start a new session in a repository and ask:

```text
Use $levara-memory-workflow to continue work on this project. Show which
collection you selected and recall prior decisions about deployment.
```

A correct setup should call `levara_instructions`, `set_context`, `wake_up`,
and an appropriate recall tool. It should not create a memory merely to prove
that writes work.

After a real, verified decision or root-cause investigation, the agent should
save a concise memory and mention the collection and memory type in its final
response.

## Safety and noise control

- Keep write approvals enabled in the MCP client.
- Never store credentials or unredacted production data.
- Treat recalled memories as potentially stale until verified.
- Keep source code and commit history in source control.
- Keep temporary work in the task tracker or current agent runtime.
- Avoid automatic pinning and large end-of-session summaries.
- Review memory behavior and duplicate rates before broad team rollout.

## Remove the skill

Delete the copied `levara-memory-workflow` directory from the client's skills
directory and restart the client. Removing the skill does not delete existing
Levara memories.
