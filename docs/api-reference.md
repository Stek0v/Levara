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

## Route groups

The following headings preserve older links. Their maintained tables are in
the [generated REST inventory](api-contract.md#rest).

## admin

See the [`admin` group in the generated inventory](api-contract.md#rest).

## cognify

See the [`cognify` group in the generated inventory](api-contract.md#rest).

## collections

See the [`collections` group in the generated inventory](api-contract.md#rest).

## datasets

See the [`datasets` group in the generated inventory](api-contract.md#rest).

## feedback

See the [`feedback` group in the generated inventory](api-contract.md#rest).

## graph

See the [`graph` group in the generated inventory](api-contract.md#rest).

## ingest

See the [`ingest` group in the generated inventory](api-contract.md#rest).

## mcp

See the [`mcp` group in the generated inventory](api-contract.md#rest).

## memify

See the [`memify` group in the generated inventory](api-contract.md#rest).

## memory

See the [`memory` group in the generated inventory](api-contract.md#rest).

## models

See the [`models` group in the generated inventory](api-contract.md#rest).

## notebooks

See the [`notebooks` group in the generated inventory](api-contract.md#rest).

## ontology

See the [`ontology` group in the generated inventory](api-contract.md#rest).

## ops

See the [`ops` group in the generated inventory](api-contract.md#rest).

## rbac

See the [`rbac` group in the generated inventory](api-contract.md#rest).

## search

See the [`search` group in the generated inventory](api-contract.md#rest).

## sessions

See the [`sessions` group in the generated inventory](api-contract.md#rest).

## settings

See the [`settings` group in the generated inventory](api-contract.md#rest).

## sync

See the [`sync` group in the generated inventory](api-contract.md#rest).

## tenants

See the [`tenants` group in the generated inventory](api-contract.md#rest).

## users

See the [`users` group in the generated inventory](api-contract.md#rest).

## vector

See the [`vector` group in the generated inventory](api-contract.md#rest).

## vsa

See the [`vsa` group in the generated inventory](api-contract.md#rest).

## workspace

See the [`workspace` group in the generated inventory](api-contract.md#rest).

## Usage notes

REST URLs below the production API use `/api/v1`; SCIM uses the separate
`/scim/v2` base. Authentication and workflow behavior are described in
[enterprise identity](enterprise-identity.md) and
[document management](document-management.md). Inventory entries show available
routes, not proof that every provider or authorization scenario has been validated.
