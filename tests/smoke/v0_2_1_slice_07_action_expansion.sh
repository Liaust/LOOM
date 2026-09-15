#!/usr/bin/env bash
set -euo pipefail

HOST="${LOOM_DEV_HOST:-loom-dev}"
REMOTE_DIR="${LOOM_DEV_REMOTE_DIR:-/srv/loom/current}"
RUN_ID="$(date +%Y%m%d%H%M%S)-$RANDOM"
SMOKE_SLUG="v0-2-1-slice-07-$RUN_ID"
SMOKE_NAME="v0.2.1 Slice 07 Smoke $RUN_ID"
SCHEDULE_KEY="v0_2_1_slice_07_$RUN_ID"
FAIL_MANIFEST="$REMOTE_DIR/tests/smoke/scripts/fail_always/loom.script.yaml"
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

wait_job_status() {
  local job_id="$1"
  local want="$2"

  remote "for i in \$(seq 1 60); do status=\$(loom job inspect '$job_id' --json | jq -r '.data.job.status'); if [ \"\$status\" = '$want' ]; then exit 0; fi; sleep 1; done; loom job inspect '$job_id' --json >&2; exit 1"
  pass "job $job_id reached $want"
}

require_command jq
require_command ssh

log "target: $HOST"
log "run: $RUN_ID"

check_remote "loomd service active" 'systemctl is-active loomd'
check_json "main health ok" \
  'loom health --json' \
  '.ok == true and .data.status == "ok"'

remote "loom providers list --json --correlation-id corr_smoke_v0_2_1_slice_07_providers > /tmp/loom-v0-2-1-slice-07-providers.json"
provider_id="$(json_value 'cat /tmp/loom-v0-2-1-slice-07-providers.json' '.data[0].provider_id')"
[[ "$provider_id" == prov_* || "$provider_id" == provider_* ]] || fail "expected provider id, got $provider_id"
pass "captured provider id"

remote "loom capabilities list --json --correlation-id corr_smoke_v0_2_1_slice_07_capabilities > /tmp/loom-v0-2-1-slice-07-capabilities.json"
capability_id="$(json_value 'cat /tmp/loom-v0-2-1-slice-07-capabilities.json' '[.data[] | select(.compact_address == "main@system.status.read")][0].capability_endpoint_id')"
[[ "$capability_id" == endp_* || "$capability_id" == cap_* || "$capability_id" == capability_endpoint_* ]] || fail "expected system status capability endpoint id, got $capability_id"
pass "captured system status capability id"

remote "loom node list --json --correlation-id corr_smoke_v0_2_1_slice_07_nodes > /tmp/loom-v0-2-1-slice-07-nodes.json"
node_id="$(json_value 'cat /tmp/loom-v0-2-1-slice-07-nodes.json' '.data[0].node_id')"
[[ "$node_id" == node_* ]] || fail "expected node id, got $node_id"
pass "captured node id"

check_remote "capabilities screen renders action-expanded surface" \
  "loom enter --start capabilities --exit-after-render --no-boot-animation > /tmp/loom-v0-2-1-slice-07-capabilities.out
   grep -q 'Capabilities And Providers' /tmp/loom-v0-2-1-slice-07-capabilities.out
   grep -q 'Capability Explorer' /tmp/loom-v0-2-1-slice-07-capabilities.out
   grep -q 'main' /tmp/loom-v0-2-1-slice-07-capabilities.out
   ! grep -q 'No capabilities returned by the backend' /tmp/loom-v0-2-1-slice-07-capabilities.out"

check_remote "scoped capabilities search includes live address records" \
  "loom enter --start capabilities --search '#capabilities main@system.status' --exit-after-render --no-boot-animation > /tmp/loom-v0-2-1-slice-07-capability-search.out
   grep -q 'Command Palette' /tmp/loom-v0-2-1-slice-07-capability-search.out
   grep -q '\\[capability\\]' /tmp/loom-v0-2-1-slice-07-capability-search.out
   grep -q 'main@system.status.read' /tmp/loom-v0-2-1-slice-07-capability-search.out"

check_remote "provider health action runs through portal" \
  "loom enter --start capabilities --run-action 'capability.provider.${provider_id}.health' --exit-after-render > /tmp/loom-v0-2-1-slice-07-provider-health.out
   grep -q 'Action Complete' /tmp/loom-v0-2-1-slice-07-provider-health.out
   grep -q 'Provider health loaded' /tmp/loom-v0-2-1-slice-07-provider-health.out
   grep -q 'Health:' /tmp/loom-v0-2-1-slice-07-provider-health.out"

check_remote "capability usage docs action runs through portal" \
  "loom enter --start capabilities --run-action 'capability.endpoint.${capability_id}.usage_docs' --exit-after-render > /tmp/loom-v0-2-1-slice-07-usage-docs.out
   grep -q 'Action Complete' /tmp/loom-v0-2-1-slice-07-usage-docs.out
   grep -q 'Capability usage docs loaded' /tmp/loom-v0-2-1-slice-07-usage-docs.out
   grep -q 'Documents:' /tmp/loom-v0-2-1-slice-07-usage-docs.out"

check_remote "safe capability invocation runs through portal confirmation" \
  "loom enter --start capabilities --run-action 'capability.endpoint.main_system_status_read.call' --confirm --exit-after-render > /tmp/loom-v0-2-1-slice-07-capability-call.out
   grep -Eq 'Action Complete|Action Failed' /tmp/loom-v0-2-1-slice-07-capability-call.out
   grep -Eq 'Capability call was requested|Reason:' /tmp/loom-v0-2-1-slice-07-capability-call.out"

remote "loom schedules create --key '$SCHEDULE_KEY' --name '$SMOKE_NAME Schedule' --target main@system.status.read --input-json '{}' --every 24h --json --correlation-id corr_smoke_v0_2_1_slice_07_schedule > /tmp/loom-v0-2-1-slice-07-schedule.json"
schedule_id="$(json_value 'cat /tmp/loom-v0-2-1-slice-07-schedule.json' '.data.schedule.schedule_id')"
[[ "$schedule_id" == schedule_* ]] || fail "expected schedule id, got $schedule_id"
pass "created smoke schedule"

check_remote "schedule inspect and fire actions run through portal" \
  "loom enter --start automations --run-action 'automation.schedule.${schedule_id}.inspect' --exit-after-render > /tmp/loom-v0-2-1-slice-07-schedule-inspect.out
   grep -q 'Action Complete' /tmp/loom-v0-2-1-slice-07-schedule-inspect.out
   grep -q 'Schedule detail loaded' /tmp/loom-v0-2-1-slice-07-schedule-inspect.out
   loom enter --start automations --run-action 'automation.schedule.${schedule_id}.fire_now' --confirm --exit-after-render > /tmp/loom-v0-2-1-slice-07-schedule-fire.out
   grep -q 'Action Complete' /tmp/loom-v0-2-1-slice-07-schedule-fire.out
   grep -q 'Schedule fire was requested' /tmp/loom-v0-2-1-slice-07-schedule-fire.out
   fire_id=\"\$(sed -n 's/^Fire: //p' /tmp/loom-v0-2-1-slice-07-schedule-fire.out | head -n 1)\"
   test -n \"\$fire_id\"
   loom schedule fires '$schedule_id' --json | jq -e --arg fire_id \"\$fire_id\" '([.data[] | select(.schedule_fire_id == \$fire_id)] | length) == 1' >/dev/null"

check_remote "node health action runs through portal" \
  "loom enter --start nodes --run-action 'node.${node_id}.health' --exit-after-render > /tmp/loom-v0-2-1-slice-07-node-health.out
   grep -q 'Action Complete' /tmp/loom-v0-2-1-slice-07-node-health.out
   grep -q 'Node health loaded' /tmp/loom-v0-2-1-slice-07-node-health.out"

check_json "create smoke project for failed job fixture" \
  "loom project create '$SMOKE_NAME' --slug '$SMOKE_SLUG' --description 'v0.2.1 Slice 07 portal action smoke' --if-not-exists --json --correlation-id corr_smoke_v0_2_1_slice_07_project" \
  ".ok == true and .data.project.project.slug == \"$SMOKE_SLUG\""

check_json "register failing script fixture" \
  "loom script register '$FAIL_MANIFEST' --project '$SMOKE_SLUG' --activate --json --correlation-id corr_smoke_v0_2_1_slice_07_fail_register" \
  '.ok == true and .data.script.slug == "fail_always" and .data.version.status == "active"'

remote "loom script run fail_always --project '$SMOKE_SLUG' --no-wait --json --correlation-id corr_smoke_v0_2_1_slice_07_fail_enqueue > /tmp/loom-v0-2-1-slice-07-fail-enqueue.json"
failed_job_id="$(json_value 'cat /tmp/loom-v0-2-1-slice-07-fail-enqueue.json' '.data.job.job.job_id')"
[[ "$failed_job_id" == job_* ]] || fail "expected failed fixture job id, got $failed_job_id"
pass "captured failed fixture job id"

remote "loom jobs run next --once --json --correlation-id corr_smoke_v0_2_1_slice_07_fail_run > /tmp/loom-v0-2-1-slice-07-fail-run.json"
wait_job_status "$failed_job_id" "failed"

check_remote "failed job detail actions run through portal and exhausted retry is explicit" \
  "loom enter --start jobs --run-action 'job.${failed_job_id}.inspect' --exit-after-render > /tmp/loom-v0-2-1-slice-07-job-inspect.out
   grep -q 'Action Complete' /tmp/loom-v0-2-1-slice-07-job-inspect.out
   grep -q 'Job detail loaded' /tmp/loom-v0-2-1-slice-07-job-inspect.out
   loom enter --start jobs --run-action 'job.${failed_job_id}.logs' --exit-after-render > /tmp/loom-v0-2-1-slice-07-job-logs.out
   grep -q 'Action Complete' /tmp/loom-v0-2-1-slice-07-job-logs.out
   grep -q 'Job logs loaded' /tmp/loom-v0-2-1-slice-07-job-logs.out
   loom enter --start jobs --run-action 'job.${failed_job_id}.outputs' --exit-after-render > /tmp/loom-v0-2-1-slice-07-job-outputs.out
   grep -q 'Action Complete' /tmp/loom-v0-2-1-slice-07-job-outputs.out
   grep -q 'Job outputs loaded' /tmp/loom-v0-2-1-slice-07-job-outputs.out
   loom enter --start jobs --preview-action 'job.${failed_job_id}.retry' --exit-after-render > /tmp/loom-v0-2-1-slice-07-job-retry.out
   grep -q 'Retry Job' /tmp/loom-v0-2-1-slice-07-job-retry.out
   grep -q 'This job has exhausted its configured attempts' /tmp/loom-v0-2-1-slice-07-job-retry.out"

check_remote "direct event endpoint test action is explicit about missing backend test API when endpoints exist" \
  "endpoint_id=\$(loom endpoints list --json | jq -r '.data[0].endpoint_id // empty')
   if [ -n \"\$endpoint_id\" ]; then
     loom enter --start automations --preview-action \"automation.direct_endpoint.\${endpoint_id}.test\" --exit-after-render > /tmp/loom-v0-2-1-slice-07-endpoint-test.out
     grep -q 'Test Direct Event Endpoint' /tmp/loom-v0-2-1-slice-07-endpoint-test.out
     grep -q 'Direct event endpoint test requires a dedicated backend test API' /tmp/loom-v0-2-1-slice-07-endpoint-test.out
   fi"

log "completed $pass_count checks"
