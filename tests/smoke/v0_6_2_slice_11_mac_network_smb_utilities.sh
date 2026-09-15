#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
HELPER="${LOOM_MACBOOK_HELPER:-$ROOT_DIR/scripts/loom-macbook}"
RAYCAST_DIR="$ROOT_DIR/scripts/raycast"
MAIN_SMB_HOST="${LOOM_MAIN_SMB_HOST:-loom-storage}"
MAIN_SMB_SHARE="${LOOM_MAIN_SMB_SHARE:-loom-storage}"
MAIN_SMB_USER="${LOOM_MAIN_SMB_USER:-loomshare}"
MAIN_STORAGE_MOUNT="${LOOM_MAIN_STORAGE_MOUNT:-$HOME/loom-storage}"
FINDER_MOUNT="/Volumes/${MAIN_SMB_SHARE}"
NODE_KEY="${LOOM_WORKSPACE_NODE_KEY:-macbook}"
pass_count=0

fail() {
  printf 'v0.6.2 slice 11 Mac network/SMB utilities: %s\n' "$*" >&2
  exit 1
}

pass() {
  pass_count=$((pass_count + 1))
  printf '[ok] %s\n' "$*"
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

raycast_exposed_scripts() {
  find "$RAYCAST_DIR" -maxdepth 1 -type f -perm +111 -print | sort | while IFS= read -r file; do
    if grep -q '@raycast.schemaVersion' "$file"; then
      basename "$file"
    fi
  done
}

mount_point() {
  for path in "$MAIN_STORAGE_MOUNT" "$FINDER_MOUNT"; do
    if mount 2>/dev/null | grep -F " on ${path} " >/dev/null 2>&1; then
      printf '%s\n' "$path"
      return 0
    fi
  done
  return 1
}

require_command bash

bash -n "$HELPER"
for script in "$RAYCAST_DIR"/*.sh; do
  bash -n "$script"
done
pass "MacBook helper and Raycast scripts parse"

expected="$(
  printf '%s\n' \
    loom-cloud-mount.sh \
    loom-network-disable.sh \
    loom-network-enable.sh \
    loom-network-status.sh \
    mac-sshtunnel-main.sh \
    mac-stop-sshtunnel-main.sh \
    | sort
)"
actual="$(raycast_exposed_scripts)"
if [[ "$actual" != "$expected" ]]; then
  printf 'Expected exposed Raycast scripts:\n%s\n\nActual:\n%s\n' "$expected" "$actual" >&2
  fail "Raycast exposure is not limited to network and RDP commands"
fi
pass "Raycast exposure is limited to network and RDP commands"

"$HELPER" --help | grep -q 'smb-host-alias-status'
"$HELPER" --help | grep -q 'smb-host-alias-install'
pass "SMB host alias commands are advertised"

grep -F 'key_use_agent = true' "$HELPER" >/dev/null \
  || fail "main storage rclone config must use ssh-agent for passphrase-protected keys"
pass "main storage rclone config preserves the ssh-agent credential boundary"

grep -F 'known_hosts_file = $HOME/.ssh/known_hosts' "$HELPER" >/dev/null \
  || fail "main storage rclone config must validate the server host key"
pass "main storage rclone config validates the server host key"

if [[ "${LOOM_RUN_PRODUCTION_MACBOOK_STORAGE_SMOKE:-}" != "1" ]]; then
  pass "production MacBook network/storage checks skipped; set LOOM_RUN_PRODUCTION_MACBOOK_STORAGE_SMOKE=1 to run them"
  printf '[smoke] completed %d checks\n' "$pass_count"
  exit 0
fi

[[ "$(uname -s)" == "Darwin" ]] || fail "production MacBook storage smoke must run on macOS"
require_command nc
require_command ssh
require_command curl

"$HELPER" smb-host-alias-status >/dev/null
pass "SMB host alias resolves correctly"

"$HELPER" network-check >/dev/null
pass "WireGuard route, SSH, and private main HTTP are healthy"

nc -G 5 -z "$MAIN_SMB_HOST" 445 >/dev/null 2>&1 \
  || fail "SMB port is not reachable at $MAIN_SMB_HOST:445"
pass "SMB port is reachable"

mounted="$(mount_point || true)"
if [[ -z "$mounted" ]]; then
  fail "LOOM Main storage is not mounted. Mount with smb://${MAIN_SMB_USER}@${MAIN_SMB_HOST}/${MAIN_SMB_SHARE} or the Raycast storage helper."
fi
pass "LOOM Main storage mount is available at $mounted"

documents="$mounted/main/Documents"
[[ -d "$documents" ]] || fail "main/Documents missing from mounted storage: $documents"
probe="$documents/.loom-smb-smoke-$(date +%Y%m%dT%H%M%S)-$$.txt"
printf 'LOOM SMB smoke\n' >"$probe"
grep -q 'LOOM SMB smoke' "$probe"
rm -f "$probe"
pass "main/Documents is writable and removable"

backups="$mounted/$NODE_KEY/Backups"
[[ -d "$backups" ]] || fail "generated backup view missing from mounted storage: $backups"
write_probe="$backups/.loom-write-probe-$$"
if touch "$write_probe" 2>/dev/null; then
  rm -f "$write_probe"
  fail "generated backup view is writable: $backups"
fi
pass "generated backup view is read-only"

printf '[smoke] completed %d checks\n' "$pass_count"
