#!/usr/bin/env bash
set -euo pipefail

HOST="${LOOM_DEV_HOST:-loom-dev}"
pass_count=0

log() {
  printf '[smoke] %s\n' "$*"
}

pass() {
  pass_count=$((pass_count + 1))
  printf '[ok] %s\n' "$*"
}

fail() {
  printf '[fail] %s\n' "$*" >&2
  exit 1
}

require_command() {
  if ! command -v "$1" >/dev/null 2>&1; then
    fail "missing required command: $1"
  fi
}

remote() {
  ssh -o BatchMode=yes "$HOST" "$@"
}

check_remote() {
  local label="$1"
  local command="$2"

  remote "$command" >/dev/null
  pass "$label"
}

check_json() {
  local label="$1"
  local command="$2"
  local filter="$3"

  remote "$command | jq -e '$filter' >/dev/null"
  pass "$label"
}

require_command jq
require_command ssh

log "target: $HOST"

check_json "main health ok" \
  'loom health --json' \
  '.ok == true and .data.status == "ok"'

check_remote "default palette is navigation-only" \
  "NO_COLOR=1 LOOM_NO_ANIMATION=1 loom enter --search 'main@system.status' --exit-after-render --no-boot-animation > /tmp/loom-v0-2-1-slice-10-default-search.out
   grep -q 'Command Palette' /tmp/loom-v0-2-1-slice-10-default-search.out
   grep -q 'No matching navigation results' /tmp/loom-v0-2-1-slice-10-default-search.out
   ! grep -q 'main@system.status.read' /tmp/loom-v0-2-1-slice-10-default-search.out
   ! grep -q '\\[capability\\]' /tmp/loom-v0-2-1-slice-10-default-search.out"

check_remote "scoped palette starts with narrowing scope suggestions" \
  "NO_COLOR=1 LOOM_NO_ANIMATION=1 loom enter --search '#' --exit-after-render --no-boot-animation > /tmp/loom-v0-2-1-slice-10-scope-root.out
   grep -q 'Scoped Search' /tmp/loom-v0-2-1-slice-10-scope-root.out
   grep -q '#capabilities' /tmp/loom-v0-2-1-slice-10-scope-root.out
   grep -q '#workers' /tmp/loom-v0-2-1-slice-10-scope-root.out
   NO_COLOR=1 LOOM_NO_ANIMATION=1 loom enter --search '#capa' --exit-after-render --no-boot-animation > /tmp/loom-v0-2-1-slice-10-scope-narrow.out
   grep -q '#capabilities' /tmp/loom-v0-2-1-slice-10-scope-narrow.out
   ! grep -q '#workers' /tmp/loom-v0-2-1-slice-10-scope-narrow.out"

check_remote "capability scoped search uses address prefixes" \
  "NO_COLOR=1 LOOM_NO_ANIMATION=1 loom enter --search '#capabilities main@' --exit-after-render --no-boot-animation > /tmp/loom-v0-2-1-slice-10-cap-provider-search.out
   grep -q 'Capability Search' /tmp/loom-v0-2-1-slice-10-cap-provider-search.out
   grep -q '\\[provider\\]' /tmp/loom-v0-2-1-slice-10-cap-provider-search.out
   grep -q 'main@system' /tmp/loom-v0-2-1-slice-10-cap-provider-search.out
   NO_COLOR=1 LOOM_NO_ANIMATION=1 loom enter --search '#capabilities main@system.status' --exit-after-render --no-boot-animation > /tmp/loom-v0-2-1-slice-10-cap-endpoint-search.out
   grep -q 'Capability Search' /tmp/loom-v0-2-1-slice-10-cap-endpoint-search.out
   grep -q '\\[capability\\]' /tmp/loom-v0-2-1-slice-10-cap-endpoint-search.out
   grep -q 'main@system.status.read' /tmp/loom-v0-2-1-slice-10-cap-endpoint-search.out"

check_remote "worker actions require worker scoped search" \
  "NO_COLOR=1 LOOM_NO_ANIMATION=1 loom enter --search selfcheck --exit-after-render --no-boot-animation > /tmp/loom-v0-2-1-slice-10-default-worker-search.out
   ! grep -q 'Run Worker Selfcheck Once' /tmp/loom-v0-2-1-slice-10-default-worker-search.out
   NO_COLOR=1 LOOM_NO_ANIMATION=1 loom enter --search '#workers run worker selfcheck' --exit-after-render --no-boot-animation > /tmp/loom-v0-2-1-slice-10-worker-search.out
   grep -q 'Worker Search' /tmp/loom-v0-2-1-slice-10-worker-search.out
   grep -q '\\[worker\\]' /tmp/loom-v0-2-1-slice-10-worker-search.out
   grep -q 'Run Worker Selfcheck Once' /tmp/loom-v0-2-1-slice-10-worker-search.out
   ! grep -q 'Raw: loom' /tmp/loom-v0-2-1-slice-10-worker-search.out"

check_remote "capability screen starts at node scope level" \
  "NO_COLOR=1 LOOM_NO_ANIMATION=1 loom enter --start capabilities --exit-after-render --no-boot-animation > /tmp/loom-v0-2-1-slice-10-capabilities-screen.out
   grep -q 'Capability Explorer' /tmp/loom-v0-2-1-slice-10-capabilities-screen.out
   grep -q 'main' /tmp/loom-v0-2-1-slice-10-capabilities-screen.out
   grep -q 'providers' /tmp/loom-v0-2-1-slice-10-capabilities-screen.out
   ! grep -q 'No capabilities returned by the backend' /tmp/loom-v0-2-1-slice-10-capabilities-screen.out"

check_remote "home logo keeps portal subtitle separated" \
  "NO_COLOR=1 LOOM_NO_ANIMATION=1 loom enter --start home --exit-after-render --no-boot-animation > /tmp/loom-v0-2-1-slice-10-home.out
   grep -q 'LOOM Portal' /tmp/loom-v0-2-1-slice-10-home.out
   awk '/LOOM Portal/{if (prev == \"\") found=1} {prev=\$0} END{exit(found?0:1)}' /tmp/loom-v0-2-1-slice-10-home.out"

log "completed $pass_count checks"
