
=== p3@bench (n=240) ===
  zero_hits=0  top_changed=150 (62%)
  score_gap_top_bottom: p25=0.0437 p50=0.0912 p75=0.1280 p90=0.1832 min=0.0030 max=0.2455
  score_gap_top1_top2 : p25=0.0144 p50=0.0419 p75=0.0712 p90=0.1465 min=0.0004 max=0.1838
  score_top1          : p25=0.163 p50=0.235 p75=0.451 p90=0.537 min=-0.047 max=0.592
  latency_ms no/with: p50 74/1298  p95 205/1506
  rerank_outcome: {'reranked': 240}
  query_kind: {'code-precise': 60, 'concept-paraphrase': 60, 'mixed': 60, 'adversarial': 20, 'ooc': 40}

=== p3@rpi64 (n=240) ===
  zero_hits=0  top_changed=105 (44%)
  score_gap_top_bottom: p25=0.0548 p50=0.0821 p75=0.1252 p90=0.2013 min=0.0110 max=0.4242
  score_gap_top1_top2 : p25=0.0000 p50=0.0016 p75=0.0430 p90=0.1294 min=0.0000 max=0.3763
  score_top1          : p25=0.383 p50=0.606 p75=0.677 p90=0.765 min=0.202 max=0.819
  latency_ms no/with: p50 14/1437  p95 28/1526
  rerank_outcome: {'fallback': 89, 'reranked': 151}
  query_kind: {'code-precise': 60, 'concept-paraphrase': 60, 'mixed': 60, 'adversarial': 20, 'ooc': 40}

=== p4@bench (n=360) ===
  zero_hits=0  top_changed=195 (54%)
  score_gap_top_bottom: p25=0.0301 p50=0.0595 p75=0.0785 p90=0.1379 min=0.0065 max=0.1519
  score_gap_top1_top2 : p25=0.0107 p50=0.0246 p75=0.0425 p90=0.0545 min=0.0003 max=0.1125
  score_top1          : p25=0.249 p50=0.365 p75=0.461 p90=0.581 min=0.147 max=0.737
  latency_ms no/with: p50 83/222  p95 275/680
  rerank_outcome: {'reranked': 360}
  query_kind: {'broad': 90, 'sharp': 60, 'ambiguous': 60, 'paraphrase': 60, 'incident': 30, 'ooc': 60}

=== p4@pi (n=388) ===
  zero_hits=28  top_changed=240 (62%)
  score_gap_top_bottom: p25=0.0218 p50=0.0306 p75=0.0609 p90=0.1444 min=0.0049 max=0.1990
  score_gap_top1_top2 : p25=0.0035 p50=0.0079 p75=0.0176 p90=0.0410 min=0.0001 max=0.1209
  score_top1          : p25=0.462 p50=0.529 p75=0.584 p90=0.617 min=0.151 max=0.743
  latency_ms no/with: p50 30337/30429  p95 30383/30481
  rerank_outcome: {'no_results': 28, 'reranked': 360}
  query_kind: {'broad': 100, 'sharp': 64, 'ambiguous': 64, 'paraphrase': 64, 'incident': 32, 'ooc': 64}

=== p5@bench (n=360) ===
  zero_hits=0  top_changed=285 (79%)
  score_gap_top_bottom: p25=0.0297 p50=0.0551 p75=0.0946 p90=0.1036 min=0.0144 max=0.1794
  score_gap_top1_top2 : p25=0.0076 p50=0.0187 p75=0.0341 p90=0.0503 min=0.0008 max=0.0665
  score_top1          : p25=0.238 p50=0.299 p75=0.388 p90=0.416 min=0.023 max=0.469
  latency_ms no/with: p50 83/203  p95 256/505
  rerank_outcome: {'reranked': 360}
  query_kind: {'on-topic': 195, 'off-topic': 90, 'broad-room': 75}

=== p5@pi (n=360) ===
  zero_hits=0  top_changed=225 (62%)
  score_gap_top_bottom: p25=0.0205 p50=0.0300 p75=0.0430 p90=0.0853 min=0.0027 max=0.2072
  score_gap_top1_top2 : p25=0.0033 p50=0.0060 p75=0.0147 p90=0.0302 min=0.0000 max=0.0383
  score_top1          : p25=0.366 p50=0.418 p75=0.534 p90=0.571 min=0.233 max=0.611
  latency_ms no/with: p50 30315/30403  p95 30340/30448
  rerank_outcome: {'reranked': 360}
  query_kind: {'on-topic': 195, 'off-topic': 90, 'broad-room': 75}

=== threshold sweep: %% of traffic where gap > T (rerank skipped) ===
  T         p3@bench    p3@rpi64    p4@bench       p4@pi    p5@bench       p5@pi
   0.020         83%         92%         88%         75%         88%         75%
   0.030         75%         92%         75%         50%         75%         42%
   0.040         75%         88%         67%         38%         62%         29%
   0.050         71%         83%         54%         29%         54%         12%
   0.060         67%         71%         50%         25%         46%         12%
   0.070         62%         58%         42%         25%         38%         12%
   0.080         50%         50%         25%         17%         38%         12%
   0.100         50%         38%         17%         12%         21%          8%
   0.130         25%         25%         12%         12%          4%          8%
   0.160         12%         17%          0%          8%          4%          8%
   0.200          8%         12%          0%          0%          0%          4%

=== gate-wrong rate at T: %% of skipped queries where rerank WOULD have changed top ===
  T             p3@bench        p3@rpi64        p4@bench           p4@pi        p5@bench           p5@pi
   0.020  130/200 ( 65%)  105/220 ( 48%)  180/315 ( 57%)  165/270 ( 61%)  240/315 ( 76%)  150/270 ( 56%)
   0.030  120/180 ( 67%)  105/220 ( 48%)  165/270 ( 61%)  105/180 ( 58%)  210/270 ( 78%)   60/150 ( 40%)
   0.040  120/180 ( 67%)  103/210 ( 49%)  150/240 ( 62%)   75/135 ( 56%)  180/225 ( 80%)   30/105 ( 29%)
   0.050  110/170 ( 65%)  100/200 ( 50%)  120/195 ( 62%)   60/105 ( 57%)  150/195 ( 77%)   15/45  ( 33%)
   0.060  100/160 ( 62%)   81/170 ( 48%)  105/180 ( 58%)   45/90  ( 50%)  120/165 ( 73%)   15/45  ( 33%)
   0.070   90/150 ( 60%)   68/140 ( 49%)   75/150 ( 50%)   45/90  ( 50%)  105/135 ( 78%)   15/45  ( 33%)
   0.080   60/120 ( 50%)   57/120 ( 48%)   45/90  ( 50%)   30/60  ( 50%)  105/135 ( 78%)   15/45  ( 33%)
   0.100   60/120 ( 50%)   43/90  ( 48%)   30/60  ( 50%)   15/45  ( 33%)   60/75  ( 80%)   15/30  ( 50%)
   0.130   20/60  ( 33%)   27/60  ( 45%)   15/45  ( 33%)   15/45  ( 33%)    0/15  (  0%)   15/30  ( 50%)
   0.160    0/30  (  0%)   17/40  ( 42%)               -   15/30  ( 50%)    0/15  (  0%)   15/30  ( 50%)
   0.200    0/20  (  0%)    7/30  ( 23%)               -               -               -   15/15  (100%)

=== recommendation scan ===
       T   avg_skip%   avg_wrong%
   0.020       83.3%        60.5%
   0.030       68.1%        58.6%
   0.040       59.7%        57.1%
   0.050       50.7%        57.3%
   0.060       45.1%        54.1%
   0.070       39.6%        53.3%
   0.080       31.9%        51.4%
   0.100       24.3%        51.9%
   0.130       14.6%        32.5%
   0.160        8.3%        28.5%
   0.200        4.2%        41.1%

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

=== model: pi (n=748) ===

=== pi (n=748) ===
  zero_hits=28  top_changed=465 (62%)
  score_gap_top_bottom: p25=0.0206 p50=0.0300 p75=0.0450 p90=0.1444 min=0.0027 max=0.2072
  score_gap_top1_top2 : p25=0.0033 p50=0.0067 p75=0.0158 p90=0.0354 min=0.0000 max=0.1209
  score_top1          : p25=0.385 p50=0.477 p75=0.556 p90=0.611 min=0.151 max=0.743
  latency_ms no/with: p50 30326/30416  p95 30372/30472
  rerank_outcome: {'no_results': 28, 'reranked': 720}
  query_kind: {'broad': 100, 'sharp': 64, 'ambiguous': 64, 'paraphrase': 64, 'incident': 32, 'ooc': 64, 'on-topic': 195, 'off-topic': 90, 'broad-room': 75}

=== threshold sweep: %% of traffic where gap > T (rerank skipped) ===
  T               pi
   0.020         75%
   0.030         46%
   0.040         33%
   0.050         21%
   0.060         19%
   0.070         19%
   0.080         15%
   0.100         10%
   0.130         10%
   0.160          8%
   0.200          2%

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

=== model: rpi64 (n=240) ===

=== rpi64 (n=240) ===
  zero_hits=0  top_changed=105 (44%)
  score_gap_top_bottom: p25=0.0548 p50=0.0821 p75=0.1252 p90=0.2013 min=0.0110 max=0.4242
  score_gap_top1_top2 : p25=0.0000 p50=0.0016 p75=0.0430 p90=0.1294 min=0.0000 max=0.3763
  score_top1          : p25=0.383 p50=0.606 p75=0.677 p90=0.765 min=0.202 max=0.819
  latency_ms no/with: p50 14/1437  p95 28/1526
  rerank_outcome: {'fallback': 89, 'reranked': 151}
  query_kind: {'code-precise': 60, 'concept-paraphrase': 60, 'mixed': 60, 'adversarial': 20, 'ooc': 40}

=== threshold sweep: %% of traffic where gap > T (rerank skipped) ===
  T            rpi64
   0.020         92%
   0.030         92%
   0.040         88%
   0.050         83%
   0.060         71%
   0.070         58%
   0.080         50%
   0.100         38%
   0.130         25%
   0.160         17%
   0.200         12%

## Cross-model comparison

| model | n | mean_recall_top5 | top1_keyword_hit_rate | p50 gap | p50 lat_no_rerank_ms | p50 lat_with_rerank_ms |
|---|---|---|---|---|---|---|
| granite | 960 | 0.000 | 0.000 | 0.0124 | 159.0 | 252.0 |
| pi | 748 | 0.000 | 0.000 | 0.0300 | 30326.0 | 30416.0 |
| potion | 960 | 0.000 | 0.000 | 0.0678 | 81.0 | 243.5 |
| rpi64 | 240 | 0.000 | 0.000 | 0.0821 | 14.0 | 1437.5 |
