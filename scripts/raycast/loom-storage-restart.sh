#!/usr/bin/env bash

# Support script. Intentionally not exposed as a Raycast command.

set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

bash "${SCRIPT_DIR}/loom-storage-unmount.sh" >/dev/null
bash "${SCRIPT_DIR}/loom-storage-mount.sh"
