
=== p4@bench (n=360) ===
  zero_hits=0  top_changed=255 (71%)
  score_gap_top_bottom: p25=0.0119 p50=0.0201 p75=0.0356 p90=0.0748 min=0.0072 max=0.1128
  score_gap_top1_top2 : p25=0.0019 p50=0.0054 p75=0.0088 p90=0.0350 min=0.0001 max=0.0450
  score_top1          : p25=0.691 p50=0.731 p75=0.760 p90=0.808 min=0.382 max=0.855
  latency_ms no/with: p50 130/229  p95 169/289
  rerank_outcome: {'reranked': 360}
  query_kind: {'broad': 90, 'sharp': 60, 'ambiguous': 60, 'paraphrase': 60, 'incident': 30, 'ooc': 60}

=== p5@bench (n=360) ===
  zero_hits=0  top_changed=285 (79%)
  score_gap_top_bottom: p25=0.0116 p50=0.0186 p75=0.0352 p90=0.0542 min=0.0051 max=0.0775
  score_gap_top1_top2 : p25=0.0013 p50=0.0044 p75=0.0095 p90=0.0189 min=0.0002 max=0.0232
  score_top1          : p25=0.650 p50=0.708 p75=0.751 p90=0.807 min=0.515 max=0.817
  latency_ms no/with: p50 130/227  p95 203/313
  rerank_outcome: {'reranked': 360}
  query_kind: {'on-topic': 195, 'off-topic': 90, 'broad-room': 75}

=== threshold sweep: %% of traffic where gap > T (rerank skipped) ===
  T         p4@bench    p5@bench
   0.020         50%         50%
   0.030         33%         33%
   0.040         21%         21%
   0.050         21%         12%
   0.060         12%          4%
   0.070         12%          4%
   0.080          4%          0%
   0.100          4%          0%
   0.130          0%          0%
   0.160          0%          0%
   0.200          0%          0%

=== gate-wrong rate at T: %% of skipped queries where rerank WOULD have changed top ===
  T             p4@bench        p5@bench
   0.020  105/180 ( 58%)  135/180 ( 75%)
   0.030   45/120 ( 38%)  105/120 ( 88%)
   0.040   15/75  ( 20%)   60/75  ( 80%)
   0.050   15/75  ( 20%)   45/45  (100%)
   0.060   15/45  ( 33%)   15/15  (100%)
   0.070   15/45  ( 33%)   15/15  (100%)
   0.080    0/15  (  0%)               -
   0.100    0/15  (  0%)               -
   0.130               -               -
   0.160               -               -
   0.200               -               -

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

=== model: granite (n=720) ===

=== granite (n=720) ===
  zero_hits=0  top_changed=540 (75%)
  score_gap_top_bottom: p25=0.0117 p50=0.0201 p75=0.0353 p90=0.0560 min=0.0051 max=0.1128
  score_gap_top1_top2 : p25=0.0013 p50=0.0045 p75=0.0092 p90=0.0229 min=0.0001 max=0.0450
  score_top1          : p25=0.670 p50=0.719 p75=0.757 p90=0.808 min=0.382 max=0.855
  latency_ms no/with: p50 130/228  p95 177/302
  rerank_outcome: {'reranked': 720}
  query_kind: {'broad': 90, 'sharp': 60, 'ambiguous': 60, 'paraphrase': 60, 'incident': 30, 'ooc': 60, 'on-topic': 195, 'off-topic': 90, 'broad-room': 75}

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

=== model: potion (n=720) ===

=== potion (n=720) ===
  zero_hits=0  top_changed=495 (69%)
  score_gap_top_bottom: p25=0.0293 p50=0.0505 p75=0.0881 p90=0.1087 min=0.0039 max=0.1764
  score_gap_top1_top2 : p25=0.0063 p50=0.0171 p75=0.0326 p90=0.0665 min=0.0003 max=0.1262
  score_top1          : p25=0.246 p50=0.299 p75=0.417 p90=0.535 min=0.023 max=0.737
  latency_ms no/with: p50 51/167  p95 107/369
  rerank_outcome: {'reranked': 720}
  query_kind: {'broad': 90, 'sharp': 60, 'ambiguous': 60, 'paraphrase': 60, 'incident': 30, 'ooc': 60, 'on-topic': 195, 'off-topic': 90, 'broad-room': 75}

=== threshold sweep: %% of traffic where gap > T (rerank skipped) ===
  T           potion
   0.020         85%
   0.030         73%
   0.040         62%
   0.050         54%
   0.060         46%
   0.070         40%
   0.080         27%
   0.100         21%
   0.130          6%
   0.160          4%
   0.200          0%

## Cross-model comparison

| model | n | mean_recall_top5 | top1_keyword_hit_rate | p50 gap | p50 lat_no_rerank_ms | p50 lat_with_rerank_ms |
|---|---|---|---|---|---|---|
| granite | 720 | 0.000 | 0.000 | 0.0201 | 130.0 | 228.0 |
| potion | 720 | 0.000 | 0.000 | 0.0505 | 51.0 | 167.0 |
