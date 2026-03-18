.PHONY: help up down full-stack full-stack-down build test benchmark proto clean install-hook qv-stack qv-stack-down qv-stack-logs

# Default
help:
	@echo "Cognee + VectraDB Development Commands"
	@echo ""
	@echo "  make up              Start VectraDB + Prometheus (dev mode)"
	@echo "  make down            Stop dev services"
	@echo "  make full-stack      Start all services (Cognee, VectraDB, PG, Neo4j, Redis)"
	@echo "  make full-stack-down Stop all full-stack services"
	@echo "  make build           Build all Docker images"
	@echo "  make test            Run VectraDB adapter tests"
	@echo "  make benchmark       Run VectraDB vs LanceDB benchmark"
	@echo "  make install-hook    Install Cognee git post-commit hook"
	@echo "  make clean           Remove data volumes and temp files"
	@echo ""
	@echo "QV Mode (local llama-server + full stack):"
	@echo "  make qv-stack        Start all services for qv mode (requires qv running on :9004)"
	@echo "  make qv-stack-down   Stop qv-stack services"
	@echo "  make qv-stack-logs   Follow Cognee logs (qv-stack)"
	@echo ""
	@echo "Configuration:"
	@echo "  cp .env.template .env   # then edit .env with your API keys"

# --- Dev Mode (VectraDB only) ---

up:
	docker compose up -d --build

down:
	docker compose down

# --- Full Stack ---

full-stack:
	@if [ ! -f .env ]; then echo "Error: .env not found. Run: cp .env.template .env"; exit 1; fi
	docker compose -f docker-compose.full-stack.yml up -d --build

full-stack-down:
	docker compose -f docker-compose.full-stack.yml down

# --- Build ---

build:
	docker compose -f docker-compose.full-stack.yml build

# --- Tests ---

test:
	cd tests && python -m pytest -v

benchmark:
	cd benchmarks && python vectradb_vs_lancedb.py

# --- Proto Generation ---

proto:
	$(MAKE) -C VectraDB proto

# --- Git Hook ---

install-hook:
	bash tools/install-cognee-hook.sh

# --- QV Mode (local llama-server + full stack) ---

qv-stack:
	@if ! curl -sf http://localhost:9004/health > /dev/null 2>&1; then \
		echo "Error: qv (llama-server) not running on port 9004. Run 'qv' first."; exit 1; fi
	docker compose -f docker-compose.qv.yml up -d --build

qv-stack-down:
	docker compose -f docker-compose.qv.yml down

qv-stack-logs:
	docker compose -f docker-compose.qv.yml logs -f cognee

# --- Cleanup ---

clean:
	docker compose -f docker-compose.full-stack.yml down -v 2>/dev/null || true
	docker compose down -v 2>/dev/null || true
	rm -rf VectraDB/data/ VectraDB/bin/ VectraDB/*.snap
