# Getting Started with Levara

## Prerequisites

- Go 1.26+ (for building from source)
- Docker and Docker Compose (alternative)

### Optional Dependencies

- **Neo4j** -- for graph database features (entity relationships, Cypher queries)
- **Ollama** -- for local LLM (entity extraction, summarization, chain-of-thought)
- **Embedding server** -- for computing vector embeddings (or use built-in provider)

## Installation

### From Source

```bash
go install github.com/stek0v/levara/cmd/server@latest
go install github.com/stek0v/levara/cmd/cli@latest
```

### Build Locally

```bash
git clone https://github.com/stek0v/levara.git
cd levara
make build
```

This produces two binaries:
- `levara-server` -- the main server
- `levara` -- the CLI tool

### Docker

```bash
docker compose -f deploy/docker/docker-compose.yml up -d
```

## First Run

### Start the Server

```bash
# Minimal: SQLite backend, no external dependencies
./levara-server -standalone=true -dim=768 -port=8080
```

Verify it is running:

```bash
curl http://localhost:8080/health
# {"status":"ok","vectors":0}
```

### Add Data

#### Via CLI

```bash
# Add a text document
levara add "Levara is a knowledge graph engine built in Go. It combines vector search with graph databases." --dataset=demo

# Add a file
levara add ./my-document.pdf --dataset=demo

# Add a directory of files
levara add ./documents/ --dataset=demo
```

#### Via HTTP API

```bash
curl -X POST http://localhost:8080/api/v1/datasets/demo/add \
  -H "Content-Type: application/json" \
  -d '{"text": "Levara is a knowledge graph engine built in Go."}'
```

### Build Knowledge Graph

```bash
# Process added data into a knowledge graph
levara cognify --dataset=demo --wait
```

This step:
1. Chunks documents into semantic segments
2. Computes vector embeddings
3. Extracts entities and relationships (if LLM is configured)
4. Builds the HNSW index
5. Optionally populates Neo4j graph

### Search

```bash
# Vector search
levara search "What is Levara?" --dataset=demo

# Graph completion search
levara search "How does the architecture work?" --type=GRAPH_COMPLETION --dataset=demo

# Hybrid search (vector + BM25)
levara search "knowledge graph engine" --type=HYBRID --dataset=demo
```

## Configuration

### Environment Variables

Create a `.env` file or export variables:

```bash
export DB_PROVIDER=sqlite
export VECTOR_DIM=768
export LLM_PROVIDER=ollama
export LLM_MODEL=qwen3.5:latest
export OLLAMA_URL=http://127.0.0.1:11434
```

### With Neo4j

```bash
export NEO4J_URI=bolt://localhost:7687
export NEO4J_USER=neo4j
export NEO4J_PASSWORD=your_password
```

### With OpenAI

```bash
export LLM_PROVIDER=openai
export LLM_API_KEY=sk-...
export LLM_MODEL=gpt-4o
```

## MCP Integration

To use Levara with Claude Desktop or Cursor, add to your MCP config:

```json
{
  "mcpServers": {
    "levara": {
      "url": "http://localhost:8080/mcp"
    }
  }
}
```

Then ask your AI assistant to search, add data, or manage datasets through natural language.

## Next Steps

- [API Reference](api-reference.md) -- full HTTP and gRPC API documentation
- [Deployment Guide](deployment.md) -- Docker, Raspberry Pi, systemd setup
- [README](../README.md) -- feature overview and benchmarks
