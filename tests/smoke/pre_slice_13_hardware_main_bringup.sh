#!/usr/bin/env bash
set -euo pipefail

HARDWARE_HOST="${LOOM_HARDWARE_HOST:-loom-hardware}"
VPS_HOST="${LOOM_VPS_HOST:-loom-vps}"
HARDWARE_WG_IP="${LOOM_HARDWARE_WG_IP:-10.44.0.4}"
HARDWARE_HTTP_URL="${LOOM_HARDWARE_HTTP_URL:-http://${HARDWARE_WG_IP}:8080}"
EXPECT_WIREGUARD="${LOOM_HARDWARE_EXPECT_WIREGUARD:-0}"
EXPECT_LOOMD="${LOOM_HARDWARE_EXPECT_LOOMD:-0}"
EXPECT_RDP="${LOOM_HARDWARE_EXPECT_RDP:-0}"

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

log "hardware host: $HARDWARE_HOST"
log "vps host: $VPS_HOST"
log "hardware WireGuard IP: $HARDWARE_WG_IP"
log "hardware HTTP URL: $HARDWARE_HTTP_URL"
log "expect WireGuard: $EXPECT_WIREGUARD"
log "expect loomd: $EXPECT_LOOMD"
log "expect RDP: $EXPECT_RDP"

check_remote "$HARDWARE_HOST" "hardware SSH alias is reachable" \
  'hostname >/dev/null'

check_remote "$HARDWARE_HOST" "hardware is installed NixOS" \
  'test -e /etc/NIXOS && command -v nixos-version >/dev/null && nixos-version >/dev/null'

check_remote "$HARDWARE_HOST" "hardware is not booted from an ISO root filesystem" \
  'test "$(findmnt -n -o FSTYPE /)" != "iso9660"'

check_remote "$HARDWARE_HOST" "loomadmin can use passwordless sudo during bring-up" \
  'sudo -n true'

check_remote "$HARDWARE_HOST" "OpenSSH is active" \
  'systemctl is-active sshd'

check_remote "$HARDWARE_HOST" "GNOME display manager is active" \
  'systemctl is-active display-manager'

check_remote "$HARDWARE_HOST" "base developer tools exist" \
  'command -v git && command -v rsync && command -v jq && command -v curl'

check_remote "$HARDWARE_HOST" "GNOME remote desktop tooling is installed or declared" \
  'command -v grdctl || command -v gnome-remote-desktop || systemctl --global cat gnome-remote-desktop.service'

check_remote "$HARDWARE_HOST" "LOOM source directory exists or is ready to create" \
  'test -d /srv/loom/current || sudo test -d /srv/loom || sudo mkdir -p /srv/loom/current'

if [[ "$EXPECT_WIREGUARD" == "1" ]]; then
  check_remote "$HARDWARE_HOST" "hardware WireGuard interface has expected address" \
    "ip -br addr show wg0 | grep -F '${HARDWARE_WG_IP}/24'"

  check_remote "$HARDWARE_HOST" "hardware reaches VPS over WireGuard" \
    'ping -c 3 -W 2 10.44.0.1'

  check_remote "$VPS_HOST" "VPS has hardware peer allowed IP" \
    "wg show wg0 | grep -F '${HARDWARE_WG_IP}/32'"
fi

if [[ "$EXPECT_LOOMD" == "1" ]]; then
  check_remote "$HARDWARE_HOST" "hardware LOOM services are active" \
    'systemctl is-active loomd postgresql'

  check_remote "$HARDWARE_HOST" "hardware LOOM local health is ok" \
    'loom health --json | jq -e ".ok == true and .data.status == \"ok\" and .data.checks.migrations.status == \"ok\" and .data.checks.bootstrap.status == \"ok\""'

  check_remote "$HARDWARE_HOST" "hardware LOOM HTTP listener binds to WireGuard address" \
    "ss -lnt | grep -F '${HARDWARE_WG_IP}:8080'"

  check_remote "$HARDWARE_HOST" "hardware LOOM HTTP listener is not wildcard" \
    '! ss -lnt | grep -E "(0\\.0\\.0\\.0|\\[::\\]):8080([[:space:]]|$)"'

  check_remote "$HARDWARE_HOST" "hardware LOOM storage paths exist with loom ownership" \
    'sudo stat -c "%U:%G %n" /var/lib/loom /var/lib/loom/object-store /var/lib/loom/private-backups /etc/loom | awk '"'"'{ if ($1 != "loom:loom" && $1 != "root:loom") exit 1 }'"'"''

  if [[ "$EXPECT_WIREGUARD" == "1" ]]; then
    check_remote "$HARDWARE_HOST" "hardware LOOM health works over WireGuard HTTP locally" \
      "curl -fsS '${HARDWARE_HTTP_URL}/v1/health' | jq -e '.ok == true and .data.status == \"ok\"'"
  fi
fi

if [[ "$EXPECT_RDP" == "1" ]]; then
  check_remote "$HARDWARE_HOST" "RDP is listening on the hardware" \
    'ss -lnt | grep -E "(:|\\])3389([[:space:]]|$)"'

  check_remote "$HARDWARE_HOST" "RDP is not listening on wildcard IPv4" \
    '! ss -lnt | grep -E "0\\.0\\.0\\.0:3389([[:space:]]|$)"'

  if [[ "$EXPECT_WIREGUARD" == "1" ]]; then
    check_remote "$HARDWARE_HOST" "RDP is reachable on WireGuard address" \
      "ss -lnt | grep -F '${HARDWARE_WG_IP}:3389'"
  fi
fi

if [[ "$EXPECT_WIREGUARD" == "1" || "$EXPECT_LOOMD" == "1" || "$EXPECT_RDP" == "1" ]]; then
  check_remote "$VPS_HOST" "VPS is not exposing LOOM app or RDP ports publicly" \
    '! ss -lnt | grep -E "(:|\\])(8080|3389)([[:space:]]|$)"'
fi

log "completed $pass_count checks"
