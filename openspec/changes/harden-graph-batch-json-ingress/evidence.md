# Evidence

Bounded change accepted2/2 on 2026-10-07. Independent audit confirms original T15 acceptance remains valid.

## Behavior

The gRPC handler rejects nil requests/records and malformed, trailing or non-object properties before constructing the external writer. Empty/null objects retain compatibility. Exact numeric edge bounds reach writer validation as json.Number; node fields and ordinary nested/weight values preserve prior float64 decoding. Writer failure response counts/errors remain unchanged.

Root source review corrected an initial node-bound decoding compatibility gap before tests; no failing runtime result is invented. Parser/handler cases verify InvalidArgument before an allowed unreachable endpoint. Actual bufconn gRPC dispatch to the owned Neo4j instance proves exact9007199254740993.0 int64 storage, ordinary properties, and fraction9007199254740992.1/underflow1e-400 rejection with no surviving nodes or reservations.

## Current gates

Frozen/postgate revision `2eb1dca16b0047918185760b41dcb22dee79090a+dirty:1b87189a6b8dbffe73c1e50dbd5e32391a4a3df81c4a931a877e807859c5b83a` (374 untracked) unchanged through commands.

- Session94445 actualexit0: `go test ./internal/grpc ./pkg/graphdb -run 'GraphBatch|BatchWriteGraph|Neo4jEpisode' -timeout=4m -json`,13leafPASS/0FAIL/0SKIP, bothpackagesPASS. Log `/tmp/levara-t15-grpc-json-live-first.jsonl`, SHA256 `53a5faeb2edf18d653ed3019d30e770ff2a7c6a80bbc03cd1a53bdf8f171815b`.
- Session94603 `make contract-check` actualexit0, quiet log `/tmp/levara-t15-grpc-json-contract.log`, SHA256 `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`.
- Strict OpenSpec/diffcheck exit0; fresh Gortex detect success. Independent reviewer parsed exact live log/hash and inspected final source, accepted both tasks.

GOFLAGS retained `-p=1 -ldflags=-w -skip=^TestMemoryREST`; tests also count1. Owned Neo4j/APOC5.26.31 loopback17687, exact container `d855b30e1ca156faa57a750219002720ba5a6ffc988f74b135ffaca61ef19091`; no production/destructive shared-db cleanup. Prior episode/path/neighborhood evidence and failed historical gates remain intact.

No proto/schema/profile/flag/dependency changes, new RPC authority, deployment or provider-quality claim. Accepted Neo4j second precision and authoritative SQL microseconds remain separate.
