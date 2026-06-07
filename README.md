# LevaraOS

> Persistent memory, search, and workspace infrastructure for AI agents.

LevaraOS is a local-first, self-hostable memory platform for developers and
teams working with AI agents. The main engine lives in [`Levara/`](Levara/): a
Go service that exposes HTTP, MCP, and gRPC surfaces over vector search, BM25,
temporal graph memory, Markdown workspaces, sync, auth/RBAC, audit, and
enterprise adapter seams.

<CardGroup cols={2}>
  <Card title="Read the main README" href="Levara/README.md">
    Full MDX-style project overview, architecture, setup, profiles, APIs, and contribution guide.
  </Card>
  <Card title="Pick a product profile" href="Levara/docs/profile-presets.md">
    Personal, Solo Pro, Team, and Enterprise runtime presets.
  </Card>
</CardGroup>

## Quick Start

```bash
cd Levara
go test ./pkg/profile ./cmd/server
make build

cp deploy/profiles/personal.local.env.example .env
./levara-server -config-check
./levara-server -standalone=true -dim=768 -port=8080 -grpc-port=50051
```

## Repository Layout

| Path | Purpose |
|---|---|
| [`Levara/`](Levara/) | Current Go engine, MCP server, REST/gRPC APIs, docs, deploy profiles |
| [`Levara/webui/`](Levara/webui/) | Next.js Web UI |
| [`docs/product/`](docs/product/) | Product/GTM notes outside the engine module |
| [`examples/`](examples/) | Integration examples |

## Product Materials

- [Main README](Levara/README.md)
- [Product ladder](Levara/docs/product-ladder.md)
- [Profile presets](Levara/docs/profile-presets.md)
- [Marketing index](Levara/docs/marketing/README.md)
- [Market segments](docs/product/market-segments.md)
- [Security diff checklist](Levara/docs/security-diff-checklist.md)

## License

MIT. See [`Levara/LICENSE`](Levara/LICENSE).
