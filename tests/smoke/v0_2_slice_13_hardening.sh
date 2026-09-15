#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." && pwd)"

MAIN_HOST="${LOOM_DEV_HOST:-loom-dev}"
MAIN_URL="${LOOM_MAIN_URL:-http://10.44.0.2:8080}"
REMOTE_DIR="${LOOM_DEV_REMOTE_DIR:-/srv/loom/current}"
WORKSPACE_SCRIPT="$REPO_ROOT/scripts/loom-workspace"
PRE_SLICE_SMOKE="$REPO_ROOT/tests/smoke/pre_slice_10_part_2_wireguard.sh"
RUN_ID="$(date +%Y%m%d%H%M%S)-$RANDOM"
TOKEN_ID="${RUN_ID//-/}"
NODE_KEY="v0-2-slice-13-hardening-${RUN_ID}"
SMOKE_SLUG="v0-2-slice-13-$RUN_ID"
SMOKE_NAME="v0.2 Slice 13 Hardening $RUN_ID"
REMOTE_TMP="/tmp/loom-node-agent-v0-2-slice-13-${RUN_ID}"
REMOTE_CONFIG="${REMOTE_TMP}/config.json"
REMOTE_OFFLINE_CONFIG="${REMOTE_TMP}/offline-config.json"
REMOTE_STATE="${REMOTE_TMP}/state.json"
REMOTE_DATA="${REMOTE_TMP}/data"
REMOTE_VAULT="${REMOTE_TMP}/vault"
ACCEPTED_TOKEN="v02slice13accepted${TOKEN_ID}"
QUEUE_TOKEN="v02slice13queue${TOKEN_ID}"
OVERSIZE_TOKEN="v02slice13oversize${TOKEN_ID}"
GMAIL_INTEGRATION_KEY="gmail_bridge_${TOKEN_ID}"
GMAIL_ENDPOINT_SLUG="gmail-url-${TOKEN_ID}"
GMAIL_MESSAGE_ID="gmail_msg_${TOKEN_ID}"
GMAIL_EMAIL_ID="email_${TOKEN_ID}"
GMAIL_TOKEN="gmaildirectevent${TOKEN_ID}"
GMAIL_URL="http://127.0.0.1/gmail-smoke/${TOKEN_ID}?token=${GMAIL_TOKEN}"
GMAIL_SCRIPT_MANIFEST="$REMOTE_DIR/tests/smoke/scripts/gmail_url_capture/loom.script.yaml"
GMAIL_AUTH_JSON="/tmp/loom-v0-2-slice-13-gmail-auth-$TOKEN_ID.json"
GMAIL_ENDPOINT_JSON="/tmp/loom-v0-2-slice-13-gmail-endpoint-$TOKEN_ID.json"
GMAIL_UNAUTH_BODY="/tmp/loom-v0-2-slice-13-gmail-unauth-$TOKEN_ID.json"
GMAIL_POST_BODY="/tmp/loom-v0-2-slice-13-gmail-post-$TOKEN_ID.json"
GMAIL_DUP_BODY="/tmp/loom-v0-2-slice-13-gmail-duplicate-$TOKEN_ID.json"
GMAIL_EVENT_JSON="/tmp/loom-v0-2-slice-13-gmail-event-$TOKEN_ID.json"
GMAIL_RAW_JSON="/tmp/loom-v0-2-slice-13-gmail-raw-$TOKEN_ID.json"
GMAIL_JOB_JSON="/tmp/loom-v0-2-slice-13-gmail-job-$TOKEN_ID.json"
GMAIL_OUTPUTS_JSON="/tmp/loom-v0-2-slice-13-gmail-outputs-$TOKEN_ID.json"
GMAIL_INGEST_WORKER_JSON="/tmp/loom-v0-2-slice-13-gmail-ingest-worker-$TOKEN_ID.json"
GMAIL_DISPATCHER_JSON="/tmp/loom-v0-2-slice-13-gmail-dispatcher-$TOKEN_ID.json"

pass_count=0

log() {
  printf '[smoke] %s\n' "$*"
}

section() {
  printf '\n[section] %s\n' "$*"
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

workspace_ssh() {
  "$WORKSPACE_SCRIPT" ssh "$@"
}

agent_json() {
  "$WORKSPACE_SCRIPT" run -- \
    --config "$REMOTE_CONFIG" \
    --state "$REMOTE_STATE" \
    --data-dir "$REMOTE_DATA" \
    --json \
    "$@"
}

agent_json_config() {
  local config_path="$1"
  shift

  "$WORKSPACE_SCRIPT" run -- \
    --config "$config_path" \
    --state "$REMOTE_STATE" \
    --data-dir "$REMOTE_DATA" \
    --json \
    "$@"
}

assert_remote() {
  local label="$1"
  local command="$2"

  main_ssh "$command" >/dev/null
  pass "$label"
}

check_json_value() {
  local label="$1"
  local json="$2"
  local filter="$3"

  if ! printf '%s' "$json" | jq -e "$filter" >/dev/null; then
    printf '%s\n' "$json" >&2
    fail "$label"
  fi
  pass "$label"
}

check_main_json() {
  local label="$1"
  local command="$2"
  local filter="$3"
  local json

  if ! json="$(main_ssh "$command")"; then
    fail "$label"
  fi
  if ! printf '%s' "$json" | jq -e "$filter" >/dev/null; then
    printf '%s\n' "$json" >&2
    fail "$label"
  fi
  pass "$label"
}

check_main_json_arg() {
  local label="$1"
  local command="$2"
  local arg="$3"
  local filter="$4"
  local json

  if ! json="$(main_ssh "$command")"; then
    fail "$label"
  fi
  if ! printf '%s' "$json" | jq -e --arg value "$arg" "$filter" >/dev/null; then
    printf '%s\n' "$json" >&2
    fail "$label"
  fi
  pass "$label"
}

main_json_value() {
  local command="$1"
  local filter="$2"

  main_ssh "$command | jq -r '$filter'"
}

run_worker_once_json() {
  local worker="$1"
  local output_file="$2"
  local key_prefix="$3"

  main_ssh "rm -f '$output_file' '$output_file.err'; for i in \$(seq 1 10); do if loom worker run '$worker' --once --json --idempotency-key '${key_prefix}_'\$i > '$output_file' 2> '$output_file.err'; then exit 0; fi; sleep 1; done; cat '$output_file.err' >&2; test -s '$output_file' && cat '$output_file' >&2; exit 1"
  pass "ran $worker once"
}

drive_direct_event_invocation_created() {
  local direct_event_id="$1"
  local output_file="$2"
  local key_prefix="$3"

  main_ssh "for i in \$(seq 1 40); do loom direct-event inspect '$direct_event_id' --json > '$output_file'; invocation_id=\$(jq -r '.data.direct_event.invocation_id // \"\"' '$output_file'); status=\$(jq -r '.data.direct_event.status // \"\"' '$output_file'); if echo \"\$invocation_id\" | grep -q '^invocation_'; then exit 0; fi; case \"\$status\" in failed|rejected) cat '$output_file' >&2; exit 1;; esac; loom worker run main.direct_event_ingest --once --json --idempotency-key '${key_prefix}_'\$i > '/tmp/${key_prefix}_ingest_'\$i'.json' 2> '/tmp/${key_prefix}_ingest_'\$i'.err' || true; sleep 1; done; cat '$output_file' >&2; exit 1"
  pass "direct event created invocation"
}

drive_direct_event_job_created() {
  local direct_event_id="$1"
  local output_file="$2"
  local key_prefix="$3"

  main_ssh "for i in \$(seq 1 40); do loom direct-event inspect '$direct_event_id' --json > '$output_file'; job_id=\$(jq -r '.data.direct_event.job_id // \"\"' '$output_file'); status=\$(jq -r '.data.direct_event.status // \"\"' '$output_file'); if echo \"\$job_id\" | grep -q '^job_'; then exit 0; fi; case \"\$status\" in failed|rejected) cat '$output_file' >&2; exit 1;; esac; loom worker run main.automation_dispatcher --once --json --idempotency-key '${key_prefix}_'\$i > '/tmp/${key_prefix}_dispatcher_'\$i'.json' 2> '/tmp/${key_prefix}_dispatcher_'\$i'.err' || true; sleep 1; done; cat '$output_file' >&2; exit 1"
  pass "direct event created routed job"
}

drive_job_completed() {
  local job_id="$1"
  local output_file="$2"
  local key_prefix="$3"

  main_ssh "for i in \$(seq 1 60); do loom job inspect '$job_id' --json > '$output_file'; status=\$(jq -r '.data.job.status' '$output_file'); if [ \"\$status\" = completed ]; then exit 0; fi; case \"\$status\" in failed|timed_out|cancelled) cat '$output_file' >&2; exit 1;; esac; loom worker run main.job_runner --once --json --idempotency-key '${key_prefix}_'\$i > '/tmp/${key_prefix}_job_runner_'\$i'.json' 2> '/tmp/${key_prefix}_job_runner_'\$i'.err' || true; sleep 1; done; cat '$output_file' >&2; exit 1"
  pass "job completed"
}

# Slice 13 failure matrix.
#
# failure kind                  expected surface
# daemon unreachable             health/status command failure
# node main unreachable           local node-agent offline_queueing
# job script failure              jobs failures + portal jobs screen
# direct event auth failure       direct-events failures + status counts
# duplicate direct event          duplicate direct_event record, no new job
# backup verification failure     maintenance finding
# watched-root queue pressure     watched-root finding/degraded status
# index queue failure             indexes failures
# portal machine mode             JSON error / non-ANSI human error
#
# Part 1 proves the baseline inspection surfaces. Parts 2 and 3 replace the
# lightweight placeholders below with deliberate failure and recovery drills.

require_command jq
require_command ssh
require_command rsync

log "main: $MAIN_HOST"
log "main URL: $MAIN_URL"
log "remote dir: $REMOTE_DIR"
log "workspace script: $WORKSPACE_SCRIPT"
log "node key: $NODE_KEY"
log "project: $SMOKE_SLUG"
log "run: $RUN_ID"

section "baseline"
assert_remote "SSH target reachable" 'hostname'
assert_remote "remote source dir present" "test -d '$REMOTE_DIR'"
assert_remote "jq available remotely" 'command -v jq'
assert_remote "curl available remotely" 'command -v curl'
assert_remote "loomd service active" 'systemctl is-active loomd'
assert_remote "loom CLI installed" 'command -v loom && loom version'
check_main_json "main health ok and migrated for v0.2 hardening" \
  'loom health --json --correlation-id corr_smoke_v0_2_slice_13_health' \
  '.ok == true and .data.status == "ok" and .data.checks.migrations.current_version >= 25'
pass "hardening failure matrix encoded"

section "worker and queue health"
check_main_json "worker list includes all v0.2 main workers" \
  'loom workers list --json --correlation-id corr_smoke_v0_2_slice_13_workers' \
  '.ok == true
   and ([.data[] | select(.worker_key == "main.worker_selfcheck" and .worker_kind == "worker_selfcheck")] | length) == 1
   and ([.data[] | select(.worker_key == "main.policy_expiry" and .worker_kind == "policy_expiry")] | length) == 1
   and ([.data[] | select(.worker_key == "main.realtime_expiry" and .worker_kind == "realtime_expiry")] | length) == 1
   and ([.data[] | select(.worker_key == "main.db_maintenance" and .worker_kind == "db_maintenance")] | length) == 1
   and ([.data[] | select(.worker_key == "main.main_backup" and .worker_kind == "main_backup")] | length) == 1
   and ([.data[] | select(.worker_key == "main.object_store_integrity_sample" and .worker_kind == "object_store_integrity")] | length) == 1
   and ([.data[] | select(.worker_key == "main.indexer_text" and .worker_kind == "indexer_text")] | length) == 1
   and ([.data[] | select(.worker_key == "main.job_runner" and .worker_kind == "job_runner")] | length) == 1
   and ([.data[] | select(.worker_key == "main.job_sweeper" and .worker_kind == "job_sweeper")] | length) == 1
   and ([.data[] | select(.worker_key == "main.automation_scheduler" and .worker_kind == "automation_scheduler")] | length) == 1
   and ([.data[] | select(.worker_key == "main.automation_dispatcher" and .worker_kind == "automation_dispatcher")] | length) == 1
   and ([.data[] | select(.worker_key == "main.direct_event_ingest" and .worker_kind == "direct_event_ingest")] | length) == 1'

for worker in \
  main.worker_selfcheck \
  main.policy_expiry \
  main.realtime_expiry \
  main.db_maintenance \
  main.main_backup \
  main.object_store_integrity_sample \
  main.indexer_text \
  main.job_runner \
  main.job_sweeper \
  main.automation_scheduler \
  main.automation_dispatcher \
  main.direct_event_ingest
do
  check_main_json_arg "worker inspectable: $worker" \
    "loom worker inspect '$worker' --json --correlation-id corr_smoke_v0_2_slice_13_worker_${TOKEN_ID}" \
    "$worker" \
    '.ok == true and .data.instance.worker_key == $value and .data.instance.enabled == true'
done

check_main_json "maintenance status readable" \
  'loom maintenance status --json --correlation-id corr_smoke_v0_2_slice_13_maintenance_status' \
  '.ok == true and (.data.overall_status | type) == "string"'
check_main_json "maintenance backup status readable" \
  'loom maintenance backup status --json --correlation-id corr_smoke_v0_2_slice_13_backup_status' \
  '.ok == true and (.data.status | type) == "string"'
check_main_json "jobs status readable" \
  'loom jobs status --json --correlation-id corr_smoke_v0_2_slice_13_jobs_status' \
  '.ok == true and (.data | type) == "object"'
check_main_json "indexes status readable" \
  'loom indexes status --json --correlation-id corr_smoke_v0_2_slice_13_indexes_status' \
  '.ok == true and (.data | type) == "array"'
check_main_json "schedules status readable" \
  'loom schedules status --json --correlation-id corr_smoke_v0_2_slice_13_schedules_status' \
  '.ok == true and (.data | type) == "object"'
check_main_json "direct-events status readable" \
  'loom direct-events status --json --correlation-id corr_smoke_v0_2_slice_13_direct_events_status' \
  '.ok == true and (.data | type) == "object"'
check_main_json "watched-roots status readable" \
  'loom watched-roots status --json --correlation-id corr_smoke_v0_2_slice_13_watched_roots_status' \
  '.ok == true and (.data | type) == "array"'
check_main_json "watched-root backup batches readable" \
  'loom watched-roots backups batches --json --correlation-id corr_smoke_v0_2_slice_13_watched_root_backup_batches' \
  '.ok == true and (.data | type) == "array"'

section "backup restore drill"
BACKUP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/loom-v0-2-slice-13-backup.XXXXXX")"
BACKUP_DIR="$(LOOM_BACKUP_HOST="$MAIN_HOST" LOOM_BACKUP_ROOT="$BACKUP_ROOT" "$REPO_ROOT/scripts/loom-backup" create)"
[[ -d "$BACKUP_DIR" ]] || fail "backup directory was not created: $BACKUP_DIR"
pass "created main backup for restore drill"

"$REPO_ROOT/scripts/loom-backup" verify "$BACKUP_DIR" | grep -q "backup ok:"
pass "backup verification succeeds"

DUMP_FILE="$(jq -r '.database.dump_file' "$BACKUP_DIR/manifest.json")"
OBJECT_STORE_DIR="$(jq -r '.paths.object_store' "$BACKUP_DIR/manifest.json")"
PRIVATE_BACKUPS_DIR="$(jq -r '.paths.private_backups' "$BACKUP_DIR/manifest.json")"
[[ -s "$BACKUP_DIR/$DUMP_FILE" ]] || fail "backup dump is missing or empty"
[[ -d "$BACKUP_DIR/$OBJECT_STORE_DIR" ]] || fail "object-store snapshot directory is missing"
[[ -d "$BACKUP_DIR/$PRIVATE_BACKUPS_DIR" ]] || fail "private-backups snapshot directory is missing"
pass "backup snapshot contains database, object-store, and private-backup artifacts"

RESTORE_DRILL_OUTPUT="$(LOOM_BACKUP_HOST="$MAIN_HOST" LOOM_BACKUP_RESTORE_DRILL_HOST="$MAIN_HOST" "$REPO_ROOT/scripts/loom-backup" restore-drill "$BACKUP_DIR")"
[[ "$RESTORE_DRILL_OUTPUT" == restore\ drill\ ok:* ]] || fail "restore drill did not report success: $RESTORE_DRILL_OUTPUT"
pass "restore drill succeeds non-destructively"

check_main_json "active main remains healthy after restore drill" \
  'loom health --json --correlation-id corr_smoke_v0_2_slice_13_health_after_restore' \
  '.ok == true and .data.status == "ok" and .data.checks.migrations.current_version >= 25'

section "gmail-style direct event"
assert_remote "Gmail URL capture script package is visible to loomd" \
  "test -r '$GMAIL_SCRIPT_MANIFEST' && test -x '$REMOTE_DIR/tests/smoke/scripts/gmail_url_capture/gmail_url_capture.sh' && sudo -u loom test -r '$GMAIL_SCRIPT_MANIFEST'"

check_main_json "register Gmail URL capture script" \
  "loom script register '$GMAIL_SCRIPT_MANIFEST' --activate --json --correlation-id corr_smoke_v0_2_slice_13_gmail_script_register" \
  '.ok == true and .data.script.slug == "gmail_url_capture" and .data.version.status == "active"'

check_main_json "create Gmail bridge external integration" \
  "loom integrations create --key '$GMAIL_INTEGRATION_KEY' --name 'v0.2 Slice 13 Gmail Bridge' --main-auth-level 5 --json --correlation-id corr_smoke_v0_2_slice_13_gmail_integration" \
  ".ok == true and .data.integration.integration_key == \"$GMAIL_INTEGRATION_KEY\" and .data.integration.status == \"active\""

main_ssh "loom integration auth create '$GMAIL_INTEGRATION_KEY' --kind bearer_header --json --correlation-id corr_smoke_v0_2_slice_13_gmail_auth > '$GMAIL_AUTH_JSON'"
check_main_json "create Gmail bridge bearer auth profile" \
  "cat $GMAIL_AUTH_JSON" \
  '.ok == true and (.data.profile.auth_profile_id | startswith("integration_auth_profile_")) and (.data.token | length) > 20'

gmail_auth_profile_id="$(main_json_value "cat $GMAIL_AUTH_JSON" '.data.profile.auth_profile_id')"
gmail_token="$(main_json_value "cat $GMAIL_AUTH_JSON" '.data.token')"
[[ "$gmail_auth_profile_id" == integration_auth_profile_* ]] || fail "expected auth profile id, got $gmail_auth_profile_id"
[[ -n "$gmail_token" ]] || fail "expected Gmail bridge bearer token"
pass "captured Gmail bridge auth profile and token"

main_ssh "loom direct-event endpoints create --slug '$GMAIL_ENDPOINT_SLUG' --name 'v0.2 Slice 13 Gmail URL' --integration '$GMAIL_INTEGRATION_KEY' --event-type gmail.url.detected --target main@script-runner.script.run --response-mode accepted --auth-profile '$gmail_auth_profile_id' --idempotency-strategy payload_path --idempotency-path '\$.message_id' --map 'script_ref=literal:gmail_url_capture' --map 'execution_mode=literal:enqueue_only' --map 'input.url=\$.body.url' --map 'input.message_id=\$.body.message_id' --map 'input.email_id=\$.body.email_id' --map 'input.token=\$.body.token' --required script_ref --required execution_mode --required input.url --required input.message_id --required input.email_id --timeout 30s --json --correlation-id corr_smoke_v0_2_slice_13_gmail_endpoint > '$GMAIL_ENDPOINT_JSON'"
check_main_json "create accepted Gmail-style endpoint" \
  "cat $GMAIL_ENDPOINT_JSON" \
  ".ok == true and .data.endpoint.endpoint_slug == \"$GMAIL_ENDPOINT_SLUG\" and .data.endpoint.response_mode == \"accepted\""

gmail_endpoint_id="$(main_json_value "cat $GMAIL_ENDPOINT_JSON" '.data.endpoint.endpoint_id')"
[[ "$gmail_endpoint_id" == direct_event_endpoint_* ]] || fail "expected direct event endpoint id, got $gmail_endpoint_id"
pass "captured Gmail-style endpoint id"

main_ssh "code=\$(curl -sS --unix-socket /run/loom/loomd.sock -o '$GMAIL_UNAUTH_BODY' -w '%{http_code}' -H 'Content-Type: application/json' --data '{\"url\":\"$GMAIL_URL\",\"message_id\":\"unauth_$GMAIL_MESSAGE_ID\",\"email_id\":\"$GMAIL_EMAIL_ID\",\"token\":\"$GMAIL_TOKEN\"}' 'http://loom/v1/direct-events/ingest/$GMAIL_ENDPOINT_SLUG'); test \"\$code\" = 401"
pass "unauthenticated Gmail-style ingest is rejected"

main_ssh "curl -sS --unix-socket /run/loom/loomd.sock -H 'Content-Type: application/json' -H 'Authorization: Bearer $gmail_token' --data '{\"url\":\"$GMAIL_URL\",\"message_id\":\"$GMAIL_MESSAGE_ID\",\"email_id\":\"$GMAIL_EMAIL_ID\",\"token\":\"$GMAIL_TOKEN\"}' 'http://loom/v1/direct-events/ingest/$GMAIL_ENDPOINT_SLUG' > '$GMAIL_POST_BODY'"
check_main_json "authenticated Gmail-style ingest is accepted async" \
  "cat $GMAIL_POST_BODY" \
  ".ok == true
   and .data.status == \"accepted\"
   and .data.response_mode == \"accepted\"
   and .data.direct_event.status == \"accepted\"
   and .data.direct_event.invocation_id == null"

gmail_direct_event_id="$(main_json_value "cat $GMAIL_POST_BODY" '.data.direct_event.direct_event_id')"
[[ "$gmail_direct_event_id" == direct_event_* ]] || fail "expected Gmail direct event id, got $gmail_direct_event_id"
pass "captured Gmail-style direct event id"

run_worker_once_json "main.direct_event_ingest" "$GMAIL_INGEST_WORKER_JSON" "smoke_v0_2_slice_13_gmail_ingest_$TOKEN_ID"
drive_direct_event_invocation_created "$gmail_direct_event_id" "$GMAIL_EVENT_JSON" "smoke_v0_2_slice_13_gmail_ingest_wait_$TOKEN_ID"
check_main_json "Gmail-style event links invocation after ingest" \
  "cat $GMAIL_EVENT_JSON" \
  ".ok == true
   and .data.direct_event.direct_event_id == \"$gmail_direct_event_id\"
   and (.data.direct_event.invocation_id | startswith(\"invocation_\"))
   and (.data.direct_event | has(\"raw_body_json\") | not)"

gmail_invocation_id="$(main_json_value "cat $GMAIL_EVENT_JSON" '.data.direct_event.invocation_id')"
[[ "$gmail_invocation_id" == invocation_* ]] || fail "expected Gmail invocation id, got $gmail_invocation_id"
pass "captured Gmail-style invocation id"

run_worker_once_json "main.automation_dispatcher" "$GMAIL_DISPATCHER_JSON" "smoke_v0_2_slice_13_gmail_dispatcher_$TOKEN_ID"
drive_direct_event_job_created "$gmail_direct_event_id" "$GMAIL_EVENT_JSON" "smoke_v0_2_slice_13_gmail_dispatch_wait_$TOKEN_ID"
check_main_json "Gmail-style dispatcher links route capability call and job" \
  "cat $GMAIL_EVENT_JSON" \
  ".ok == true
   and (.data.direct_event.route_id | startswith(\"route_\"))
   and (.data.direct_event.capability_call_id | startswith(\"capability_call_\"))
   and (.data.direct_event.job_id | startswith(\"job_\"))"

gmail_route_id="$(main_json_value "cat $GMAIL_EVENT_JSON" '.data.direct_event.route_id')"
gmail_call_id="$(main_json_value "cat $GMAIL_EVENT_JSON" '.data.direct_event.capability_call_id')"
gmail_job_id="$(main_json_value "cat $GMAIL_EVENT_JSON" '.data.direct_event.job_id')"
[[ "$gmail_route_id" == route_* ]] || fail "expected Gmail route id, got $gmail_route_id"
[[ "$gmail_call_id" == capability_call_* ]] || fail "expected Gmail capability call id, got $gmail_call_id"
[[ "$gmail_job_id" == job_* ]] || fail "expected Gmail job id, got $gmail_job_id"
pass "captured Gmail-style route, capability call, and job ids"

drive_job_completed "$gmail_job_id" "$GMAIL_JOB_JSON" "smoke_v0_2_slice_13_gmail_job_$TOKEN_ID"
check_main_json "Gmail URL capture job logs contain mapped payload fields" \
  "cat $GMAIL_JOB_JSON" \
  ".ok == true
   and .data.job.status == \"completed\"
   and ([.data.logs[].tail_text] | join(\"\\n\") | contains(\"$GMAIL_MESSAGE_ID\"))
   and ([.data.logs[].tail_text] | join(\"\\n\") | contains(\"$GMAIL_EMAIL_ID\"))
   and ([.data.logs[].tail_text] | join(\"\\n\") | contains(\"$GMAIL_URL\"))
   and ([.data.logs[].tail_text] | join(\"\\n\") | contains(\"$GMAIL_TOKEN\"))"

main_ssh "loom job outputs '$gmail_job_id' --json --correlation-id corr_smoke_v0_2_slice_13_gmail_outputs > '$GMAIL_OUTPUTS_JSON'"
check_main_json "Gmail URL capture job outputs contain mapped fields" \
  "cat $GMAIL_OUTPUTS_JSON" \
  ".ok == true
   and ([.data[].value_json | tostring] | join(\"\\n\") | contains(\"$GMAIL_MESSAGE_ID\"))
   and ([.data[].value_json | tostring] | join(\"\\n\") | contains(\"$GMAIL_EMAIL_ID\"))
   and ([.data[].value_json | tostring] | join(\"\\n\") | contains(\"$GMAIL_URL\"))
   and ([.data[].value_json | tostring] | join(\"\\n\") | contains(\"$GMAIL_TOKEN\"))"

main_ssh "curl -sS --unix-socket /run/loom/loomd.sock -H 'Content-Type: application/json' -H 'Authorization: Bearer $gmail_token' --data '{\"url\":\"$GMAIL_URL\",\"message_id\":\"$GMAIL_MESSAGE_ID\",\"email_id\":\"$GMAIL_EMAIL_ID\",\"token\":\"$GMAIL_TOKEN\"}' 'http://loom/v1/direct-events/ingest/$GMAIL_ENDPOINT_SLUG' > '$GMAIL_DUP_BODY'"
check_main_json "duplicate Gmail-style event reuses original job" \
  "cat $GMAIL_DUP_BODY" \
  ".ok == true
   and .data.duplicate == true
   and .data.direct_event.direct_event_id == \"$gmail_direct_event_id\"
   and .data.direct_event.invocation_id == \"$gmail_invocation_id\"
   and .data.direct_event.job_id == \"$gmail_job_id\""

check_main_json "Gmail-style raw payload surface is explicit and redacted" \
  "loom direct-event raw '$gmail_direct_event_id' --json --correlation-id corr_smoke_v0_2_slice_13_gmail_raw" \
  ".ok == true
   and .data.direct_event_id == \"$gmail_direct_event_id\"
   and .data.body_json.message_id == \"$GMAIL_MESSAGE_ID\"
   and .data.body_json.email_id == \"$GMAIL_EMAIL_ID\"
   and .data.body_json.url == \"$GMAIL_URL\"
   and .data.body_json.token == \"$GMAIL_TOKEN\"
   and .data.headers_json.authorization == \"[redacted]\""

check_main_json "direct-event failures include rejected Gmail unauthenticated event" \
  'loom direct-events failures --json --correlation-id corr_smoke_v0_2_slice_13_gmail_failures' \
  ".ok == true
   and ([.data[] | select(.status == \"rejected\" and .endpoint_id == \"$gmail_endpoint_id\")] | length) >= 1"

section "node offline reconnect"
"$PRE_SLICE_SMOKE"
pass "remote WireGuard prerequisite passed"

"$WORKSPACE_SCRIPT" sync
"$WORKSPACE_SCRIPT" run -- version >/dev/null
pass "loom-node-agent builds on workspace VM"

workspace_ssh "rm -rf '$REMOTE_TMP' && mkdir -p '$REMOTE_TMP'"
pass "created isolated workspace node-agent state"

agent_json \
  init \
  --main-url "$MAIN_URL" \
  --node-key "$NODE_KEY" \
  --display-name "v0.2 Slice 13 Hardening ${RUN_ID}" \
  --kind workspace \
  --role workspace \
  --runtime-class workspace >/dev/null
pass "workspace node-agent initialized"

TOKEN_JSON="$(main_ssh "loom node enrollment-token create --ttl-seconds 1800 --json --correlation-id corr_smoke_v0_2_slice_13_enrollment_token")"
TOKEN_VALUE="$(printf '%s' "$TOKEN_JSON" | jq -r '.data.token_value')"
[[ "$TOKEN_VALUE" == node_enroll_* ]] || fail "expected enrollment token"
pass "owner created enrollment token"

ENROLL_JSON="$(agent_json enroll --token "$TOKEN_VALUE")"
REQUEST_ID="$(printf '%s' "$ENROLL_JSON" | jq -r '.data.node_enrollment_request_id')"
check_json_value "workspace created pending enrollment request" "$ENROLL_JSON" \
  '.ok == true and .data.status == "pending" and (.data.node_enrollment_request_id | startswith("node_enrollment_request_"))'

APPROVAL_JSON="$(main_ssh "loom node enrollment-request approve '$REQUEST_ID' --json --correlation-id corr_smoke_v0_2_slice_13_enrollment_approve")"
NODE_ID="$(printf '%s' "$APPROVAL_JSON" | jq -r '.data.node.node_id')"
CREDENTIAL_ID="$(printf '%s' "$APPROVAL_JSON" | jq -r '.data.credential.node_credential_id')"
CREDENTIAL_TOKEN="$(printf '%s' "$APPROVAL_JSON" | jq -r '.data.credential_token')"
[[ "$NODE_ID" == node_* ]] || fail "expected approved node id"
[[ "$CREDENTIAL_ID" == node_credential_* ]] || fail "expected credential id"
[[ "$CREDENTIAL_TOKEN" == node_cred_* ]] || fail "expected credential token"
check_json_value "owner approved workspace enrollment" "$APPROVAL_JSON" \
  '.ok == true and .data.request.status == "approved" and .data.node.status == "active" and .data.node.credential_status == "active"'

agent_json \
  credential import \
  --node-id "$NODE_ID" \
  --credential-id "$CREDENTIAL_ID" \
  --credential-token "$CREDENTIAL_TOKEN" >/dev/null
pass "workspace imported approved credential"

HEARTBEAT_JSON="$(agent_json workers run node-agent.node_heartbeat --once)"
check_json_value "node-agent heartbeat starts healthy" "$HEARTBEAT_JSON" \
  '.ok == true and .data.run.status == "succeeded" and .data.health.status == "healthy" and .data.run.result_json.presence_state == "online"'

MESSAGE_IDEMPOTENCY_KEY="v0-2-slice-13-message-${RUN_ID}"
MESSAGE_JSON="$(main_ssh "loom message enqueue --node '$NODE_ID' --kind main.ping --input '{\"text\":\"hello from v0.2 slice 13\"}' --idempotency-key '$MESSAGE_IDEMPOTENCY_KEY' --json --correlation-id corr_smoke_v0_2_slice_13_message_enqueue")"
MESSAGE_ID="$(printf '%s' "$MESSAGE_JSON" | jq -r '.data.communication_message_id')"
[[ "$MESSAGE_ID" == communication_message_* ]] || fail "expected communication message id"
check_json_value "main enqueued ping for workspace node" "$MESSAGE_JSON" \
  '.ok == true and .data.kind == "main.ping" and .data.status == "available"'

POLL_JSON="$(agent_json poll --once --no-flush --max-messages 10)"
RUNTIME_OUTBOX_ID="$(printf '%s' "$POLL_JSON" | jq -r '.data.processed[0].runtime_ack_outbox_id')"
[[ "$RUNTIME_OUTBOX_ID" == local_outbox_* ]] || fail "expected runtime outbox id"
check_json_value "poll queues ack outbox without flushing" "$POLL_JSON" \
  ".ok == true and (.data.poll.messages | length) == 1 and .data.poll.messages[0].communication_message_id == \"$MESSAGE_ID\" and .data.processed[0].runtime_ack_outbox_status == \"pending\""

OUTBOX_PENDING_JSON="$(agent_json outbox status)"
check_json_value "node-agent outbox starts with pending ack" "$OUTBOX_PENDING_JSON" \
  '.ok == true and .data.counts.pending == 1 and .data.counts.done == 0'

agent_json_config "$REMOTE_OFFLINE_CONFIG" \
  init \
  --main-url "http://127.0.0.1:1" \
  --node-key "$NODE_KEY" \
  --display-name "v0.2 Slice 13 Offline Config ${RUN_ID}" \
  --kind workspace \
  --role workspace \
  --runtime-class workspace >/dev/null
pass "workspace offline config initialized"

OFFLINE_FLUSH_JSON="$(agent_json_config "$REMOTE_OFFLINE_CONFIG" outbox flush --max-items 10)"
check_json_value "offline outbox flush surfaces retryable failure locally" "$OFFLINE_FLUSH_JSON" \
  '.ok == true and .data.submitted == 1 and .data.failed == 1 and .data.summary.counts.failed == 1'

OFFLINE_HEARTBEAT_JSON="$(agent_json_config "$REMOTE_OFFLINE_CONFIG" workers run node-agent.node_heartbeat --once)"
check_json_value "offline heartbeat records offline queueing locally" "$OFFLINE_HEARTBEAT_JSON" \
  '.ok == true and .data.run.status == "failed" and .data.health.status == "offline_queueing"'

RECOVERY_FLUSH_JSON="$(agent_json outbox flush --max-items 10)"
check_json_value "normal outbox flush recovers queued ack after reconnect" "$RECOVERY_FLUSH_JSON" \
  '.ok == true and .data.submitted == 1 and .data.done == 1 and .data.summary.counts.pending == 0 and .data.summary.counts.failed == 0'

POST_FLUSH_INSPECT_JSON="$(main_ssh "loom message inspect '$MESSAGE_ID' --json --correlation-id corr_smoke_v0_2_slice_13_message_inspect")"
check_json_value "main message inspect shows recovered ack result" "$POST_FLUSH_INSPECT_JSON" \
  '.ok == true and .data.status == "acked" and .data.result_json.pong == true and .data.result_json.received_text == "hello from v0.2 slice 13"'

RECOVERY_HEARTBEAT_JSON="$(agent_json workers run node-agent.node_heartbeat --once)"
check_json_value "normal heartbeat recovers after offline simulation" "$RECOVERY_HEARTBEAT_JSON" \
  '.ok == true and .data.run.status == "succeeded" and .data.health.status == "healthy"'

HEALTH_JSON="$(main_ssh "loom node health '$NODE_ID' --json --correlation-id corr_smoke_v0_2_slice_13_node_health")"
check_json_value "main node health includes recovered runtime queue summary" "$HEALTH_JSON" \
  '.ok == true and .data.pending_messages == 0 and .data.last_heartbeat.storage_status_json.runtime.queues.outbox_done >= 1'

section "watched-root pressure"
check_main_json "create smoke project for watched-root pressure" \
  "loom project create '$SMOKE_NAME' --slug '$SMOKE_SLUG' --description 'v0.2 Slice 13 watched-root pressure smoke' --if-not-exists --json --correlation-id corr_smoke_v0_2_slice_13_project_create" \
  ".ok == true and .data.project.project.slug == \"$SMOKE_SLUG\""

workspace_ssh "mkdir -p '$REMOTE_VAULT/.obsidian' && printf '# Accepted\n\nToken: $ACCEPTED_TOKEN\n' > '$REMOTE_VAULT/00-Accepted.md' && printf '# Queue Limit\n\nToken: $QUEUE_TOKEN\n' > '$REMOTE_VAULT/01-QueueLimit.md' && { printf '# Oversize\n\nToken: $OVERSIZE_TOKEN\n'; head -c 160 /dev/zero | tr '\0' 'x'; printf '\n'; } > '$REMOTE_VAULT/99-Oversize.md' && printf '{\"workspace\":\"ignored\"}\n' > '$REMOTE_VAULT/.obsidian/workspace.json'"
pass "created watched-root pressure fixture"

ROOTS_JSON="$(agent_json filesystem roots add --key slice13 --path "$REMOTE_VAULT")"
check_json_value "filesystem safe root configured for pressure fixture" "$ROOTS_JSON" \
  '.ok == true and ([.data.safe_roots[] | select(.root_key == "slice13" and .allow_list == true)] | length) == 1'

ADD_JSON="$(agent_json watched-roots add pressure --safe-root slice13 --path . --include '**/*.md' --exclude '.obsidian/**' --sync-mode selected_files --index-mode markdown_text --delete-mode tombstone --project "$SMOKE_SLUG" --sync-max-file-bytes 96 --backup-mode incremental_raw --backup-max-file-bytes 1024 --backup-max-pending-items 1)"
check_json_value "watched root configured with bounded sync/index/backup policy" "$ADD_JSON" \
  '.ok == true
   and .data.config.root_key == "pressure"
   and .data.config.sync_policy.mode == "selected_files"
   and .data.config.index_policy.mode == "markdown_text"
   and .data.config.backup_policy.mode == "incremental_raw"
   and .data.config.backup_policy.max_pending_items == 1'

RUN_PRESSURE_JSON="$(agent_json watched-roots run pressure --once --mode full --stability-window 0s --flush)"
check_json_value "watched-root pressure degrades visibly instead of silently dropping work" "$RUN_PRESSURE_JSON" \
  '.ok == true
   and .data.status == "degraded"
   and .data.output_plan.counts.failed >= 1
   and .data.output_plan.counts.backup_files >= 2
   and .data.output_plan.counts.skipped >= 1
   and ([.data.output_plan.actions[] | select(.relative_path == "99-Oversize.md" and .action_kind == "skipped" and .reason_code == "skipped_too_large")] | length) == 1
   and .data.backup_output_flush.status == "recorded"
   and .data.main_report.status == "recorded"
   and .data.main_report.findings_reported >= 1'

LOCAL_BACKUP_STATUS_JSON="$(agent_json watched-roots backups status pressure)"
check_json_value "local backup status exposes accepted bounded backup item" "$LOCAL_BACKUP_STATUS_JSON" \
  '.ok == true
   and .data.counts.accepted >= 1
   and .data.limits.max_pending_items == 1
   and .data.limits.mode == "incremental_raw"'

LOCAL_PRESSURE_STATUS_JSON="$(agent_json watched-roots status pressure)"
check_json_value "local watched-root status exposes pressure findings" "$LOCAL_PRESSURE_STATUS_JSON" \
  '.ok == true
   and (.data.roots | length) == 1
   and (.data.roots[0].status == "healthy" or .data.roots[0].status == "degraded")
   and .data.roots[0].findings >= 1
   and .data.roots[0].backup_status.counts.accepted >= 1
   and .data.roots[0].backup_status.limits.max_pending_items == 1'

EXPLAIN_ACCEPTED_JSON="$(agent_json watched-roots explain pressure --path 00-Accepted.md)"
ACCEPTED_OBJECT_ID="$(printf '%s' "$EXPLAIN_ACCEPTED_JSON" | jq -r '.data.state.main_object_id')"
ACCEPTED_VERSION_ID="$(printf '%s' "$EXPLAIN_ACCEPTED_JSON" | jq -r '.data.state.main_version_id')"
[[ "$ACCEPTED_OBJECT_ID" == object_* ]] || fail "expected accepted main object id, got $ACCEPTED_OBJECT_ID"
[[ "$ACCEPTED_VERSION_ID" == version_* ]] || fail "expected accepted main version id, got $ACCEPTED_VERSION_ID"
check_json_value "accepted markdown records main sync refs" "$EXPLAIN_ACCEPTED_JSON" \
  ".ok == true and .data.state.main_object_id == \"$ACCEPTED_OBJECT_ID\" and .data.state.index_status == \"queued\""

EXPLAIN_OVERSIZE_JSON="$(agent_json watched-roots explain pressure --path 99-Oversize.md)"
check_json_value "oversize markdown records skipped sync state" "$EXPLAIN_OVERSIZE_JSON" \
  '.ok == true and .data.state.sync_status == "skipped" and .data.state.last_output_error_code == "skipped_too_large"'

check_main_json "main watched-root status exposes degraded pressure report" \
  "loom watched-roots status --node '$NODE_ID' --root pressure --json --correlation-id corr_smoke_v0_2_slice_13_pressure_status" \
  '.ok == true and (.data | length) == 1 and .data[0].root.status == "degraded" and (.data[0].latest_findings | length) >= 1'

check_main_json "main watched-root findings include backup queue limit" \
  "loom watched-roots findings --node '$NODE_ID' --root pressure --status open --json --correlation-id corr_smoke_v0_2_slice_13_pressure_findings" \
  '.ok == true and ([.data[] | select(.kind == "backup_queue_limit_reached")] | length) >= 1'

main_ssh "loom indexes run text --once --json --correlation-id corr_smoke_v0_2_slice_13_indexer_pressure > /tmp/loom-v0-2-slice-13-indexer-pressure-$TOKEN_ID.json"
check_main_json "text indexer processes accepted pressure markdown" \
  "cat /tmp/loom-v0-2-slice-13-indexer-pressure-$TOKEN_ID.json" \
  '.ok == true and .data.run.run_status == "succeeded" and (.data.run.result_summary_json.indexed | type) == "number"'

check_main_json "search finds accepted pressure token" \
  "loom search '$ACCEPTED_TOKEN' --project '$SMOKE_SLUG' --json --correlation-id corr_smoke_v0_2_slice_13_search_accepted" \
  ".ok == true and .data.result_count >= 1 and ([.data.results[] | select(.object_id == \"$ACCEPTED_OBJECT_ID\" and .object_version_id == \"$ACCEPTED_VERSION_ID\")] | length) >= 1"

check_main_json "search does not find oversize skipped token" \
  "loom search '$OVERSIZE_TOKEN' --project '$SMOKE_SLUG' --json --correlation-id corr_smoke_v0_2_slice_13_search_oversize" \
  '.ok == true and .data.result_count == 0'

check_main_json "post-pressure jobs status readable" \
  'loom jobs status --json --correlation-id corr_smoke_v0_2_slice_13_jobs_post_pressure' \
  '.ok == true and (.data | type) == "object"'
check_main_json "post-pressure indexes status readable" \
  'loom indexes status --json --correlation-id corr_smoke_v0_2_slice_13_indexes_post_pressure' \
  '.ok == true and (.data | type) == "array"'
check_main_json "post-pressure workers status readable" \
  'loom workers list --json --correlation-id corr_smoke_v0_2_slice_13_workers_post_pressure' \
  '.ok == true and (.data | type) == "array"'
check_main_json "post-pressure maintenance status readable" \
  'loom maintenance status --json --correlation-id corr_smoke_v0_2_slice_13_maintenance_post_pressure' \
  '.ok == true and (.data | type) == "object"'

section "portal inspection"
assert_remote "portal home renders" \
  'loom enter --start home --exit-after-render | grep -q "Attention"'
assert_remote "portal background renders" \
  'loom enter --start background --exit-after-render | grep -q "Background Operations"'
assert_remote "portal automations renders" \
  'loom enter --start automations --exit-after-render | grep -q "Automation Center"'
assert_remote "portal jobs renders" \
  'loom enter --start jobs --exit-after-render | grep -q "Jobs And Search"'
assert_remote "portal nodes renders" \
  'loom enter --start nodes --exit-after-render | grep -q "Nodes And Watched Roots"'
assert_remote "portal direct-event search reaches automation center" \
  'loom enter --search "direct event" --exit-after-render | grep -q "Automation Center"'
assert_remote "portal watched-root search reaches nodes screen" \
  'loom enter --search "watched root" --exit-after-render | grep -q "Nodes And Watched Roots"'
assert_remote "portal backup search reaches backup surfaces" \
  'loom enter --search backup --exit-after-render | grep -q "Backup"'
assert_remote "portal direct-event status action preview shows raw command" \
  'loom enter --preview-action raw.direct_events.status --exit-after-render | grep -q "Raw: loom direct-events status"'
assert_remote "portal watched-root backup action preview shows raw command" \
  'loom enter --preview-action raw.watched_roots.backups.status --exit-after-render | grep -q "Raw: loom watched-roots backups status"'

section "machine-safe output"
assert_remote "raw JSON output remains clean" \
  'loom health --json --correlation-id corr_smoke_v0_2_slice_13_machine_health | jq -e ".ok == true"'
assert_remote "plain status output remains one line" \
  'test "$(loom status --plain | wc -l)" -eq 1'
assert_remote "NO_COLOR suppresses ANSI for human worker list" \
  'NO_COLOR=1 loom workers list >/tmp/loom-v0-2-slice-13-workers-no-color.out && ! grep -q "$(printf "\033")" /tmp/loom-v0-2-slice-13-workers-no-color.out'
assert_remote "portal rejects noninteractive mode without ANSI" \
  'set +e
   LOOM_NONINTERACTIVE=1 loom enter >/tmp/loom-v0-2-slice-13-enter-noninteractive.out 2>/tmp/loom-v0-2-slice-13-enter-noninteractive.err
   status=$?
   set -e
   test "$status" -ne 0
   test ! -s /tmp/loom-v0-2-slice-13-enter-noninteractive.out
   grep -q "portal.unavailable" /tmp/loom-v0-2-slice-13-enter-noninteractive.err
   ! grep -q "$(printf "\033")" /tmp/loom-v0-2-slice-13-enter-noninteractive.err'
assert_remote "portal rejects JSON machine mode as JSON" \
  'set +e
   loom --json enter >/tmp/loom-v0-2-slice-13-enter-json.out 2>/tmp/loom-v0-2-slice-13-enter-json.err
   status=$?
   set -e
   test "$status" -ne 0
   test ! -s /tmp/loom-v0-2-slice-13-enter-json.err
   jq -e ".ok == false and .error.code == \"portal.machine_mode_unsupported\"" /tmp/loom-v0-2-slice-13-enter-json.out'

section "docs/version acceptance"
[[ -f "$REPO_ROOT/docs/v0.2 Acceptance.md" ]] || fail "missing docs/v0.2 Acceptance.md"
grep -q "v0-2-acceptance" "$REPO_ROOT/docs/v0.2 Acceptance.md"
grep -q "restore-drill" "$REPO_ROOT/docs/Operations - Backup And Restore.md"
pass "v0.2 acceptance and restore-drill docs are present"

log "completed $pass_count checks"
