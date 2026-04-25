.PHONY: all build run test benchmark clean docker arm64 proto proto-go proto-python swag

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
