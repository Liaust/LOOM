#!/bin/zsh

# @raycast.schemaVersion 1
# @raycast.title Stop LOOM Main RDP Tunnel
# @raycast.mode compact
# @raycast.packageName LOOM
# @raycast.icon assets/loom-rdp.png
# @raycast.description Stop the local SSH tunnel used for RDP to the production LOOM main node.

set -euo pipefail
export LC_ALL=C
export LANG=C
export PATH="/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"

LOCAL_PORT="${LOOM_RDP_LOCAL_PORT:-3390}"

pids="$(lsof -tiTCP:"${LOCAL_PORT}" -sTCP:LISTEN 2>/dev/null || true)"

if [ -z "${pids}" ]; then
  echo "No LOOM RDP tunnel is listening on 127.0.0.1:${LOCAL_PORT}"
  exit 0
fi

stopped=0

for pid in ${pids}; do
  command_line="$(ps -p "${pid}" -o command= 2>/dev/null || true)"

  if [[ "${command_line}" == ssh* && "${command_line}" == *"127.0.0.1:3389"* ]] &&
     [[ "${command_line}" == *"loom-main"* || "${command_line}" == *"loom-hardware"* || "${command_line}" == *"10.44.0.2"* ]]; then
    kill "${pid}" >/dev/null 2>&1 || true
    stopped=$((stopped + 1))
  fi
done

if [ "${stopped}" -eq 0 ]; then
  echo "Port ${LOCAL_PORT} is in use, but it does not look like the LOOM RDP SSH tunnel."
  exit 1
fi

echo "Stopped ${stopped} LOOM RDP tunnel process(es)."
