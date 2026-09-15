#!/usr/bin/env bash
set -euo pipefail

MAIN_HOST="${LOOM_MAIN_HOST:-${LOOM_DEV_HOST:-loom-dev}}"
WORKSPACE_HOST="${LOOM_WORKSPACE_HOST:-loom-workspace}"
VPS_HOST="${LOOM_VPS_HOST:-loom-vps}"
MAIN_WG_IP="${LOOM_MAIN_WG_IP:-10.44.0.2}"
WORKSPACE_WG_IP="${LOOM_WORKSPACE_WG_IP:-10.44.0.3}"
VPS_WG_IP="${LOOM_VPS_WG_IP:-10.44.0.1}"
MAIN_HTTP_PORT="${LOOM_MAIN_HTTP_PORT:-8080}"
MAIN_HTTP_URL="${LOOM_MAIN_HTTP_URL:-http://${MAIN_WG_IP}:${MAIN_HTTP_PORT}}"

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

require_local_command() {
  if ! command -v "$1" >/dev/null 2>&1; then
    fail "missing local command: $1"
  fi
}

require_local_command ssh

log "main: $MAIN_HOST"
log "workspace: $WORKSPACE_HOST"
log "vps: $VPS_HOST"
log "main WireGuard IP: $MAIN_WG_IP"
log "workspace WireGuard IP: $WORKSPACE_WG_IP"
log "VPS WireGuard IP: $VPS_WG_IP"
log "main HTTP URL: $MAIN_HTTP_URL"

check_remote "$MAIN_HOST" "main LOOM services are active" \
  'systemctl is-active loomd postgresql wireguard-wg0'

check_remote "$MAIN_HOST" "loomd hardening properties are active" \
  'systemctl show loomd \
    -p NoNewPrivileges \
    -p PrivateTmp \
    -p ProtectSystem \
    -p ProtectHome \
    -p RestrictSUIDSGID \
    -p LockPersonality \
    -p UMask \
    -p SystemCallArchitectures \
    | grep -Fx NoNewPrivileges=yes \
    && systemctl show loomd -p PrivateTmp | grep -Fx PrivateTmp=yes \
    && systemctl show loomd -p ProtectSystem | grep -Fx ProtectSystem=full \
    && systemctl show loomd -p ProtectHome | grep -Fx ProtectHome=read-only \
    && systemctl show loomd -p RestrictSUIDSGID | grep -Fx RestrictSUIDSGID=yes \
    && systemctl show loomd -p LockPersonality | grep -Fx LockPersonality=yes \
    && systemctl show loomd -p UMask | grep -Fx UMask=0027 \
    && systemctl show loomd -p SystemCallArchitectures | grep -Fx SystemCallArchitectures=native'

check_remote "$MAIN_HOST" "loomd has explicit runtime/data write paths" \
  'systemctl show loomd -p ReadWritePaths | grep -F "/var/lib/loom" | grep -F "/run/loom"'

check_remote "$MAIN_HOST" "main local Unix-socket API still works" \
  'loom health --json | jq -e ".ok == true and .data.status == \"ok\" and .data.checks.migrations.status == \"ok\""'

check_remote "$MAIN_HOST" "main WireGuard interface has expected address" \
  "ip -br addr show wg0 | grep -F '${MAIN_WG_IP}/24'"

check_remote "$MAIN_HOST" "main HTTP listener is bound only to WireGuard address" \
  "listeners=\$(ss -H -lnt \"sport = :${MAIN_HTTP_PORT}\" | awk '{print \$4}' | sort -u); test \"\$listeners\" = '${MAIN_WG_IP}:${MAIN_HTTP_PORT}'"

check_remote "$MAIN_HOST" "main HTTP listener is not wildcard" \
  "! ss -H -lnt \"sport = :${MAIN_HTTP_PORT}\" | awk '{print \$4}' | grep -E '(^0\\.0\\.0\\.0:|^\\[::\\]:)'"

check_remote "$MAIN_HOST" "PostgreSQL listens only on loopback TCP addresses" \
  "listeners=\$(ss -H -lnt \"sport = :5432\" | awk '{print \$4}' | sort -u); test -n \"\$listeners\"; ! printf '%s\n' \"\$listeners\" | grep -Ev '^(127\\.0\\.0\\.1:5432|\\[::1\\]:5432)$'"

check_remote "$MAIN_HOST" "LOOM runtime paths remain writable by service user" \
  'sudo -u loom test -w /var/lib/loom && sudo -u loom test -w /var/lib/loom/object-store && sudo -u loom test -w /var/lib/loom/private-backups'

check_remote "$VPS_HOST" "VPS WireGuard hub is active" \
  "ip -br addr show wg0 | grep -F '${VPS_WG_IP}/24'"

check_remote "$VPS_HOST" "VPS has no loomd service" \
  '! systemctl cat loomd >/dev/null 2>&1'

check_remote "$VPS_HOST" "VPS has no active PostgreSQL service" \
  '! systemctl is-active --quiet postgresql'

check_remote "$VPS_HOST" "VPS has no LOOM canonical state directory" \
  'test ! -e /var/lib/loom'

check_remote "$VPS_HOST" "VPS has no PostgreSQL TCP listener" \
  "! ss -H -lnt \"sport = :5432\" | grep ."

check_remote "$VPS_HOST" "VPS is not exposing LOOM app or RDP ports" \
  "! ss -H -lnt | awk '{print \$4}' | grep -E '(:|\\])(8080|3389)$'"

check_remote "$WORKSPACE_HOST" "workspace WireGuard interface has expected address" \
  "ip -br addr show wg0 | grep -F '${WORKSPACE_WG_IP}/24'"

check_remote "$WORKSPACE_HOST" "workspace reaches main over WireGuard" \
  "ping -c 3 -W 2 '${MAIN_WG_IP}'"

check_remote "$WORKSPACE_HOST" "workspace can call main private HTTP health" \
  "nix-shell -p curl jq --run 'curl -fsS ${MAIN_HTTP_URL}/v1/health | jq -e \".ok == true and .data.status == \\\"ok\\\"\"'"

log "completed $pass_count checks"
