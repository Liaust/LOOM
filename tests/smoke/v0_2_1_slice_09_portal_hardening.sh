#!/usr/bin/env bash
set -euo pipefail

HOST="${LOOM_DEV_HOST:-loom-dev}"
REMOTE_DIR="${LOOM_DEV_REMOTE_DIR:-/srv/loom/current}"
RUN_ID="$(date +%Y%m%d%H%M%S)-$RANDOM"
TOKEN_ID="${RUN_ID//-/}"
SMOKE_SLUG="v0-2-1-slice-09-$RUN_ID"
SMOKE_NAME="v0.2.1 Slice 09 Smoke $RUN_ID"
INGEST_PATH="$REMOTE_DIR/tests/smoke/v0_2_1_slice_09_$RUN_ID.md"
OBJECT_NAME="v0-2-1-slice-09-portal-hardening.md"
UNIQUE_TOKEN="v021slice09portal$TOKEN_ID"
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
log "unique token: $UNIQUE_TOKEN"

check_remote "loomd service active" 'systemctl is-active loomd'
check_json "main health ok" \
  'loom health --json' \
  '.ok == true and .data.status == "ok"'

check_json "raw status JSON remains machine-clean" \
  'loom status --json' \
  '.ok == true and .data.status == "ok"'

check_json "raw workers JSON remains machine-clean" \
  'loom workers list --json' \
  '.ok == true and (.data | type) == "array"'

check_json "raw capabilities JSON remains machine-clean" \
  'loom capabilities list --json' \
  '.ok == true and (.data | type) == "array"'

check_json "raw providers JSON remains machine-clean" \
  'loom providers list --json' \
  '.ok == true and (.data | type) == "array"'

check_json "raw schedules JSON remains machine-clean" \
  'loom schedules list --json' \
  '.ok == true and (.data | type) == "array"'

check_json "raw jobs JSON remains machine-clean" \
  'loom jobs list --json' \
  '.ok == true and (.data | type) == "array"'

check_json "raw objects JSON remains machine-clean" \
  'loom object list --json' \
  '.ok == true and (.data | type) == "array"'

check_json "raw indexes JSON remains machine-clean" \
  'loom indexes status --json' \
  '.ok == true and (.data | type) == "array"'

check_json "raw nodes JSON remains machine-clean" \
  'loom node list --json' \
  '.ok == true and (.data | type) == "array"'

check_remote "portal help remains human-readable and raw-cli safe" \
  "NO_COLOR=1 loom enter --help > /tmp/loom-v0-2-1-slice-09-enter-help.out
   grep -q 'Open the LOOM terminal portal' /tmp/loom-v0-2-1-slice-09-enter-help.out
   grep -q -- '--exit-after-render' /tmp/loom-v0-2-1-slice-09-enter-help.out
   ! grep -q \"\$(printf '\\033')\" /tmp/loom-v0-2-1-slice-09-enter-help.out"

check_remote "portal rejects JSON machine mode without stderr" \
  "set +e
   loom --json enter > /tmp/loom-v0-2-1-slice-09-json-enter.out 2> /tmp/loom-v0-2-1-slice-09-json-enter.err
   status=\$?
   set -e
   test \$status -ne 0
   test ! -s /tmp/loom-v0-2-1-slice-09-json-enter.err
   jq -e '.ok == false and .error.code == \"portal.machine_mode_unsupported\"' /tmp/loom-v0-2-1-slice-09-json-enter.out >/dev/null"

check_remote "portal rejects plain machine mode without ANSI" \
  "set +e
   loom --plain enter > /tmp/loom-v0-2-1-slice-09-plain-enter.out 2> /tmp/loom-v0-2-1-slice-09-plain-enter.err
   status=\$?
   set -e
   test \$status -ne 0
   grep -q 'portal.machine_mode_unsupported' /tmp/loom-v0-2-1-slice-09-plain-enter.err
   ! grep -q \"\$(printf '\\033')\" /tmp/loom-v0-2-1-slice-09-plain-enter.err"

check_remote "portal rejects noninteractive mode without ANSI" \
  "set +e
   LOOM_NONINTERACTIVE=1 loom enter > /tmp/loom-v0-2-1-slice-09-noninteractive.out 2> /tmp/loom-v0-2-1-slice-09-noninteractive.err
   status=\$?
   set -e
   test \$status -ne 0
   test ! -s /tmp/loom-v0-2-1-slice-09-noninteractive.out
   grep -q 'portal.unavailable' /tmp/loom-v0-2-1-slice-09-noninteractive.err
   ! grep -q \"\$(printf '\\033')\" /tmp/loom-v0-2-1-slice-09-noninteractive.err"

for screen in home database background automations jobs nodes capabilities; do
  check_remote "portal renders $screen screen with no raw commands by default" \
    "NO_COLOR=1 LOOM_NO_ANIMATION=1 loom enter --start '$screen' --exit-after-render --no-boot-animation > /tmp/loom-v0-2-1-slice-09-screen-$screen.out
     test -s /tmp/loom-v0-2-1-slice-09-screen-$screen.out
     ! grep -q \"\$(printf '\\033')\" /tmp/loom-v0-2-1-slice-09-screen-$screen.out
     ! grep -q 'Raw: loom' /tmp/loom-v0-2-1-slice-09-screen-$screen.out
     ! grep -q 'Section Commands' /tmp/loom-v0-2-1-slice-09-screen-$screen.out"
done

check_remote "capabilities screen renders live providers and capabilities" \
  "NO_COLOR=1 LOOM_NO_ANIMATION=1 loom enter --start capabilities --exit-after-render --no-boot-animation > /tmp/loom-v0-2-1-slice-09-capabilities.out
   grep -q 'Capabilities And Providers' /tmp/loom-v0-2-1-slice-09-capabilities.out
   grep -q 'Capability Explorer' /tmp/loom-v0-2-1-slice-09-capabilities.out
   grep -q 'main' /tmp/loom-v0-2-1-slice-09-capabilities.out
   ! grep -q 'No capabilities returned by the backend' /tmp/loom-v0-2-1-slice-09-capabilities.out"

check_remote "background portal reflects raw worker inventory" \
  "NO_COLOR=1 LOOM_NO_ANIMATION=1 loom enter --start background --exit-after-render --no-boot-animation > /tmp/loom-v0-2-1-slice-09-background-live.out
   grep -Eq 'main.worker_selfcheck|Worker Selfcheck' /tmp/loom-v0-2-1-slice-09-background-live.out"

check_remote "write database portal hardening object fixture" \
  "mkdir -p '$REMOTE_DIR/tests/smoke' && printf '# v0.2.1 Slice 09 Portal Hardening\n\nPortal hardening database token: $UNIQUE_TOKEN.\n' > '$INGEST_PATH' && chmod 0644 '$INGEST_PATH' && sudo -n -u loom test -r '$INGEST_PATH'"

check_json "create slice 09 smoke project" \
  "loom project create '$SMOKE_NAME' --slug '$SMOKE_SLUG' --description 'v0.2.1 Slice 09 portal hardening smoke' --if-not-exists --json --correlation-id corr_smoke_v0_2_1_slice_09_project" \
  ".ok == true and .data.project.project.slug == \"$SMOKE_SLUG\""

remote "loom object ingest '$INGEST_PATH' --project '$SMOKE_SLUG' --name '$OBJECT_NAME' --json --correlation-id corr_smoke_v0_2_1_slice_09_ingest > /tmp/loom-v0-2-1-slice-09-ingest.json"
check_json "slice 09 object ingest succeeds" \
  'cat /tmp/loom-v0-2-1-slice-09-ingest.json' \
  '.ok == true and (.data.object.object.object_id | startswith("object_"))'

object_id="$(json_value 'cat /tmp/loom-v0-2-1-slice-09-ingest.json' '.data.object.object.object_id')"
[[ "$object_id" == object_* ]] || fail "expected object id, got $object_id"
pass "captured object id"

remote "loom indexes run text --once --json --correlation-id corr_smoke_v0_2_1_slice_09_indexer > /tmp/loom-v0-2-1-slice-09-indexer.json"
check_json "text indexer processes slice 09 object" \
  'cat /tmp/loom-v0-2-1-slice-09-indexer.json' \
  '.ok == true and .data.run.run_status == "succeeded"'

check_remote "database search portal renders indexed object" \
  "for attempt in 1 2 3 4 5 6 7 8; do
     if NO_COLOR=1 LOOM_NO_ANIMATION=1 loom enter --database-search '$UNIQUE_TOKEN' --exit-after-render --no-boot-animation > /tmp/loom-v0-2-1-slice-09-database-search.out && grep -q '$object_id' /tmp/loom-v0-2-1-slice-09-database-search.out; then
       exit 0
     fi
     loom indexes run text --once --json --correlation-id corr_smoke_v0_2_1_slice_09_indexer_retry_\$attempt >/tmp/loom-v0-2-1-slice-09-indexer-retry.json || true
     sleep 1
   done
   grep -q '$object_id' /tmp/loom-v0-2-1-slice-09-database-search.out"

check_remote "portal navigation search renders only navigation categories" \
  "NO_COLOR=1 LOOM_NO_ANIMATION=1 loom enter --search workers --exit-after-render --no-boot-animation > /tmp/loom-v0-2-1-slice-09-search-workers.out
   grep -q 'Command Palette' /tmp/loom-v0-2-1-slice-09-search-workers.out
   grep -q '\\[screen\\]' /tmp/loom-v0-2-1-slice-09-search-workers.out
   ! grep -q '\\[worker\\]' /tmp/loom-v0-2-1-slice-09-search-workers.out"

check_remote "capability addresses appear through scoped search" \
  "NO_COLOR=1 LOOM_NO_ANIMATION=1 loom enter --start capabilities --search '#capabilities main@system.status' --exit-after-render --no-boot-animation > /tmp/loom-v0-2-1-slice-09-search-capability.out
   grep -q 'Command Palette' /tmp/loom-v0-2-1-slice-09-search-capability.out
   grep -q '\\[capability\\]' /tmp/loom-v0-2-1-slice-09-search-capability.out
   grep -q 'main@system.status.read' /tmp/loom-v0-2-1-slice-09-search-capability.out"

check_remote "worker actions appear only through scoped search" \
  "NO_COLOR=1 LOOM_NO_ANIMATION=1 loom enter --search '#workers run worker selfcheck' --exit-after-render --no-boot-animation > /tmp/loom-v0-2-1-slice-09-search-worker-action.out
   grep -q 'Command Palette' /tmp/loom-v0-2-1-slice-09-search-worker-action.out
   grep -q '\\[worker\\]' /tmp/loom-v0-2-1-slice-09-search-worker-action.out
   grep -q 'Run Worker Selfcheck Once' /tmp/loom-v0-2-1-slice-09-search-worker-action.out
   ! grep -q 'Raw: loom' /tmp/loom-v0-2-1-slice-09-search-worker-action.out"

remote "loom workers list --json --correlation-id corr_smoke_v0_2_1_slice_09_workers > /tmp/loom-v0-2-1-slice-09-workers.json"
worker_key="$(json_value 'cat /tmp/loom-v0-2-1-slice-09-workers.json' '[.data[] | select(.worker_key == "main.worker_selfcheck")][0].worker_key // [.data[] | select(.worker_key == "main.indexer_text")][0].worker_key // .data[0].worker_key')"
[[ -n "$worker_key" && "$worker_key" != "null" ]] || fail "could not discover worker key"
pass "captured worker key"

check_remote "portal action lifecycle previews gates and runs safe action" \
  "NO_COLOR=1 LOOM_NO_ANIMATION=1 loom enter --preview-action worker.selfcheck.run_once --exit-after-render --no-boot-animation > /tmp/loom-v0-2-1-slice-09-action-preview.out
   grep -q 'Action Preview' /tmp/loom-v0-2-1-slice-09-action-preview.out
   grep -q 'Run Worker Selfcheck Once' /tmp/loom-v0-2-1-slice-09-action-preview.out
   set +e
   NO_COLOR=1 LOOM_NO_ANIMATION=1 loom enter --run-action worker.selfcheck.run_once --exit-after-render --no-boot-animation > /tmp/loom-v0-2-1-slice-09-action-confirm-required.out 2>&1
   status=\$?
   set -e
   test \$status -ne 0
   grep -q 'portal.action_confirmation_required' /tmp/loom-v0-2-1-slice-09-action-confirm-required.out
   NO_COLOR=1 LOOM_NO_ANIMATION=1 loom enter --run-action worker.selfcheck.run_once --confirm --exit-after-render --no-boot-animation > /tmp/loom-v0-2-1-slice-09-action-confirmed.out
   grep -q 'Action Complete' /tmp/loom-v0-2-1-slice-09-action-confirmed.out
   grep -Eq 'Worker|Run' /tmp/loom-v0-2-1-slice-09-action-confirmed.out
   loom worker runs main.worker_selfcheck --json --correlation-id corr_smoke_v0_2_1_slice_09_action_runs | jq -e '.ok == true and (.data | length) >= 1' >/dev/null"

check_remote "command mode preview and completion remain usable" \
  "NO_COLOR=1 LOOM_NO_ANIMATION=1 loom enter --command-preview '\$ list capabilities' --exit-after-render --no-boot-animation > /tmp/loom-v0-2-1-slice-09-command-preview.out
   grep -q 'Command Preview' /tmp/loom-v0-2-1-slice-09-command-preview.out
   grep -q 'Canonical: loom capabilities list' /tmp/loom-v0-2-1-slice-09-command-preview.out
   NO_COLOR=1 LOOM_NO_ANIMATION=1 loom enter --command-complete '\$ worker run ma' --exit-after-render --no-boot-animation > /tmp/loom-v0-2-1-slice-09-command-complete.out
   grep -q 'Completions' /tmp/loom-v0-2-1-slice-09-command-complete.out
   grep -q '$worker_key' /tmp/loom-v0-2-1-slice-09-command-complete.out"

check_remote "command mode rejects shell and recursive portal commands" \
  "NO_COLOR=1 LOOM_NO_ANIMATION=1 loom enter --command-run '\$ health | jq .' --exit-after-render --no-boot-animation > /tmp/loom-v0-2-1-slice-09-shell-block.out
   grep -q 'Command Failed' /tmp/loom-v0-2-1-slice-09-shell-block.out
   grep -q 'Shell operators are not supported' /tmp/loom-v0-2-1-slice-09-shell-block.out
   NO_COLOR=1 LOOM_NO_ANIMATION=1 loom enter --command-run '\$ enter' --exit-after-render --no-boot-animation > /tmp/loom-v0-2-1-slice-09-enter-block.out
   grep -q 'Command Failed' /tmp/loom-v0-2-1-slice-09-enter-block.out
   grep -q 'interactive portal' /tmp/loom-v0-2-1-slice-09-enter-block.out"

check_remote "command mode requires confirmation for effectful commands" \
  "set +e
   NO_COLOR=1 LOOM_NO_ANIMATION=1 loom enter --command-run '\$ worker run $worker_key --once' --exit-after-render --no-boot-animation > /tmp/loom-v0-2-1-slice-09-confirm-required.out 2>&1
   status=\$?
   set -e
   test \$status -ne 0
   grep -q 'portal.command_confirmation_required' /tmp/loom-v0-2-1-slice-09-confirm-required.out
   grep -q 'can change LOOM state' /tmp/loom-v0-2-1-slice-09-confirm-required.out"

check_remote "confirmed command mode worker run succeeds" \
  "NO_COLOR=1 LOOM_NO_ANIMATION=1 loom enter --command-run '\$ worker run $worker_key --once' --confirm --exit-after-render --no-boot-animation > /tmp/loom-v0-2-1-slice-09-confirmed-worker.out
   grep -q 'Command Complete' /tmp/loom-v0-2-1-slice-09-confirmed-worker.out
   grep -q 'r rerun' /tmp/loom-v0-2-1-slice-09-confirmed-worker.out
   grep -q 'Canonical: loom worker run $worker_key --once' /tmp/loom-v0-2-1-slice-09-confirmed-worker.out"

check_remote "narrow no-color one-shot render remains readable" \
  "COLUMNS=40 NO_COLOR=1 LOOM_NO_ANIMATION=1 loom enter --start home --exit-after-render --no-boot-animation > /tmp/loom-v0-2-1-slice-09-narrow.out
   test -s /tmp/loom-v0-2-1-slice-09-narrow.out
   grep -Eq 'LOOM|System|Attention' /tmp/loom-v0-2-1-slice-09-narrow.out
   ! grep -q \"\$(printf '\\033')\" /tmp/loom-v0-2-1-slice-09-narrow.out"

log "completed $pass_count checks"
