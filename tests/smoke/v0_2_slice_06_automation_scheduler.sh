#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." && pwd)"

HOST="${LOOM_DEV_HOST:-loom-dev}"
REMOTE_DIR="${LOOM_DEV_REMOTE_DIR:-/srv/loom/current}"
RUN_ID="$(date +%Y%m%d%H%M%S)-$RANDOM"
TOKEN_ID="${RUN_ID//-/}"
SCHEDULE_KEY="s06_${TOKEN_ID}"
SMOKE_NAME="v0.2 Slice 06 Smoke $RUN_ID"
UNIQUE_TOKEN="slicesixautomationv02slice06$TOKEN_ID"
CREATE_JSON="/tmp/loom-v0-2-slice-06-create-$TOKEN_ID.json"
SCHEDULER_ONE_JSON="/tmp/loom-v0-2-slice-06-scheduler-one-$TOKEN_ID.json"
SCHEDULER_TWO_JSON="/tmp/loom-v0-2-slice-06-scheduler-two-$TOKEN_ID.json"
DISPATCHER_ONE_JSON="/tmp/loom-v0-2-slice-06-dispatcher-one-$TOKEN_ID.json"
DISPATCHER_TWO_JSON="/tmp/loom-v0-2-slice-06-dispatcher-two-$TOKEN_ID.json"
FIRES_ONE_JSON="/tmp/loom-v0-2-slice-06-fires-one-$TOKEN_ID.json"
FIRES_TWO_JSON="/tmp/loom-v0-2-slice-06-fires-two-$TOKEN_ID.json"
FIRES_MANUAL_JSON="/tmp/loom-v0-2-slice-06-fires-manual-$TOKEN_ID.json"
INVOCATION_JSON="/tmp/loom-v0-2-slice-06-invocation-$TOKEN_ID.json"
MANUAL_JSON="/tmp/loom-v0-2-slice-06-manual-$TOKEN_ID.json"
STATUS_JSON="/tmp/loom-v0-2-slice-06-status-$TOKEN_ID.json"
INPUT_JSON="{\"smoke\":\"v0_2_slice_06\",\"token\":\"$UNIQUE_TOKEN\"}"
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

run_worker_once_json() {
  local worker="$1"
  local output_file="$2"
  local key_prefix="$3"

  remote "rm -f '$output_file' '$output_file.err'; for i in \$(seq 1 10); do if loom worker run '$worker' --once --json --idempotency-key '${key_prefix}_'\$i > '$output_file' 2> '$output_file.err'; then exit 0; fi; sleep 1; done; cat '$output_file.err' >&2; test -s '$output_file' && cat '$output_file' >&2; exit 1"
  pass "ran $worker once"
}

drive_invocation_succeeded() {
  local invocation_id="$1"
  local output_file="$2"
  local key_prefix="$3"
  local label="$4"

  remote "for i in \$(seq 1 60); do loom invocation inspect '$invocation_id' --json > '$output_file'; status=\$(jq -r '.data.status' '$output_file'); if [ \"\$status\" = succeeded ]; then exit 0; fi; case \"\$status\" in failed|timed_out|requires_manual_action|cancelled) cat '$output_file' >&2; exit 1;; esac; loom worker run main.automation_dispatcher --once --json --idempotency-key '${key_prefix}_'\$i > '/tmp/${key_prefix}_dispatcher_'\$i'.json' 2> '/tmp/${key_prefix}_dispatcher_'\$i'.err' || true; sleep 1; done; cat '$output_file' >&2; exit 1"
  pass "$label"
}

log "target: $HOST"
log "run: $RUN_ID"
log "schedule key: $SCHEDULE_KEY"
log "unique token: $UNIQUE_TOKEN"

check_remote "SSH target reachable" 'hostname'
check_remote "loomd service active" 'systemctl is-active loomd'
check_remote "loom CLI installed" 'command -v loom && loom version'
check_remote "jq available for smoke assertions" 'command -v jq'

check_json "health reports automation scheduler migration" \
  'loom health --json' \
  '.ok == true and .data.status == "ok" and .data.checks.migrations.current_version >= 21'

remote "loom workers list --json --correlation-id corr_smoke_v0_2_slice_06_workers > /tmp/loom-v0-2-slice-06-workers-$TOKEN_ID.json"
check_json "worker list includes automation scheduler and dispatcher" \
  "cat /tmp/loom-v0-2-slice-06-workers-$TOKEN_ID.json" \
  '.ok == true
   and ([.data[] | select(.worker_key == "main.automation_scheduler" and .worker_kind == "automation_scheduler")] | length) == 1
   and ([.data[] | select(.worker_key == "main.automation_dispatcher" and .worker_kind == "automation_dispatcher")] | length) == 1'

check_json "automation scheduler is inspectable" \
  'loom worker inspect main.automation_scheduler --json --correlation-id corr_smoke_v0_2_slice_06_scheduler_inspect' \
  '.ok == true and .data.instance.worker_key == "main.automation_scheduler" and .data.instance.enabled == true'

check_json "automation dispatcher is inspectable" \
  'loom worker inspect main.automation_dispatcher --json --correlation-id corr_smoke_v0_2_slice_06_dispatcher_inspect' \
  '.ok == true and .data.instance.worker_key == "main.automation_dispatcher" and .data.instance.enabled == true'

remote "loom schedules create --key '$SCHEDULE_KEY' --name '$SMOKE_NAME' --description 'v0.2 Slice 06 automation scheduler smoke' --target main@system.status.read --input-json '$INPUT_JSON' --one-shot now --misfire run_if_late_within --lateness-window 5m --timeout 30s --json --correlation-id corr_smoke_v0_2_slice_06_create > '$CREATE_JSON'"
check_json "created one-shot schedule" \
  "cat $CREATE_JSON" \
  ".ok == true
   and .data.schedule.schedule_key == \"$SCHEDULE_KEY\"
   and .data.schedule.schedule_kind == \"one_shot\"
   and .data.schedule.status == \"active\"
   and .data.schedule.target_profile_json.capability_ref == \"main@system.status.read\""

schedule_id="$(json_value "cat $CREATE_JSON" '.data.schedule.schedule_id')"
automation_id="$(json_value "cat $CREATE_JSON" '.data.automation.automation_id')"
[[ "$schedule_id" == schedule_* ]] || fail "expected schedule id, got $schedule_id"
[[ "$automation_id" == automation_* ]] || fail "expected automation id, got $automation_id"
pass "captured schedule and automation ids"

check_json "automation list includes smoke schedule automation" \
  "loom automations list --json --correlation-id corr_smoke_v0_2_slice_06_automations" \
  ".ok == true and ([.data[] | select(.automation_id == \"$automation_id\" and .source_kind == \"schedule\")] | length) == 1"

check_json "automation inspect links schedule" \
  "loom automation inspect '$automation_id' --json --correlation-id corr_smoke_v0_2_slice_06_automation_inspect" \
  ".ok == true and .data.automation.automation_id == \"$automation_id\" and ([.data.schedules[] | select(.schedule_id == \"$schedule_id\")] | length) == 1"

run_worker_once_json "main.automation_scheduler" "$SCHEDULER_ONE_JSON" "smoke_v0_2_slice_06_scheduler_one_$TOKEN_ID"
check_json "scheduler run returns structured result" \
  "cat $SCHEDULER_ONE_JSON" \
  '.ok == true
   and .data.run.run_status == "succeeded"
   and .data.run.result_summary_json.schema_version == "automation_scheduler.result.v0.2"'

remote "loom schedule fires '$SCHEDULE_KEY' --json --correlation-id corr_smoke_v0_2_slice_06_fires_one > '$FIRES_ONE_JSON'"
check_json "scheduler created exactly one fire for smoke schedule" \
  "cat $FIRES_ONE_JSON" \
  '.ok == true
   and (.data | length) == 1
   and (.data[0].status == "invocation_created" or .data[0].status == "completed")
   and (.data[0].invocation_id | startswith("invocation_"))'

fire_id="$(json_value "cat $FIRES_ONE_JSON" '.data[0].schedule_fire_id')"
invocation_id="$(json_value "cat $FIRES_ONE_JSON" '.data[0].invocation_id')"
[[ "$fire_id" == schedule_fire_* ]] || fail "expected schedule fire id, got $fire_id"
[[ "$invocation_id" == invocation_* ]] || fail "expected invocation id, got $invocation_id"
pass "captured scheduler fire and invocation ids"

run_worker_once_json "main.automation_scheduler" "$SCHEDULER_TWO_JSON" "smoke_v0_2_slice_06_scheduler_two_$TOKEN_ID"
remote "loom schedule fires '$SCHEDULE_KEY' --json --correlation-id corr_smoke_v0_2_slice_06_fires_two > '$FIRES_TWO_JSON'"
check_json "second scheduler pass does not duplicate one-shot fire" \
  "cat $FIRES_TWO_JSON" \
  ".ok == true and ([.data[] | select(.schedule_fire_id == \"$fire_id\")] | length) == 1 and (.data | length) == 1"

run_worker_once_json "main.automation_dispatcher" "$DISPATCHER_ONE_JSON" "smoke_v0_2_slice_06_dispatcher_one_$TOKEN_ID"
check_json "dispatcher run returns structured result" \
  "cat $DISPATCHER_ONE_JSON" \
  '.ok == true
   and .data.run.run_status == "succeeded"
   and .data.run.result_summary_json.schema_version == "automation_dispatcher.result.v0.2"'

drive_invocation_succeeded "$invocation_id" "$INVOCATION_JSON" "smoke_v0_2_slice_06_drive_first_$TOKEN_ID" "scheduler-created invocation succeeded"
route_id="$(json_value "cat $INVOCATION_JSON" '.data.route_id // ""')"
call_id="$(json_value "cat $INVOCATION_JSON" '.data.capability_call_id // ""')"
[[ "$route_id" == route_* ]] || fail "expected route id, got $route_id"
[[ "$call_id" == capability_call_* ]] || fail "expected capability call id, got $call_id"
check_json "invocation links completed routing records" \
  "cat $INVOCATION_JSON" \
  ".ok == true
   and .data.invocation_id == \"$invocation_id\"
   and .data.status == \"succeeded\"
   and .data.route_id == \"$route_id\"
   and .data.capability_call_id == \"$call_id\""

check_json "route inspect resolves scheduler-created route" \
  "loom route inspect '$route_id' --json --correlation-id corr_smoke_v0_2_slice_06_route" \
  ".ok == true and .data.route_id == \"$route_id\" and .data.status == \"completed\""

check_json "capability-call inspect resolves scheduler-created call" \
  "loom capability-call inspect '$call_id' --json --correlation-id corr_smoke_v0_2_slice_06_call" \
  ".ok == true and .data.capability_call_id == \"$call_id\" and .data.status == \"completed\""

remote "loom schedule fire '$SCHEDULE_KEY' --reason 'slice 06 smoke manual fire' --idempotency-key 'smoke_v0_2_slice_06_manual_$TOKEN_ID' --json --correlation-id corr_smoke_v0_2_slice_06_manual > '$MANUAL_JSON'"
check_json "manual schedule fire creates pending invocation" \
  "cat $MANUAL_JSON" \
  ".ok == true
   and .data.schedule.schedule_key == \"$SCHEDULE_KEY\"
   and .data.fire.status == \"invocation_created\"
   and .data.invocation.status == \"pending\"
   and (.data.fire.schedule_fire_id | startswith(\"schedule_fire_\"))
   and (.data.invocation.invocation_id | startswith(\"invocation_\"))"

manual_fire_id="$(json_value "cat $MANUAL_JSON" '.data.fire.schedule_fire_id')"
manual_invocation_id="$(json_value "cat $MANUAL_JSON" '.data.invocation.invocation_id')"
[[ "$manual_fire_id" == schedule_fire_* ]] || fail "expected manual fire id, got $manual_fire_id"
[[ "$manual_invocation_id" == invocation_* ]] || fail "expected manual invocation id, got $manual_invocation_id"
pass "captured manual fire and invocation ids"

run_worker_once_json "main.automation_dispatcher" "$DISPATCHER_TWO_JSON" "smoke_v0_2_slice_06_dispatcher_two_$TOKEN_ID"
drive_invocation_succeeded "$manual_invocation_id" "$INVOCATION_JSON" "smoke_v0_2_slice_06_drive_manual_$TOKEN_ID" "manual invocation succeeded"

remote "loom schedule fires '$SCHEDULE_KEY' --json --correlation-id corr_smoke_v0_2_slice_06_fires_manual > '$FIRES_MANUAL_JSON'"
check_json "manual fire is visible after dispatch" \
  "cat $FIRES_MANUAL_JSON" \
  ".ok == true
   and ([.data[] | select(.schedule_fire_id == \"$manual_fire_id\" and .status == \"completed\")] | length) == 1
   and ([.data[] | select(.schedule_fire_id == \"$fire_id\")] | length) == 1"

remote "loom schedules status --json --correlation-id corr_smoke_v0_2_slice_06_status > '$STATUS_JSON'"
check_json "schedule status summary exposes counts and worker keys" \
  "cat $STATUS_JSON" \
  '.ok == true
   and .data.scheduler_worker_key == "main.automation_scheduler"
   and .data.dispatcher_worker_key == "main.automation_dispatcher"
   and (.data.active_schedule_count | type) == "number"
   and (.data.completed_schedule_count | type) == "number"
   and (.data.missed_fire_count | type) == "number"
   and (.data.pending_invocation_count | type) == "number"
   and (.data.failed_invocation_count | type) == "number"'

check_json "invocation list includes smoke invocations" \
  "loom invocations list --automation '$automation_id' --json --correlation-id corr_smoke_v0_2_slice_06_invocations" \
  ".ok == true
   and ([.data[] | select(.invocation_id == \"$invocation_id\" and .status == \"succeeded\")] | length) == 1
   and ([.data[] | select(.invocation_id == \"$manual_invocation_id\" and .status == \"succeeded\")] | length) == 1"

check_json "invocation failures list is structured and failure-only" \
  'loom invocations failures --json --correlation-id corr_smoke_v0_2_slice_06_failures' \
  '.ok == true
   and (.data | type) == "array"
   and ([.data[] | select(.status != "failed" and .status != "timed_out" and .status != "requires_manual_action" and .status != "waiting_approval")] | length) == 0'

check_json "automation scheduler health is inspectable after run" \
  'loom worker inspect main.automation_scheduler --json --correlation-id corr_smoke_v0_2_slice_06_scheduler_health' \
  '.ok == true
   and .data.instance.worker_key == "main.automation_scheduler"
   and (.data.health.health_status == "healthy" or .data.health.health_status == "running")'

check_json "automation dispatcher health is inspectable after run" \
  'loom worker inspect main.automation_dispatcher --json --correlation-id corr_smoke_v0_2_slice_06_dispatcher_health' \
  '.ok == true
   and .data.instance.worker_key == "main.automation_dispatcher"
   and (.data.health.health_status == "healthy" or .data.health.health_status == "running")'

check_json "automation scheduler run history is visible" \
  'loom worker runs main.automation_scheduler --json --correlation-id corr_smoke_v0_2_slice_06_scheduler_runs' \
  '.ok == true and (.data | length) >= 1'

check_json "automation dispatcher run history is visible" \
  'loom worker runs main.automation_dispatcher --json --correlation-id corr_smoke_v0_2_slice_06_dispatcher_runs' \
  '.ok == true and (.data | length) >= 1'

if grep -R "psql\|QueryContext\|QueryRowContext" "$REPO_ROOT/internal/loomcli" >/dev/null 2>&1; then
  fail "local loom CLI contains direct PostgreSQL access"
fi
pass "local CLI does not query PostgreSQL directly"

check_remote "remote CLI does not query PostgreSQL directly" \
  "if grep -R \"psql\\|QueryContext\\|QueryRowContext\" '$REMOTE_DIR/internal/loomcli' >/dev/null 2>&1; then exit 1; fi"

log "completed $pass_count checks"
