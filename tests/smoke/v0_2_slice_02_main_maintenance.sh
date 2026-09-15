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

log "target: $HOST"
log "run: $RUN_ID"

check_remote "SSH target reachable" 'hostname'
check_remote "loomd service active" 'systemctl is-active loomd'
check_remote "loom CLI installed" 'command -v loom && loom version'
check_remote "jq available for smoke assertions" 'command -v jq'

check_json "health reports Slice 02 main maintenance migration" \
  'loom health --json' \
  '.ok == true and .data.status == "ok" and .data.checks.migrations.current_version >= 18'

remote "loom workers list --json --correlation-id corr_smoke_v0_2_slice_02_workers_list > /tmp/loom-v0-2-slice-02-workers.json"
check_json "worker list includes all main maintenance workers" \
  'cat /tmp/loom-v0-2-slice-02-workers.json' \
  '.ok == true
   and ([.data[] | select(.worker_key == "main.policy_expiry" and .worker_kind == "policy_expiry")] | length) == 1
   and ([.data[] | select(.worker_key == "main.realtime_expiry" and .worker_kind == "realtime_expiry")] | length) == 1
   and ([.data[] | select(.worker_key == "main.db_maintenance" and .worker_kind == "db_maintenance")] | length) == 1'

remote "loom worker run main.policy_expiry --once --json --correlation-id corr_smoke_v0_2_slice_02_policy_expiry > /tmp/loom-v0-2-slice-02-policy-expiry.json"
policy_run_id="$(json_value 'cat /tmp/loom-v0-2-slice-02-policy-expiry.json' '.data.run.worker_run_id')"
[[ "$policy_run_id" == worker_run_* ]] || fail "expected policy expiry worker run id"
pass "policy expiry worker returned a worker run id"

check_json "policy expiry worker run records counters" \
  'cat /tmp/loom-v0-2-slice-02-policy-expiry.json' \
  '.ok == true
   and .data.run.run_status == "succeeded"
   and .data.run.trigger_kind == "manual"
   and (.data.run.counters_json | has("approvals_expired"))
   and (.data.run.counters_json | has("grants_expired"))
   and (.data.run.counters_json | has("total_expired"))
   and .data.health.health_status == "healthy"'

remote "loom worker run main.realtime_expiry --once --json --correlation-id corr_smoke_v0_2_slice_02_realtime_expiry > /tmp/loom-v0-2-slice-02-realtime-expiry.json"
realtime_run_id="$(json_value 'cat /tmp/loom-v0-2-slice-02-realtime-expiry.json' '.data.run.worker_run_id')"
[[ "$realtime_run_id" == worker_run_* ]] || fail "expected realtime expiry worker run id"
pass "realtime expiry worker returned a worker run id"

check_json "realtime expiry worker run records counters" \
  'cat /tmp/loom-v0-2-slice-02-realtime-expiry.json' \
  '.ok == true
   and .data.run.run_status == "succeeded"
   and .data.run.trigger_kind == "manual"
   and (.data.run.counters_json | has("notifications_expired"))
   and (.data.run.counters_json | has("leases_expired"))
   and (.data.run.counters_json | has("subscriptions_expired"))
   and (.data.run.counters_json | has("presence_marked_stale"))
   and (.data.run.counters_json | has("progress_feeds_closed"))
   and (.data.run.counters_json | has("total_transitions"))
   and .data.health.health_status == "healthy"'

remote "loom worker run main.db_maintenance --once --json --correlation-id corr_smoke_v0_2_slice_02_db_maintenance > /tmp/loom-v0-2-slice-02-db-maintenance.json"
db_run_id="$(json_value 'cat /tmp/loom-v0-2-slice-02-db-maintenance.json' '.data.run.worker_run_id')"
[[ "$db_run_id" == worker_run_* ]] || fail "expected db maintenance worker run id"
pass "database maintenance worker returned a worker run id"

check_json "database maintenance worker reports migration status" \
  'cat /tmp/loom-v0-2-slice-02-db-maintenance.json' \
  '.ok == true
   and .data.run.run_status == "succeeded"
   and .data.run.trigger_kind == "manual"
   and .data.run.result_summary_json.schema_version == "db_maintenance.result.v0.2"
   and .data.run.result_summary_json.migration_status == "ok"
   and .data.run.result_summary_json.current_version >= 18
   and .data.run.result_summary_json.latest_version >= 18
   and (.data.run.result_summary_json.largest_tables | type) == "array"
   and .data.health.health_status == "healthy"'

remote "loom maintenance status --json --correlation-id corr_smoke_v0_2_slice_02_maintenance_status > /tmp/loom-v0-2-slice-02-maintenance-status.json"
check_json "maintenance status includes all main maintenance workers" \
  'cat /tmp/loom-v0-2-slice-02-maintenance-status.json' \
  '.ok == true
   and (.data.overall_status == "ok" or .data.overall_status == "warning" or .data.overall_status == "critical")
   and ([.data.workers[] | select(.worker_key == "main.policy_expiry")] | length) == 1
   and ([.data.workers[] | select(.worker_key == "main.realtime_expiry")] | length) == 1
   and ([.data.workers[] | select(.worker_key == "main.db_maintenance")] | length) == 1'

remote "loom maintenance db status --json --correlation-id corr_smoke_v0_2_slice_02_maintenance_db_status > /tmp/loom-v0-2-slice-02-maintenance-db-status.json"
check_json "maintenance db status reports current migrations" \
  'cat /tmp/loom-v0-2-slice-02-maintenance-db-status.json' \
  '.ok == true
   and .data.status == "ok"
   and .data.migration_status == "ok"
   and .data.current_version >= 18
   and .data.latest_version >= 18
   and .data.latest_run.worker_run_id == "'"$db_run_id"'"'

remote "loom maintenance findings list --json --correlation-id corr_smoke_v0_2_slice_02_findings > /tmp/loom-v0-2-slice-02-findings.json"
check_json "maintenance findings list returns an array" \
  'cat /tmp/loom-v0-2-slice-02-findings.json' \
  '.ok == true and (.data | type) == "array"'

remote "loom worker runs main.policy_expiry --json --correlation-id corr_smoke_v0_2_slice_02_policy_runs > /tmp/loom-v0-2-slice-02-policy-runs.json"
check_json "policy expiry run history includes smoke run" \
  'cat /tmp/loom-v0-2-slice-02-policy-runs.json' \
  '.ok == true and ([.data[] | select(.worker_run_id == "'"$policy_run_id"'")] | length) == 1'

remote "loom worker runs main.realtime_expiry --json --correlation-id corr_smoke_v0_2_slice_02_realtime_runs > /tmp/loom-v0-2-slice-02-realtime-runs.json"
check_json "realtime expiry run history includes smoke run" \
  'cat /tmp/loom-v0-2-slice-02-realtime-runs.json' \
  '.ok == true and ([.data[] | select(.worker_run_id == "'"$realtime_run_id"'")] | length) == 1'

remote "loom worker runs main.db_maintenance --json --correlation-id corr_smoke_v0_2_slice_02_db_runs > /tmp/loom-v0-2-slice-02-db-runs.json"
check_json "database maintenance run history includes smoke run" \
  'cat /tmp/loom-v0-2-slice-02-db-runs.json' \
  '.ok == true and ([.data[] | select(.worker_run_id == "'"$db_run_id"'")] | length) == 1'

check_remote "supervisor has produced a realtime expiry tick" '
  set -e
  for _ in $(seq 1 10); do
    loom worker runs main.realtime_expiry --trigger supervisor_tick --json > /tmp/loom-v0-2-slice-02-supervisor-realtime.json
    if jq -e ".ok == true and (.data | length) >= 1" /tmp/loom-v0-2-slice-02-supervisor-realtime.json >/dev/null; then
      exit 0
    fi
    sleep 2
  done
  exit 1
'

log "completed $pass_count checks"
