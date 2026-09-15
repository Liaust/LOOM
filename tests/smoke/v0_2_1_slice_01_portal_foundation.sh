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

assert_remote "main health ok for portal foundation smoke" \
  'loom health --json | jq -e ".ok == true and .data.status == \"ok\"" >/dev/null'

assert_remote "portal help exposes foundation flags" \
  'loom enter --help | grep -q -- "--start" && loom enter --help | grep -q -- "--exit-after-render" && loom enter --help | grep -q -- "--no-boot-animation"'

assert_remote "portal machine JSON mode still rejects cleanly" \
  'set +e
   loom --json enter >/tmp/loom-v0-2-1-enter-json.out 2>/tmp/loom-v0-2-1-enter-json.err
   status=$?
   set -e
   test "$status" -ne 0
   test ! -s /tmp/loom-v0-2-1-enter-json.err
   jq -e ".ok == false and .error.code == \"portal.machine_mode_unsupported\"" /tmp/loom-v0-2-1-enter-json.out >/dev/null'

assert_remote "portal noninteractive mode still rejects without ANSI" \
  'set +e
   LOOM_NONINTERACTIVE=1 loom enter >/tmp/loom-v0-2-1-enter-noninteractive.out 2>/tmp/loom-v0-2-1-enter-noninteractive.err
   status=$?
   set -e
   test "$status" -ne 0
   test ! -s /tmp/loom-v0-2-1-enter-noninteractive.out
   grep -q "portal.unavailable" /tmp/loom-v0-2-1-enter-noninteractive.err
   ! grep -q "$(printf "\033")" /tmp/loom-v0-2-1-enter-noninteractive.err'

assert_remote "portal one-shot screens render from loader spine" \
  'loom enter --start home --exit-after-render --no-boot-animation | grep -q "Attention"
   loom enter --start background --exit-after-render --no-boot-animation | grep -q "Worker Groups"
   loom enter --start automations --exit-after-render --no-boot-animation | grep -q "Direct Events"
   loom enter --start jobs --exit-after-render --no-boot-animation | grep -q "Jobs And Search"
   loom enter --start nodes --exit-after-render --no-boot-animation | grep -q "Nodes And Watched Roots"
   loom enter --start capabilities --exit-after-render --no-boot-animation | grep -q "Capabilities And Providers"'

assert_remote "portal no-color one-shot render contains no ANSI" \
  'NO_COLOR=1 LOOM_NO_ANIMATION=1 loom enter --start background --exit-after-render --no-boot-animation >/tmp/loom-v0-2-1-enter-no-color.out
   grep -q "Background Operations" /tmp/loom-v0-2-1-enter-no-color.out
   ! grep -q "$(printf "\033")" /tmp/loom-v0-2-1-enter-no-color.out'

assert_remote "portal search one-shot path still works" \
  'loom enter --search workers --exit-after-render | grep -q "Background Operations"
   loom enter --search "direct event" --exit-after-render | grep -q "Automation Center"
   loom enter --search "watched root" --exit-after-render | grep -q "Nodes And Watched Roots"'

assert_remote "portal action preview still works" \
  'loom enter --preview-action raw.workers.list --exit-after-render | grep -q "Raw: loom workers list"'

assert_remote "portal confirmed safe action still runs" \
  'loom enter --run-action worker.selfcheck.run_once --confirm --exit-after-render >/tmp/loom-v0-2-1-enter-action.out
   grep -q "Action Complete" /tmp/loom-v0-2-1-enter-action.out
   grep -q "Run: worker_run_" /tmp/loom-v0-2-1-enter-action.out'

log "completed $pass_count checks"
