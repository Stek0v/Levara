#!/usr/bin/env python3
"""Print MTEB SciDocs corpus stats for a quick human sanity check.

Run from the repo root:

    python3 deploy/rerank/eval/dump_fixture.py
"""
from __future__ import annotations

import os
import sys
from collections import Counter

# Allow running as a script without installing the package.
HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)

from corpus_fixture import (  # noqa: E402
    flatten_docs,
    load_scidocs,
    make_queries,
)


def main() -> int:
    try:
        rows = load_scidocs()
    except FileNotFoundError as e:
        print(f"ERROR: {e}", file=sys.stderr)
        return 1

    docs = flatten_docs(rows)
    queries = make_queries(rows)

    total_pos = sum(1 for d in docs if d["label"] == "pos")
    total_neg = sum(1 for d in docs if d["label"] == "neg")
    pos_ratio = (total_pos / len(docs)) if docs else 0.0
    mean_len = (sum(len(d["text"]) for d in docs) / len(docs)) if docs else 0.0

    print("MTEB SciDocs reranking corpus")
    print("-" * 40)
    print(f"rows (queries with pos/neg pairs):  {len(rows)}")
    print(f"flattened docs (deduped):           {len(docs)}")
    print(f"  positives:                        {total_pos}")
    print(f"  negatives:                        {total_neg}")
    print(f"positive ratio:                     {pos_ratio:.4f}")
    print(f"mean doc length (chars):            {mean_len:.1f}")
    print(f"queries with >=1 relevant doc:      {len(queries)}")
    print()
    print("Top-3 queries by relevant-doc count:")
    top = sorted(queries, key=lambda q: len(q["relevant_ids"]), reverse=True)[:3]
    for i, q in enumerate(top, 1):
        qtxt = q["query"]
        if len(qtxt) > 80:
            qtxt = qtxt[:77] + "..."
        print(f"  {i}. ({len(q['relevant_ids'])} relevant) {qtxt}")

    # Bonus: label distribution sanity
    label_counts = Counter(d["label"] for d in docs)
    print()
    print(f"label distribution: {dict(label_counts)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
