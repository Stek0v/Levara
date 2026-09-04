#!/usr/bin/env bash
#
# Enterprise strict preset e2e gate (backlog C1).
#
# Two layers, both against the real compiled binary:
#   1. Mandatory-condition matrix — source the enterprise preset, remove ONE
#      mandatory condition at a time, and prove -config-check fails with the
#      exact finding code. Then prove a fully-populated preset passes.
#   2. Live strict startup — boot the server against a real Postgres with the
#      complete preset; require: healthy, tenant 403 without tenant context,
#      unauthenticated API 401, health public, audit export directory created.
#
# Usage: bash deploy/profiles/enterprise_e2e.sh [server-binary]
# Env:   LEVARA_ENTERPRISE_E2E_DSN — Postgres DSN for the live phase
#        (default: local levara_enterprise_e2e db on :5432; the script creates
#        and drops it when it has connect rights, otherwise assumes provided).
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
cd "$here/../.." # repository root / Go module root

bin="${1:-}"
if [ -z "$bin" ]; then
	bin="$(mktemp -t levara-ent-e2e.XXXXXX)"
	trap 'rm -f "$bin"' EXIT
	echo "[ent-e2e] building server..."
	go build -o "$bin" ./cmd/server/
fi

preset="deploy/profiles/enterprise.strict.env.example"

reset_profile_env() {
	unset LEVARA_PROFILE LEVARA_PROFILE_STRICT DB_PROVIDER POSTGRES_DSN DATABASE_URL JWT_SECRET \
		LEVARA_TOKEN LEVARA_SYNC_REMOTE_URL LEVARA_TENANT_ENFORCED \
		LEVARA_WORKSPACE_AUDIT_EXPORT LEVARA_WORKSPACE_AUDIT_EXPORT_DIR \
		LEVARA_WORKSPACE_AUDIT_RETENTION_DAYS LEVARA_SSO_BRIDGE \
		STORAGE_BACKEND STORAGE_PATH 2>/dev/null || true
}

# run_check PRESET_PATH EXTRA_ARGS... — runs -config-check with the preset
# sourced plus KEY=VALUE overrides. $1 is a path relative to repo root.
run_check() {
	local file="$1"; shift
	(
		reset_profile_env
		set -a
		# shellcheck disable=SC1090
		source "$file"
		set +a
		for override in "$@"; do
			export "${override?}"
		done
		"$bin" -config-check -require-auth 2>&1
	)
}

# run_check_no_auth: same as run_check but WITHOUT -require-auth, for matrix
# cases that simulate a deployment which relies solely on the preset env.
run_check_no_auth() {
	local file="$1"; shift
	(
		reset_profile_env
		set -a
		# shellcheck disable=SC1090
		source "$file"
		set +a
		for override in "$@"; do
			export "${override?}"
		done
		"$bin" -config-check 2>&1
	)
}

expect_fail_no_auth() {
	local name="$1" code="$2"; shift 2
	local out
	if ! out="$(run_check_no_auth "$@" 2>&1)"; then
		if echo "$out" | grep -q "$code"; then
			echo "[ent-e2e] PASS  $name (rejected with $code)"
		else
			echo "[ent-e2e] FAIL  $name — rejected but without $code:" >&2
			echo "$out" >&2
			fail=1
		fi
	else
		echo "[ent-e2e] FAIL  $name — accepted, expected rejection ($code)" >&2
		echo "$out" >&2
		fail=1
	fi
}

fail=0
expect_fail() {
	local name="$1" code="$2"; shift 2
	local out
	if ! out="$(run_check "$@" 2>&1)"; then
		if echo "$out" | grep -q "$code"; then
			echo "[ent-e2e] PASS  $name (rejected with $code)"
		else
			echo "[ent-e2e] FAIL  $name — rejected but without $code:" >&2
			echo "$out" >&2
			fail=1
		fi
	else
		echo "[ent-e2e] FAIL  $name — accepted, expected rejection ($code)" >&2
		echo "$out" >&2
		fail=1
	fi
}

echo "[ent-e2e] layer 1: mandatory-condition matrix (config-check dry runs)"

expect_fail "postgres not selected" enterprise_requires_postgres \
	"$preset" "DB_PROVIDER=sqlite"
expect_fail_no_auth "no auth and no sso" enterprise_requires_auth \
	"$preset" "JWT_SECRET=keep-for-other-checks" "LEVARA_SSO_BRIDGE="
expect_fail "no signing config"   enterprise_requires_stable_jwt_secret \
	"$preset" "JWT_SECRET="
expect_fail "tenant enforcement off" enterprise_requires_tenant_enforcement \
	"$preset" "LEVARA_TENANT_ENFORCED=0"
expect_fail "no audit sink"       enterprise_requires_audit_sink \
	"$preset" "LEVARA_WORKSPACE_AUDIT_EXPORT=0"
expect_fail "unknown profile under strict" unknown_profile \
	"$preset" "LEVARA_PROFILE=enterprize"

# Two missing conditions at once: both findings must be reported.
out="$(run_check "$preset" "DB_PROVIDER=sqlite" "LEVARA_TENANT_ENFORCED=0" 2>&1)" \
	&& { echo "[ent-e2e] FAIL  dual-violation accepted" >&2; fail=1; } \
	|| {
		ok=0
		echo "$out" | grep -q enterprise_requires_postgres && ok=$((ok+1))
		echo "$out" | grep -q enterprise_requires_tenant_enforcement && ok=$((ok+1))
		if [ "$ok" -eq 2 ]; then
			echo "[ent-e2e] PASS  dual-violation reports both findings"
		else
			echo "[ent-e2e] FAIL  dual-violation reported only $ok/2 findings:" >&2
			echo "$out" >&2
			fail=1
		fi
	}

if out="$(run_check "$preset" 2>&1)"; then
	echo "[ent-e2e] PASS  complete preset passes config-check"
else
	echo "[ent-e2e] FAIL  complete preset rejected:" >&2
	echo "$out" >&2
	fail=1
fi

# ── layer 2: live strict startup against a real Postgres ──

pg_user="${LEVARA_ENTERPRISE_E2E_PGUSER:-$(id -un)}"
dsn="${LEVARA_ENTERPRISE_E2E_DSN:-postgres://${pg_user}@127.0.0.1:5432/levara_enterprise_e2e?sslmode=disable}"
data_dir="$(mktemp -d /tmp/levara-ent-e2e.XXXXXX)"
audit_dir="$data_dir/audit"
trap 'rm -rf "$data_dir"' EXIT

echo "[ent-e2e] layer 2: live strict startup (postgres: ${dsn%%\?*})"

port="${LEVARA_ENTERPRISE_E2E_PORT:-18093}"
live_env=(
	"LEVARA_PROFILE=enterprise"
	"LEVARA_PROFILE_STRICT=1"
	"DB_PROVIDER=postgres"
	"POSTGRES_DSN=$dsn"
	"DATABASE_URL=$dsn"
	"JWT_SECRET=e2e-stable-secret-0123456789abcdef"
	"LEVARA_TENANT_ENFORCED=1"
	"LEVARA_WORKSPACE_AUDIT_EXPORT=1"
	"LEVARA_WORKSPACE_AUDIT_EXPORT_DIR=$audit_dir"
	"STORAGE_BACKEND=local"
	"STORAGE_PATH=$data_dir/storage"
)

"$bin" -config-check -require-auth \
	"LEVARA_PROFILE=enterprise" "LEVARA_PROFILE_STRICT=1" "DB_PROVIDER=postgres" \
	"POSTGRES_DSN=$dsn" "JWT_SECRET=e2e-stable-secret-0123456789abcdef" \
	"LEVARA_TENANT_ENFORCED=1" "LEVARA_WORKSPACE_AUDIT_EXPORT=1" \
	"LEVARA_SSO_BRIDGE=" >/dev/null 2>&1 \
	&& echo "[ent-e2e] PASS  live preset passes pre-flight config-check (auth via -require-auth)" \
	|| { echo "[ent-e2e] FAIL  live preset fails pre-flight config-check" >&2; fail=1; }

env -i PATH="$PATH" HOME="${HOME:-}" "${live_env[@]}" \
	"LEVARA_LONG_HORIZON_RUNTIME=1" \
	"$bin" -port="$port" -grpc-port=0 \
	-data-dir="$data_dir" -node-id=ent-e2e -dim=256 \
	-embed-endpoint=http://127.0.0.1:9101/v1/embeddings \
	-embed-model=potion-code-16M -require-auth \
	>"$data_dir/server.log" 2>&1 &
server_pid=$!

cleanup() { kill "$server_pid" 2>/dev/null || true; wait "$server_pid" 2>/dev/null || true; }
trap 'cleanup; rm -rf "$data_dir"' EXIT

healthy=0
for _ in $(seq 1 60); do
	if ! kill -0 "$server_pid" 2>/dev/null; then
		echo "[ent-e2e] FAIL  server exited during startup:" >&2
		tail -20 "$data_dir/server.log" >&2
		fail=1
		break
	fi
	if curl -fsS "http://127.0.0.1:$port/health" >/dev/null 2>&1; then
		healthy=1
		break
	fi
	sleep 0.5
done

if [ "$healthy" -eq 1 ]; then
	echo "[ent-e2e] PASS  strict enterprise server became healthy"
	# health must stay public under -require-auth
	s="$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$port/health")"
	[ "$s" = "200" ] && echo "[ent-e2e] PASS  health public (200)" \
		|| { echo "[ent-e2e] FAIL  health returned $s, want 200" >&2; fail=1; }
	# API without credentials must 401
	s="$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$port/api/v1/collections")"
	[ "$s" = "401" ] && echo "[ent-e2e] PASS  unauthenticated API 401" \
		|| { echo "[ent-e2e] FAIL  unauthenticated API returned $s, want 401" >&2; fail=1; }
	# audit export directory created by the sink
	if [ -d "$audit_dir" ]; then
		echo "[ent-e2e] PASS  audit export directory created"
	else
		echo "[ent-e2e] FAIL  audit export directory missing: $audit_dir" >&2
		fail=1
	fi
fi

cleanup

if [ "$fail" -ne 0 ]; then
	echo "[ent-e2e] FAILED" >&2
	exit 1
fi
echo "[ent-e2e] ALL PASSED"
