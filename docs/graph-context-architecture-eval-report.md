# Graph Context Architecture Eval Report

Generated: 2026-06-18T14:00:28Z

Scenarios: budget_pressure, distractor_heavy, fanout, mixed_production, predicate_synonym

| Mode | recall@k | MRR | nDCG@k | predicate precision | tenant leak | expired leak | VSA avg | SQL avg | p95 latency (us) | qps |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| sql_only | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.00 | 8.00 | 863 | 1306 |
| sql_first | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.000 | 0.00 | 8.00 | 784 | 1397 |
| vsa_first | 1.000 | 1.000 | 1.000 | 1.000 | 0.000 | 0.000 | 4.00 | 4.00 | 93080 | 11 |
| vsa_only | 1.000 | 1.000 | 1.000 | 1.000 | 0.000 | 0.000 | 4.00 | 0.00 | 95922 | 11 |

## Lift vs sql_first

| Mode | recall lift | MRR lift | nDCG lift | p95 delta (us) |
|---|---:|---:|---:|---:|
| sql_only | 0.000 | 0.000 | 0.000 | 79 |
| vsa_first | 1.000 | 1.000 | 1.000 | 92296 |
| vsa_only | 1.000 | 1.000 | 1.000 | 95138 |
