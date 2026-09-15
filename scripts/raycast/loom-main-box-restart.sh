#!/usr/bin/env bash

# Support script for the optional writable Main Box mount.

set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
bash "${SCRIPT_DIR}/loom-main-box-unmount.sh" >/dev/null
bash "${SCRIPT_DIR}/loom-main-box-mount.sh"
