#!/bin/bash
set -euo pipefail

# Prepared offline workflow. Set LEVARA_BACKUP_QUIESCE=1 explicitly to stop an
# active local service, and configure its exact inventory in backup.env.
# Never install/enable this script or timer without choosing a downtime window.
: "${LEVARA_BACKUP_NODE_ID:?Set the exact running server node ID}"
: "${LEVARA_BACKUP_SHARDS:?Set the exact running server shard count}"
: "${LEVARA_BACKUP_DIM:?Set the exact running server default vector dimension}"
: "${DB_PROVIDER:?Set sqlite or postgres}"
if [[ "${LEVARA_BACKUP_STANDALONE:-}" != "true" ]]; then
    echo 'backup: only an explicitly configured standalone local node is supported' >&2
    exit 1
fi

backup_dir="${1:-${LEVARA_BACKUP_DIR:-/var/backups/levara}}"
data_dir="${LEVARA_DATA_DIR:-/var/lib/levara/data}"
service_name="${LEVARA_BACKUP_SERVICE:-levara.service}"
backup_user="${LEVARA_BACKUP_USER:-levara}"
backup_bin="${LEVARA_BACKUP_BIN:-/usr/local/bin/levara-backup}"
was_active=0

finish() {
    result=$?
    trap - EXIT
    if [[ "$was_active" == 1 ]]; then
        if ! systemctl start "$service_name"; then
            echo 'backup: failed to restart the previously active service' >&2
            result=1
        fi
    fi
    exit "$result"
}
trap finish EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

if [[ "${LEVARA_BACKUP_QUIESCE:-0}" == 1 ]]; then
    if [[ "$(id -u)" != 0 ]]; then
        echo 'backup: service quiescence requires running this prepared script as root' >&2
        exit 1
    fi
    if systemctl is-active --quiet "$service_name"; then
        was_active=1
        systemctl stop "$service_name"
    fi
fi

args=(verified --standalone=true --data-dir "$data_dir"
      --node-id "$LEVARA_BACKUP_NODE_ID" --shards "$LEVARA_BACKUP_SHARDS"
      --dim "$LEVARA_BACKUP_DIM" --db-provider "$DB_PROVIDER"
      --output-dir "$backup_dir" --timeout "${LEVARA_BACKUP_TIMEOUT:-15m}")
[[ -z "${LEVARA_WORKSPACE_PATH:-}" ]] || args+=(--workspace-path "$LEVARA_WORKSPACE_PATH")
[[ -z "${LEVARA_UPLOADS_PATH:-}" ]] || args+=(--uploads-path "$LEVARA_UPLOADS_PATH")
[[ -z "${LEVARA_SQLITE_PATH:-}" ]] || args+=(--sqlite-path "$LEVARA_SQLITE_PATH")
[[ -z "${LEVARA_POSTGRES_BIN_DIR:-}" ]] || args+=(--postgres-bin-dir "$LEVARA_POSTGRES_BIN_DIR")

# initdb must run unprivileged. Credentials stay in the inherited environment;
# backup receipts contain inventory/proof only, never a copied environment file.
if [[ "$(id -u)" == 0 ]]; then
    install -d -m 0700 -o "$backup_user" -g "$backup_user" "$backup_dir"
    runuser --preserve-environment -u "$backup_user" -- "$backup_bin" "${args[@]}"
else
    "$backup_bin" "${args[@]}"
fi
# Keep every immutable successful archive. Configure retention separately only
# after proving off-host durability; a failed run never advances last success.
