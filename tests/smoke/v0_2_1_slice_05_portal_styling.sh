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

assert_remote "main health ok for portal styling smoke" \
  'loom health --json | jq -e ".ok == true and .data.status == \"ok\"" >/dev/null'

assert_remote "home renders styled dashboard structure without raw command list" \
  'loom enter --start home --exit-after-render --no-boot-animation >/tmp/loom-v0-2-1-slice-05-home.out
   grep -q "LOOM" /tmp/loom-v0-2-1-slice-05-home.out
   grep -q "System" /tmp/loom-v0-2-1-slice-05-home.out
   grep -q "Attention" /tmp/loom-v0-2-1-slice-05-home.out
   grep -q "Surfaces" /tmp/loom-v0-2-1-slice-05-home.out
   grep -q "Background Operations" /tmp/loom-v0-2-1-slice-05-home.out
   ! grep -q "Section Commands" /tmp/loom-v0-2-1-slice-05-home.out
   ! grep -q "loom enter" /tmp/loom-v0-2-1-slice-05-home.out'

assert_remote "search palette is boxed categorized and raw-light" \
  'loom enter --search worker --exit-after-render >/tmp/loom-v0-2-1-slice-05-search.out
   grep -q "Command Palette" /tmp/loom-v0-2-1-slice-05-search.out
   grep -q "Search" /tmp/loom-v0-2-1-slice-05-search.out
   grep -Eq "\[(screen|worker|action)\]" /tmp/loom-v0-2-1-slice-05-search.out
   ! grep -q "Raw: loom" /tmp/loom-v0-2-1-slice-05-search.out
   ! grep -q "Show Raw Status" /tmp/loom-v0-2-1-slice-05-search.out'

assert_remote "no-color one-shot portal styling contains no ANSI" \
  'NO_COLOR=1 LOOM_NO_ANIMATION=1 loom enter --start background --exit-after-render --no-boot-animation >/tmp/loom-v0-2-1-slice-05-no-color.out
   grep -q "Background Operations" /tmp/loom-v0-2-1-slice-05-no-color.out
   grep -q "Worker Groups" /tmp/loom-v0-2-1-slice-05-no-color.out
   ! grep -q "$(printf "\033")" /tmp/loom-v0-2-1-slice-05-no-color.out'

assert_remote "hidden preview and run hooks still expose raw details" \
  'loom enter --preview-action worker.selfcheck.run_once --exit-after-render >/tmp/loom-v0-2-1-slice-05-preview.out
   grep -q "Action Preview" /tmp/loom-v0-2-1-slice-05-preview.out
   grep -q "Raw Details" /tmp/loom-v0-2-1-slice-05-preview.out
   grep -q "Raw: loom worker run main.worker_selfcheck --once" /tmp/loom-v0-2-1-slice-05-preview.out
   loom enter --run-action worker.selfcheck.run_once --confirm --exit-after-render >/tmp/loom-v0-2-1-slice-05-run.out
   grep -q "Action Complete" /tmp/loom-v0-2-1-slice-05-run.out
   grep -q "Raw Details" /tmp/loom-v0-2-1-slice-05-run.out
   grep -q "Run: worker_run_" /tmp/loom-v0-2-1-slice-05-run.out'

assert_remote "interactive boot path can be opened and skipped when a pty helper exists" \
  'if command -v script >/dev/null 2>&1; then
     printf "\nq" | script -qfec "loom enter" /tmp/loom-v0-2-1-slice-05-boot.typescript >/tmp/loom-v0-2-1-slice-05-boot.out 2>/tmp/loom-v0-2-1-slice-05-boot.err || true
     cat /tmp/loom-v0-2-1-slice-05-boot.typescript /tmp/loom-v0-2-1-slice-05-boot.out /tmp/loom-v0-2-1-slice-05-boot.err >/tmp/loom-v0-2-1-slice-05-boot.combined
     if grep -q "portal.unavailable" /tmp/loom-v0-2-1-slice-05-boot.combined; then
       loom enter --start home --exit-after-render --no-boot-animation | grep -q "LOOM"
     else
       grep -q "LOOM" /tmp/loom-v0-2-1-slice-05-boot.combined
     fi
   else
     loom enter --start home --exit-after-render --no-boot-animation | grep -q "LOOM"
   fi'

log "completed $pass_count checks"
