#!/usr/bin/env bash
set -euo pipefail

MAIN_HOST="${LOOM_DEV_HOST:-loom-dev}"

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

main_ssh() {
  ssh -o BatchMode=yes "$MAIN_HOST" "$@"
}

assert_remote() {
  local label="$1"
  local command="$2"

  main_ssh "$command"
  pass "$label"
}

require_command jq
require_command ssh

log "main: $MAIN_HOST"

assert_remote "main health ok for portal action lifecycle smoke" \
  'loom health --json | jq -e ".ok == true and .data.status == \"ok\"" >/dev/null'

assert_remote "portal action preview is typed and keeps raw command secondary" \
  'loom enter --preview-action worker.selfcheck.run_once --exit-after-render >/tmp/loom-v0-2-1-slice-03-preview.out
   grep -q "Action Preview" /tmp/loom-v0-2-1-slice-03-preview.out
   grep -q "Action: Run Worker Selfcheck Once" /tmp/loom-v0-2-1-slice-03-preview.out
   grep -q "Target: main.worker_selfcheck" /tmp/loom-v0-2-1-slice-03-preview.out
   grep -q "Risk: safe_run" /tmp/loom-v0-2-1-slice-03-preview.out
   grep -q "Confirmation: required before execution" /tmp/loom-v0-2-1-slice-03-preview.out
   grep -q "Raw Details" /tmp/loom-v0-2-1-slice-03-preview.out
   grep -q "Raw: loom worker run main.worker_selfcheck --once" /tmp/loom-v0-2-1-slice-03-preview.out'

assert_remote "portal safe action refuses missing confirmation" \
  'set +e
   loom enter --run-action worker.selfcheck.run_once --exit-after-render >/tmp/loom-v0-2-1-slice-03-no-confirm.out 2>/tmp/loom-v0-2-1-slice-03-no-confirm.err
   status=$?
   set -e
   test "$status" -ne 0
   cat /tmp/loom-v0-2-1-slice-03-no-confirm.out /tmp/loom-v0-2-1-slice-03-no-confirm.err | grep -qi "confirmation"'

assert_remote "portal confirmed safe action renders in-portal result" \
  'loom enter --run-action worker.selfcheck.run_once --confirm --exit-after-render >/tmp/loom-v0-2-1-slice-03-run.out
   grep -q "Action Complete" /tmp/loom-v0-2-1-slice-03-run.out
   grep -q "Action: Run Worker Selfcheck Once" /tmp/loom-v0-2-1-slice-03-run.out
   grep -q "Worker: main.worker_selfcheck" /tmp/loom-v0-2-1-slice-03-run.out
   grep -q "Run: worker_run_" /tmp/loom-v0-2-1-slice-03-run.out
   grep -q "Raw Details" /tmp/loom-v0-2-1-slice-03-run.out'

assert_remote "confirmed portal worker run is visible in worker history" \
  'run_id="$(sed -n "s/^Run: //p" /tmp/loom-v0-2-1-slice-03-run.out | head -n 1)"
   test -n "$run_id"
   loom worker runs main.worker_selfcheck --json >/tmp/loom-v0-2-1-slice-03-worker-runs.json
   jq -e --arg run_id "$run_id" '"'"'(.data // []) | map(.worker_run_id) | index($run_id) != null'"'"' /tmp/loom-v0-2-1-slice-03-worker-runs.json >/dev/null'

assert_remote "portal scoped worker search no longer exposes raw commands by default" \
  'loom enter --search "#workers run worker selfcheck" --exit-after-render >/tmp/loom-v0-2-1-slice-03-search.out
   grep -q "Run Worker Selfcheck Once" /tmp/loom-v0-2-1-slice-03-search.out
   ! grep -q "Raw: loom" /tmp/loom-v0-2-1-slice-03-search.out'

assert_remote "portal surfaces render friendly action labels" \
  'loom enter --start background --exit-after-render --no-boot-animation >/tmp/loom-v0-2-1-slice-03-background.out
   loom enter --start automations --exit-after-render --no-boot-animation >/tmp/loom-v0-2-1-slice-03-automations.out
   loom enter --start jobs --exit-after-render --no-boot-animation >/tmp/loom-v0-2-1-slice-03-jobs.out
   grep -q "Available Actions" /tmp/loom-v0-2-1-slice-03-background.out
   grep -q "Run Worker Selfcheck Once" /tmp/loom-v0-2-1-slice-03-background.out
   grep -q "Run Automation Scheduler Once" /tmp/loom-v0-2-1-slice-03-automations.out
   grep -q "Run Job Runner Once" /tmp/loom-v0-2-1-slice-03-jobs.out
   ! grep -q "loom worker run" /tmp/loom-v0-2-1-slice-03-background.out'

assert_remote "portal operational screens still render after action lifecycle changes" \
  'loom enter --start home --exit-after-render --no-boot-animation | grep -q "Attention"
   loom enter --start capabilities --exit-after-render --no-boot-animation | grep -q "Capabilities And Providers"
   loom enter --start nodes --exit-after-render --no-boot-animation | grep -q "Nodes And Watched Roots"'

log "completed $pass_count checks"
