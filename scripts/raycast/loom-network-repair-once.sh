#!/usr/bin/env bash

# Support script. Intentionally not exposed as a Raycast command.

set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd -- "$SCRIPT_DIR/../.." && pwd)"
RUNNER="$REPO_DIR/scripts/loom-network-watchdog"
USER_HOME="${LOOM_USER_HOME:-$HOME}"
WG_CONF="${LOOM_MACBOOK_WG_CONF:-$USER_HOME/.loom/secrets/wireguard/loomwg0.conf}"
MAIN_SMB_HOST="${LOOM_MAIN_SMB_HOST:-loom-storage}"

sh_quote() {
  printf "'%s'" "${1//\'/\'\\\'\'}"
}

CMD="env LOOM_USER_HOME=$(sh_quote "$USER_HOME") LOOM_MACBOOK_WG_CONF=$(sh_quote "$WG_CONF") LOOM_MAIN_SMB_HOST=$(sh_quote "$MAIN_SMB_HOST") $(sh_quote "$RUNNER") --once"

osascript <<APPLESCRIPT
do shell script "$CMD" with administrator privileges
APPLESCRIPT

echo "LOOM network repair check completed."
