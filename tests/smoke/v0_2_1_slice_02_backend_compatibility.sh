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

assert_remote "main health ok for portal backend compatibility smoke" \
  'loom health --json | jq -e ".ok == true and .data.status == \"ok\"" >/dev/null'

assert_remote "portal renders every existing live-backed screen" \
  'loom enter --start home --exit-after-render --no-boot-animation | grep -q "Attention"
   loom enter --start background --exit-after-render --no-boot-animation | grep -q "Worker Groups"
   loom enter --start automations --exit-after-render --no-boot-animation | grep -q "Recent Invocations"
   loom enter --start jobs --exit-after-render --no-boot-animation | grep -q "Index Queue"
   loom enter --start nodes --exit-after-render --no-boot-animation | grep -q "Sync Status"
   loom enter --start capabilities --exit-after-render --no-boot-animation | grep -q "Provider Advertisements"'

assert_remote "capabilities portal reflects live provider and capability data" \
  'loom enter --start capabilities --exit-after-render --no-boot-animation >/tmp/loom-v0-2-1-slice-02-capabilities.out
   loom providers list --json >/tmp/loom-v0-2-1-slice-02-providers.json
   loom capabilities list --json >/tmp/loom-v0-2-1-slice-02-capabilities.json
   ! grep -q "Capability forms are preview-only" /tmp/loom-v0-2-1-slice-02-capabilities.out
   provider="$(jq -r "(.data // [])[0] | (.display_name // .compact_address // .provider_key // .provider_id // empty)" /tmp/loom-v0-2-1-slice-02-providers.json)"
   capability="$(jq -r "(.data // [])[0] | (.display_name // .compact_address // .class_name // .capability_endpoint_id // empty)" /tmp/loom-v0-2-1-slice-02-capabilities.json)"
   if [ -n "$provider" ]; then
     grep -Fq "$provider" /tmp/loom-v0-2-1-slice-02-capabilities.out
   else
     grep -q "No providers returned by the backend." /tmp/loom-v0-2-1-slice-02-capabilities.out
   fi
   if [ -n "$capability" ]; then
     grep -Fq "$capability" /tmp/loom-v0-2-1-slice-02-capabilities.out
   else
     grep -q "No capabilities returned by the backend." /tmp/loom-v0-2-1-slice-02-capabilities.out
   fi'

assert_remote "background portal reflects live worker data" \
  'loom enter --start background --exit-after-render --no-boot-animation >/tmp/loom-v0-2-1-slice-02-background.out
   loom workers list --json >/tmp/loom-v0-2-1-slice-02-workers.json
   worker="$(jq -r "(.data // [])[0].worker_key // empty" /tmp/loom-v0-2-1-slice-02-workers.json)"
   if [ -n "$worker" ]; then
     grep -Fq "$worker" /tmp/loom-v0-2-1-slice-02-background.out
   else
     grep -q "Worker Groups" /tmp/loom-v0-2-1-slice-02-background.out
   fi'

assert_remote "automation portal reflects live schedule data when present" \
  'loom enter --start automations --exit-after-render --no-boot-animation >/tmp/loom-v0-2-1-slice-02-automations.out
   loom schedules list --json >/tmp/loom-v0-2-1-slice-02-schedules.json
   schedule="$(jq -r "(.data // [])[0] | (.display_name // .schedule_key // .schedule_id // empty)" /tmp/loom-v0-2-1-slice-02-schedules.json)"
   grep -q "Automations" /tmp/loom-v0-2-1-slice-02-automations.out
   grep -q "Integrations" /tmp/loom-v0-2-1-slice-02-automations.out
   grep -q "Recent Schedule Fires" /tmp/loom-v0-2-1-slice-02-automations.out
   grep -q "Recent Direct Events" /tmp/loom-v0-2-1-slice-02-automations.out
   grep -q "Recent Invocations" /tmp/loom-v0-2-1-slice-02-automations.out
   if [ -n "$schedule" ]; then
     grep -Fq "$schedule" /tmp/loom-v0-2-1-slice-02-automations.out
   fi'

assert_remote "jobs portal exposes live job, runner, and index sections" \
  'loom enter --start jobs --exit-after-render --no-boot-animation >/tmp/loom-v0-2-1-slice-02-jobs.out
   grep -q "Recent Jobs" /tmp/loom-v0-2-1-slice-02-jobs.out
   grep -q "Queued Jobs" /tmp/loom-v0-2-1-slice-02-jobs.out
   grep -q "Failed Jobs" /tmp/loom-v0-2-1-slice-02-jobs.out
   grep -q "Runners" /tmp/loom-v0-2-1-slice-02-jobs.out
   grep -q "Index Queue" /tmp/loom-v0-2-1-slice-02-jobs.out
   grep -q "Index Failures" /tmp/loom-v0-2-1-slice-02-jobs.out'

assert_remote "nodes portal reflects live watched-root data when present" \
  'loom enter --start nodes --exit-after-render --no-boot-animation >/tmp/loom-v0-2-1-slice-02-nodes.out
   loom watched-roots status --json >/tmp/loom-v0-2-1-slice-02-watched-roots.json
   root="$(jq -r "(.data // [])[0].root.root_key // empty" /tmp/loom-v0-2-1-slice-02-watched-roots.json)"
   grep -q "Watched Root Backups" /tmp/loom-v0-2-1-slice-02-nodes.out
   grep -q "Backup Batches" /tmp/loom-v0-2-1-slice-02-nodes.out
   grep -q "Sync Status" /tmp/loom-v0-2-1-slice-02-nodes.out
   grep -q "Sync Batches" /tmp/loom-v0-2-1-slice-02-nodes.out
   grep -q "Sync Conflicts" /tmp/loom-v0-2-1-slice-02-nodes.out
   grep -q "Sync Replicas" /tmp/loom-v0-2-1-slice-02-nodes.out
   grep -q "Private Backups" /tmp/loom-v0-2-1-slice-02-nodes.out
   grep -q "Deletion Requests" /tmp/loom-v0-2-1-slice-02-nodes.out
   if [ -n "$root" ]; then
     grep -Fq "$root" /tmp/loom-v0-2-1-slice-02-nodes.out
   fi'

assert_remote "portal no-color one-shot backend screen contains no ANSI" \
  'NO_COLOR=1 LOOM_NO_ANIMATION=1 loom enter --start capabilities --exit-after-render --no-boot-animation >/tmp/loom-v0-2-1-slice-02-no-color.out
   grep -q "Capabilities And Providers" /tmp/loom-v0-2-1-slice-02-no-color.out
   ! grep -q "$(printf "\033")" /tmp/loom-v0-2-1-slice-02-no-color.out'

log "completed $pass_count checks"
