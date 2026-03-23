# Contributing to Levara

Thank you for your interest in contributing to Levara. This document outlines the process for contributing to the project.

## Development Setup

### Prerequisites

- Go 1.26+
- Docker and Docker Compose (for integration tests)
- `protoc` with Go and Python plugins (for proto changes)
- `grpcurl` (for manual gRPC testing)

### Getting Started

```bash
# Clone the repository
git clone https://github.com/stek0v/levara.git
cd levara

# Build
make build

# Run tests
make test

# Run locally
make run
```

### Project Structure

```
cmd/server/     # Server entry point
cmd/cli/        # CLI tool
cmd/benchmark/  # Benchmark suite
internal/       # Core engine (store, HNSW, WAL, arena, HTTP, cluster)
pkg/            # Feature packages (LLM, graph, embed, chunker, etc.)
pipeline/       # Pipeline definitions
proto/          # Protobuf definitions
deploy/         # Docker and Raspberry Pi deployment
docs/           # Documentation
```

## Code Style

- Follow standard Go conventions (`gofmt`, `go vet`, `golangci-lint`)
- Use meaningful variable names; avoid single-letter names outside of loops
- Keep functions focused and under 80 lines where practical
- Write table-driven tests
- Add comments for exported types and functions
- Error messages should be lowercase, without trailing punctuation

### Imports

Group imports in this order, separated by blank lines:

1. Standard library
2. External dependencies
3. Internal packages

### Naming

- Package names: lowercase, single word
- Exported types: `PascalCase`
- Unexported: `camelCase`
- Constants: `PascalCase` for exported, `camelCase` for unexported
- Interfaces: use `-er` suffix where appropriate (`Searcher`, `Embedder`)

## Pull Requests

### Before Submitting

1. Create a feature branch from `main`: `git checkout -b feat/my-feature`
2. Write tests for new functionality
3. Run the full test suite: `make test`
4. Run linting: `golangci-lint run`
5. Ensure your code compiles: `make build`

### PR Guidelines

- Keep PRs focused on a single change
- Write a clear title (under 70 characters)
- Include a description of what changed and why
- Reference related issues with `Fixes #123` or `Relates to #123`
- Add benchmark results if the change affects performance

### Commit Messages

Use conventional commits:

```
feat(store): add batch upsert support
fix(wal): prevent fsync stall on high write load
docs: update API reference for search types
test(hnsw): add concurrent insert/delete test
perf(arena): reduce page allocation overhead by 30%
refactor(llm): extract provider interface
```

### Review Process

1. All PRs require at least one review
2. CI must pass (tests, linting, build)
3. Performance-sensitive changes should include benchmark comparisons
4. Breaking API changes require discussion in an issue first

## Testing

### Running Tests

```bash
# All tests
make test

# Specific package
go test ./internal/store/... -v

# With race detection
go test -race ./...

# Benchmarks
go test -bench=. ./internal/store/...
```

### Writing Tests

- Place tests in `_test.go` files alongside the code they test
- Use `testing.T` for unit tests, `testing.B` for benchmarks
- Use subtests (`t.Run`) for table-driven tests
- Clean up test data (use `t.TempDir()` for temporary directories)
- Mock external dependencies (LLM, Neo4j) in unit tests

### Integration Tests

Integration tests require running services (Levara server, Neo4j, etc.). Tag them with build constraints:

```go
//go:build integration

package store_test
```

Run with:

```bash
go test -tags=integration ./...
```

## Release Process

1. Update version in relevant files
2. Update CHANGELOG.md
3. Create a git tag: `git tag v1.x.x`
4. Push tag: `git push origin v1.x.x`
5. CI builds release binaries (linux/amd64, linux/arm64, darwin/amd64, darwin/arm64)
6. Docker image is published to registry

## Reporting Issues

- Use GitHub Issues for bug reports and feature requests
- Include Go version, OS, and relevant configuration
- For bugs: include steps to reproduce, expected vs actual behavior
- For performance issues: include benchmark data and hardware specs

## License

By contributing to Levara, you agree that your contributions will be licensed under the MIT License.
