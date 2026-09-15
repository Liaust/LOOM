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

assert_remote "main health ok and migrated for CLI portal" \
  'loom health --json | jq -e ".ok == true and .data.status == \"ok\" and .data.checks.migrations.current_version >= 25" >/dev/null'

assert_remote "enter help exposes portal flags" \
  'loom enter --help | grep -q -- "--start" && loom enter --help | grep -q -- "--exit-after-render"'

assert_remote "completion scripts generate output" \
  'loom completion zsh >/tmp/loom-completion.zsh && test -s /tmp/loom-completion.zsh && loom completion bash >/tmp/loom-completion.bash && test -s /tmp/loom-completion.bash'

assert_remote "raw machine commands remain clean" \
  'loom health --json | jq -e ".ok == true" >/dev/null && test "$(loom status --plain | wc -l)" -eq 1 && LOOM_NONINTERACTIVE=1 loom health --json | jq -e ".ok == true" >/dev/null'

assert_remote "no-color raw outputs contain no ANSI" \
  'NO_COLOR=1 loom workers list >/tmp/loom-workers-no-color.out && ! grep -q "$(printf "\033")" /tmp/loom-workers-no-color.out && LOOM_NO_COLOR=1 loom status >/tmp/loom-status-no-color.out && ! grep -q "$(printf "\033")" /tmp/loom-status-no-color.out'

assert_remote "portal rejects noninteractive mode without prompt or ANSI" \
  'set +e
   LOOM_NONINTERACTIVE=1 loom enter >/tmp/loom-enter-noninteractive.out 2>/tmp/loom-enter-noninteractive.err
   status=$?
   set -e
   test "$status" -ne 0
   test ! -s /tmp/loom-enter-noninteractive.out
   grep -q "portal.unavailable" /tmp/loom-enter-noninteractive.err
   ! grep -q "$(printf "\033")" /tmp/loom-enter-noninteractive.err'

assert_remote "portal rejects dumb terminal without terminal corruption" \
  'set +e
   TERM=dumb loom enter >/tmp/loom-enter-dumb.out 2>/tmp/loom-enter-dumb.err
   status=$?
   set -e
   test "$status" -ne 0
   test ! -s /tmp/loom-enter-dumb.out
   grep -q "portal.unavailable" /tmp/loom-enter-dumb.err
   ! grep -q "$(printf "\033")" /tmp/loom-enter-dumb.err'

assert_remote "portal rejects JSON machine mode as JSON" \
  'set +e
   loom --json enter >/tmp/loom-enter-json.out 2>/tmp/loom-enter-json.err
   status=$?
   set -e
   test "$status" -ne 0
   test ! -s /tmp/loom-enter-json.err
   jq -e ".ok == false and .error.code == \"portal.machine_mode_unsupported\"" /tmp/loom-enter-json.out >/dev/null'

assert_remote "portal renders operational screens" \
  'loom enter --start home --exit-after-render | grep -q "Attention"
   loom enter --start background --exit-after-render | grep -q "Worker Groups"
   loom enter --start automations --exit-after-render | grep -q "Automation Center"
   loom enter --start jobs --exit-after-render | grep -q "Jobs And Search"
   loom enter --start nodes --exit-after-render | grep -q "Nodes And Watched Roots"
   loom enter --start capabilities --exit-after-render | grep -q "Capabilities And Providers"'

assert_remote "portal fuzzy render paths find core domains" \
  'loom enter --search workers --exit-after-render | grep -q "Background Operations"
   loom enter --search automation --exit-after-render | grep -q "Automation Center"
   loom enter --search "direct event" --exit-after-render | grep -q "Automation Center"
   loom enter --search "watched root" --exit-after-render | grep -q "Nodes And Watched Roots"
   loom enter --search backup --exit-after-render | grep -q "Backup"
   loom enter --search index --exit-after-render | grep -q "Index"'

assert_remote "portal action preview shows raw command" \
  'loom enter --preview-action raw.workers.list --exit-after-render | grep -q "Raw: loom workers list"'

assert_remote "portal confirmed safe action runs through worker API" \
  'loom enter --run-action worker.selfcheck.run_once --confirm --exit-after-render | tee /tmp/loom-enter-run-action.out | grep -q "Action Complete" && grep -q "Run: worker_run_" /tmp/loom-enter-run-action.out'

log "completed $pass_count checks"
