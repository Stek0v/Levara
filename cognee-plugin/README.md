# cognee-cognevra

gRPC adapter connecting Cognee's VectorDBInterface to Cognevra.

## Installation

pip install -e ".[dev]"
make proto  # generate gRPC stubs

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| VECTOR_DB_PROVIDER | cognevra | Select Cognevra backend |
| VECTOR_DB_URL | localhost:50051 | gRPC server address |

## VectorDBInterface Methods

| Method | gRPC RPC | Description |
|--------|----------|-------------|
| has_collection | HasCollection | Check if collection exists |
| create_collection | CreateCollection | Create new collection |
| create_data_points | BatchInsert | Insert vectors with metadata |
| retrieve | GetByID | Get records by ID from server |
| search | Search | Vector similarity search |
| batch_search | Search (parallel) | Multiple queries via asyncio.gather |
| delete_data_points | Delete | Remove records (HNSW tombstone + WAL) |
| prune | DropCollection (all) | Remove all collections |
| embed_data | (local) | Embedding via EmbeddingEngine |

## Development

make proto          # regenerate gRPC stubs
pytest tests/ -v    # run tests
