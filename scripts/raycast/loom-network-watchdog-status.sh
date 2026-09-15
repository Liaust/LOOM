#!/usr/bin/env bash

# Support script. Intentionally not exposed as a Raycast command.

set -euo pipefail

LABEL="local.loom.network-watchdog"
LOG_FILE="${LOOM_USER_HOME:-$HOME}/.loom/logs/network-watchdog.log"

printf 'LOOM Network Watchdog\n\n'

if launchctl print "system/$LABEL" >/tmp/loom-watchdog-status.txt 2>/tmp/loom-watchdog-status.err; then
  printf 'status: running\n\n'
  sed -n '1,80p' /tmp/loom-watchdog-status.txt | sed -n '/state =/p;/last exit code =/p;/program =/p;/path =/p;/runs =/p'
else
  printf 'status: stopped\n\n'
  sed -n '1,40p' /tmp/loom-watchdog-status.err || true
fi

printf '\nRecent watchdog log:\n'
if [[ -f "$LOG_FILE" ]]; then
  tail -n 30 "$LOG_FILE"
else
  printf 'No log yet: %s\n' "$LOG_FILE"
fi
