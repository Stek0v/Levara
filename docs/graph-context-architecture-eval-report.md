# Graph Context Architecture Eval Report

Generated: 2026-06-18T18:12:57Z

Scenarios: budget_pressure, distractor_heavy, fanout, mixed_production, predicate_synonym

| Mode | recall@k | MRR | nDCG@k | predicate precision | tenant leak | expired leak | VSA avg | SQL avg | p95 latency (us) | qps |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| sql_only | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.00 | 8.00 | 2296 | 558 |
| sql_first | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.00 | 8.00 | 1709 | 1056 |
| vsa_first | 1.000 | 1.000 | 1.000 | 1.000 | 0.000 | 0.000 | 4.00 | 4.00 | 134063 | 9 |
| vsa_only | 1.000 | 1.000 | 1.000 | 1.000 | 0.000 | 0.000 | 4.00 | 0.00 | 193409 | 7 |

## Lift vs sql_first

| Mode | recall lift | MRR lift | nDCG lift | p95 delta (us) |
|---|---:|---:|---:|---:|
| sql_only | 0.000 | 0.000 | 0.000 | 587 |
| vsa_first | 1.000 | 1.000 | 1.000 | 132354 |
| vsa_only | 1.000 | 1.000 | 1.000 | 191700 |
