#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." && pwd)"

HOST="${LOOM_DEV_HOST:-loom-dev}"
REMOTE_DIR="${LOOM_DEV_REMOTE_DIR:-/srv/loom/current}"
RUN_ID="$(date +%Y%m%d%H%M%S)-$RANDOM"
TOKEN_ID="${RUN_ID//-/}"
INTEGRATION_KEY="s07_${TOKEN_ID}"
ACCEPTED_SLUG="s07-accepted-${TOKEN_ID}"
SYNC_SLUG="s07-sync-${TOKEN_ID}"
UNIQUE_TOKEN="slicesevendirectevent$TOKEN_ID"
MANIFEST_PATH="$REMOTE_DIR/tests/smoke/scripts/direct_event_echo/loom.script.yaml"
AUTH_JSON="/tmp/loom-v0-2-slice-07-auth-$TOKEN_ID.json"
ACCEPTED_ENDPOINT_JSON="/tmp/loom-v0-2-slice-07-accepted-endpoint-$TOKEN_ID.json"
SYNC_ENDPOINT_JSON="/tmp/loom-v0-2-slice-07-sync-endpoint-$TOKEN_ID.json"
PREVIEW_JSON="/tmp/loom-v0-2-slice-07-preview-$TOKEN_ID.json"
UNAUTH_BODY="/tmp/loom-v0-2-slice-07-unauth-$TOKEN_ID.json"
POST_BODY="/tmp/loom-v0-2-slice-07-post-$TOKEN_ID.json"
DUP_BODY="/tmp/loom-v0-2-slice-07-duplicate-$TOKEN_ID.json"
SYNC_BODY="/tmp/loom-v0-2-slice-07-sync-$TOKEN_ID.json"
EVENT_JSON="/tmp/loom-v0-2-slice-07-event-$TOKEN_ID.json"
RAW_JSON="/tmp/loom-v0-2-slice-07-raw-$TOKEN_ID.json"
JOB_JSON="/tmp/loom-v0-2-slice-07-job-$TOKEN_ID.json"
INGEST_WORKER_JSON="/tmp/loom-v0-2-slice-07-ingest-worker-$TOKEN_ID.json"
DISPATCHER_JSON="/tmp/loom-v0-2-slice-07-dispatcher-$TOKEN_ID.json"
JOB_RUNNER_JSON="/tmp/loom-v0-2-slice-07-job-runner-$TOKEN_ID.json"
STATUS_JSON="/tmp/loom-v0-2-slice-07-status-$TOKEN_ID.json"
FAILURES_JSON="/tmp/loom-v0-2-slice-07-failures-$TOKEN_ID.json"
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

drive_job_completed() {
  local job_id="$1"
  local output_file="$2"
  local key_prefix="$3"

  remote "for i in \$(seq 1 60); do loom job inspect '$job_id' --json > '$output_file'; status=\$(jq -r '.data.job.status' '$output_file'); if [ \"\$status\" = completed ]; then exit 0; fi; case \"\$status\" in failed|timed_out|cancelled) cat '$output_file' >&2; exit 1;; esac; loom worker run main.job_runner --once --json --idempotency-key '${key_prefix}_'\$i > '/tmp/${key_prefix}_job_runner_'\$i'.json' 2> '/tmp/${key_prefix}_job_runner_'\$i'.err' || true; sleep 1; done; cat '$output_file' >&2; exit 1"
  pass "job completed"
}

log "target: $HOST"
log "run: $RUN_ID"
log "integration: $INTEGRATION_KEY"
log "accepted endpoint: $ACCEPTED_SLUG"
log "sync endpoint: $SYNC_SLUG"
log "unique token: $UNIQUE_TOKEN"

check_remote "SSH target reachable" 'hostname'
check_remote "loomd service active" 'systemctl is-active loomd'
check_remote "loom CLI installed" 'command -v loom && loom version'
check_remote "jq available for smoke assertions" 'command -v jq'
check_remote "curl available for Unix-socket ingest" 'command -v curl'

check_json "health reports direct-event migrations" \
  'loom health --json' \
  '.ok == true and .data.status == "ok" and .data.checks.migrations.current_version >= 23'

remote "loom workers list --json --correlation-id corr_smoke_v0_2_slice_07_workers > /tmp/loom-v0-2-slice-07-workers-$TOKEN_ID.json"
check_json "worker list includes direct-event vertical workers" \
  "cat /tmp/loom-v0-2-slice-07-workers-$TOKEN_ID.json" \
  '.ok == true
   and ([.data[] | select(.worker_key == "main.direct_event_ingest" and .worker_kind == "direct_event_ingest")] | length) == 1
   and ([.data[] | select(.worker_key == "main.automation_dispatcher" and .worker_kind == "automation_dispatcher")] | length) == 1
   and ([.data[] | select(.worker_key == "main.job_runner" and .worker_kind == "job_runner")] | length) == 1'

check_remote "direct-event echo script package is visible to loomd" \
  "test -r '$MANIFEST_PATH' && test -x '$REMOTE_DIR/tests/smoke/scripts/direct_event_echo/direct_event_echo.sh' && sudo -u loom test -r '$MANIFEST_PATH'"

check_json "register direct-event echo script" \
  "loom script register '$MANIFEST_PATH' --activate --json --correlation-id corr_smoke_v0_2_slice_07_script_register" \
  '.ok == true and .data.script.slug == "direct_event_echo" and .data.version.status == "active"'

check_json "create external integration" \
  "loom integrations create --key '$INTEGRATION_KEY' --name 'v0.2 Slice 07 Smoke' --main-auth-level 5 --json --correlation-id corr_smoke_v0_2_slice_07_integration" \
  ".ok == true and .data.integration.integration_key == \"$INTEGRATION_KEY\" and .data.integration.status == \"active\""

remote "loom integration auth create '$INTEGRATION_KEY' --kind bearer_header --json --correlation-id corr_smoke_v0_2_slice_07_auth > '$AUTH_JSON'"
check_json "create bearer auth profile" \
  "cat $AUTH_JSON" \
  '.ok == true and (.data.profile.auth_profile_id | startswith("integration_auth_profile_")) and (.data.token | length) > 20'

auth_profile_id="$(json_value "cat $AUTH_JSON" '.data.profile.auth_profile_id')"
token="$(json_value "cat $AUTH_JSON" '.data.token')"
[[ "$auth_profile_id" == integration_auth_profile_* ]] || fail "expected auth profile id, got $auth_profile_id"
[[ -n "$token" ]] || fail "expected bearer token"
pass "captured auth profile and bearer token"

remote "loom direct-event endpoints create --slug '$ACCEPTED_SLUG' --name 'v0.2 Slice 07 Accepted' --integration '$INTEGRATION_KEY' --event-type gmail.url.detected --target main@script-runner.script.run --response-mode accepted --auth-profile '$auth_profile_id' --idempotency-strategy payload_path --idempotency-path '\$.message_id' --map 'script_ref=literal:direct_event_echo' --map 'execution_mode=literal:enqueue_only' --map 'input.url=\$.body.url' --map 'input.message_id=\$.body.message_id' --map 'input.token=\$.body.token' --required script_ref --required execution_mode --required input.url --required input.message_id --timeout 30s --json --correlation-id corr_smoke_v0_2_slice_07_accepted_endpoint > '$ACCEPTED_ENDPOINT_JSON'"
check_json "create accepted-mode endpoint" \
  "cat $ACCEPTED_ENDPOINT_JSON" \
  ".ok == true and .data.endpoint.endpoint_slug == \"$ACCEPTED_SLUG\" and .data.endpoint.response_mode == \"accepted\""

accepted_endpoint_id="$(json_value "cat $ACCEPTED_ENDPOINT_JSON" '.data.endpoint.endpoint_id')"
[[ "$accepted_endpoint_id" == direct_event_endpoint_* ]] || fail "expected direct event endpoint id, got $accepted_endpoint_id"
pass "captured accepted endpoint id"

remote "loom direct-event endpoint preview '$ACCEPTED_SLUG' --body-json '{\"url\":\"https://example.invalid/$UNIQUE_TOKEN\",\"message_id\":\"msg_$TOKEN_ID\",\"token\":\"$UNIQUE_TOKEN\"}' --json --correlation-id corr_smoke_v0_2_slice_07_preview > '$PREVIEW_JSON'"
check_json "mapping preview produces script runner input" \
  "cat $PREVIEW_JSON" \
  ".ok == true
   and .data.input_json.script_ref == \"direct_event_echo\"
   and .data.input_json.execution_mode == \"enqueue_only\"
   and .data.input_json.input.url == \"https://example.invalid/$UNIQUE_TOKEN\"
   and .data.input_json.input.message_id == \"msg_$TOKEN_ID\""

remote "code=\$(curl -sS --unix-socket /run/loom/loomd.sock -o '$UNAUTH_BODY' -w '%{http_code}' -H 'Content-Type: application/json' --data '{\"url\":\"https://example.invalid/$UNIQUE_TOKEN\",\"message_id\":\"unauth_$TOKEN_ID\",\"token\":\"$UNIQUE_TOKEN\"}' 'http://loom/v1/direct-events/ingest/$ACCEPTED_SLUG'); test \"\$code\" = 401"
pass "unauthenticated ingest is rejected"

remote "curl -sS --unix-socket /run/loom/loomd.sock -H 'Content-Type: application/json' -H 'Authorization: Bearer $token' --data '{\"url\":\"https://example.invalid/$UNIQUE_TOKEN\",\"message_id\":\"msg_$TOKEN_ID\",\"token\":\"$UNIQUE_TOKEN\"}' 'http://loom/v1/direct-events/ingest/$ACCEPTED_SLUG' > '$POST_BODY'"
check_json "authenticated ingest accepted without immediate invocation" \
  "cat $POST_BODY" \
  ".ok == true
   and .data.status == \"accepted\"
   and .data.response_mode == \"accepted\"
   and .data.direct_event.status == \"accepted\"
   and .data.direct_event.invocation_id == null"

direct_event_id="$(json_value "cat $POST_BODY" '.data.direct_event.direct_event_id')"
[[ "$direct_event_id" == direct_event_* ]] || fail "expected direct event id, got $direct_event_id"
pass "captured accepted direct event id"

run_worker_once_json "main.direct_event_ingest" "$INGEST_WORKER_JSON" "smoke_v0_2_slice_07_ingest_$TOKEN_ID"
check_json "direct-event ingest worker created invocation" \
  "cat $INGEST_WORKER_JSON" \
  ".ok == true and .data.run.run_status == \"succeeded\" and .data.run.result_summary_json.created_invocations >= 1"

remote "loom direct-event inspect '$direct_event_id' --json --correlation-id corr_smoke_v0_2_slice_07_event_after_ingest > '$EVENT_JSON'"
check_json "direct event links invocation after ingest worker" \
  "cat $EVENT_JSON" \
  ".ok == true
   and .data.direct_event.direct_event_id == \"$direct_event_id\"
   and .data.direct_event.status == \"invocation_created\"
   and (.data.direct_event.invocation_id | startswith(\"invocation_\"))
   and (.data.direct_event | has(\"raw_body_json\") | not)"

invocation_id="$(json_value "cat $EVENT_JSON" '.data.direct_event.invocation_id')"
[[ "$invocation_id" == invocation_* ]] || fail "expected invocation id, got $invocation_id"
pass "captured direct-event invocation id"

run_worker_once_json "main.automation_dispatcher" "$DISPATCHER_JSON" "smoke_v0_2_slice_07_dispatcher_$TOKEN_ID"
remote "loom direct-event inspect '$direct_event_id' --json --correlation-id corr_smoke_v0_2_slice_07_event_after_dispatch > '$EVENT_JSON'"
check_json "dispatcher links route capability call and job" \
  "cat $EVENT_JSON" \
  ".ok == true
   and .data.direct_event.status == \"completed\"
   and (.data.direct_event.route_id | startswith(\"route_\"))
   and (.data.direct_event.capability_call_id | startswith(\"capability_call_\"))
   and (.data.direct_event.job_id | startswith(\"job_\"))"

route_id="$(json_value "cat $EVENT_JSON" '.data.direct_event.route_id')"
call_id="$(json_value "cat $EVENT_JSON" '.data.direct_event.capability_call_id')"
job_id="$(json_value "cat $EVENT_JSON" '.data.direct_event.job_id')"
[[ "$route_id" == route_* ]] || fail "expected route id, got $route_id"
[[ "$call_id" == capability_call_* ]] || fail "expected capability call id, got $call_id"
[[ "$job_id" == job_* ]] || fail "expected job id, got $job_id"
pass "captured route, capability call, and job ids"

drive_job_completed "$job_id" "$JOB_JSON" "smoke_v0_2_slice_07_job_$TOKEN_ID"
check_json "direct-event echo job captured smoke token" \
  "cat $JOB_JSON" \
  ".ok == true
   and .data.job.status == \"completed\"
   and ([.data.logs[].tail_text] | join(\"\\n\") | contains(\"$UNIQUE_TOKEN\"))"

remote "curl -sS --unix-socket /run/loom/loomd.sock -H 'Content-Type: application/json' -H 'Authorization: Bearer $token' --data '{\"url\":\"https://example.invalid/$UNIQUE_TOKEN\",\"message_id\":\"msg_$TOKEN_ID\",\"token\":\"$UNIQUE_TOKEN\"}' 'http://loom/v1/direct-events/ingest/$ACCEPTED_SLUG' > '$DUP_BODY'"
check_json "duplicate accepted event reuses original occurrence" \
  "cat $DUP_BODY" \
  ".ok == true
   and .data.duplicate == true
   and .data.direct_event.direct_event_id == \"$direct_event_id\"
   and .data.direct_event.invocation_id == \"$invocation_id\"
   and .data.direct_event.job_id == \"$job_id\""

remote "loom direct-event endpoints create --slug '$SYNC_SLUG' --name 'v0.2 Slice 07 Sync Wait' --integration '$INTEGRATION_KEY' --event-type gmail.sync.test --target main@system.status.read --response-mode sync_wait --sync-wait-timeout 10s --auth-profile '$auth_profile_id' --idempotency-strategy payload_path --idempotency-path '\$.message_id' --timeout 30s --json --correlation-id corr_smoke_v0_2_slice_07_sync_endpoint > '$SYNC_ENDPOINT_JSON'"
check_json "create sync-wait endpoint" \
  "cat $SYNC_ENDPOINT_JSON" \
  ".ok == true and .data.endpoint.endpoint_slug == \"$SYNC_SLUG\" and .data.endpoint.response_mode == \"sync_wait\""

remote "code=\$(curl -sS --unix-socket /run/loom/loomd.sock -o '$SYNC_BODY' -w '%{http_code}' -H 'Content-Type: application/json' -H 'Authorization: Bearer $token' --data '{\"message_id\":\"sync_$TOKEN_ID\",\"token\":\"$UNIQUE_TOKEN\"}' 'http://loom/v1/direct-events/ingest/$SYNC_SLUG'); test \"\$code\" = 200"
check_json "sync-wait direct event completes inline through dispatcher" \
  "cat $SYNC_BODY" \
  '.ok == true
   and .data.response_mode == "sync_wait"
   and .data.direct_event.status == "completed"
   and (.data.direct_event.invocation_id | startswith("invocation_"))
   and (.data.direct_event.route_id | startswith("route_"))
   and (.data.direct_event.capability_call_id | startswith("capability_call_"))'

remote "loom direct-event raw '$direct_event_id' --json --correlation-id corr_smoke_v0_2_slice_07_raw > '$RAW_JSON'"
check_json "raw payload surface is explicit and redacted" \
  "cat $RAW_JSON" \
  ".ok == true
   and .data.direct_event_id == \"$direct_event_id\"
   and .data.body_json.token == \"$UNIQUE_TOKEN\"
   and .data.headers_json.authorization == \"[redacted]\""

remote "loom direct-events failures --json --correlation-id corr_smoke_v0_2_slice_07_failures > '$FAILURES_JSON'"
check_json "failures surface includes rejected unauthenticated event" \
  "cat $FAILURES_JSON" \
  ".ok == true
   and ([.data[] | select(.status == \"rejected\" and .endpoint_id == \"$accepted_endpoint_id\")] | length) >= 1"

remote "loom direct-events status --json --correlation-id corr_smoke_v0_2_slice_07_status > '$STATUS_JSON'"
check_json "direct-event status summary exposes counts and workers" \
  "cat $STATUS_JSON" \
  '.ok == true
   and .data.direct_event_ingest_worker_key == "main.direct_event_ingest"
   and .data.dispatcher_worker_key == "main.automation_dispatcher"
   and (.data.active_endpoint_count | type) == "number"
   and (.data.active_integration_count | type) == "number"
   and (.data.accepted_count | type) == "number"
   and (.data.failed_count | type) == "number"'

check_json "direct-event ingest worker run history is visible" \
  'loom worker runs main.direct_event_ingest --json --correlation-id corr_smoke_v0_2_slice_07_ingest_runs' \
  '.ok == true and (.data | length) >= 1'

check_json "automation dispatcher run history is visible" \
  'loom worker runs main.automation_dispatcher --json --correlation-id corr_smoke_v0_2_slice_07_dispatcher_runs' \
  '.ok == true and (.data | length) >= 1'

if grep -R "psql\|QueryContext\|QueryRowContext" "$REPO_ROOT/internal/loomcli" >/dev/null 2>&1; then
  fail "local loom CLI contains direct PostgreSQL access"
fi
pass "local CLI does not query PostgreSQL directly"

check_remote "remote CLI does not query PostgreSQL directly" \
  "if grep -R \"psql\\|QueryContext\\|QueryRowContext\" '$REMOTE_DIR/internal/loomcli' >/dev/null 2>&1; then exit 1; fi"

log "completed $pass_count checks"
