#!/usr/bin/env bash

# Support script. Intentionally not exposed as a Raycast command.

set -euo pipefail

PLIST="/Library/LaunchDaemons/local.loom.network-watchdog.plist"
LABEL="local.loom.network-watchdog"

osascript <<APPLESCRIPT
do shell script "launchctl bootout system '$PLIST' >/dev/null 2>&1 || true; launchctl disable system/$LABEL >/dev/null 2>&1 || true; rm -f '$PLIST'" with administrator privileges
APPLESCRIPT

echo "LOOM network watchdog is stopped."
