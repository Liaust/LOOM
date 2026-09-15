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

assert_remote "main health ok for portal worker/index/maintenance smoke" \
  'loom health --json | jq -e ".ok == true and .data.status == \"ok\"" >/dev/null'

assert_remote "background renders live worker and maintenance actions without raw command lines" \
  'loom enter --start background --exit-after-render --no-boot-animation >/tmp/loom-v0-2-1-slice-04-background.out
   grep -q "Worker Groups" /tmp/loom-v0-2-1-slice-04-background.out
   grep -q "Available Actions" /tmp/loom-v0-2-1-slice-04-background.out
   grep -q "Run Worker Selfcheck Once" /tmp/loom-v0-2-1-slice-04-background.out
   grep -q "Database Maintenance" /tmp/loom-v0-2-1-slice-04-background.out
   grep -q "Main Backups" /tmp/loom-v0-2-1-slice-04-background.out
   grep -q "Object Store" /tmp/loom-v0-2-1-slice-04-background.out
   grep -q "Run Main Backup Once" /tmp/loom-v0-2-1-slice-04-background.out
   grep -q "Run Object-Store Sample Scan" /tmp/loom-v0-2-1-slice-04-background.out
   ! grep -q "loom worker run" /tmp/loom-v0-2-1-slice-04-background.out'

assert_remote "dynamic portal worker action creates a worker run" \
  'loom enter --run-action worker.main_worker_selfcheck.run_once --confirm --exit-after-render >/tmp/loom-v0-2-1-slice-04-worker-run.out
   grep -q "Action Complete" /tmp/loom-v0-2-1-slice-04-worker-run.out
   grep -q "Worker: main.worker_selfcheck" /tmp/loom-v0-2-1-slice-04-worker-run.out
   grep -q "Run: worker_run_" /tmp/loom-v0-2-1-slice-04-worker-run.out
   run_id="$(sed -n "s/^Run: //p" /tmp/loom-v0-2-1-slice-04-worker-run.out | head -n 1)"
   test -n "$run_id"
   loom worker runs main.worker_selfcheck --json >/tmp/loom-v0-2-1-slice-04-worker-runs.json
   jq -e --arg run_id "$run_id" '"'"'(.data // []) | map(.worker_run_id) | index($run_id) != null'"'"' /tmp/loom-v0-2-1-slice-04-worker-runs.json >/dev/null'

assert_remote "jobs screen renders index actions" \
  'loom enter --start jobs --exit-after-render --no-boot-animation >/tmp/loom-v0-2-1-slice-04-jobs.out
   grep -q "Index Queue" /tmp/loom-v0-2-1-slice-04-jobs.out
   grep -q "Index Failures" /tmp/loom-v0-2-1-slice-04-jobs.out
   grep -q "Retry Failed Index Work" /tmp/loom-v0-2-1-slice-04-jobs.out
   grep -q "Run Text Indexer Once" /tmp/loom-v0-2-1-slice-04-jobs.out'

assert_remote "retry failed index work runs through portal action lifecycle" \
  'loom enter --run-action index.retry_failed --confirm --exit-after-render >/tmp/loom-v0-2-1-slice-04-index-retry.out
   grep -q "Action Complete" /tmp/loom-v0-2-1-slice-04-index-retry.out
   grep -q "Retried:" /tmp/loom-v0-2-1-slice-04-index-retry.out
   grep -q "Skipped:" /tmp/loom-v0-2-1-slice-04-index-retry.out'

assert_remote "text indexer run still works through portal" \
  'loom enter --run-action worker.indexer_text.run_once --confirm --exit-after-render >/tmp/loom-v0-2-1-slice-04-indexer-run.out
   grep -q "Action Complete" /tmp/loom-v0-2-1-slice-04-indexer-run.out
   grep -q "Worker: main.indexer_text" /tmp/loom-v0-2-1-slice-04-indexer-run.out
   grep -q "Run: worker_run_" /tmp/loom-v0-2-1-slice-04-indexer-run.out'

assert_remote "object-store sample scan is exposed as a portal action" \
  'loom enter --run-action maintenance.object_store.scan_sample --confirm --exit-after-render >/tmp/loom-v0-2-1-slice-04-object-scan.out
   grep -Eq "Action Complete|Action Failed" /tmp/loom-v0-2-1-slice-04-object-scan.out
   grep -Eq "Object-store scan|Reason:|Run:" /tmp/loom-v0-2-1-slice-04-object-scan.out'

assert_remote "previous portal slices still render core screens" \
  'loom enter --start home --exit-after-render --no-boot-animation | grep -q "Attention"
   loom enter --start capabilities --exit-after-render --no-boot-animation | grep -q "Capabilities And Providers"
   loom enter --start nodes --exit-after-render --no-boot-animation | grep -q "Nodes And Watched Roots"'

log "completed $pass_count checks"
