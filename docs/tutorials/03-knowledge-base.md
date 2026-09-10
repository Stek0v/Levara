# Tutorial 03 — Build a Knowledge Base

This workflow stores a document, processes it, and verifies retrieval from the
same collection. Start the SQLite-backed semantic server in
[getting started](../getting-started.md#add-semantic-document-search). It needs a
working embedding endpoint with matching dimension. An LLM is optional for
chunk search and required for full model-based graph extraction.

## 1. Add and process

From the checkout root:

```bash
export LEVARA_URL=http://127.0.0.1:8080/api/v1
./levara add 'Alice works at Acme. Bob reports to Alice.' --dataset=company-notes
./levara cognify --dataset=company-notes --collection=company-kb --wait
```

For a file, use `./levara add --file=./company.md --dataset=company-notes`.
`add` persists input; `cognify` processes selected dataset records. The dataset
and retrieval collection have different names here deliberately: permissions
are attached to the dataset, while `collection` routes the index/search.

Require an explicit successful terminal result. A run ID, an accepted upload or
an HTTP 200 is not proof that extraction/indexing completed. See
[document management](../document-management.md) for failed extraction, retries,
already-processed no-ops and selected-collection status.

## 2. Check lexical and semantic retrieval

```bash
./levara search 'Alice Acme' --collection=company-kb --type=CHUNKS_LEXICAL --top-k=3
./levara search 'who employs Alice' --collection=company-kb --type=HYBRID --top-k=3
```

The equivalent REST lexical request is:

```bash
curl -fsS http://127.0.0.1:8080/api/v1/search/text \
  -H 'Content-Type: application/json' \
  -d '{"query_text":"Alice Acme","query_type":"CHUNKS_LEXICAL","collection":"company-kb","top_k":3}'
```

For MCP, use the actual argument names:

```text
search(search_query="who employs Alice", search_type="HYBRID",
       collection="company-kb", top_k=3, rerank=false)
```

Omitting the search type selects AUTO, not a guaranteed BM25 or HYBRID path.
Inspect the returned text and its source, not just the number of hits.
[Search strategies](../search-strategies-guide.md) covers routing and rerank.

## 3. Optional graph extraction

Configure `LLM_PROVIDER`, `LLM_ENDPOINT`, `LLM_MODEL` and any provider key using
[integrations](../integrations.md). Graph persistence also needs configured SQL
or Neo4j. Model output is variable: do not assert a particular edge just because
the input contains two names. Inspect the graph after a completed extraction.

```text
query_entity(name="Alice")
```

`query_entity(name=..., as_of=...)` reads a historical view of edges with temporal
validity. It does not infer a historical fact missing from your source material.

## 4. Manage and share the document

Use the WebUI dataset page to inspect extracted text, download the original,
and grant an individual user viewer/editor/admin access to the whole dataset.
For one document or an organization group, use the authenticated document ACL
REST flow in [document management](../document-management.md); the WebUI panel
and CLI commands are still pending. Shared editors should target the existing
dataset ID through WebUI/API; CLI `add --dataset` currently resolves a dataset
name for the caller.

Deletion is not the inverse of every ingestion path. MCP `delete` takes
`dataset_id`, not `data_id`, and does not promise complete physical erasure of
all originals and derivatives. Do not use global `prune` as tutorial rollback.
Use an isolated tutorial dataset and follow [document management](../document-management.md)
for the implemented deletion scope.

Next: [Team deployment](04-team-deploy.md), [document acceptance scenarios](../document-workflow-scenarios.md),
or [project ingestion](../project-ingest.md) for a complete repository.
