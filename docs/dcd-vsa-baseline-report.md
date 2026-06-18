# DCD VSA Baseline Report

Generated: 2026-06-18T19:06:16Z

Cases: 52

Scenarios: context_budget, distractor_heavy, fanout, predicate_specific

## Summary

| Mode | domain@1 | collection@1 | document recall@k | fact recall@k | MRR | nDCG@k | predicate precision | distractor rate | tenant leak | expired leak | p95 latency (us) |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| global_vsa_first | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.621 | 1.000 | 0.000 | 0.000 | 4 |
| oracle_route_vsa | 1.000 | 1.000 | 1.000 | 1.000 | 1.000 | 1.000 | 0.621 | 0.862 | 0.000 | 0.000 | 3 |
| wrong_route_vsa | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.621 | 1.000 | 0.000 | 0.000 | 1 |
| empty_route_vsa | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.621 | 1.000 | 0.000 | 0.000 | 2 |

## Lift vs global_vsa_first

| Mode | fact recall lift | MRR lift | nDCG lift | distractor delta |
|---|---:|---:|---:|---:|
| oracle_route_vsa | 1.000 | 1.000 | 1.000 | -0.138 |
| wrong_route_vsa | 0.000 | 0.000 | 0.000 | 0.000 |
| empty_route_vsa | 0.000 | 0.000 | 0.000 | 0.000 |

## By Scenario

### context_budget

| Mode | fact recall@k | MRR | nDCG@k | distractor rate | context avg |
|---|---:|---:|---:|---:|---:|
| global_vsa_first | 0.000 | 0.000 | 0.000 | 1.000 | 5.00 |
| oracle_route_vsa | 1.000 | 1.000 | 1.000 | 0.800 | 5.00 |
| wrong_route_vsa | 0.000 | 0.000 | 0.000 | 1.000 | 5.00 |
| empty_route_vsa | 0.000 | 0.000 | 0.000 | 1.000 | 5.00 |

### distractor_heavy

| Mode | fact recall@k | MRR | nDCG@k | distractor rate | context avg |
|---|---:|---:|---:|---:|---:|
| global_vsa_first | 0.000 | 0.000 | 0.000 | 1.000 | 8.00 |
| oracle_route_vsa | 1.000 | 1.000 | 1.000 | 0.875 | 8.00 |
| wrong_route_vsa | 0.000 | 0.000 | 0.000 | 1.000 | 8.00 |
| empty_route_vsa | 0.000 | 0.000 | 0.000 | 1.000 | 8.00 |

### fanout

| Mode | fact recall@k | MRR | nDCG@k | distractor rate | context avg |
|---|---:|---:|---:|---:|---:|
| global_vsa_first | 0.000 | 0.000 | 0.000 | 1.000 | 8.00 |
| oracle_route_vsa | 1.000 | 1.000 | 1.000 | 0.875 | 8.00 |
| wrong_route_vsa | 0.000 | 0.000 | 0.000 | 1.000 | 8.00 |
| empty_route_vsa | 0.000 | 0.000 | 0.000 | 1.000 | 8.00 |

### predicate_specific

| Mode | fact recall@k | MRR | nDCG@k | distractor rate | context avg |
|---|---:|---:|---:|---:|---:|
| global_vsa_first | 0.000 | 0.000 | 0.000 | 1.000 | 8.00 |
| oracle_route_vsa | 1.000 | 1.000 | 1.000 | 0.875 | 8.00 |
| wrong_route_vsa | 0.000 | 0.000 | 0.000 | 1.000 | 8.00 |
| empty_route_vsa | 0.000 | 0.000 | 0.000 | 1.000 | 8.00 |

## Notes

- This is a pre-implementation baseline. It uses synthetic route metadata and does not change production searchHandler behavior.
- global_vsa_first models current VSA-before-SQL without DCD route narrowing.
- oracle_route_vsa models the upper bound of perfect domain/collection/document routing before VSA candidate selection.
- wrong_route_vsa and empty_route_vsa validate graceful fallback behavior.
