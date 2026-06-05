from analyze import (
    group_by_model,
    summarize_quality,
    render_cross_model_markdown,
)


def _rec(model, gap, recall, top1):
    return {
        "embed_model": model,
        "score_gap_top_bottom": gap,
        "keyword_hits_top5": recall,
        "top1_keyword_hit": top1,
        "latency_no_rerank_ms": 10.0,
        "latency_with_rerank_ms": 50.0,
        "rerank_outcome": "reranked",
    }


def test_group_by_model_splits_correctly():
    recs = [_rec("potion", 0.1, 2, True), _rec("granite", 0.2, 3, True), _rec("potion", 0.15, 1, False)]
    g = group_by_model(recs)
    assert set(g) == {"potion", "granite"}
    assert len(g["potion"]) == 2
    assert len(g["granite"]) == 1


def test_summarize_quality_means():
    recs = [_rec("p", 0.1, 2, True), _rec("p", 0.2, 4, False), _rec("p", 0.0, 0, False)]
    s = summarize_quality(recs)
    assert abs(s["mean_recall_top5"] - 2.0) < 1e-9
    assert abs(s["top1_keyword_hit_rate"] - (1/3)) < 1e-9
    assert s["n"] == 3


def test_cross_model_markdown_has_all_models():
    recs = [_rec("potion", 0.1, 2, True), _rec("granite", 0.2, 3, True), _rec("jina", 0.05, 1, False)]
    md = render_cross_model_markdown(group_by_model(recs))
    assert "potion" in md and "granite" in md and "jina" in md
    assert "mean_recall_top5" in md
    assert "top1_keyword_hit_rate" in md
