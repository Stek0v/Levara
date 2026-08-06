# Memory policy

Use this reference to produce small, durable, high-signal memories.

## System boundaries

| System | Authoritative for |
| --- | --- |
| Levara | Distilled context an AI agent should recall across sessions |
| Source control | Code, symbols, diffs, commits, and change history |
| Task tracker | TODOs, reminders, assignments, and temporary follow-ups |
| Project documentation | Runbooks, specifications, and long-form knowledge |

Store the reusable conclusion in Levara and locate authoritative detail in its
own system when needed.

## Naming

- `collection`: stable project or domain slug such as `levara`, `payments`, or
  the repository name.
- `room`: subsystem or topic such as `memory-index`, `deploy`, `auth`, or
  `incident-management`.
- `key`: durable concept such as `idle-polling-root-cause`; avoid paths and
  symbol names.

## Entry templates

### Decision

```text
Chosen: <decision>.
Why: <reason>.
Tradeoff: <important cost or rejected alternative>.
```

### Discovery

```text
Symptom: <observable behavior>.
Root cause: <verified mechanism>.
Fix: <conceptual remediation>.
Validation: <tests or measurements>.
```

### Fact

```text
<stable objective fact>, verified by <source category> on <date when freshness matters>.
```

### Preference

```text
The user prefers <behavior> because <reason, if known>.
```

### Advice

```text
When <condition>, do <action>, because <reason learned from evidence>.
```

### Event

```text
YYYY-MM-DD: <significant milestone and outcome>.
```

## Good examples

```text
collection: levara
room: memory-index
hall: discovery
key: idle-polling-root-cause
value: Symptom: idle Levara consumed significant CPU. Root cause: every worker polled the durable outbox at a very short interval, producing repeated empty transactional claims. Fix: use a configurable, conservative idle interval while draining an existing backlog without delay. Validation: compare idle CPU samples and verify save/recall behavior.
```

```text
collection: levara
room: memory-index
hall: decision
key: worker-poll-default
value: Chosen: use a conservative default memory-index polling interval. Why: idle database traffic should be inexpensive while first-job latency remains below an interactive threshold. Tradeoff: deployments can override the interval for a different latency and power balance.
```

## Reject or redirect

- "Edit this function at this path" -> source control or current task state.
- "This pull request changed three files" -> source control.
- "Try another value later" -> task tracker.
- Full incident report -> project documentation; keep only the root cause and
  reusable operational lesson in Levara.
- Any credential or bearer token -> never store.
