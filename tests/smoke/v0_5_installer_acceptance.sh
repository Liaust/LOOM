#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

log() {
  printf '[smoke] %s\n' "$*"
}

log "running v0.5 main installer acceptance"
"$SCRIPT_DIR/v0_5_main_installer.sh"

log "running v0.5 workspace installer acceptance"
"$SCRIPT_DIR/v0_5_workspace_installer.sh"

log "running v0.5 hardware/simulated installer acceptance"
"$SCRIPT_DIR/v0_5_hardware_installer.sh"

log "v0.5 installer acceptance passed"
