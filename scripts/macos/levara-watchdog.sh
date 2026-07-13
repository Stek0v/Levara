#!/usr/bin/env bash
set -euo pipefail

# macOS Levara watchdog.
#
# Intended to run from launchd every 60-300 seconds. It checks:
#   - Levara /health/details dependency readiness;
#   - /api/v1/errors if the server exposes ErrorTracker records;
#   - new local log lines for panic/error patterns.
#
# Notifications are sent through macOS Notification Center via osascript.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

LEVARA_URL="${LEVARA_URL:-http://127.0.0.1:8081}"
LEVARA_LOG_PATH="${LEVARA_LOG_PATH:-$REPO_ROOT/data/logs/levara-local.log}"
LEVARA_WATCHDOG_STATE_DIR="${LEVARA_WATCHDOG_STATE_DIR:-$HOME/Library/Application Support/Levara/watchdog}"
LEVARA_WATCHDOG_COOLDOWN_SECONDS="${LEVARA_WATCHDOG_COOLDOWN_SECONDS:-900}"
LEVARA_WATCHDOG_SCAN_LINES="${LEVARA_WATCHDOG_SCAN_LINES:-400}"
LEVARA_WATCHDOG_NOTIFY_MODE="${LEVARA_WATCHDOG_NOTIFY_MODE:-notification}"

mkdir -p "$LEVARA_WATCHDOG_STATE_DIR"

now_epoch() {
  date +%s
}

state_file_for() {
  local key="$1"
  printf '%s/%s.state' "$LEVARA_WATCHDOG_STATE_DIR" "$(printf '%s' "$key" | tr -c 'A-Za-z0-9_.-' '_')"
}

notify() {
	local title="$1"
	local subtitle="$2"
	local message="$3"
	if ! command -v osascript >/dev/null 2>&1; then
		return
	fi
	if [[ "$LEVARA_WATCHDOG_NOTIFY_MODE" == "notification" || "$LEVARA_WATCHDOG_NOTIFY_MODE" == "both" ]]; then
		osascript - "$title" "$subtitle" "$message" <<'OSA' >/dev/null 2>&1 || true
on run argv
  display notification (item 3 of argv) with title (item 1 of argv) subtitle (item 2 of argv)
end run
OSA
	fi
	if [[ "$LEVARA_WATCHDOG_NOTIFY_MODE" == "dialog" || "$LEVARA_WATCHDOG_NOTIFY_MODE" == "both" ]]; then
		osascript - "$title" "$subtitle" "$message" <<'OSA' >/dev/null 2>&1 || true
on run argv
  display dialog ((item 2 of argv) & return & return & (item 3 of argv)) with title (item 1 of argv) buttons {"OK"} default button "OK" giving up after 20
end run
OSA
	fi
}

alert_once() {
  local key="$1"
  local subtitle="$2"
  local message="$3"
  local file
  file="$(state_file_for "$key")"
  local now last_ts last_sig sig
  now="$(now_epoch)"
  sig="$(printf '%s' "$subtitle|$message" | shasum -a 256 | awk '{print $1}')"
  last_ts=0
  last_sig=""
  if [[ -f "$file" ]]; then
    IFS=' ' read -r last_ts last_sig < "$file" || true
    last_ts="${last_ts:-0}"
    last_sig="${last_sig:-}"
  fi
  if (( now - last_ts >= LEVARA_WATCHDOG_COOLDOWN_SECONDS )) || [[ "$sig" != "$last_sig" ]]; then
    notify "Levara watchdog" "$subtitle" "$message"
    printf '%s %s\n' "$now" "$sig" > "$file"
  fi
}

http_get() {
  local url="$1"
  curl -fsS --max-time 5 "$url" 2>/dev/null || true
}

check_health() {
  local body degraded
  body="$(http_get "$LEVARA_URL/health/details")"
  if [[ -z "$body" ]]; then
    alert_once "health-unreachable" "Backend unreachable" "$LEVARA_URL/health/details did not respond"
    return
  fi
  degraded="$(printf '%s' "$body" | /usr/bin/python3 -c '
import json, sys
services = json.load(sys.stdin).get("services", {})
bad = {"error", "fail", "unavailable", "unreachable"}
items = [f"{name}={details.get('"'"'status'"'"')}" for name, details in services.items()
         if isinstance(details, dict) and details.get("status") in bad]
print(", ".join(sorted(items)))
' 2>/dev/null || echo "invalid health response")"
  if [[ -n "$degraded" ]]; then
    alert_once "health-degraded" "Backend degraded" "$degraded"
  fi
}

check_error_tracker() {
  local body total latest msg count ts key
  body="$(http_get "$LEVARA_URL/api/v1/errors?limit=5")"
  [[ -z "$body" ]] && return 0
  total="$(printf '%s' "$body" | /usr/bin/python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("total", 0))' 2>/dev/null || echo 0)"
  [[ "$total" =~ ^[0-9]+$ ]] || total=0
  (( total > 0 )) || return 0

  latest="$(printf '%s' "$body" | /usr/bin/python3 -c '
import json, sys
d = json.load(sys.stdin)
errs = d.get("errors") or []
if not errs:
    raise SystemExit
e = errs[0]
print("{}\t{}\t{}\t{}".format(e.get("timestamp",""), e.get("component","unknown"), e.get("count",1), e.get("message","")[:220]))
' 2>/dev/null || true)"
  [[ -n "$latest" ]] || return 0
  IFS=$'\t' read -r ts key count msg <<< "$latest"
  alert_once "error-tracker-$key" "Tracked error: $key" "count=$count at=$ts: $msg"
}

new_log_lines() {
  local log_path="$1"
  [[ -f "$log_path" ]] || return 0
  local state lines last lines_to_read
  state="$(state_file_for "log-lines")"
  lines="$(wc -l < "$log_path" | tr -d ' ')"
  last=0
  [[ -f "$state" ]] && read -r last < "$state" || true
  last="${last:-0}"
  if (( lines < last )); then
    last=0
  fi
  lines_to_read=$(( lines - last ))
  if (( lines_to_read <= 0 )); then
    printf '%s\n' "$lines" > "$state"
    return 0
  fi
  if (( lines_to_read > LEVARA_WATCHDOG_SCAN_LINES )); then
    tail -n "$LEVARA_WATCHDOG_SCAN_LINES" "$log_path"
  else
    sed -n "$((last + 1)),${lines}p" "$log_path"
  fi
  printf '%s\n' "$lines" > "$state"
}

check_log_file() {
  [[ -f "$LEVARA_LOG_PATH" ]] || return 0
  local matches first
  matches="$(new_log_lines "$LEVARA_LOG_PATH" | grep -Ei 'panic:|fatal|level":"ERROR"| ERROR |error=' || true)"
  [[ -n "$matches" ]] || return 0
  first="$(printf '%s\n' "$matches" | head -n 1 | tr '\n' ' ' | cut -c1-240)"
  alert_once "log-error" "Log error detected" "$first"
}

main() {
  check_health
  check_error_tracker
  check_log_file
}

main "$@"
