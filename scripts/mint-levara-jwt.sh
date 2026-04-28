#!/bin/bash
# mint-levara-jwt.sh — issue a service JWT via Levara /auth/login
#
# Usage:
#   bash scripts/mint-levara-jwt.sh [subject]
#
# Examples:
#   bash scripts/mint-levara-jwt.sh picoclaw
#
# The subject maps to <subject>@service.local in Levara's DB. If the account
# doesn't exist yet, it will be registered with a random password on first run
# (the password is stored in $HOME/.config/levara-service-keys/<subject>.key).
#
# Env overrides:
#   LEVARA_URL=http://localhost:8080   Levara HTTP API base URL
#   LEVARA_ADMIN_USER=admin            Admin username for registration bootstrap
#   LEVARA_ADMIN_PASS=...              Admin password

set -euo pipefail

SUBJECT="${1:-picoclaw}"
SERVICE_EMAIL="${SUBJECT}@service.local"
LEVARA_URL="${LEVARA_URL:-http://localhost:8080}"
KEY_DIR="$HOME/.config/levara-service-keys"
KEY_FILE="$KEY_DIR/${SUBJECT}.key"

die() { echo "ERROR: $*" >&2; exit 1; }

mkdir -p "$KEY_DIR"
chmod 700 "$KEY_DIR"

# Generate or load service account password
if [[ ! -f "$KEY_FILE" ]]; then
  SERVICE_PASS=$(openssl rand -hex 24)
  echo "$SERVICE_PASS" > "$KEY_FILE"
  chmod 600 "$KEY_FILE"

  # Register the service account in Levara
  REG_RESP=$(curl -sf -X POST "$LEVARA_URL/auth/register" \
    -H "Content-Type: application/json" \
    -d "{\"password\":\"${SERVICE_PASS}\",\"email\":\"${SERVICE_EMAIL}\"}" \
    2>&1) || {
    # Registration might fail if user already exists — that's fine
    warn_msg="Registration returned error (user may exist): $REG_RESP"
    echo "⚠ $warn_msg" >&2
  }
fi

SERVICE_PASS=$(cat "$KEY_FILE")

# Login and get JWT
LOGIN_RESP=$(curl -sf -X POST "$LEVARA_URL/auth/login" \
  -H "Content-Type: application/json" \
  -d "{\"username\":\"${SERVICE_EMAIL}\",\"password\":\"${SERVICE_PASS}\"}" \
  2>&1) || die "Login failed for $SUBJECT at $LEVARA_URL: $LOGIN_RESP"

# Extract token from response
# Levara login returns: {"access_token":"...","token_type":"bearer",...}
TOKEN=$(echo "$LOGIN_RESP" | python3 -c "
import json, sys
resp = json.load(sys.stdin)
token = resp.get('access_token') or resp.get('token') or resp.get('jwt')
if not token:
    raise ValueError('No token in response: ' + str(list(resp.keys())))
print(token)
" 2>&1) || die "Could not parse token from: $LOGIN_RESP"

# Print token to stdout (caller captures it)
echo "$TOKEN"
