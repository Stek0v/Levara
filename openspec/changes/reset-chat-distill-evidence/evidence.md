# Evidence — 2026-10-06

Status: implementation and contract tasks 1.1/1.2 complete; integration task 2.1 OPEN. Original T08 OPEN; original roadmap remains 7/32 accepted. No commit, deployment, schema migration, external model call or historical backfill.

## Observed evidence reset

- Original RED `/tmp/levara-distill-evidence-red.log`, exit 1: six failing leaves, receipt-backed overwrite / legacy verified overwrite / fresh insert on SQLite and native PostgreSQL. Actual Task/pass receipt and ToolSaveMemory establish checked text before the real importer plus stub-model distillation.
- Worker GREEN exit 0, 5.767s, and race exit 0, 8.668s: each 76 RUN/PASS nodes, zero skips. Preview, full-row foreign/shared/sibling controls and Task/import ledger snapshots remain unchanged.
- Root inspected actual diff, full new regression and original RED. Root `go test -count=1 -race -v ./pkg/mcp -run 'ChatDistill|MemoryWriteEvidence|Tools|ArchitectureContract'`: exit 0, 13.886s, 87 RUN/PASS, zero failures/skips. Raw `/tmp/levara-t08-root-focused-race.log`.
- Root real-JWT existing chat source/access matrix, both SQL dialects and MCP transports: exit 0, HTTP 2.748s, three RUN/PASS nodes, no skips. Raw `/tmp/levara-t08-chat-wire-green.log`. This covers existing ordinary chat dispatch, not imported-transcript authorization.
- Independent `/root/pin_transport_map` reviewed actual native SQL, placeholder sequence, canonical identity, snapshots and raw RED/GREEN/race: no blocker within evidence-reset scope. Follow-up confirmed descriptor and guide accuracy. Existing saved-field dry-run description mismatch is separately tracked as I32.
- Generator `make contract` exit 0. Fresh subsequent `make contract-check` exit 0; docs 0.468s and cmd/contract 1.186s pass. Strict OpenSpec validation and git diff check passed. Generated files were not hand-edited. Gortex detect completed with truncated/lower-bound coverage, not acceptance proof.

## Current source evidence

```text
tool_chat_distill.go               ec2aca28ee9e64430908b504034656c597d93786d44e596f0aa36c63fd72f50f
tool_chat_distill_test.go          c250ed93fc159ea0a861a63eca3f51a5b785f0c2d3e5f9b529d3ccf22ce2f9a4
tool_chat_distill_evidence_test.go d01ca39af6ac3b39d4ec1b589b2c0b3f2967b6f6e6f6ba7c000235b2376d7669
```

Root-owned PostgreSQL 16.15, database levara_roadmap_test, port 53350. Both SQL branches explicitly store unverified, empty Task ID and JSON [] receipts; same identity/provenance/response branches remain compatible.

## Integration gate failure — not accepted

`GOFLAGS=-json LEVARA_TEST_POSTGRES_DSN=<isolated root DB> make test-commit` actually exited 2. Raw `/tmp/levara-t08-test-commit.log`:

- S0–S2 passed; docs 0.806s, access 153.708s, full MCP 317.942s. Other unit/core packages passed, some cached.
- S3 reported async enqueue 100ms and recovery 500ms timing-oracle failures, then the entire HTTP package hit its 10-minute limit, 601.125s. S4 did not run.
- Root focused retry exited 1, 103.067s: same timing failures; raw `/tmp/levara-t08-gate-failure-focused.log`.
- All implementation/test/Makefile/module hashes in the pre-gate 121-file manifest remained unchanged through these attempts. Manifest `/tmp/levara-t08-integration-source-manifest.json`, SHA-256 `93139782d4b10c2908e77009b5a527e7c45ff73013efba4190de5b9c088f1bc2`, HEAD 2eb1dca16b0047918185760b41dcb22dee79090a. Later planning/ledger metadata is not claimed as the revision tested by those commands.
- I31 remains under native phase/pool investigation. Source closes recovery SELECT rows before UPDATE; timing failure alone does not prove a production self-deadlock. A package timeout while an SSE case has just started does not establish an SSE defect.

## Limits and next action

Resolve I31 with observed evidence and rerun a complete stable-source gate before checking task 2.1. Continue original T08 with caller/tenant diary isolation (I29), native PG diary write (I30), imported-transcript authority, cancellation/routing, dedupe/empty and separate model factual-quality acceptance. Stub-model tests establish SQL/evidence behavior only.
