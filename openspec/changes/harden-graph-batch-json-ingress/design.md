# Design

## Context

See proposal.md. The accepted writer validates exact json.Number timestamps, but generic gRPC currently discards JSON errors and rounds numbers first.

## Goals / Non-Goals

Validate the existing ingress without new schema, flags, dependencies or authorization behavior. Preserve ordinary float64/nested property decoding and successful response shape.

## Decisions

- Decode node and edge properties and check errors before constructing the external writer. Empty input and JSON null retain empty-object compatibility; non-object, malformed and trailing input fail InvalidArgument. Reject nil proto records instead of panicking.
- Retain existing ordinary JSON decoding. Re-read only numeric valid_from/valid_until raw fields as json.Number, preserving exact decimal/exponent values for writer validation; strings/null retain their existing types. Do not globally change arbitrary numeric property serialization.
- Writer bound failures retain existing zero-count/errors response. Malformed properties return InvalidArgument before Neo4j connectivity, so bad endpoints cannot mask input errors.
- Use one small parser helper and focused actual-handler/owned live checks. No new RPC surface or graph store abstraction.

## Risks / Trade-offs

- Previously silently ignored malformed JSON now errors → deliberate trust-boundary correction with unchanged valid inputs.
- Shared private database → only owned NEO4J_TEST_* instance and targeted unique fixture cleanup; no parallel destructive helpers.

## Migration Plan

No migration/deployment. Record focused live and contract/strict/diff results and independent review; preserve prior accepted writer receipts.
