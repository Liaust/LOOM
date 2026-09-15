#!/bin/zsh

# @raycast.schemaVersion 1
# @raycast.title LOOM Main RDP Tunnel
# @raycast.mode compact
# @raycast.packageName LOOM
# @raycast.icon assets/loom-rdp.png
# @raycast.description Open an iTerm2 SSH tunnel for RDP to the production LOOM main node.

set -euo pipefail
export LC_ALL=C
export LANG=C
export PATH="/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"

LOCAL_PORT="${LOOM_RDP_LOCAL_PORT:-3390}"
SSH_HOST="${LOOM_RDP_SSH_HOST:-loom-main}"
REMOTE_HOST="${LOOM_RDP_REMOTE_HOST:-127.0.0.1}"
REMOTE_PORT="${LOOM_RDP_REMOTE_PORT:-3389}"
SCRIPT_DIR="$(cd -- "$(dirname -- "$0")" && pwd)"
RDP_FILE="${SCRIPT_DIR}/assets/loom-main-admin.rdp"
PROXY_JUMP="${LOOM_RDP_PROXY_JUMP:-none}"

if lsof -nP -iTCP:"${LOCAL_PORT}" -sTCP:LISTEN >/dev/null 2>&1; then
  open "${RDP_FILE}" >/dev/null 2>&1 || true
  echo "LOOM RDP tunnel is already listening on 127.0.0.1:${LOCAL_PORT}"
  exit 0
fi

if ! osascript -e 'id of application "iTerm2"' >/dev/null 2>&1; then
  echo "iTerm2 is not installed or is not visible to AppleScript."
  exit 1
fi

ssh_command="ssh -o ProxyJump=${PROXY_JUMP} -o ExitOnForwardFailure=yes -o ServerAliveInterval=30 -o ServerAliveCountMax=3 -N -L ${LOCAL_PORT}:${REMOTE_HOST}:${REMOTE_PORT} ${SSH_HOST}"

osascript <<APPLESCRIPT
tell application "iTerm2"
  activate
  create window with default profile
  tell current session of current window
    write text "${ssh_command}"
  end tell
end tell
APPLESCRIPT

for _ in {1..20}; do
  if lsof -nP -iTCP:"${LOCAL_PORT}" -sTCP:LISTEN >/dev/null 2>&1; then
    break
  fi
  sleep 0.25
done

open "${RDP_FILE}" >/dev/null 2>&1 || true

echo "Started LOOM RDP tunnel on 127.0.0.1:${LOCAL_PORT}"
