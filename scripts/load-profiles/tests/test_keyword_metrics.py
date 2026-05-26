from runner import keyword_metrics


def _resp(*texts):
    return [{"id": str(i), "score": 1.0 - i*0.1, "metadata": {"text": t}}
            for i, t in enumerate(texts)]


def test_no_keywords_returns_zero():
    metrics = keyword_metrics(_resp("foo bar"), expected_keywords=[])
    assert metrics == {"keyword_hits_top5": 0, "top1_keyword_hit": False}


def test_top1_match_case_insensitive():
    metrics = keyword_metrics(_resp("Redis Cache invalidation logic"),
                              expected_keywords=["redis", "cache"])
    assert metrics["top1_keyword_hit"] is True
    assert metrics["keyword_hits_top5"] == 2


def test_only_top5_window_counted():
    docs = _resp("a", "b", "c", "d", "e", "redis-server", "cache-key")
    metrics = keyword_metrics(docs, expected_keywords=["redis", "cache"])
    assert metrics["keyword_hits_top5"] == 0
    assert metrics["top1_keyword_hit"] is False


def test_each_keyword_counted_at_most_once_per_query():
    docs = _resp("redis redis redis", "redis again", "x", "y", "z")
    metrics = keyword_metrics(docs, expected_keywords=["redis"])
    assert metrics["keyword_hits_top5"] == 1


def test_empty_response_safe():
    metrics = keyword_metrics(None, expected_keywords=["x"])
    assert metrics == {"keyword_hits_top5": 0, "top1_keyword_hit": False}
    metrics = keyword_metrics([], expected_keywords=["x"])
    assert metrics == {"keyword_hits_top5": 0, "top1_keyword_hit": False}
