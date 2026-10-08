# Tasks

Root alone runs Go/format/generators with GOFLAGS='-p=1 -ldflags=-w -count=1 -skip=^TestMemoryREST'. Only the verified owned Neo4j/APOC instance at loopback17687 may supply live evidence. No production operation.

## 1. Ingress correction

- [x] 1.1 Parse node/edge JSON before external writer construction, preserve exact temporal numeric fields and valid ordinary decoding, reject nil records and invalid objects; add focused parser/handler/live tests. DoD: malformed/trailing/scalar/array errors precede unreachable backend; null/empty/ordinary numeric/nested properties remain compatible; exact large bounds round-trip and rounded fraction/underflow batches report zero effects.

## 2. Acceptance

- [x] 2.1 Run focused actual gRPC/Neo4j gates, contract drift, strict validation, diffcheck and independent review; DoD: current frozen revision/actual exit0 receipts and evidence.md, ledger issue closed. No proto/schema hand edit or new authority claim.
