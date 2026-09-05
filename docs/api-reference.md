# REST API Reference

This page is a reader guide. The maintained, machine-generated route inventory
is [api-contract.md](api-contract.md), with [contract.json](contract.json) for
programmatic use. `make contract` regenerates those files; `make contract-check`
checks their structural drift. This page no longer duplicates their route table.

## Base URLs and authentication

Application routes use the `/api/v1` prefix: for example, a generated `/datasets`
entry is requested as `/api/v1/datasets`. The catalogue includes canonical,
legacy, operational and alias entries; its total is not a canonical-only count.
Bootstrap health/auth/identity routes are registered separately and are not all
listed in that catalogue. Swagger is an annotation-derived subset.

For local JWT/API keys, OIDC bearer verification, SAML SP and SCIM Users routes,
see [enterprise identity](enterprise-identity.md). It specifies exact prefixes
and supported operations. Protected requests require the configured credentials;
public health, login/registration and transport discovery have separate rules.

## MCP transport settings

Use `/mcp` for the legacy session-compatible transport and the initial examples
in [getting started](getting-started.md). The versioned `/mcp/2026-07-28` endpoint
is stateless: each request supplies its own metadata and collection. It uses
`server/discover` instead of `initialize`; `set_context` is unavailable there.

For the versioned endpoint, send `Content-Type: application/json`,
`Accept: application/json, text/event-stream`, and:

| Header | Required value |
|---|---|
| `MCP-Protocol-Version` | `2026-07-28`, matching the body metadata |
| `Mcp-Method` | The JSON-RPC `method` |
| `Mcp-Name` | Tool `params.name` for `tools/call`, or `params.uri` for `resources/read` |
| `Authorization` | Configured bearer credential for protected calls |

Every request includes this object inside `params`:

```json
{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"my-client","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}
```

Use a client that implements these requirements; changing only its endpoint URL
is insufficient. Current request/error behavior is exercised by
[transport tests](../internal/http/mcp_latest_test.go) and
[authentication tests](../internal/http/mcp_latest_auth_test.go).

## Choose a workflow

- Upload files or text, check extraction/indexing, search with source evidence:
  [document management](document-management.md).
- Grant/revoke an individual's dataset role and verify denied access:
  [document scenarios](document-workflow-scenarios.md).
- Configure a deployment and inspect failures:
  [profile presets](profile-presets.md), [WebUI operations](webui-operations.md).
- Use Markdown source files and revision-aware writes:
  [workspace guide](markdown-native-workspace.md).

Response and error shapes belong to the individual endpoint or MCP tool.
Do not infer success from HTTP200 alone for MCP: inspect its tool result/error.
For asynchronous processing, retain the returned run ID and inspect terminal
status before treating uploaded content as searchable.

## Usage notes

REST URLs below the production API use `/api/v1`; SCIM uses the separate
`/scim/v2` base. Authentication and workflow behavior are described in
[enterprise identity](enterprise-identity.md) and
[document management](document-management.md). Inventory entries show available
routes, not proof that every provider or authorization scenario has been validated.
