#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." && pwd)"

HOST="${LOOM_DEV_HOST:-loom-dev}"
REMOTE_DIR="${LOOM_DEV_REMOTE_DIR:-/srv/loom/current}"
RUN_ID="$(date +%Y%m%d%H%M%S)-$RANDOM"
TOKEN_ID="${RUN_ID//-/}"
SMOKE_SLUG="v0-2-slice-05-$RUN_ID"
SMOKE_NAME="v0.2 Slice 05 Smoke $RUN_ID"
INGEST_PATH="$REMOTE_DIR/tests/smoke/v0_2_slice_05_$RUN_ID.md"
WORD_MANIFEST="$REMOTE_DIR/tests/smoke/scripts/word_count/loom.script.yaml"
FAIL_MANIFEST="$REMOTE_DIR/tests/smoke/scripts/fail_always/loom.script.yaml"
UNIQUE_TOKEN="slicefiveartifactv02slice05$TOKEN_ID"
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

wait_job_status() {
  local job_id="$1"
  local want="$2"

  remote "for i in \$(seq 1 60); do status=\$(loom job inspect '$job_id' --json | jq -r '.data.job.status'); if [ \"\$status\" = '$want' ]; then exit 0; fi; sleep 1; done; loom job inspect '$job_id' --json >&2; exit 1"
  pass "job $job_id reached $want"
}

log "target: $HOST"
log "run: $RUN_ID"
log "unique token: $UNIQUE_TOKEN"

check_remote "SSH target reachable" 'hostname'
check_remote "loomd service active" 'systemctl is-active loomd'
check_remote "loom CLI installed" 'command -v loom && loom version'
check_remote "jq available for smoke assertions" 'command -v jq'

check_json "health reports job runner migration" \
  'loom health --json' \
  '.ok == true and .data.status == "ok" and .data.checks.migrations.current_version >= 20'

remote "loom workers list --json --correlation-id corr_smoke_v0_2_slice_05_workers > /tmp/loom-v0-2-slice-05-workers.json"
check_json "worker list includes job runner and sweeper" \
  'cat /tmp/loom-v0-2-slice-05-workers.json' \
  '.ok == true
   and ([.data[] | select(.worker_key == "main.job_runner" and .worker_kind == "job_runner")] | length) == 1
   and ([.data[] | select(.worker_key == "main.job_sweeper" and .worker_kind == "job_sweeper")] | length) == 1'

check_json "job runner is inspectable" \
  'loom worker inspect main.job_runner --json --correlation-id corr_smoke_v0_2_slice_05_runner_inspect' \
  '.ok == true and .data.instance.worker_key == "main.job_runner" and .data.instance.enabled == true'

check_json "job sweeper is inspectable" \
  'loom worker inspect main.job_sweeper --json --correlation-id corr_smoke_v0_2_slice_05_sweeper_inspect' \
  '.ok == true and .data.instance.worker_key == "main.job_sweeper" and .data.instance.enabled == true'

check_remote "script packages are visible to loomd" \
  "test -r '$WORD_MANIFEST' && test -x '$REMOTE_DIR/tests/smoke/scripts/word_count/word_count.sh' && test -r '$FAIL_MANIFEST' && test -x '$REMOTE_DIR/tests/smoke/scripts/fail_always/fail_always.sh' && sudo -n -u loom test -r '$WORD_MANIFEST' && sudo -n -u loom test -r '$FAIL_MANIFEST'"

check_remote "write smoke input file visible to loomd" \
  "mkdir -p '$REMOTE_DIR/tests/smoke' && printf '# v0.2 Slice 05 Smoke\n\nQueued job runner unique token: $UNIQUE_TOKEN.\n' > '$INGEST_PATH' && chmod 0644 '$INGEST_PATH' && sudo -n -u loom test -r '$INGEST_PATH'"

check_json "create smoke project" \
  "loom project create '$SMOKE_NAME' --slug '$SMOKE_SLUG' --description 'v0.2 Slice 05 job runner smoke' --if-not-exists --json --correlation-id corr_smoke_v0_2_slice_05_project" \
  ".ok == true and .data.project.project.slug == \"$SMOKE_SLUG\""

remote "loom object ingest '$INGEST_PATH' --project '$SMOKE_SLUG' --name v0-2-slice-05-input.md --json --correlation-id corr_smoke_v0_2_slice_05_ingest > /tmp/loom-v0-2-slice-05-ingest.json"
object_id="$(json_value 'cat /tmp/loom-v0-2-slice-05-ingest.json' '.data.object.object.object_id')"
[[ "$object_id" == object_* ]] || fail "expected object id, got $object_id"
pass "captured source object id"

check_json "register word_count" \
  "loom script register '$WORD_MANIFEST' --project '$SMOKE_SLUG' --activate --json --correlation-id corr_smoke_v0_2_slice_05_word_register" \
  '.ok == true and .data.script.slug == "word_count" and .data.version.status == "active"'

remote "loom script run word_count --object '$object_id' --project '$SMOKE_SLUG' --no-wait --json --correlation-id corr_smoke_v0_2_slice_05_word_enqueue > /tmp/loom-v0-2-slice-05-word-enqueue.json"
job_id="$(json_value 'cat /tmp/loom-v0-2-slice-05-word-enqueue.json' '.data.job.job.job_id')"
[[ "$job_id" == job_* ]] || fail "expected queued job id, got $job_id"
check_json "script run returns queued job in enqueue mode" \
  'cat /tmp/loom-v0-2-slice-05-word-enqueue.json' \
  '.ok == true and .data.execution_mode == "enqueue_only" and .data.wait_result == "queued" and (.data.job.job.status == "queued" or .data.job.job.status == "running" or .data.job.job.status == "completed")'

check_remote "queued job is visible or already claimed by supervisor" \
  "status=\$(loom job inspect '$job_id' --json | jq -r '.data.job.status'); if [ \"\$status\" = queued ]; then loom jobs queue --json | jq -e --arg job '$job_id' '([.data[] | select(.job_id == \$job)] | length) == 1'; else test \"\$status\" = running -o \"\$status\" = completed; fi"

check_json "runner status is inspectable before execution" \
  'loom runners status --json --correlation-id corr_smoke_v0_2_slice_05_runner_status_before' \
  '.ok == true and (.data.runner_count | type) == "number" and (.data.queued_count | type) == "number" and (.data.current_job_count | type) == "number"'

remote "loom jobs run next --once --json --correlation-id corr_smoke_v0_2_slice_05_run_next > /tmp/loom-v0-2-slice-05-run-next.json"
check_json "job runner run command returns structured result" \
  'cat /tmp/loom-v0-2-slice-05-run-next.json' \
  '.ok == true and .data.run.run_status == "succeeded" and .data.run.result_summary_json.schema_version == "job_runner.result.v0.2"'

wait_job_status "$job_id" "completed"

remote "loom job inspect '$job_id' --json --correlation-id corr_smoke_v0_2_slice_05_job_done > /tmp/loom-v0-2-slice-05-job-done.json"
artifact_id="$(json_value 'cat /tmp/loom-v0-2-slice-05-job-done.json' '.data.artifacts[0].artifact_id')"
artifact_object_id="$(json_value 'cat /tmp/loom-v0-2-slice-05-job-done.json' '.data.artifacts[0].object_id')"
[[ "$artifact_id" == artifact_* ]] || fail "expected artifact id, got $artifact_id"
[[ "$artifact_object_id" == object_* ]] || fail "expected artifact object id, got $artifact_object_id"
check_json "completed job has logs outputs and artifact" \
  'cat /tmp/loom-v0-2-slice-05-job-done.json' \
  '.ok == true and .data.job.status == "completed" and (.data.outputs | length) >= 1 and (.data.logs | length) >= 3 and (.data.artifacts | length) >= 1'

check_remote "job logs are readable" \
  "loom job logs '$job_id' | grep -q 'counted'"

check_json "job outputs command returns outputs" \
  "loom job outputs '$job_id' --json --correlation-id corr_smoke_v0_2_slice_05_outputs" \
  '.ok == true and (.data | length) >= 1'

remote "loom indexes run text --once --json --correlation-id corr_smoke_v0_2_slice_05_index_artifact > /tmp/loom-v0-2-slice-05-index-artifact.json"
check_json "artifact content is searchable after indexing worker" \
  "loom search '$UNIQUE_TOKEN' --project '$SMOKE_SLUG' --json --correlation-id corr_smoke_v0_2_slice_05_search" \
  ".ok == true and .data.result_count >= 1 and ([.data.results[].object_id] | index(\"$artifact_object_id\"))"

check_json "register fail_always" \
  "loom script register '$FAIL_MANIFEST' --project '$SMOKE_SLUG' --activate --json --correlation-id corr_smoke_v0_2_slice_05_fail_register" \
  '.ok == true and .data.script.slug == "fail_always" and .data.version.status == "active"'

remote "loom script run fail_always --project '$SMOKE_SLUG' --no-wait --json --correlation-id corr_smoke_v0_2_slice_05_fail_enqueue > /tmp/loom-v0-2-slice-05-fail-enqueue.json"
failed_job_id="$(json_value 'cat /tmp/loom-v0-2-slice-05-fail-enqueue.json' '.data.job.job.job_id')"
[[ "$failed_job_id" == job_* ]] || fail "expected failed fixture job id, got $failed_job_id"
remote "loom jobs run next --once --json --correlation-id corr_smoke_v0_2_slice_05_fail_run > /tmp/loom-v0-2-slice-05-fail-run.json"
check_json "failing script does not make job runner unhealthy" \
  'cat /tmp/loom-v0-2-slice-05-fail-run.json' \
  '.ok == true and .data.run.run_status == "succeeded" and .data.health.health_status == "healthy"'
wait_job_status "$failed_job_id" "failed"

check_json "failed job appears in failures list" \
  "loom jobs failures --json --correlation-id corr_smoke_v0_2_slice_05_failures" \
  ".ok == true and ([.data[] | select(.job_id == \"$failed_job_id\" and .status == \"failed\")] | length) == 1"

check_json "explicit retry requeues failed job" \
  "loom job retry '$failed_job_id' --force --json --correlation-id corr_smoke_v0_2_slice_05_retry" \
  ".ok == true and .data.job_id == \"$failed_job_id\" and .data.status == \"queued\""

remote "loom script run word_count --object '$object_id' --project '$SMOKE_SLUG' --no-wait --json --correlation-id corr_smoke_v0_2_slice_05_cancel_enqueue > /tmp/loom-v0-2-slice-05-cancel-enqueue.json"
cancel_job_id="$(json_value 'cat /tmp/loom-v0-2-slice-05-cancel-enqueue.json' '.data.job.job.job_id')"
[[ "$cancel_job_id" == job_* ]] || fail "expected cancel job id, got $cancel_job_id"
check_json "queued cancellation works" \
  "loom job cancel '$cancel_job_id' --json --correlation-id corr_smoke_v0_2_slice_05_cancel" \
  ".ok == true and .data.job_id == \"$cancel_job_id\" and .data.status == \"cancelled\""

remote "loom worker run main.job_sweeper --once --json --correlation-id corr_smoke_v0_2_slice_05_sweeper_run > /tmp/loom-v0-2-slice-05-sweeper-run.json"
check_json "job sweeper run returns structured result" \
  'cat /tmp/loom-v0-2-slice-05-sweeper-run.json' \
  '.ok == true and .data.run.run_status == "succeeded" and .data.run.result_summary_json.schema_version == "job_sweeper.result.v0.2" and (.data.run.result_summary_json.runners_marked_offline | type) == "number"'

check_remote "job sweeper human inspect works" \
  "loom worker inspect main.job_sweeper | grep -q 'Worker: main.job_sweeper'"

check_json "runner inspect works after job runner execution" \
  'loom runner inspect main-local-runner --json --correlation-id corr_smoke_v0_2_slice_05_runner_inspect_after' \
  '.ok == true and .data.runner_key == "main-local-runner"'

if grep -R "psql\|QueryContext\|QueryRowContext" "$REPO_ROOT/internal/loomcli" >/dev/null 2>&1; then
  fail "local loom CLI contains direct PostgreSQL access"
fi
pass "local CLI does not query PostgreSQL directly"

check_remote "remote CLI does not query PostgreSQL directly" \
  "if grep -R \"psql\\|QueryContext\\|QueryRowContext\" '$REMOTE_DIR/internal/loomcli' >/dev/null 2>&1; then exit 1; fi"

log "completed $pass_count checks"
