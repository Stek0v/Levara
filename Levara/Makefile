.PHONY: all build run test benchmark clean docker arm64 proto proto-go proto-python

# Variables
BINARY_NAME=levara
CLI_NAME=levara
SERVER_NAME=levara-server
DOCKER_IMAGE=levara:latest

all: build

# --- Build ---

build:
	@echo "Building Levara..."
	@go build -o $(SERVER_NAME) ./cmd/server/
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
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="-s -w" -o levara-arm64 ./cmd/server/

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
	@protoc --go_out=. --go-grpc_out=. proto/levara.proto
	@echo "Done."

proto-python:
	@echo "Generating Python gRPC stubs..."
	@python3 -m grpc_tools.protoc \
		-Iproto \
		--python_out=./generated \
		--grpc_python_out=./generated \
		proto/levara.proto
	@echo "Done."

proto: proto-go proto-python

# --- Utilities ---

clean:
	@rm -f $(SERVER_NAME) $(CLI_NAME) levara-arm64
	@rm -rf bin/
	@rm -f *.snap
	@echo "Clean complete."
