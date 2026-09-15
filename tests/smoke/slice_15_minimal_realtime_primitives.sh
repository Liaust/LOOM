#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." && pwd)"

HOST="${LOOM_DEV_HOST:-loom-dev}"
REMOTE_DIR="${LOOM_DEV_REMOTE_DIR:-/srv/loom/current}"
RUN_ID="$(date +%Y%m%d%H%M%S)-$RANDOM"
SMOKE_SLUG="slice-15-smoke-$RUN_ID"
SMOKE_NAME="Slice 15 Smoke $RUN_ID"
INGEST_PATH="$REMOTE_DIR/tests/smoke/slice_15_input_$RUN_ID.md"
MANIFEST_PATH="$REMOTE_DIR/tests/smoke/scripts/word_count/loom.script.yaml"
TOPIC_PATH="smoke.slice15.$RUN_ID"
LEASE_RESOURCE="custom:slice15-$RUN_ID"
EXPIRING_LEASE_RESOURCE="custom:slice15-expire-$RUN_ID"
NODE_KEY="slice-15-presence-$RUN_ID"
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
log "project slug: $SMOKE_SLUG"
log "topic: $TOPIC_PATH"

check_remote "SSH target reachable" 'hostname'
check_remote "loomd service active" 'systemctl is-active loomd'
check_remote "loom CLI installed" 'command -v loom && loom version'
check_remote "jq and curl available for smoke assertions" 'command -v jq && command -v curl'

check_json "health reports Slice 15 realtime migration" \
  'loom health --json' \
  '.ok == true and .data.status == "ok" and .data.checks.migrations.current_version >= 12'

check_json "project create succeeds" \
  "loom project create '$SMOKE_NAME' --slug '$SMOKE_SLUG' --description 'Slice 15 realtime smoke project' --if-not-exists --json --correlation-id corr_smoke_slice_15_project_create" \
  ".ok == true and .data.project.project.slug == \"$SMOKE_SLUG\""

check_remote "script package is visible to loomd" \
  "test -r '$MANIFEST_PATH' && test -x '$REMOTE_DIR/tests/smoke/scripts/word_count/word_count.sh' && sudo -u loom test -r '$MANIFEST_PATH'"

check_remote "write smoke input file visible to loomd" \
  "mkdir -p '$REMOTE_DIR/tests/smoke' && printf '# Slice 15 Smoke\n\nRealtime progress validates job execution for $RUN_ID.\n' > '$INGEST_PATH' && chmod 0644 '$INGEST_PATH' && sudo -u loom test -r '$INGEST_PATH'"

remote "loom object ingest '$INGEST_PATH' --project '$SMOKE_SLUG' --name slice-15-input.md --json --correlation-id corr_smoke_slice_15_object_ingest > /tmp/loom-slice-15-ingest.json"
object_id="$(json_value 'cat /tmp/loom-slice-15-ingest.json' '.data.object.object.object_id')"
[[ "$object_id" == object_* ]] || fail "expected object id, got $object_id"
pass "captured source object id"

remote "loom script register '$MANIFEST_PATH' --project '$SMOKE_SLUG' --activate --json --correlation-id corr_smoke_slice_15_script_register > /tmp/loom-slice-15-register.json"
check_json "script register succeeds" \
  'cat /tmp/loom-slice-15-register.json' \
  '.ok == true and .data.script.slug == "word_count" and .data.version.status == "active"'

TOKEN_JSON="$(remote "loom node enrollment-token create --ttl-seconds 600 --json")"
TOKEN_VALUE="$(printf '%s' "$TOKEN_JSON" | jq -r '.data.token_value')"
[[ "$TOKEN_VALUE" == node_enroll_* ]] || fail "expected enrollment token"
pass "owner created enrollment token"

remote "loom node enrollment-request create '$NODE_KEY' --token '$TOKEN_VALUE' --display-name 'Slice 15 Presence $RUN_ID' --kind workspace --role workspace --runtime-class workspace --json > /tmp/loom-slice-15-enroll.json"
request_id="$(json_value 'cat /tmp/loom-slice-15-enroll.json' '.data.node_enrollment_request_id')"
[[ "$request_id" == node_enrollment_request_* ]] || fail "expected enrollment request id"
pass "created node enrollment request"

remote "loom node enrollment-request approve '$request_id' --json > /tmp/loom-slice-15-approve.json"
node_id="$(json_value 'cat /tmp/loom-slice-15-approve.json' '.data.node.node_id')"
credential_token="$(json_value 'cat /tmp/loom-slice-15-approve.json' '.data.credential_token')"
[[ "$node_id" == node_* ]] || fail "expected node id"
[[ "$credential_token" == node_cred_* ]] || fail "expected credential token"
pass "approved presence test node"

heartbeat_payload="$(jq -n --arg node "$node_id" --arg token "$credential_token" --arg run "$RUN_ID" '{
  node_ref: $node,
  credential_token: $token,
  runtime_version: "slice-15-smoke",
  reported_status: "ok",
  inbox_backlog: 0,
  outbox_backlog: 0,
  storage_status: {"status": "ok"},
  error_summary: {"status": "none"},
  metadata: {"slice": "15", "run_id": $run}
}')"
remote "curl --fail --silent --show-error --unix-socket /run/loom/loomd.sock -H 'Content-Type: application/json' -H 'X-Loom-Correlation-ID: corr_smoke_slice_15_heartbeat' -d '$heartbeat_payload' http://loom/v1/node-agent/heartbeat > /tmp/loom-slice-15-heartbeat.json"
check_json "heartbeat projected into realtime presence" \
  "loom realtime presence inspect '$node_id' --json" \
  ".ok == true and .data.subject_ref == \"$node_id\" and .data.state == \"online\""

remote "loom realtime topic create '$TOPIC_PATH' --json --correlation-id corr_smoke_slice_15_topic_create > /tmp/loom-slice-15-topic.json"
topic_id="$(json_value 'cat /tmp/loom-slice-15-topic.json' '.data.topic_id')"
[[ "$topic_id" == topic_* ]] || fail "expected topic id"
pass "created realtime topic"

remote "loom realtime subscription create '$TOPIC_PATH' --cursor from_start --json --correlation-id corr_smoke_slice_15_subscription_create > /tmp/loom-slice-15-subscription.json"
subscription_id="$(json_value 'cat /tmp/loom-slice-15-subscription.json' '.data.subscription_id')"
[[ "$subscription_id" == subscription_* ]] || fail "expected subscription id"
pass "created realtime subscription"

remote "loom realtime topic publish '$TOPIC_PATH' --type smoke.slice15 --input '{\"run_id\":\"$RUN_ID\"}' --json --correlation-id corr_smoke_slice_15_topic_publish > /tmp/loom-slice-15-publication.json"
publication_sequence="$(json_value 'cat /tmp/loom-slice-15-publication.json' '.data.sequence')"
[[ "$publication_sequence" -gt 0 ]] || fail "expected publication sequence"
pass "published retained realtime message"

check_json "subscription poll replays retained publication" \
  "loom realtime subscription poll '$subscription_id' --json" \
  ".ok == true and (.data.publications | length) >= 1 and ([.data.publications[].sequence] | index($publication_sequence))"

check_json "subscription acknowledgement advances cursor" \
  "loom realtime subscription ack '$subscription_id' --sequence '$publication_sequence' --json" \
  ".ok == true and .data.cursor_sequence == $publication_sequence and .data.last_acknowledged_sequence == $publication_sequence"

check_remote "reset active script grants before approval notification check" '
  loom grants list --json --status active --actor agent:dev-low --target-node main --capability main@script-runner.script.run |
    jq -r ".data[]?.grant_id" |
    while read -r grant; do
      if [ -n "$grant" ]; then
        loom grant revoke "$grant" --actor owner --reason "slice 15 smoke reset" --json >/dev/null
      fi
    done
'

remote "loom capability call main@script-runner.script.run --actor agent:dev-low --request-approval --approval-reason 'slice 15 approval notification' --json --correlation-id corr_smoke_slice_15_approval_required > /tmp/loom-slice-15-approval-required.json"
approval_id="$(json_value 'cat /tmp/loom-slice-15-approval-required.json' '.data.approval_id')"
[[ "$approval_id" == approval_* ]] || fail "expected approval id"
check_json "approval-required capability call creates notification" \
  "loom realtime notifications list --approval '$approval_id' --json" \
  ".ok == true and ([.data[] | select(.approval_id == \"$approval_id\" and .category == \"approval\")] | length) >= 1"
notification_id="$(json_value "loom realtime notifications list --approval '$approval_id' --json" '.data[0].notification_id')"
[[ "$notification_id" == notification_* ]] || fail "expected notification id"
check_json "approval notification acknowledgement works" \
  "loom realtime notification ack '$notification_id' --json" \
  ".ok == true and .data.status == \"acknowledged\""

remote "loom script run word_count --object '$object_id' --project '$SMOKE_SLUG' --json --correlation-id corr_smoke_slice_15_script_run > /tmp/loom-slice-15-run.json"
job_id="$(json_value 'cat /tmp/loom-slice-15-run.json' '.data.job.job.job_id')"
[[ "$job_id" == job_* ]] || fail "expected job id"
check_json "script run still completes" \
  'cat /tmp/loom-slice-15-run.json' \
  ".ok == true and .data.job.job.status == \"completed\" and .data.job.job.job_id == \"$job_id\""
check_json "job progress feed records lifecycle updates" \
  "loom realtime progress inspect 'job:$job_id' --json --limit 20" \
  ".ok == true and .data.feed.current_status == \"succeeded\" and .data.feed.source_ref == \"$job_id\" and ([.data.updates[].status] | index(\"running\") and index(\"succeeded\"))"

remote "loom realtime lease request --resource '$LEASE_RESOURCE' --mode exclusive --duration 5m --json --correlation-id corr_smoke_slice_15_lease_request > /tmp/loom-slice-15-lease.json"
lease_id="$(json_value 'cat /tmp/loom-slice-15-lease.json' '.data.lease_id')"
[[ "$lease_id" == lease_* ]] || fail "expected lease id"
pass "granted exclusive lease"

check_remote "conflicting lease request is denied" "
  set +e
  loom realtime lease request --resource '$LEASE_RESOURCE' --mode write --duration 5m --json --correlation-id corr_smoke_slice_15_lease_conflict > /tmp/loom-slice-15-lease-conflict.json 2> /tmp/loom-slice-15-lease-conflict.err
  status=\$?
  set -e
  test \"\$status\" -ne 0
  jq -e '.ok == false' /tmp/loom-slice-15-lease-conflict.json >/dev/null
"

check_json "lease release works" \
  "loom realtime lease release '$lease_id' --reason 'slice 15 smoke complete' --json" \
  ".ok == true and .data.status == \"released\" and .data.release_reason == \"slice 15 smoke complete\""

remote "loom realtime lease request --resource '$EXPIRING_LEASE_RESOURCE' --mode exclusive --duration 2s --json --correlation-id corr_smoke_slice_15_lease_expiring > /tmp/loom-slice-15-expiring-lease.json"
expiring_lease_id="$(json_value 'cat /tmp/loom-slice-15-expiring-lease.json' '.data.lease_id')"
[[ "$expiring_lease_id" == lease_* ]] || fail "expected expiring lease id"
pass "granted short lease"

sleep 8
check_json "expiry worker marks short lease expired" \
  "loom realtime lease inspect '$expiring_lease_id' --json" \
  ".ok == true and .data.status == \"expired\""

printf '[smoke] slice 15 completed with %d checks\n' "$pass_count"
