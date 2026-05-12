# Markdown Workspace Answer Contract

`workspace_search` и `workspace_read` должны возвращать не просто релевантный текст, а проверяемый citation contract. Это контракт для агент-хостов: `Codex`, `Claude`, `Cursor` и любых MCP-клиентов.

## Search Contract

`workspace_search` обязан возвращать:

- `answer_contract.required = true`
- `answer_contract.search_tool = "workspace_search"`
- `answer_contract.read_tool = "workspace_read"`
- `answer_contract.citation_field = "citation"`
- `answer_contract.exact_read_required = true`
- `answer_contract.required_fields` со следующими ключами:
  `project_id`, `branch`, `path`, `generation`, `collection`, `heading_path`, `chunk_id`, `vector_id`, `source_uri`

Каждый search result должен содержать `citation` с минимумом:

- `source_id`
- `project_id`
- `branch`
- `path`
- `generation`
- `collection`
- `chunk_id`
- `vector_id`
- `source_uri`
- `read_tool`
- `read_args`
- `stale`
- `potentially_stale`

## Read Contract

`workspace_read` обязан возвращать:

- file-level `citation` для exact source
- chunk-level `citations` для chunks текущего manifest/generation
- `source_uri` в формате `workspace://<project>/<branch>/<path>#<anchor>` если у chunk есть heading

## Freshness Rules

- Если пользователь явно запросил неактивную generation, `freshness.stale = true` и каждый result `citation.stale = true`.
- Если watcher показывает pending reconcile для той же project/branch, `freshness.potentially_stale = true` и каждый result `citation.potentially_stale = true`.
- Denied responses и missing active generation не должны подделывать успешный citation contract.
