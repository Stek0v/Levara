# Code and Git knowledge

`codify` supports `.go` (standard Go AST parser) and `.py` (heuristic regular-expression extraction), including case-insensitive extensions. JS, TS and other extensions are unsupported and return tool errors. Malformed Go returns an error before ingestion; Python results do not certify valid Python syntax or semantic call resolution.

Authenticated instance-wide code and Git tools require an active instance administrator and no selected tenant. Code publication also requires read/write credential permissions. Server repository paths refer to the server's filesystem. Caller-supplied ownership fields do not grant access.

## Static code publication

Example MCP `tools/call` arguments:

```json
{"name":"codify","arguments":{"code":"package main\nfunc main() {}\n","filename":"main.go","collection":"code_knowledge"}}
```

Omitted collection defaults to `code_knowledge`. Successful output preserves `language`, `entities`, `relations`, `text` and `details`; counts describe extracted code, while the published graph additionally represents file modules and explicit external references. Qualified declarations and server source identity prevent arbitrary merging of same-name methods or different immutable sources with the same filename.

The call completes synchronously. Static extraction makes no LLM request and bypasses semantic/temporal model enrichment. With native SQL it first ingests immutable raw source, then publishes derived graph knowledge through checked source generation, source hash/revision and current publication rules. Configured embedding also indexes source/graph through the existing pipeline; absent embedding leaves native SQL graph publication available. Trusted local nil-DB use returns analysis only.

Processing errors return `isError=true`; a failed index may leave its immutable ingested raw source, but it is not certified as a successful current publication. SQL/vector effects are not atomic. Identical source content follows existing ingestion reuse and reprocessing semantics; changing a filename is not a request to replace a prior immutable source. Source replacement/retirement follows the existing document lifecycle. Historical graph rows or stale vectors may remain physically while ineligible for current retrieval.

## Repository analysis

```sh
levara git analyze --repo /absolute/server/repository --limit 100
levara git analyze --repo /absolute/server/repository --since 2026-01-01
levara git search "migration rationale"
```

Git subprocesses honor the request deadline/cancellation. An initialized repository with no commits, or a filter with no matches, returns the existing zero-commit result. Missing/invalid repositories remain errors. Without embedding, analysis returns a text preview; with embedding, the existing source-scoped background pipeline indexes selected commit text into `git_commits`.

Repeated requests report the selected commit count and use native immutable ingestion/reprocessing rules; there is no global commit-hash exactly-once or deduplication guarantee. CLI commands fail for connection/read/HTTP/JSON-RPC/MCP errors and malformed responses, including `isError=true` inside HTTP200.

## Verification scope

The change uses local Git/in-process model controls plus native SQLite and dedicated disposable PostgreSQL. It introduces no schema migration, JS/TS analyzer, Python compiler, repository-wide symbol resolution, new publisher or dependency. Live Neo4j parity, external models/corpora, performance and production rollout require separate evidence; original roadmap dependencies remain tracked independently.
