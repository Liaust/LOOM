#!/usr/bin/env bash

# Support script for the optional writable Main Box mount.

set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
"${SCRIPT_DIR}/../loom-macbook" unmount-main-box-smb
