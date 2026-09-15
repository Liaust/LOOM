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

check_json "health reports Slice 01 worker-core migration" \
  'loom health --json' \
  '.ok == true and .data.status == "ok" and .data.checks.migrations.current_version >= 17'

remote "loom workers list --json --correlation-id corr_smoke_v0_2_slice_01_workers_list > /tmp/loom-v0-2-slice-01-workers.json"
check_json "worker list includes selfcheck worker" \
  'cat /tmp/loom-v0-2-slice-01-workers.json' \
  '.ok == true
   and ([.data[] | select(.worker_key == "main.worker_selfcheck" and .worker_kind == "worker_selfcheck")] | length) == 1'

remote "loom worker inspect main.worker_selfcheck --json --correlation-id corr_smoke_v0_2_slice_01_inspect_before > /tmp/loom-v0-2-slice-01-inspect-before.json"
check_json "worker inspect returns seeded selfcheck detail" \
  'cat /tmp/loom-v0-2-slice-01-inspect-before.json' \
  '.ok == true
   and .data.instance.worker_key == "main.worker_selfcheck"
   and .data.instance.worker_kind == "worker_selfcheck"
   and .data.instance.lifecycle_status == "active"
   and .data.instance.enabled == true'

remote "loom worker run main.worker_selfcheck --once --reason smoke --json --correlation-id corr_smoke_v0_2_slice_01_worker_run > /tmp/loom-v0-2-slice-01-run.json"
worker_run_id="$(json_value 'cat /tmp/loom-v0-2-slice-01-run.json' '.data.run.worker_run_id')"
[[ "$worker_run_id" == worker_run_* ]] || fail "expected worker run id"
pass "selfcheck worker returned a worker run id"

check_json "selfcheck worker run succeeded and recorded health" \
  'cat /tmp/loom-v0-2-slice-01-run.json' \
  '.ok == true
   and .data.run.worker_run_id == "'"$worker_run_id"'"
   and .data.run.run_status == "succeeded"
   and .data.run.trigger_kind == "manual"
   and .data.health.health_status == "healthy"
   and .data.health.severity == "info"
   and ([.data.checkpoints[] | select(.checkpoint_key == "default")] | length) == 1
   and (.data.run.counters_json.db_checks // 0) >= 1
   and (.data.run.counters_json.instance_checks // 0) >= 1'

remote "loom worker inspect main.worker_selfcheck --json --correlation-id corr_smoke_v0_2_slice_01_inspect_after > /tmp/loom-v0-2-slice-01-inspect-after.json"
check_json "worker inspect reflects latest successful run" \
  'cat /tmp/loom-v0-2-slice-01-inspect-after.json' \
  '.ok == true
   and .data.instance.worker_key == "main.worker_selfcheck"
   and .data.instance.last_run_id == "'"$worker_run_id"'"
   and .data.instance.last_success_at != null
   and .data.health.health_status == "healthy"
   and .data.latest_checkpoint.checkpoint_key == "default"'

remote "loom worker runs main.worker_selfcheck --json --correlation-id corr_smoke_v0_2_slice_01_runs > /tmp/loom-v0-2-slice-01-runs.json"
check_json "worker run history includes selfcheck run" \
  'cat /tmp/loom-v0-2-slice-01-runs.json' \
  '.ok == true
   and ([.data[] | select(.worker_run_id == "'"$worker_run_id"'" and .run_status == "succeeded")] | length) == 1'

log "completed $pass_count checks"
