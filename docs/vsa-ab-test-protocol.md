# VSA A/B Test Protocol

Goal: show the retrieval effect of enabling Levara's VSA graph-memory layer.

## Hypothesis

For the same SQL graph and the same entity query, the VSA layer contributes
additional predicate-scoped graph facts only after the VSA index is rebuilt.

## A/B Conditions

- A, VSA off: graph tables contain facts, but `vsa_fact_shards` /
  `vsa_fact_members` are empty.
- B, VSA on: the same graph is indexed with `RebuildFromGraph`.

The test isolates VSA contribution by calling `vsaGraphContext` directly.

## Metric

`fact_recall = expected_facts_found / expected_facts_total`

The current fixture expects four facts for `Checkout` in dataset `ds-vsa`:

- `Checkout CALLS Orders`
- `Checkout CALLS Payments`
- `Checkout CALLS Ledger`
- `Checkout OWNED_BY Oncall`

Dataset isolation is also checked by seeding a cross-dataset `TenantB` edge and
asserting it does not appear in the VSA context.

## Command

```bash
go test ./internal/http -run TestVSAGraphContextABShowsRecallLift -v
```

Expected result:

```text
VSA A/B fact_recall without=0.00 with=1.00 context_without=0 context_with=4
```

## Interpretation

The test demonstrates that enabling/rebuilding VSA turns a graph-only corpus
into retrievable predicate facts through the VSA graph-memory layer, while
preserving dataset filtering.
