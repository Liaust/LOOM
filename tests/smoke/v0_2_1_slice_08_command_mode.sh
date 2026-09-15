#!/usr/bin/env bash
set -euo pipefail

HOST="${LOOM_DEV_HOST:-loom-dev}"
RUN_ID="$(date +%Y%m%d%H%M%S)-$RANDOM"
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

json_value() {
  local command="$1"
  local filter="$2"

  remote "$command | jq -r '$filter'"
}

require_command jq
require_command ssh

log "target: $HOST"
log "run: $RUN_ID"

check_remote "loomd service active" 'systemctl is-active loomd'
check_json "main health ok" \
  'loom health --json' \
  '.ok == true and .data.status == "ok"'

remote "loom workers list --json --correlation-id corr_smoke_v0_2_1_slice_08_workers > /tmp/loom-v0-2-1-slice-08-workers.json"
worker_key="$(json_value 'cat /tmp/loom-v0-2-1-slice-08-workers.json' '[.data[] | select(.worker_key == "main.worker_selfcheck")][0].worker_key // [.data[] | select(.worker_key == "main.indexer_text")][0].worker_key // .data[0].worker_key')"
[[ -n "$worker_key" && "$worker_key" != "null" ]] || fail "could not discover worker key"
pass "captured worker key"

remote "loom node list --json --correlation-id corr_smoke_v0_2_1_slice_08_nodes > /tmp/loom-v0-2-1-slice-08-nodes.json"
node_key="$(json_value 'cat /tmp/loom-v0-2-1-slice-08-nodes.json' '.data[0].node_key // .data[0].node_id // "main"')"
[[ -n "$node_key" && "$node_key" != "null" ]] || fail "could not discover node key"
pass "captured node key"

check_remote "command preview canonicalizes friendly aliases" \
  "NO_COLOR=1 LOOM_NO_ANIMATION=1 loom enter --command-preview '\$ list capabilities' --exit-after-render --no-boot-animation > /tmp/loom-v0-2-1-slice-08-preview.out
   grep -q 'Command Preview' /tmp/loom-v0-2-1-slice-08-preview.out
   grep -q 'Canonical: loom capabilities list' /tmp/loom-v0-2-1-slice-08-preview.out
   grep -q 'Classification' /tmp/loom-v0-2-1-slice-08-preview.out
   grep -q 'inspect' /tmp/loom-v0-2-1-slice-08-preview.out"

check_remote "search palette enters command mode for dollar input" \
  "NO_COLOR=1 LOOM_NO_ANIMATION=1 loom enter --search '\$ list capabilities' --exit-after-render --no-boot-animation > /tmp/loom-v0-2-1-slice-08-palette.out
   grep -q 'LOOM Command' /tmp/loom-v0-2-1-slice-08-palette.out
   grep -q 'Run LOOM commands inside the portal' /tmp/loom-v0-2-1-slice-08-palette.out
   grep -q 'Canonical: loom capabilities list' /tmp/loom-v0-2-1-slice-08-palette.out"

check_remote "inspect commands execute inside portal command mode" \
  "NO_COLOR=1 LOOM_NO_ANIMATION=1 loom enter --command-run '\$ health' --exit-after-render --no-boot-animation > /tmp/loom-v0-2-1-slice-08-health.out
   grep -q 'Command Complete' /tmp/loom-v0-2-1-slice-08-health.out
   grep -q 'LOOM daemon:' /tmp/loom-v0-2-1-slice-08-health.out
   NO_COLOR=1 LOOM_NO_ANIMATION=1 loom enter --command-run '\$ status' --exit-after-render --no-boot-animation > /tmp/loom-v0-2-1-slice-08-status.out
   grep -q 'Command Complete' /tmp/loom-v0-2-1-slice-08-status.out
   grep -q 'LOOM:' /tmp/loom-v0-2-1-slice-08-status.out"

check_remote "friendly aliases execute through canonical commands" \
  "NO_COLOR=1 LOOM_NO_ANIMATION=1 loom enter --command-run '\$ list capabilities' --exit-after-render --no-boot-animation > /tmp/loom-v0-2-1-slice-08-alias-run.out
   grep -q 'Command Complete' /tmp/loom-v0-2-1-slice-08-alias-run.out
   grep -q 'Canonical: loom capabilities list' /tmp/loom-v0-2-1-slice-08-alias-run.out
   grep -q 'main@system.status.read' /tmp/loom-v0-2-1-slice-08-alias-run.out
   NO_COLOR=1 LOOM_NO_ANIMATION=1 loom enter --command-run '\$ show sync status --node $node_key' --exit-after-render --no-boot-animation > /tmp/loom-v0-2-1-slice-08-sync-alias.out
   grep -q 'Command Complete' /tmp/loom-v0-2-1-slice-08-sync-alias.out
   grep -q 'Canonical: loom sync status --node $node_key' /tmp/loom-v0-2-1-slice-08-sync-alias.out"

check_remote "command completions include static and dynamic entries" \
  "NO_COLOR=1 LOOM_NO_ANIMATION=1 loom enter --command-complete '\$ cap' --exit-after-render --no-boot-animation > /tmp/loom-v0-2-1-slice-08-complete-static.out
   grep -q 'LOOM Command' /tmp/loom-v0-2-1-slice-08-complete-static.out
   grep -q 'Completions' /tmp/loom-v0-2-1-slice-08-complete-static.out
   grep -q 'capabilities' /tmp/loom-v0-2-1-slice-08-complete-static.out
   NO_COLOR=1 LOOM_NO_ANIMATION=1 loom enter --command-complete '\$ worker run ma' --exit-after-render --no-boot-animation > /tmp/loom-v0-2-1-slice-08-complete-worker.out
   grep -q 'Completions' /tmp/loom-v0-2-1-slice-08-complete-worker.out
   grep -q '$worker_key' /tmp/loom-v0-2-1-slice-08-complete-worker.out
   NO_COLOR=1 LOOM_NO_ANIMATION=1 loom enter --command-complete '\$ capability inspect main@system' --exit-after-render --no-boot-animation > /tmp/loom-v0-2-1-slice-08-complete-capability.out
   grep -q 'Completions' /tmp/loom-v0-2-1-slice-08-complete-capability.out
   grep -q 'main@system.status.read' /tmp/loom-v0-2-1-slice-08-complete-capability.out"

check_remote "shell syntax is rejected before execution" \
  "NO_COLOR=1 LOOM_NO_ANIMATION=1 loom enter --command-preview '\$ health | jq .' --exit-after-render --no-boot-animation > /tmp/loom-v0-2-1-slice-08-shell-block.out
   grep -q 'Shell operators are not supported' /tmp/loom-v0-2-1-slice-08-shell-block.out"

check_remote "recursive portal command is blocked" \
  "NO_COLOR=1 LOOM_NO_ANIMATION=1 loom enter --command-run '\$ enter' --exit-after-render --no-boot-animation > /tmp/loom-v0-2-1-slice-08-enter-block.out
   grep -q 'Command Failed' /tmp/loom-v0-2-1-slice-08-enter-block.out
   grep -q 'interactive portal' /tmp/loom-v0-2-1-slice-08-enter-block.out"

check_remote "dangerous command classes are blocked" \
  "NO_COLOR=1 LOOM_NO_ANIMATION=1 loom enter --command-preview '\$ integration revoke gmail' --exit-after-render --no-boot-animation > /tmp/loom-v0-2-1-slice-08-dangerous.out
   grep -q 'Command Preview' /tmp/loom-v0-2-1-slice-08-dangerous.out
   grep -q 'Dangerous commands are blocked' /tmp/loom-v0-2-1-slice-08-dangerous.out"

check_remote "safe commands require confirmation" \
  "set +e
   NO_COLOR=1 LOOM_NO_ANIMATION=1 loom enter --command-run '\$ worker run $worker_key --once' --exit-after-render --no-boot-animation > /tmp/loom-v0-2-1-slice-08-confirm-required.out 2>&1
   status=\$?
   set -e
   test \$status -ne 0
   grep -q 'portal.command_confirmation_required' /tmp/loom-v0-2-1-slice-08-confirm-required.out"

check_remote "confirmed safe commands execute through the portal" \
  "NO_COLOR=1 LOOM_NO_ANIMATION=1 loom enter --command-run '\$ worker run $worker_key --once' --confirm --exit-after-render --no-boot-animation > /tmp/loom-v0-2-1-slice-08-confirmed-worker.out
   grep -q 'Command Complete' /tmp/loom-v0-2-1-slice-08-confirmed-worker.out
   grep -q 'Canonical: loom worker run $worker_key --once' /tmp/loom-v0-2-1-slice-08-confirmed-worker.out
   grep -Eq 'Run:|Worker' /tmp/loom-v0-2-1-slice-08-confirmed-worker.out
   loom worker runs '$worker_key' --json --correlation-id corr_smoke_v0_2_1_slice_08_worker_runs | jq -e '.ok == true and (.data | length) >= 1' >/dev/null"

log "completed $pass_count checks"
