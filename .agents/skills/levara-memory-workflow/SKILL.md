---
name: levara-memory-workflow
description: Automatically restore and curate durable project context through a Levara MCP server. Use implicitly for substantial coding, debugging, architecture, code review, SRE incident, operational investigation, and cross-session continuation work when Levara tools are available. At task start select the project collection and recall relevant context; before recommendations recall prior decisions; after verified outcomes save only durable decisions, discoveries, facts, preferences, advice, or dated events while excluding secrets, ephemeral state, code details, and duplicates.
---

# Levara memory workflow

Use Levara as durable agent memory, not as a transcript store or replacement
for source control, task tracking, or human documentation.

## Check availability

1. Prefer the native `levara` MCP tools.
2. Call `levara_instructions` once per session and follow the embedded server
   contract when it is stricter than this skill.
3. If Levara is unavailable, continue the user's task. Do not debug the
   connection unless requested; mention skipped memory synchronization only
   when it materially matters.
4. Never expose, retrieve, or copy Levara credentials while using memory.

## Start substantial work

1. Derive a stable collection from the repository or explicit project name.
   Do not use an absolute filesystem path.
2. Call `set_context` with that collection.
3. Call `wake_up` with the default small token budget.
4. Recall the task topic before proposing architecture, revisiting a bug, or
   recommending an operational change. Filter by `room` or `hall` when known.
5. Use `get_project_context` only for broad onboarding or resuming after a
   long gap; avoid its larger payload during routine tasks.

Treat recalled memory as a lead, not current truth. Verify it against current
code, configuration, telemetry, or user direction whenever it may be stale.

## Recall while working

- Recall prior `decision` entries before making a competing recommendation.
- Recall `discovery` entries before debugging a previously touched subsystem.
- Recall `preference` entries when shaping deliverables or workflow.
- Use `search` for indexed knowledge beyond curated memories.
- Do not interrupt the task merely to narrate routine memory reads.

## Capture durable outcomes

Save explicit decisions, verified root causes, durable user preferences, and
significant dated events as soon as they become clear. At meaningful
milestones and before finishing, run a final capture check for outcomes that
were not already saved.

Save an outcome only when all are true:

1. It is likely to help in a future session.
2. It is supported by observed evidence, tests, current source, or an explicit
   user choice.
3. It can be stated atomically and concisely.
4. It is not better represented by source control, a task tracker, project
   documentation, or another authoritative system.
5. It contains no secret or unnecessarily sensitive data.

Choose the hall deliberately:

- `decision`: what was chosen and why, including the important tradeoff.
- `discovery`: symptom, root cause, fix, and validation.
- `fact`: stable objective information that is not a secret.
- `preference`: a durable user preference or working style.
- `advice`: a reusable rule learned from evidence.
- `event`: a significant milestone with an absolute date in the value.

Always set `collection`, `room`, and `hall`. Use a concept-oriented key, not a
filename or function name.

Before saving, search or recall similar memories:

- Skip equivalent duplicates.
- Preserve history with `supersedes_memory_id` when a new outcome replaces an
  older one.
- Prefer at most three new memories per task. Exceed that only when the task
  genuinely produced more independent durable outcomes.
- Do not pin automatically. Pin only an explicitly declared global hard rule
  or a user-requested critical fact.
- Respect host approval prompts for all writes; never bypass them.

Read [references/memory-policy.md](references/memory-policy.md) when choosing
between memory types, drafting a non-trivial entry, or deciding which system
should remain authoritative.

## Never save

- Passwords, API keys, tokens, private keys, credentials, or unredacted
  secrets.
- Raw chat transcripts, logs, stack traces, or large command outputs.
- Temporary TODOs, current progress, speculative guesses, or unverified
  diagnoses.
- File paths, function names, code snippets, commit history, or facts easily
  recovered from source control.
- Data the user asks not to remember.
- Entries that merely paraphrase existing memory.

## Finish transparently

Do not turn memory into a long status section. When a material memory write
occurred, add one concise sentence to the final response naming the collection
and captured memory types. Say nothing about Levara when no outcome passed the
durable-memory gate.
