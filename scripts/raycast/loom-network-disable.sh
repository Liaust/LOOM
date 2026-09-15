#!/bin/zsh

# @raycast.schemaVersion 1
# @raycast.title LOOM Network Down
# @raycast.mode compact
# @raycast.packageName LOOM
# @raycast.icon assets/loom-network.png
# @raycast.description Set LOOM network state to down; stop the watchdog, unmount storage, and bring WireGuard down.

set -euo pipefail
export LC_ALL=C
export LANG=C
export PATH="/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"

SCRIPT_DIR="$(cd -- "$(dirname -- "$0")" && pwd)"
LOOM_MACBOOK="${SCRIPT_DIR}/../loom-macbook"
WATCHDOG_PLIST="/Library/LaunchDaemons/local.loom.network-watchdog.plist"
WATCHDOG_LABEL="local.loom.network-watchdog"

"$LOOM_MACBOOK" cloud-disable || true
"$LOOM_MACBOOK" storage-disable || true

if launchctl print "system/${WATCHDOG_LABEL}" >/dev/null 2>&1 || [[ -f "$WATCHDOG_PLIST" ]]; then
  osascript <<APPLESCRIPT
do shell script "launchctl bootout system '$WATCHDOG_PLIST' >/dev/null 2>&1 || true" with administrator privileges
APPLESCRIPT
  echo "Stopped LOOM network watchdog"
fi

"$LOOM_MACBOOK" wg-down

echo "LOOM network is down."
