# DCD VSA Load Baseline Report

Generated: 2026-06-18T19:06:41Z

Cases: 52

Iterations: 200

| Mode | queries | p50 latency (us) | p95 latency (us) | p99 latency (us) | max latency (us) | qps |
|---|---:|---:|---:|---:|---:|---:|
| global_vsa_first | 200 | 1 | 11 | 81 | 408 | 179594 |
| oracle_route_vsa | 200 | 1 | 2 | 3 | 5 | 748595 |
| wrong_route_vsa | 200 | 0 | 9 | 46 | 233 | 268892 |
| empty_route_vsa | 200 | 0 | 0 | 1 | 2 | 1574803 |
