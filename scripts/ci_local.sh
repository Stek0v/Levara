#!/usr/bin/env bash
# Local CI-equivalent runner: reproduces the pull_request gate (go-ci jobs +
# lint-prom-rules + secrets-scan) without consuming GitHub Actions capacity.
#
# Version manifest (parity with .github/workflows/go-ci.yml):
#   golangci-lint v2.12.2, gitleaks v8.24.3, promtool v2.55.1,
#   govulncheck latest, node 22, python >= 3.12 (best effort).
# Deviations from CI: test shards run as one merged pass (same coverage);
# Playwright browsers are already installed locally; postgres databases are
# local throwaways (created on demand) instead of service containers.
#
# Usage:
#   bash scripts/ci_local.sh                    # full run
#   bash scripts/ci_local.sh --only lint,vet_build
#   bash scripts/ci_local.sh --list
# Env overrides: LEVARA_TEST_POSTGRES_DSN, LEVARA_ENTERPRISE_E2E_DSN,
#   LEVARA_TASK_LOAD_DSN, LEVARA_CI_LOCAL_OUT.
set -uo pipefail

REPO_ROOT=$(git rev-parse --show-toplevel) || exit 1
cd "$REPO_ROOT"
OUT=${LEVARA_CI_LOCAL_OUT:-/tmp/levara-ci-local}
TOOLS="$HOME/.cache/levara-ci-tools/bin"
mkdir -p "$OUT" "$TOOLS"
GOLANGCI_VERSION=v2.12.2
GITLEAKS_VERSION=v8.24.3
PROM_VERSION=2.55.1
NODE_MAJOR=22

RESULTS=()   # "name=PASS|FAIL|SKIP"
ONLY=""
while [ $# -gt 0 ]; do
  case "$1" in
    --only) ONLY="$2"; shift 2 ;;
    --list) grep -oE "^run_job [a-z0-9_-]+" "$0" | awk '{print $2}'; exit 0 ;;
    *) echo "unknown flag: $1" >&2; exit 2 ;;
  esac
done

want() { [ -z "$ONLY" ] || printf '%s\n' "$ONLY" | tr ',' '\n' | grep -qx "$1"; }

run_job() {
  local name=$1; shift
  want "$name" || { RESULTS+=("$name=SKIP"); return 0; }
  local log="$OUT/$name.log"
  echo "== $name =="
  if "$@" >"$log" 2>&1; then
    RESULTS+=("$name=PASS"); tail -n 2 "$log" | sed 's/^/   /'
  else
    RESULTS+=("$name=FAIL"); echo "   FAILED — tail of $log:"; tail -n 12 "$log" | sed 's/^/   /'
  fi
}

ensure_db() {
  local db=$1
  psql -h localhost -U "$(whoami)" -d postgres -tAc "SELECT 1 FROM pg_database WHERE datname='$db'" 2>/dev/null | grep -q 1 \
    || createdb -h localhost -U "$(whoami)" "$db"
  printf 'postgres://%s@localhost:5432/%s?sslmode=disable' "$(whoami)" "$db"
}

# --- pinned tool bootstrap (installed once into $TOOLS) ----------------------
if [ ! -x "$TOOLS/golangci-lint" ]; then
  echo "installing golangci-lint ${GOLANGCI_VERSION} ..." >&2
  GOBIN="$TOOLS" go install "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@${GOLANGCI_VERSION}" >&2
fi
if [ ! -x "$TOOLS/gitleaks" ]; then
  echo "installing gitleaks ${GITLEAKS_VERSION} ..." >&2
  GOBIN="$TOOLS" go install "github.com/zricethezav/gitleaks/v8@${GITLEAKS_VERSION}" >&2
fi
if [ ! -x "$TOOLS/govulncheck" ]; then
  echo "installing govulncheck ..." >&2
  GOBIN="$TOOLS" go install golang.org/x/vuln/cmd/govulncheck@latest >&2
fi
if [ ! -x "$TOOLS/promtool" ]; then
  OS=$(uname -s | tr '[:upper:]' '[:lower:]'); ARCH=$(uname -m | sed s/x86_64/amd64/)
  echo "installing promtool ${PROM_VERSION} (${OS}-${ARCH}) ..." >&2
  curl -sSL "https://github.com/prometheus/prometheus/releases/download/v${PROM_VERSION}/prometheus-${PROM_VERSION}.${OS}-${ARCH}.tar.gz" \
    | tar -xz --strip-components=1 -C "$TOOLS" "prometheus-${PROM_VERSION}.${OS}-${ARCH}/promtool" >&2
fi
GOLANGCI="$TOOLS/golangci-lint"; GITLEAKS="$TOOLS/gitleaks"
GOVULN="$TOOLS/govulncheck"; PROMTOOL="$TOOLS/promtool"

NODE=""
if command -v fnm >/dev/null 2>&1 && fnm list 2>/dev/null | grep -q "v${NODE_MAJOR}"; then
  NODE="fnm exec --using ${NODE_MAJOR}"
else
  echo "WARN: node ${NODE_MAJOR} not found via fnm; using $(node --version 2>/dev/null || echo none)" >&2
fi
PY="python3"
command -v pyenv >/dev/null 2>&1 && pyenv versions --bare 2>/dev/null | grep -q "^3.12" && PY="PYENV_VERSION=3.12 python3"
echo "tools: node=$($NODE --version 2>/dev/null || echo none) python=$($PY --version 2>&1)"

# Throwaway databases are prepared in the outer shell so job commands stay
# plain executables (functions do not survive into the job subshells).
DSN_CI=${LEVARA_TEST_POSTGRES_DSN:-$(ensure_db levara_ci)}
DSN_E2E=${LEVARA_ENTERPRISE_E2E_DSN:-$(ensure_db levara_enterprise_e2e)}
DSN_TASK=${LEVARA_TASK_LOAD_DSN:-$(ensure_db levara_taskload)}
PGUSER_LOCAL=$(whoami)

# --- jobs (names mirror branch-protection contexts where 1:1) -----------------
run_job vet_build bash -c 'go vet ./... && go build ./...'

run_job golangci-lint "$GOLANGCI" run --timeout=5m

run_job test bash -c "go list ./... | grep -v '/webui/node_modules/' | grep -v '/internal/http\$' | xargs go test -count=1"

run_job http-test go test -count=1 ./internal/http

run_job targeted-race bash -c 'go test -race -count=1 ./pkg/audit ./pkg/memoryindex ./pkg/mcp ./internal/store ./pkg/vectorstore ./pkg/bm25 ./pkg/router ./pkg/workspace && go test -race -count=1 ./internal/http -run "Audit|MemoryBehavior|MCP.*Auth|MCP.*Session|Tenant.*Isolation|Outbox|Consolidation"'

run_job architecture-contract make contract-check

run_job release-artifact-shape bash -c "make release-artifact >/dev/null && mv levara-release.tar.gz '$OUT/'"

run_job reachable-vulnerabilities "$GOVULN" ./...

run_job memory-behavior-eval bash -c "$PY -m py_compile benchmark/memory_behavior_eval/run_memory_behavior_eval.py && $PY benchmark/memory_behavior_eval/run_memory_behavior_eval.py --fake --label ci-local --output '$OUT/memory_behavior_eval.json' >/dev/null"

run_job webui bash -c '
  set -e; cd webui
  '"$NODE"' npm ci >/dev/null
  '"$NODE"' npm run lint
  LEVARA_API_URL=http://127.0.0.1:8081 '"$NODE"' npm run build >/dev/null
  '"$NODE"' npx playwright install chromium >/dev/null 2>&1
  '"$NODE"' npm run test:e2e
  '"$NODE"' npm audit --omit=dev --audit-level=high'

run_job postgres-audit-outbox env LEVARA_TEST_POSTGRES_DSN="$DSN_CI" go test -count=1 ./pkg/audit ./pkg/memoryindex ./pkg/mcp -run Postgres

run_job enterprise-e2e env LEVARA_ENTERPRISE_E2E_DSN="$DSN_E2E" LEVARA_ENTERPRISE_E2E_PGUSER="$PGUSER_LOCAL" bash deploy/profiles/enterprise_e2e.sh

run_job task-load-gate env LEVARA_TASK_LOAD_DSN="$DSN_TASK" LEVARA_TASK_LOAD_PGUSER="$PGUSER_LOCAL" bash benchmark/task_load_gate.sh

run_job compose-configuration bash -c '
  set -e
  docker compose -f docker-compose.yml config --quiet
  docker compose -f docker-compose.qwen-stack.yml config --quiet
  docker compose -f docker-compose.pi.yml config --quiet
  docker compose -f docker-compose.yml -f docker-compose.qwen3.yml config --quiet
  docker compose -f deploy/docker/docker-compose.yml config --quiet'

run_job memory-auth-isolation bash -c '
  set -e
  go test -count=1 ./internal/http ./pkg/mcp -run "Auth|Owner|Isolation|APIKey|Session"
  go test -count=1 ./pkg/memoryindex ./pkg/mcp -run "Outbox|RecoverRunning|DeleteMemory"
  go test -race -count=1 ./internal/http -run "MCP.*Auth|MCP.*Session|Tenant.*Isolation"'

run_job privacy-smoke bash -c '
  set -e
  go test -count=1 ./pkg/audit -run "^(TestSanitizeArgsDropsSecrets|TestSanitizeArgsCollapsesVectors|TestSanitizeArgsTruncatesLongStrings|TestSanitizeArgsMarshalsNested|TestSanitizeArgsDropsNestedSecrets|TestSanitizeEventScrubsLeaks)$"
  go test -count=1 ./internal/http -run "^(TestAgentTrajectoriesFiltersPaginationAndAdminArgs|TestMemoryReviewPromptOmitsRawArgsAndParser|TestMemoryTraceExportStreamsGoodSanitizedJSONL|TestMemoryTraceExportEmptyAndAdminRequired|TestMemoryTraceExportRejectsUnsupportedQuality|TestMemoryScaffoldDecisionRequiresAdmin|TestRecordMCPAuditSanitizesArgs)$"'

run_job promtool-rules "$PROMTOOL" check rules docs/prometheus-verify.rules.yml

run_job gitleaks-scan "$GITLEAKS" git . --no-banner --redact --config .gitleaks.toml

# --- summary -----------------------------------------------------------------
echo
echo "==================== SUMMARY (logs: $OUT) ===================="
fail=0; skipped=0
for r in "${RESULTS[@]}"; do
  name=${r%%=*}; status=${r##*=}
  printf '%-28s %s\n' "$name" "$status"
  case "$status" in
    PASS) ;;
    SKIP) skipped=$((skipped+1)) ;;
    *) fail=$((fail+1)) ;;
  esac
done
[ "$skipped" -gt 0 ] && echo "($skipped skipped by --only)"
[ "$fail" -eq 0 ] && echo "CI-LOCAL: PASS" || echo "CI-LOCAL: $fail job(s) failed"
exit $(( fail > 0 ? 1 : 0 ))
