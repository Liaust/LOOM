#!/bin/zsh

# @raycast.schemaVersion 1
# @raycast.title LOOM Network Up
# @raycast.mode compact
# @raycast.packageName LOOM
# @raycast.icon assets/loom-network.png
# @raycast.description Set LOOM network state to up; a system watchdog keeps WireGuard continuously on.

set -euo pipefail
export LC_ALL=C
export LANG=C
export PATH="/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"

SCRIPT_DIR="$(cd -- "$(dirname -- "$0")" && pwd)"
LOOM_MACBOOK="${SCRIPT_DIR}/../loom-macbook"
MAIN_SMB_HOST="${LOOM_MAIN_SMB_HOST:-loom-storage}"
MAIN_SMB_SHARE="${LOOM_MAIN_SMB_SHARE:-loom-storage}"
MAIN_SMB_USER="${LOOM_MAIN_SMB_USER:-loomshare}"
SMB_FINDER_URL="${LOOM_MAIN_SMB_FINDER_URL:-smb://${MAIN_SMB_USER}@${MAIN_SMB_HOST}/${MAIN_SMB_SHARE}}"

status_output() {
  "${SCRIPT_DIR}/loom-network-status.sh" 2>/dev/null || true
}

status_has_line() {
  local output="$1"
  local line="$2"
  [[ "$output" == "$line"$'\n'* || "$output" == *$'\n'"$line"$'\n'* || "$output" == *$'\n'"$line" ]]
}

current_status="$(status_output)"
if ! status_has_line "$current_status" "network: UP" || ! status_has_line "$current_status" "watchdog: ON"; then
  env LC_ALL=C LANG=C bash "${SCRIPT_DIR}/loom-network-watchdog-start.sh" >/dev/null
fi

network_ready=0
for _ in {1..40}; do
  current_status="$(status_output)"
  if status_has_line "$current_status" "network: UP"; then
    network_ready=1
    break
  fi
  sleep 0.5
done

if [[ "$network_ready" -ne 1 ]]; then
  echo "LOOM network watchdog started, but the private route did not become ready yet."
  echo
  printf '%s\n' "$current_status"
  exit 1
fi

cloud_output="$("$LOOM_MACBOOK" cloud-enable 2>&1 || true)"
storage_output="$("$LOOM_MACBOOK" storage-enable 2>&1 || true)"

echo "LOOM network is up and will be kept up by the watchdog."
if [[ -n "$storage_output" ]]; then
  printf '\nStorage:\n'
  printf '%s\n' "$storage_output"
  if printf '%s\n' "$storage_output" | grep -qiE 'smb_auth|Authentication error|SMB credential'; then
    printf '\nStorage authentication needs attention. Opening Finder so you can refresh the SMB password for %s.\n' "$SMB_FINDER_URL"
    printf 'When prompted, use the LOOM Main SMB credential and save it in Keychain.\n'
    open "$SMB_FINDER_URL" >/dev/null 2>&1 || true
  fi
fi
if [[ -n "$cloud_output" ]]; then
  printf '\nCloud:\n%s\n' "$cloud_output"
fi
