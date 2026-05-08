.PHONY: help up down full-stack full-stack-down build test benchmark proto clean install-hook qv-stack qv-stack-down qv-stack-logs stack-dev stack-dev-down stack-dev-logs stack-dev-reset

# Default
help:
	@echo "Cognee + Levara Development Commands"
	@echo ""
	@echo "  make up              Start Levara + Prometheus (dev mode)"
	@echo "  make down            Stop dev services"
	@echo "  make full-stack      Start all services (Cognee, Levara, PG, Neo4j, Redis)"
	@echo "  make full-stack-down Stop all full-stack services"
	@echo "  make build           Build all Docker images"
	@echo "  make test            Run Levara adapter tests"
	@echo "  make benchmark       Run Levara vs LanceDB benchmark"
	@echo "  make install-hook    Install Cognee git post-commit hook"
	@echo "  make clean           Remove data volumes and temp files"
	@echo ""
	@echo "LevaraOS unified stack (Levara + MemoryFS + mem0 + Ollama + PG):"
	@echo "  make stack-dev       One-command bootstrap: up + wait-for-health + pull embed model"
	@echo "  make stack-dev-down  Stop the LevaraOS stack"
	@echo "  make stack-dev-logs  Follow Levara logs"
	@echo "  make stack-dev-reset Destroy volumes + reboot (use after Cognee-era upgrade)"
	@echo ""
	@echo "QV Mode (local llama-server + full stack):"
	@echo "  make qv-stack        Start all services for qv mode (requires qv running on :9004)"
	@echo "  make qv-stack-down   Stop qv-stack services"
	@echo "  make qv-stack-logs   Follow Cognee logs (qv-stack)"
	@echo ""
	@echo "Configuration:"
	@echo "  cp .env.template .env   # then edit .env with your API keys"

# --- Dev Mode (Levara only) ---

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
	$(MAKE) -C Levara proto

# --- Git Hook ---

install-hook:
	bash tools/install-cognee-hook.sh

# --- LevaraOS Stack (one-command dev) ---

stack-dev:
	@bash tools/stack-dev-up.sh

stack-dev-down:
	docker compose -f docker-compose.levaraos.yml down

stack-dev-logs:
	docker compose -f docker-compose.levaraos.yml logs -f levara

# Wipe the LevaraOS postgres + levara volumes and bring the stack back up.
# Use after upgrading from an older (Cognee-era) volume whose Postgres user
# does not match the current credentials. DESTRUCTIVE — Postgres metadata
# (memories, datasets, feedback) and Levara's persisted vectors are deleted.
stack-dev-reset:
	@echo "About to destroy LevaraOS volumes (postgres-data, levara-data, ...)."
	@read -p "Type 'yes' to confirm: " ans && [ "$$ans" = "yes" ] || (echo "aborted"; exit 1)
	docker compose -f docker-compose.levaraos.yml down -v
	@bash tools/stack-dev-up.sh

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
	rm -rf Levara/data/ Levara/bin/ Levara/*.snap
