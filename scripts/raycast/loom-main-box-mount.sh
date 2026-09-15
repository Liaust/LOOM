#!/usr/bin/env bash

# Support script for the optional writable Main Box mount.

set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
LOOM_MACBOOK="${SCRIPT_DIR}/../loom-macbook"
BOX_HOST="${LOOM_MAIN_BOX_SMB_HOST:-${LOOM_MAIN_SMB_HOST:-loom-storage}}"
BOX_USER="${LOOM_MAIN_BOX_SMB_USER:-${LOOM_MAIN_SMB_USER:-loomshare}}"
BOX_SHARE="${LOOM_MAIN_BOX_SMB_SHARE:-loom-main-box}"
BOX_MOUNT="${LOOM_MAIN_BOX_MOUNT:-$HOME/loom-main-box}"
BOX_URL="${LOOM_MAIN_BOX_SMB_FINDER_URL:-smb://${BOX_USER}@${BOX_HOST}/${BOX_SHARE}}"

"${SCRIPT_DIR}/loom-network-enable.sh" >/dev/null
if output="$("$LOOM_MACBOOK" mount-main-box-smb 2>&1)"; then
  printf '%s\n' "$output"
  open "$BOX_MOUNT" >/dev/null 2>&1 || true
  exit 0
fi

printf '%s\n\n' "$output"
echo "Direct mount did not complete. Opening ${BOX_URL} so Finder can refresh the dedicated SMB credential in Keychain."
open "$BOX_URL"
exit 1
