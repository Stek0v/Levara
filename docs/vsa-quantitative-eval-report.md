# VSA Quantitative Evaluation Report

Generated: 2026-06-18T12:53:42Z

Cases: 52

## Summary

| Mode | fact_recall@k | MRR | nDCG@k | predicate_precision@k | p95 latency (us) |
|---|---:|---:|---:|---:|---:|
| baseline_sql_graph | 0.000 | 0.000 | 0.000 | 0.914 | 454 |
| vsa_empty_index | 0.000 | 0.000 | 0.000 | 0.000 | 18 |
| current_append | 0.000 | 0.000 | 0.000 | 0.914 | 460 |
| vsa_first | 1.000 | 1.000 | 1.000 | 0.971 | 95947 |
| vsa_only | 1.000 | 1.000 | 1.000 | 1.000 | 91050 |

## Lift vs Baseline

| Mode | fact_recall lift | MRR lift | nDCG lift | precision lift | p95 latency delta (us) |
|---|---:|---:|---:|---:|---:|
| vsa_empty_index | 0.000 | 0.000 | 0.000 | -0.914 | -436 |
| current_append | 0.000 | 0.000 | 0.000 | 0.000 | 6 |
| vsa_first | 1.000 | 1.000 | 1.000 | 0.057 | 95493 |
| vsa_only | 1.000 | 1.000 | 1.000 | 0.086 | 90596 |

## By Scenario

### context_budget

| Mode | fact_recall@k | MRR | nDCG@k | predicate_precision@k | context facts avg |
|---|---:|---:|---:|---:|---:|
| baseline_sql_graph | 0.000 | 0.000 | 0.000 | 0.800 | 5.00 |
| vsa_empty_index | 0.000 | 0.000 | 0.000 | 0.000 | 0.00 |
| current_append | 0.000 | 0.000 | 0.000 | 0.800 | 5.00 |
| vsa_first | 1.000 | 1.000 | 1.000 | 1.000 | 5.00 |
| vsa_only | 1.000 | 1.000 | 1.000 | 1.000 | 5.00 |

### distractor_heavy

| Mode | fact_recall@k | MRR | nDCG@k | predicate_precision@k | context facts avg |
|---|---:|---:|---:|---:|---:|
| baseline_sql_graph | 0.000 | 0.000 | 0.000 | 1.000 | 10.00 |
| vsa_empty_index | 0.000 | 0.000 | 0.000 | 0.000 | 0.00 |
| current_append | 0.000 | 0.000 | 0.000 | 1.000 | 10.00 |
| vsa_first | 1.000 | 1.000 | 1.000 | 1.000 | 10.00 |
| vsa_only | 1.000 | 1.000 | 1.000 | 1.000 | 10.00 |

### fanout

| Mode | fact_recall@k | MRR | nDCG@k | predicate_precision@k | context facts avg |
|---|---:|---:|---:|---:|---:|
| baseline_sql_graph | 0.000 | 0.000 | 0.000 | 1.000 | 10.00 |
| vsa_empty_index | 0.000 | 0.000 | 0.000 | 0.000 | 0.00 |
| current_append | 0.000 | 0.000 | 0.000 | 1.000 | 10.00 |
| vsa_first | 1.000 | 1.000 | 1.000 | 1.000 | 10.00 |
| vsa_only | 1.000 | 1.000 | 1.000 | 1.000 | 10.00 |

### predicate_specific

| Mode | fact_recall@k | MRR | nDCG@k | predicate_precision@k | context facts avg |
|---|---:|---:|---:|---:|---:|
| baseline_sql_graph | 0.000 | 0.000 | 0.000 | 0.800 | 10.00 |
| vsa_empty_index | 0.000 | 0.000 | 0.000 | 0.000 | 0.00 |
| current_append | 0.000 | 0.000 | 0.000 | 0.800 | 10.00 |
| vsa_first | 1.000 | 1.000 | 1.000 | 0.900 | 10.00 |
| vsa_only | 1.000 | 1.000 | 1.000 | 1.000 | 9.00 |

## Notes

- current_append models the current graph-search integration: SQL context consumes budget before VSA is appended.
- vsa_first models a VSA-aware budget policy: predicate VSA facts are retrieved before SQL graph filler.
- TestVSABeforeSQLGraphPreservesTargetUnderBudget checks the order directly: VSA-before-SQL finds the target in all 52 cases, SQL-before-VSA finds none.
- Positive vsa_first lift quantifies VSA signal; flat current_append lift indicates budget allocation can hide that signal.
