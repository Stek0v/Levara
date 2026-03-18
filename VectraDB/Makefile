.PHONY: all build run test benchmark docker-build docker-run clean

# Variables
BINARY_NAME=vectradb
DOCKER_IMAGE=vectradb:latest

# Default command if you just type 'make'
all: build

# --- Local Development ---

# Build the binary into a /bin folder
build:
	@echo "Building VectraDB..."
	@mkdir -p bin
	@go build -o bin/$(BINARY_NAME) ./cmd/server/main.go
	@echo "✅ Build complete: bin/$(BINARY_NAME)"

# Run the server locally
run: build
	@echo "Starting server on :8080..."
	@./bin/$(BINARY_NAME)

# Run the benchmark tool locally
benchmark:
	@echo "Running Benchmark Suite..."
	@go run ./cmd/benchmark/main.go

# --- Docker Operations ---

# Build the optimized Docker image
docker-build:
	@echo "Building Docker Image..."
	@docker build --network=host -t $(DOCKER_IMAGE) .
	@echo "✅ Docker Image built: $(DOCKER_IMAGE)"

# Run the container (detached) with persistence
docker-run:
	@echo "Starting Container..."
	@docker run -d --rm \
		-p 8080:8080 \
		-v $(PWD)/data:/root/ \
		--name vectra_instance \
		$(DOCKER_IMAGE)
	@echo "✅ Container running. Logs: 'docker logs vectra_instance'"

# Stop the running container
docker-stop:
	@docker stop vectra_instance || true

# --- Utilities ---

# Clean up binaries and snapshots
clean:
	@echo "Cleaning up..."
	@rm -rf bin/
	@rm -f *.snap
	@rm -rf data/
	@echo "Done."

# --- Proto Generation ---

.PHONY: proto-go proto-python proto

proto-go:
	@echo "Generating Go protobuf stubs..."
	@protoc --go_out=. --go-grpc_out=. proto/vectradb.proto
	@echo "Done."

proto-python:
	@echo "Generating Python gRPC stubs..."
	@mkdir -p ../cognee/cognee/infrastructure/databases/vector/vectradb/generated
	@mkdir -p ../cognee-plugin/vectradb_adapter/generated
	@python3 -m grpc_tools.protoc \
		-Iproto \
		--python_out=../cognee/cognee/infrastructure/databases/vector/vectradb/generated \
		--grpc_python_out=../cognee/cognee/infrastructure/databases/vector/vectradb/generated \
		proto/vectradb.proto
	@cp ../cognee/cognee/infrastructure/databases/vector/vectradb/generated/vectradb_pb2.py \
		../cognee-plugin/vectradb_adapter/generated/ 2>/dev/null || true
	@cp ../cognee/cognee/infrastructure/databases/vector/vectradb/generated/vectradb_pb2_grpc.py \
		../cognee-plugin/vectradb_adapter/generated/ 2>/dev/null || true
	@echo "Done."

proto: proto-go proto-python