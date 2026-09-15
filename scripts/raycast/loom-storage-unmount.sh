#!/usr/bin/env bash

# Support script. Intentionally not exposed as a Raycast command.

set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
LOOM_MACBOOK="${SCRIPT_DIR}/../loom-macbook"
"$LOOM_MACBOOK" storage-disable >/dev/null 2>&1 || true
"$LOOM_MACBOOK" unmount-main-smb
