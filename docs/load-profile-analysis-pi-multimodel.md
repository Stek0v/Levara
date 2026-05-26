
=== p3@bench (n=240) ===
  zero_hits=240  top_changed=0 (0%)
  score_gap_top_bottom: (no data)
  score_gap_top1_top2 : (no data)
  score_top1          : (no data)
  latency_ms no/with: p50 159/159  p95 850/850
  rerank_outcome: {'no_results': 240}
  query_kind: {'code-precise': 60, 'concept-paraphrase': 60, 'mixed': 60, 'adversarial': 20, 'ooc': 40}

=== p4@bench (n=360) ===
  zero_hits=0  top_changed=255 (71%)
  score_gap_top_bottom: p25=0.0119 p50=0.0201 p75=0.0356 p90=0.0748 min=0.0072 max=0.1128
  score_gap_top1_top2 : p25=0.0019 p50=0.0054 p75=0.0088 p90=0.0350 min=0.0001 max=0.0450
  score_top1          : p25=0.691 p50=0.731 p75=0.760 p90=0.808 min=0.382 max=0.855
  latency_ms no/with: p50 161/271  p95 318/474
  rerank_outcome: {'reranked': 360}
  query_kind: {'broad': 90, 'sharp': 60, 'ambiguous': 60, 'paraphrase': 60, 'incident': 30, 'ooc': 60}

=== p5@bench (n=360) ===
  zero_hits=0  top_changed=285 (79%)
  score_gap_top_bottom: p25=0.0116 p50=0.0186 p75=0.0352 p90=0.0542 min=0.0051 max=0.0775
  score_gap_top1_top2 : p25=0.0013 p50=0.0044 p75=0.0095 p90=0.0189 min=0.0002 max=0.0232
  score_top1          : p25=0.650 p50=0.708 p75=0.751 p90=0.807 min=0.515 max=0.817
  latency_ms no/with: p50 156/257  p95 204/323
  rerank_outcome: {'reranked': 360}
  query_kind: {'on-topic': 195, 'off-topic': 90, 'broad-room': 75}

=== threshold sweep: %% of traffic where gap > T (rerank skipped) ===
  T         p3@bench    p4@bench    p5@bench
   0.020           -         50%         50%
   0.030           -         33%         33%
   0.040           -         21%         21%
   0.050           -         21%         12%
   0.060           -         12%          4%
   0.070           -         12%          4%
   0.080           -          4%          0%
   0.100           -          4%          0%
   0.130           -          0%          0%
   0.160           -          0%          0%
   0.200           -          0%          0%

=== gate-wrong rate at T: %% of skipped queries where rerank WOULD have changed top ===
  T             p3@bench        p4@bench        p5@bench
   0.020               -  105/180 ( 58%)  135/180 ( 75%)
   0.030               -   45/120 ( 38%)  105/120 ( 88%)
   0.040               -   15/75  ( 20%)   60/75  ( 80%)
   0.050               -   15/75  ( 20%)   45/45  (100%)
   0.060               -   15/45  ( 33%)   15/15  (100%)
   0.070               -   15/45  ( 33%)   15/15  (100%)
   0.080               -    0/15  (  0%)               -
   0.100               -    0/15  (  0%)               -
   0.130               -               -               -
   0.160               -               -               -
   0.200               -               -               -

=== recommendation scan ===
       T   avg_skip%   avg_wrong%
   0.020       50.0%        66.7%
   0.030       33.3%        62.5%
   0.040       20.8%        50.0%
   0.050       16.7%        60.0%
   0.060        8.3%        66.7%
   0.070        8.3%        66.7%
   0.080        2.1%         0.0%
   0.100        2.1%         0.0%
   0.130        0.0%         0.0%
   0.160        0.0%         0.0%
   0.200        0.0%         0.0%

  no threshold meets both targets (skip>30%, wrong<25%) — leave gate off or widen the corpus

=== model: granite (n=960) ===

=== granite (n=960) ===
  zero_hits=240  top_changed=540 (56%)
  score_gap_top_bottom: p25=0.0117 p50=0.0201 p75=0.0353 p90=0.0560 min=0.0051 max=0.1128
  score_gap_top1_top2 : p25=0.0013 p50=0.0045 p75=0.0092 p90=0.0229 min=0.0001 max=0.0450
  score_top1          : p25=0.670 p50=0.719 p75=0.757 p90=0.808 min=0.382 max=0.855
  latency_ms no/with: p50 159/252  p95 252/378
  rerank_outcome: {'no_results': 240, 'reranked': 720}
  query_kind: {'code-precise': 60, 'concept-paraphrase': 60, 'mixed': 60, 'adversarial': 20, 'ooc': 100, 'broad': 90, 'sharp': 60, 'ambiguous': 60, 'paraphrase': 60, 'incident': 30, 'on-topic': 195, 'off-topic': 90, 'broad-room': 75}

=== threshold sweep: %% of traffic where gap > T (rerank skipped) ===
  T          granite
   0.020         50%
   0.030         33%
   0.040         21%
   0.050         17%
   0.060          8%
   0.070          8%
   0.080          2%
   0.100          2%
   0.130          0%
   0.160          0%
   0.200          0%

=== model: potion (n=960) ===

=== potion (n=960) ===
  zero_hits=0  top_changed=630 (66%)
  score_gap_top_bottom: p25=0.0298 p50=0.0678 p75=0.1032 p90=0.1379 min=0.0030 max=0.2455
  score_gap_top1_top2 : p25=0.0099 p50=0.0236 p75=0.0453 p90=0.0661 min=0.0003 max=0.1838
  score_top1          : p25=0.221 p50=0.299 p75=0.416 p90=0.535 min=-0.047 max=0.737
  latency_ms no/with: p50 81/243  p95 265/1346
  rerank_outcome: {'reranked': 960}
  query_kind: {'code-precise': 60, 'concept-paraphrase': 60, 'mixed': 60, 'adversarial': 20, 'ooc': 100, 'broad': 90, 'sharp': 60, 'ambiguous': 60, 'paraphrase': 60, 'incident': 30, 'on-topic': 195, 'off-topic': 90, 'broad-room': 75}

=== threshold sweep: %% of traffic where gap > T (rerank skipped) ===
  T           potion
   0.020         86%
   0.030         75%
   0.040         67%
   0.050         58%
   0.060         53%
   0.070         45%
   0.080         36%
   0.100         27%
   0.130         12%
   0.160          5%
   0.200          2%

## Cross-model comparison

| model | n | mean_recall_top5 | top1_keyword_hit_rate | p50 gap | p50 lat_no_rerank_ms | p50 lat_with_rerank_ms |
|---|---|---|---|---|---|---|
| granite | 960 | 0.000 | 0.000 | 0.0124 | 159.0 | 252.0 |
| potion | 960 | 0.000 | 0.000 | 0.0678 | 81.0 | 243.5 |
