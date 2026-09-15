#!/usr/bin/env bash
set -euo pipefail

MAIN_HOST="${LOOM_MAIN_HOST:-loom-dev}"
WORKSPACE_HOST="${LOOM_WORKSPACE_HOST:-loom-workspace}"
VPS_HOST="${LOOM_VPS_HOST:-loom-vps}"
MAIN_HTTP_URL="${LOOM_MAIN_HTTP_URL:-http://10.44.0.2:8080}"

pass_count=0

log() {
  printf '[smoke] %s\n' "$*"
}

pass() {
  pass_count=$((pass_count + 1))
  printf '[ok] %s\n' "$*"
}

fail() {
  printf '[fail] %s\n' "$*" >&2
  exit 1
}

remote() {
  local host="$1"
  shift
  ssh -o BatchMode=yes "$host" "$@"
}

check_remote() {
  local host="$1"
  local label="$2"
  local command="$3"

  remote "$host" "$command" >/dev/null
  pass "$label"
}

log "main: $MAIN_HOST"
log "workspace: $WORKSPACE_HOST"
log "vps: $VPS_HOST"
log "main HTTP URL: $MAIN_HTTP_URL"

check_remote "$VPS_HOST" "VPS WireGuard hub is active" \
  'ip -br addr show wg0 | grep -F "10.44.0.1/24"'

check_remote "$VPS_HOST" "VPS has both VM peers registered" \
  'wg show wg0 | grep -F "10.44.0.2/32" && wg show wg0 | grep -F "10.44.0.3/32"'

check_remote "$VPS_HOST" "VPS is not exposing LOOM app port 8080" \
  '! ss -lnt | grep -E "(:|\\])8080([[:space:]]|$)"'

check_remote "$MAIN_HOST" "main WireGuard interface is 10.44.0.2" \
  'ip -br addr show wg0 | grep -F "10.44.0.2/24"'

check_remote "$MAIN_HOST" "main LOOM runtime is active" \
  'systemctl is-active loomd postgresql wireguard-wg0'

check_remote "$MAIN_HOST" "main HTTP listener is bound to WireGuard address" \
  'ss -lnt | grep -F "10.44.0.2:8080"'

check_remote "$MAIN_HOST" "main HTTP listener is not bound to wildcard" \
  '! ss -lnt | grep -E "(0\\.0\\.0\\.0|\\[::\\]):8080([[:space:]]|$)"'

check_remote "$WORKSPACE_HOST" "workspace WireGuard interface is 10.44.0.3" \
  'ip -br addr show wg0 | grep -F "10.44.0.3/24"'

check_remote "$WORKSPACE_HOST" "workspace reaches main over WireGuard" \
  'ping -c 3 -W 2 10.44.0.2'

check_remote "$WORKSPACE_HOST" "workspace can call main loomd health over WireGuard HTTP" \
  "nix-shell -p curl jq --run 'curl -fsS $MAIN_HTTP_URL/v1/health | jq -e \".ok == true and .data.status == \\\"ok\\\"\"'"

check_remote "$MAIN_HOST" "main local Unix-socket API still works" \
  'loom health --json | jq -e ".ok == true and .data.status == \"ok\""'

log "completed $pass_count checks"
