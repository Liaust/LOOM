#!/bin/zsh

# @raycast.schemaVersion 1
# @raycast.title LOOM Cloud Storage
# @raycast.mode compact
# @raycast.packageName LOOM
# @raycast.icon assets/loom-network.png
# @raycast.description Mount the Hetzner LOOM Cloud Storage Box over SMB and open it in Finder.

set -euo pipefail
export LC_ALL=C
export LANG=C
export PATH="/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"

SCRIPT_DIR="$(cd -- "$(dirname -- "$0")" && pwd)"
LOOM_MACBOOK="${SCRIPT_DIR}/../loom-macbook"

LOOM_CLOUD_FOLDER_PROTOCOL=smb "$LOOM_MACBOOK" mount-cloud
