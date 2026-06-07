#!/usr/bin/env bash
#
# Profile smoke: run each non-enterprise preset through the server's
# `-config-check` dry run (no listeners, no DB, no network). Proves every
# shipped preset env is internally consistent for its tier before any deploy.
#
# Enterprise is intentionally excluded: its strict profile asserts corporate
# tenant/audit/SSO/storage facts that belong to a real deployment, not a local
# smoke. The team preset is strict and requires `-require-auth` (a CLI flag env
# cannot set), so this script passes that flag for team only — matching
# docs/profile-presets.md.
#
# Usage: make profile-smoke  (or: bash deploy/profiles/smoke.sh)
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
cd "$here/../.." # repository root / Go module root

bin="$(mktemp -t levara-smoke.XXXXXX)"
trap 'rm -f "$bin"' EXIT

echo "[smoke] building server..."
go build -o "$bin" ./cmd/server/

# resetProfileEnv clears every env var the profile-fact derivation reads so a
# preset sourced in one subshell never leaks into the next, and an ambient value
# from the operator's shell never colours the result.
reset_profile_env() {
	unset LEVARA_PROFILE LEVARA_PROFILE_STRICT DB_PROVIDER JWT_SECRET \
		LEVARA_TOKEN LEVARA_SYNC_REMOTE_URL LEVARA_TENANT_ENFORCED \
		LEVARA_WORKSPACE_AUDIT_EXPORT LEVARA_SSO_BRIDGE 2>/dev/null || true
}

# check NAME ENV_FILE [extra config-check args...]
# Runs config-check in an isolated subshell with only the preset env loaded.
check() {
	local name="$1" file="$2"
	shift 2
	echo "[smoke] $name ($file)"
	(
		reset_profile_env
		set -a
		# shellcheck disable=SC1090
		source "deploy/profiles/$file"
		set +a
		"$bin" -config-check "$@"
	)
}

fail=0
check personal personal.local.env.example || fail=1
check solo_pro solo_pro.sync.env.example || fail=1
check team team.postgres.env.example -require-auth || fail=1

if [ "$fail" -ne 0 ]; then
	echo "[smoke] FAILED: a preset did not pass config-check" >&2
	exit 1
fi
echo "[smoke] personal/solo_pro/team presets pass config-check"
