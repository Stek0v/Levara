# macOS Levara watchdog

Date: 2026-06-22
Status: local operations helper

This watchdog notifies through macOS Notification Center when the local Levara
backend becomes unreachable, reports tracked errors, or writes panic/error lines
to the configured local log file.

## What it checks

- `GET $LEVARA_URL/health`
- `GET $LEVARA_URL/api/v1/errors?limit=5`
- new lines in `$LEVARA_LOG_PATH` matching `panic:`, `fatal`, JSON
  `"level":"ERROR"`, ` ERROR `, or `error=`

The script keeps state under:

```text
~/Library/Application Support/Levara/watchdog
```

It deduplicates notifications with `LEVARA_WATCHDOG_COOLDOWN_SECONDS`, default
`900` seconds.

By default the script uses Notification Center. Set
`LEVARA_WATCHDOG_NOTIFY_MODE=both` to also show a visible AppleScript dialog
that closes automatically after 20 seconds. This is useful when macOS accepts
`display notification` but hides it because of Focus, Notification Center
permissions, or the sending app identity.

## Manual run

```bash
LEVARA_URL=http://127.0.0.1:8081 \
LEVARA_LOG_PATH="$PWD/data/logs/levara-local.log" \
scripts/macos/levara-watchdog.sh
```

If Notification Center permissions are missing, macOS may prompt for permission
for Script Editor/osascript notifications.

## Install with launchd

```bash
mkdir -p "$HOME/Library/LaunchAgents" "$HOME/Library/Logs"

sed \
  -e "s#__REPO_ROOT__#$PWD#g" \
  -e "s#__HOME__#$HOME#g" \
  scripts/macos/com.stek0v.levara-watchdog.plist \
  > "$HOME/Library/LaunchAgents/com.stek0v.levara-watchdog.plist"

launchctl unload "$HOME/Library/LaunchAgents/com.stek0v.levara-watchdog.plist" 2>/dev/null || true
launchctl load "$HOME/Library/LaunchAgents/com.stek0v.levara-watchdog.plist"
launchctl kickstart -k "gui/$(id -u)/com.stek0v.levara-watchdog"
```

## Check status

```bash
launchctl print "gui/$(id -u)/com.stek0v.levara-watchdog"
tail -100 "$HOME/Library/Logs/levara-watchdog.err.log"
```

## Uninstall

```bash
launchctl unload "$HOME/Library/LaunchAgents/com.stek0v.levara-watchdog.plist" 2>/dev/null || true
rm -f "$HOME/Library/LaunchAgents/com.stek0v.levara-watchdog.plist"
```

## Configuration

Set these in the plist or environment:

| Variable | Default | Meaning |
|---|---|---|
| `LEVARA_URL` | `http://127.0.0.1:8081` | Local Levara backend URL |
| `LEVARA_LOG_PATH` | repo `data/logs/levara-local.log` | Local server stderr/stdout log to scan |
| `LEVARA_WATCHDOG_STATE_DIR` | `~/Library/Application Support/Levara/watchdog` | State and dedupe files |
| `LEVARA_WATCHDOG_COOLDOWN_SECONDS` | `900` | Minimum seconds before repeating the same alert |
| `LEVARA_WATCHDOG_SCAN_LINES` | `400` | Max new log lines to scan per run after a large jump |
| `LEVARA_WATCHDOG_NOTIFY_MODE` | `notification` | `notification`, `dialog`, or `both` |

For a server launched by launchd with logs in `~/Library/Logs`, point
`LEVARA_LOG_PATH` at that stderr or combined log file. For a systemd/Linux host,
use the Raspberry monitor or journal tooling instead.
