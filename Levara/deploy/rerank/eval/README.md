# Rerank evaluation fixtures

This directory holds shared test fixtures for Levara rerank work
(chaos / soak / perf). The current focus is the **MTEB SciDocs
reranking** corpus, which gives us realistic
(query, positive docs, negative docs) triples so rerank-quality and
load tests don't have to ship ad-hoc seed data.

## Files

- `mteb_scidocs_reranking.jsonl` — the dataset (~9 MB, **not committed**,
  reproducible). Each line is JSON:
  `{"query": "...", "positive": ["doc1", ...], "negative": ["docA", ...]}`.
- `corpus_fixture.py` — pure-stdlib loader + helpers (`load_scidocs`,
  `flatten_docs`, `make_queries`, `seed_collection`). Used by tests in
  `deploy/rerank/` (task C). Tasks D (soak) and E (perf) consume it via:

  ```python
  from corpus_fixture import load_scidocs, flatten_docs, make_queries, seed_collection
  rows    = load_scidocs(limit=500)        # raw triples
  docs    = flatten_docs(rows)             # deduped doc records w/ sci-<hash8> ids
  queries = make_queries(rows)             # query -> relevant_ids (positives)
  seed_collection(base_url, "scidocs", docs)  # ingest into a Levara instance
  ```

- `dump_fixture.py` — prints corpus stats for a quick sanity check:

  ```
  python3 deploy/rerank/eval/dump_fixture.py
  ```

## Regenerating the dataset

The JSONL is reproducible from the upstream HuggingFace dataset
[`mteb/scidocs-reranking`](https://huggingface.co/datasets/mteb/scidocs-reranking).

```bash
python -c "from datasets import load_dataset; import json; \
ds=load_dataset('mteb/scidocs-reranking', split='test'); \
open('deploy/rerank/eval/mteb_scidocs_reranking.jsonl','w').writelines( \
  json.dumps({'query':r['query'],'positive':r['positive'],'negative':r['negative']})+'\n' for r in ds)"
```

Override the location with the `MTEB_SCIDOCS_PATH` env var if needed:

```bash
MTEB_SCIDOCS_PATH=/tmp/scidocs.jsonl python3 deploy/rerank/eval/dump_fixture.py
```

## Ingest path

`seed_collection` POSTs each doc to `/api/v1/add` (JSON body with
`data`, `dataset_name`, `tags`) and then triggers `/api/v1/cognify`
with the inline `texts` array against the target collection. These are
the canonical ingest routes registered in
`Levara/internal/http/api.go`; there is **no** direct
vector-insert HTTP endpoint, so the cognify path is correct regardless
of whether an embedding function is available client-side.

## Consumers

- **Task C (this commit):** ships the fixture only — chaos and A tests
  are intentionally untouched.
- **Task D (soak):** will use `seed_collection` once on startup, then
  drive `make_queries` rows through `/search` for hours, asserting that
  the positive doc IDs stay in the top-K after rerank.
- **Task E (perf):** will reuse the same seeded collection and replay
  queries at concurrency to measure rerank latency vs. vector-only.

## Constraints / conventions

- Pure stdlib + `requests` for ingestion. No `datasets`, `torch`, etc.
  required to run tests once the JSONL exists.
- Doc IDs are stable: `sci-<sha1(text)[:8]>`. Same text => same ID
  across runs, which keeps `relevant_ids` comparable.
- The dataset file is intentionally untracked (~9 MB). Don't commit it.
