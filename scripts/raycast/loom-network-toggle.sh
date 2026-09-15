#!/usr/bin/env bash

# Support script. Intentionally not exposed as a Raycast command.

set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
"${SCRIPT_DIR}/loom-network-enable.sh" >/dev/null
bash "${SCRIPT_DIR}/loom-storage-mount.sh"
