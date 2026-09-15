#!/usr/bin/env bash

# Support script. Intentionally not exposed as a Raycast command.

set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
LOOM_MACBOOK="${SCRIPT_DIR}/../loom-macbook"
MAIN_SMB_HOST="${LOOM_MAIN_SMB_HOST:-loom-storage}"
MAIN_SMB_USER="${LOOM_MAIN_SMB_USER:-loomshare}"
MAIN_STORAGE_MOUNT="${LOOM_MAIN_STORAGE_MOUNT:-$HOME/loom-storage}"
MAIN_SMB_SHARE="${LOOM_MAIN_SMB_SHARE:-loom-storage}"
SMB_URL="${LOOM_MAIN_SMB_FINDER_URL:-smb://${MAIN_SMB_USER}@${MAIN_SMB_HOST}/${MAIN_SMB_SHARE}}"

"${SCRIPT_DIR}/loom-network-enable.sh" >/dev/null
if output="$("$LOOM_MACBOOK" mount-main-smb 2>&1)"; then
  printf '%s\n' "$output"
  open "$MAIN_STORAGE_MOUNT" >/dev/null 2>&1 || true
  exit 0
fi

printf '%s\n\n' "$output"
echo "Direct mount did not complete. Opening ${SMB_URL} so Finder can refresh the dedicated SMB credential in Keychain."
open "$SMB_URL"
exit 1
