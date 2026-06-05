.PHONY: all build run test test-commit test-release-candidate benchmark clean docker arm64 proto proto-go proto-python swag contract contract-check

# Regenerate OpenAPI spec from swaggo annotations (T13).
# Requires `go install github.com/swaggo/swag/cmd/swag@latest` once.
# Output: docs/docs.go, docs/swagger.json, docs/swagger.yaml.
swag:
	@echo "Regenerating OpenAPI spec from swaggo annotations..."
	@$$(go env GOPATH)/bin/swag init -g cmd/server/main.go -d ./ -o docs/
	@echo "Done. Serve via /swagger/* when ENV=dev."

# Variables
BINARY_NAME=levara
CLI_NAME=levara
SERVER_NAME=levara-server
DOCKER_IMAGE=levara:latest

# Build metadata wired into /version via -ldflags -X main.GitSHA / main.BuildTime.
GIT_SHA    := $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
BUILD_TIME := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS    := -X main.GitSHA=$(GIT_SHA) -X main.BuildTime=$(BUILD_TIME)

all: build

# --- Build ---

build:
	@echo "Building Levara ($(GIT_SHA) @ $(BUILD_TIME))..."
	@go build -ldflags "$(LDFLAGS)" -o $(SERVER_NAME) ./cmd/server/
	@go build -o $(CLI_NAME) ./cmd/cli/
	@echo "Build complete: $(SERVER_NAME), $(CLI_NAME)"

run: build
	@echo "Starting server on :8080..."
	@./$(SERVER_NAME)

test:
	@go test ./...

benchmark:
	@echo "Running Benchmark Suite..."
	@go run ./cmd/benchmark/main.go

# --- Test gates (docs/full-testing-scenarios.md) ---

# Every-commit gate: the focused S0-S4 suites a change must pass before it is
# committed. Ordered cheapest-first so a static/docs slip fails fast. Each suite
# is its own recipe line, so the first failure aborts the target.
test-commit:
	@echo "[S0] static + docs"
	@git diff --check
	@go test ./docs
	@echo "[S1] unit packages (access, profile, audit, workspace, mcp)"
	@go test ./pkg/access ./pkg/profile ./pkg/audit ./pkg/workspace ./pkg/mcp
	@echo "[S2] core engine (store, vectorstore, bm25)"
	@go test ./internal/store ./pkg/vectorstore ./pkg/bm25
	@echo "[S3] HTTP / MCP surface (internal/http)"
	@go test ./internal/http
	@echo "[S4] server bootstrap (cmd/server)"
	@go test ./cmd/server
	@echo "test-commit: S0-S4 green."

# Release-candidate gate: the heavier, ship-decision checks. Runs fresh
# (-count=1, no cached results) over profile smoke, sync + backup, and the
# workspace retrieval-quality eval. Pi-hardware smoke and the two-server S3 mock
# harness are NOT automated here — they remain manual per the runbook; this
# target logs that rather than silently implying full coverage.
test-release-candidate:
	@echo "[RC] profile smoke (pkg/profile + cmd/server runtime profile/audit)"
	@go test -count=1 ./pkg/profile
	@go test -count=1 ./cmd/server -run 'Profile|AuditSink'
	@echo "[RC] sync flow (internal/http TestSync*)"
	@go test -count=1 ./internal/http -run 'TestSync'
	@echo "[RC] backup / restore (pkg/backup)"
	@go test -count=1 ./pkg/backup
	@echo "[RC] workspace retrieval-quality eval"
	@go test -count=1 ./internal/http -run 'TestWorkspaceRetrievalQualityEval|TestWorkspaceEvalMetricsEmpty'
	@echo "test-release-candidate: automated gates green."
	@echo "NOTE: not automated by this target (run manually — see docs/full-testing-scenarios.md):"
	@echo "  - Pi-hardware edge smoke (10.23.0.53)"
	@echo "  - two-server sync/backup mock harness (S3 cross-instance)"

# --- Cross-compile ---

arm64:
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="-s -w $(LDFLAGS)" -o levara-arm64 ./cmd/server/

# --- Docker ---

docker:
	docker build -t $(DOCKER_IMAGE) .

docker-run:
	@docker run -d --rm \
		-p 8080:8080 \
		-v $(PWD)/data:/app/data \
		--name levara_instance \
		$(DOCKER_IMAGE)
	@echo "Container running. Logs: docker logs levara_instance"

docker-stop:
	@docker stop levara_instance || true

# --- Proto ---

proto-go:
	@echo "Generating Go protobuf stubs..."
	@mkdir -p proto/pb proto/pb/v2
	@PATH="$$(go env GOPATH)/bin:$$PATH" protoc \
		-Iproto \
		--go_out=proto/pb --go_opt=paths=source_relative \
		--go-grpc_out=proto/pb --go-grpc_opt=paths=source_relative \
		proto/levara.proto
	@PATH="$$(go env GOPATH)/bin:$$PATH" protoc \
		-Iproto \
		--go_out=proto/pb/v2 --go_opt=paths=source_relative \
		--go-grpc_out=proto/pb/v2 --go-grpc_opt=paths=source_relative \
		proto/levara_v2.proto
	@echo "Done."

proto-python:
	@echo "Generating Python gRPC stubs..."
	@mkdir -p ./generated
	@PATH="$$(go env GOPATH)/bin:$$PATH" python3 -m grpc_tools.protoc \
		-Iproto \
		--python_out=./generated \
		--grpc_python_out=./generated \
		proto/levara.proto proto/levara_v2.proto
	@echo "Done."

proto: proto-go proto-python

# --- Utilities ---

clean:
	@rm -f $(SERVER_NAME) $(CLI_NAME) levara-arm64
	@rm -rf bin/
	@rm -f *.snap
	@echo "Clean complete."

# Regenerate canonical API contract artefacts (docs/api-contract.md,
# docs/contract.json, AGENTS.md MCP section) from the in-tree inventories.
contract:
	@go run ./cmd/contract generate -out ../docs -repo ..

# CI gate: fail when committed contract artefacts drift from current
# inventories or when docs/deployment-matrix.md references an unknown
# REST endpoint.
contract-check:
	@go run ./cmd/contract validate -out ../docs -repo ..
