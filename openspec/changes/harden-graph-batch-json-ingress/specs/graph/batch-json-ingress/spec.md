# Spec Delta

## Purpose

Ensure graph batch properties reach storage validation accurately and invalid input cannot begin external writes.

## ADDED Requirements

### Requirement: Validate properties before connection
Malformed, trailing or non-object node/edge properties SHALL fail InvalidArgument before external connection or write. Empty or null properties SHALL remain compatible. Nil records SHALL be rejected.

#### Scenario: Bad properties and unreachable backend
- **WHEN** a batch includes invalid properties and an unreachable configured backend
- **THEN** InvalidArgument is returned rather than connectivity failure, with no effects

#### Scenario: Compatible empty properties
- **WHEN** properties are absent, whitespace-surrounded null or an empty object
- **THEN** valid batch handling is preserved

### Requirement: Preserve temporal number precision
Numeric valid_from and valid_until SHALL reach writer validation exactly. Ordinary property decoding and writer error responses SHALL remain compatible.

#### Scenario: Rounded fraction or underflow
- **WHEN** JSON supplies 9007199254740992.1 or 1e-400 as a temporal bound
- **THEN** writer rejects the complete batch with zero counts and no persisted effects

#### Scenario: Exact large whole second
- **WHEN** an integral decimal temporal number above float64 exact-integer precision is supplied
- **THEN** its exact int64 second is preserved and ordinary numeric properties retain their existing behavior
