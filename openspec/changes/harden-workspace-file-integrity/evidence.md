# T19 evidence

## Final acceptance — 2026-10-07

Own change accepted 11/11; original roadmap T19 accepted (16/32). Frozen revision `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:8ca1e54259d6b06c0a65ebfaffe2d60381ba26b5f6e067f246f08497671189f1`, 249 untracked; native/full/contract pre/post manifests match.

- Native SQLite/PostgreSQL and trusted-local race: actual exit0, 586 leaf PASS/0 FAIL/0 SKIP; no failed parents. `/tmp/levara-t19-native-protocol-race.jsonl`, SHA256 `a17341af8ea594ab1932ad896dad9d634d3b9de4106bf7c364b1215a1c34cb10`; receipt `c0e35716-56b0-4cb8-8f0d-3885b87e4713`.
- Required make test-commit: actual exit0, 4165 leaf PASS/0 FAIL/2 existing opt-in SKIP, all 11 packages and S0–S4 passed. `/tmp/levara-t19-full-test-commit.jsonl`, SHA256 `16efd8f090a9b2d545a1f0ec1273d2f2a70f394f5a91a24894d78c2d2b72e193`; receipt `e89a047b-8ee3-400d-b6e8-c4d09c23d6db`.
- Full/core generators and final contract-check exit0; empty `/tmp/levara-t19-contract-final-check.log`, SHA256 `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`; receipt `336a5772-ca70-4018-a944-46ec5009756c`.
- Strict OpenSpec and git diff --check exit0. Independent combined-source/contract/log audit accepts all 11 criteria and original T19; receipt `ee698f6d-9cd4-43ef-ab26-3f3341a22371`.
- Lock protocol repeat actual exit0: 200 terminal PASS across 100 runs of atomic writes and concurrent first creation; earlier 49 PASS/51 FAIL retained. Windows/amd64 and Linux/amd64 compile exit0; execution only on native macOS.

Every Go invocation excludes `^TestMemoryREST`. Metadata acceptance edits follow these functional gates; no functional bytes change. External editors remain uncooperative; directory-swap crash recovery belongs to T20. Historical metadata migration I78 and dedicated Neo4j prerequisite I98 remain open. No deployment or production migration/restart performed.

Earlier failed gates are retained in the central decomposition evidence: first native 33/1 plus HTTP build failure; second 582/3; third 583/2; lock repeat 49/51. Diagnostic 7/0 and corrected protocol 200/0 precede the final native gate. No failed result was treated as acceptance.

Native command: `go test -p=1 -ldflags=-w -count=1 -race -json ./pkg/workspace ./pkg/mcp ./internal/http -run 'Workspace|Confined|Atomic|ProcessLock|Task.*(Executor|Workspace)' -skip '^TestMemoryREST'`, using the dedicated PostgreSQL fixture DSN. Full command: `GOFLAGS='-p=1 -ldflags=-w -count=1 -json -skip=^TestMemoryREST' make test-commit`.
