# Proposal

## Why

Generic graph-batch gRPC ignores malformed properties JSON and decodes temporal numbers through float64 before writer validation. Invalid historical envelopes can become empty current assertions or rounded whole seconds.

## What Changes

- Validate every node/edge properties object before connecting or writing, preserving null/empty compatibility.
- Preserve exact numeric temporal fields while retaining existing ordinary property decoding.
- Reject malformed/non-object input with InvalidArgument and no effects; retain successful response and writer error shapes.
- Scope: existing BatchWriteGraph ingress only. Non-goals: new graph ACL, schema, proto, route, profile, flag, dependency or deployment.

## Capabilities

### New Capabilities

- `graph/batch-json-ingress`: validated exact graph-batch properties before external effects.

### Modified Capabilities

None; no main specs exist.

## Impact

Existing gRPC handler, focused parser/boundary/live checks. No proto or generated schema mutation; run contract drift check. Neo4j remains derived; SQL authority/precision unchanged. No production operation. The writer's accepted episode behavior is preserved.
