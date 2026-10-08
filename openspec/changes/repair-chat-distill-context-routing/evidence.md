# Local evidence (2026-10-06)

Original T08 remains active. This change does not accept imported-chat authority, model quality or the complete roadmap.

## Caller lifetime

Native PostgreSQL 16.15 preflight confirmed the root-owned isolated fixture on port 53350; SQLite used disposable test databases. Provider and embedding fixtures are local.

- Original context RED: exit 1, 16 failing and 10 passing leaves, package 2.401s. Cancellation still saved SQL, late preview/results succeeded, canceled embedding published vectors, whitespace invoked the provider.
- Combined GREEN: `go test -count=1 -v ./pkg/mcp -run 'ChatDistill|MemoryWriteEvidence|TestToolSaveMemory|MemoryCommitEvidenceSharedIndexScopeAndRollback'`, exit 0, 107/107 leaves, 125 nodes, zero skips, 7.545s.
- Same command with `-race`: exit 0, 107/107 leaves, zero skips/race warnings, 11.180s.
- Logs: `/tmp/levara-distill-context-{red,green,race}.log`.

Observed worker source SHA256:

```text
tool_chat_distill.go 08bf240e5dd50884f7d3369d50f1b9177a212c3fab3c273ae216b10ad39908ee
tool_save_recall_memory.go d69c8010173b6e17dbccfdbc74dd0b43d13c8d678c7ab7b52fedd6b53d7da54b
tool_chat_distill_context_test.go bef6b30c3e474f22cc0a0ea2bbb30355a90dd93601e363f6ee1831f05c411b1b
```

## Collection routing

Actual JWT MCP dispatch uses one legacy session across set_context and chat_distill, plus stateless explicit/no-default controls. Both SQL dialects assert owner, collection, provenance and unverified evidence, preserving pre-existing full-row controls.

- Before-change overlay removes only chat_distill from the collection-injection case. Correctly selected `TestMCPChatDistillCollectionRouting`: exit 1, four failing legacy default/empty leaves and eight passing controls, 1.134s.
- Current source GREEN: `go test -p 1 -count=1 -v ./internal/http -run '^TestMCPChatDistillCollectionRouting$'`, exit 0, 12/12 leaves, zero skips, 0.951s.
- Same command with `-race`: exit 0, 12/12 leaves, zero skips/race warnings, 2.857s.
- Logs: `/tmp/levara-distill-routing-{red,green,race}.log`; overlay files remain outside source. An initial incorrect regex selected no tests and supplies no evidence.

## Contracts and remaining verification

Descriptor now describes legacy versus stateless collection routing and the omitted preview saved field. Guide states cancellation and already-started publication limits. Contract generator and `make contract-check` exited 0. A full docs/contract/profile run is pending after dependent sharing types stabilize; earlier wrong profile path and a concurrent undefined ChatIdentity build are not accepted test evidence.

No further REST memory-owner probes were run, per user direction. Original scope baseline before context changes passed 204 nodes across chatimport/mcp/http, explicitly excluding TestMemoryREST; it is not a current-source integration receipt. Stable broader gate and independent combined review remain pending.

Cancellation does not roll back earlier committed SQL, interrupt an already-started contextless vector insertion or force a provider that ignores its context to return. Ordinary committed-save indexing intentionally retains its detached timeout. Source Gortex impact/detect was truncated/lower-bound and is not exhaustive correctness evidence.
