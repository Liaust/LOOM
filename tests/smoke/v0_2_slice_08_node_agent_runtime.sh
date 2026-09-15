#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." && pwd)"
WORKSPACE_SCRIPT="$REPO_ROOT/scripts/loom-workspace"
PRE_SLICE_SMOKE="$REPO_ROOT/tests/smoke/pre_slice_10_part_2_wireguard.sh"

MAIN_HOST="${LOOM_DEV_HOST:-loom-dev}"
MAIN_URL="${LOOM_MAIN_URL:-http://10.44.0.2:8080}"
RUN_ID="$(date +%Y%m%d%H%M%S)-$RANDOM"
NODE_KEY="v0-2-slice-08-node-agent-${RUN_ID}"
REMOTE_TMP="/tmp/loom-node-agent-v0-2-slice-08-${RUN_ID}"
REMOTE_CONFIG="${REMOTE_TMP}/config.json"
REMOTE_OFFLINE_CONFIG="${REMOTE_TMP}/offline-config.json"
REMOTE_STATE="${REMOTE_TMP}/state.json"
REMOTE_DATA="${REMOTE_TMP}/data"

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

agent_json() {
  "$WORKSPACE_SCRIPT" run -- \
    --config "$REMOTE_CONFIG" \
    --state "$REMOTE_STATE" \
    --data-dir "$REMOTE_DATA" \
    --json \
    "$@"
}

require_command jq
require_command ssh
require_command rsync

log "main: $MAIN_HOST"
log "main URL: $MAIN_URL"
log "node key: $NODE_KEY"

"$PRE_SLICE_SMOKE"
pass "remote WireGuard prerequisite passed"

"$WORKSPACE_SCRIPT" sync
"$WORKSPACE_SCRIPT" run -- version >/dev/null
pass "loom-node-agent builds on workspace VM"

main_ssh 'loom health --json | jq -e ".ok == true and .data.status == \"ok\" and .data.checks.migrations.current_version >= 9" >/dev/null'
pass "main health ok at migration 9 or newer"

"$WORKSPACE_SCRIPT" ssh "rm -rf '$REMOTE_TMP' && mkdir -p '$REMOTE_TMP'"
agent_json \
  init \
  --main-url "$MAIN_URL" \
  --node-key "$NODE_KEY" \
  --display-name "v0.2 Slice 08 Node Agent ${RUN_ID}" \
  --kind workspace \
  --role workspace \
  --runtime-class workspace >/dev/null
pass "workspace node-agent initialized"

TOKEN_JSON="$(main_ssh "loom node enrollment-token create --ttl-seconds 1800 --json")"
TOKEN_VALUE="$(printf '%s' "$TOKEN_JSON" | jq -r '.data.token_value')"
[[ "$TOKEN_VALUE" == node_enroll_* ]] || fail "expected enrollment token"
pass "owner created enrollment token"

ENROLL_JSON="$(agent_json enroll --token "$TOKEN_VALUE")"
REQUEST_ID="$(printf '%s' "$ENROLL_JSON" | jq -r '.data.node_enrollment_request_id')"
check_json_value "workspace created pending enrollment request" "$ENROLL_JSON" \
  '.ok == true and .data.status == "pending" and (.data.node_enrollment_request_id | startswith("node_enrollment_request_"))'

APPROVAL_JSON="$(main_ssh "loom node enrollment-request approve '$REQUEST_ID' --json")"
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

WORKERS_JSON="$(agent_json workers status)"
check_json_value "node-agent local runtime workers are inspectable" "$WORKERS_JSON" \
  '.ok == true and (.data.workers | length) == 5 and ([.data.workers[].instance.worker_key] | index("node-agent.node_heartbeat")) and ([.data.workers[].instance.worker_key] | index("node-agent.node_outbox_flusher"))'

SELFCHECK_JSON="$(agent_json workers run node-agent.node_supervisor_selfcheck --once)"
check_json_value "node-agent selfcheck worker runs locally" "$SELFCHECK_JSON" \
  '.ok == true and .data.run.status == "succeeded" and (.data.health.status == "healthy" or .data.health.status == "degraded")'

QUEUE_JSON="$(agent_json workers run node-agent.local_queue_reporter --once)"
check_json_value "node-agent queue reporter worker runs locally" "$QUEUE_JSON" \
  '.ok == true and .data.run.status == "succeeded" and .data.health.queue_summary_json.queues.outbox_pending == 0'

HEARTBEAT_JSON="$(agent_json workers run node-agent.node_heartbeat --once)"
check_json_value "node-agent heartbeat worker marks node online" "$HEARTBEAT_JSON" \
  '.ok == true and .data.run.status == "succeeded" and .data.health.status == "healthy" and .data.run.result_json.presence_state == "online"'

MESSAGE_IDEMPOTENCY_KEY="v0-2-slice-08-message-${RUN_ID}"
MESSAGE_JSON="$(main_ssh "loom message enqueue --node '$NODE_ID' --kind main.ping --input '{\"text\":\"hello from v0.2 slice 08\"}' --idempotency-key '$MESSAGE_IDEMPOTENCY_KEY' --json")"
MESSAGE_ID="$(printf '%s' "$MESSAGE_JSON" | jq -r '.data.communication_message_id')"
[[ "$MESSAGE_ID" == communication_message_* ]] || fail "expected communication message id"
check_json_value "main enqueued ping for workspace node" "$MESSAGE_JSON" \
  '.ok == true and .data.kind == "main.ping" and .data.status == "available"'

POLL_JSON="$(agent_json poll --once --no-flush --max-messages 10)"
RUNTIME_OUTBOX_ID="$(printf '%s' "$POLL_JSON" | jq -r '.data.processed[0].runtime_ack_outbox_id')"
[[ "$RUNTIME_OUTBOX_ID" == local_outbox_* ]] || fail "expected runtime outbox id"
check_json_value "poll queued ack outbox without flushing" "$POLL_JSON" \
  ".ok == true and (.data.poll.messages | length) == 1 and .data.poll.messages[0].communication_message_id == \"$MESSAGE_ID\" and .data.processed[0].runtime_ack_outbox_status == \"pending\" and (.data.processed[0].ack.ack.communication_ack_id // \"\") == \"\""

OUTBOX_PENDING_JSON="$(agent_json outbox status)"
check_json_value "node-agent outbox shows pending ack" "$OUTBOX_PENDING_JSON" \
  '.ok == true and .data.counts.pending == 1 and .data.counts.done == 0'

PRE_FLUSH_INSPECT_JSON="$(main_ssh "loom message inspect '$MESSAGE_ID' --json")"
check_json_value "main message is not acked before outbox flush" "$PRE_FLUSH_INSPECT_JSON" \
  '.ok == true and .data.status != "acked"'

FLUSH_JSON="$(agent_json outbox flush --max-items 10)"
check_json_value "node-agent outbox flush sends queued ack" "$FLUSH_JSON" \
  '.ok == true and .data.submitted == 1 and .data.done == 1 and .data.failed == 0 and .data.summary.counts.pending == 0 and .data.summary.counts.done == 1'

POST_FLUSH_INSPECT_JSON="$(main_ssh "loom message inspect '$MESSAGE_ID' --json")"
check_json_value "main message inspect shows flushed ack result" "$POST_FLUSH_INSPECT_JSON" \
  '.ok == true and .data.status == "acked" and .data.result_json.pong == true and .data.result_json.received_text == "hello from v0.2 slice 08"'

INBOX_JSON="$(agent_json inbox status)"
check_json_value "node-agent inbox shows processed message" "$INBOX_JSON" \
  '.ok == true and .data.counts.done == 1'

OUTBOX_LIST_JSON="$(agent_json outbox list --status done)"
check_json_value "node-agent outbox list exposes done ack item" "$OUTBOX_LIST_JSON" \
  ".ok == true and ([.data[] | select(.local_outbox_id == \"$RUNTIME_OUTBOX_ID\" and .kind == \"message_ack\" and .status == \"done\")] | length) == 1"

"$WORKSPACE_SCRIPT" run -- \
  --config "$REMOTE_OFFLINE_CONFIG" \
  --state "$REMOTE_STATE" \
  --data-dir "$REMOTE_DATA" \
  --json \
  init \
  --main-url "http://127.0.0.1:1" \
  --node-key "$NODE_KEY" \
  --display-name "v0.2 Slice 08 Offline Config ${RUN_ID}" \
  --kind workspace \
  --role workspace \
  --runtime-class workspace >/dev/null
OFFLINE_JSON="$("$WORKSPACE_SCRIPT" run -- \
  --config "$REMOTE_OFFLINE_CONFIG" \
  --state "$REMOTE_STATE" \
  --data-dir "$REMOTE_DATA" \
  --json \
  workers run node-agent.node_heartbeat --once)"
check_json_value "offline heartbeat records offline queueing locally" "$OFFLINE_JSON" \
  '.ok == true and .data.run.status == "failed" and .data.health.status == "offline_queueing"'

RECOVERY_HEARTBEAT_JSON="$(agent_json workers run node-agent.node_heartbeat --once)"
check_json_value "normal heartbeat recovers after offline simulation" "$RECOVERY_HEARTBEAT_JSON" \
  '.ok == true and .data.run.status == "succeeded" and .data.health.status == "healthy"'

HEALTH_JSON="$(main_ssh "loom node health '$NODE_ID' --json")"
check_json_value "main node health includes runtime heartbeat summary" "$HEALTH_JSON" \
  '.ok == true and .data.pending_messages == 0 and .data.last_heartbeat.storage_status_json.runtime.queues.outbox_done >= 1'

FINAL_WORKERS_JSON="$(agent_json workers status)"
check_json_value "worker status includes runtime run health" "$FINAL_WORKERS_JSON" \
  '.ok == true and .data.summary.queues.outbox_done >= 1 and ([.data.workers[] | select(.instance.worker_key == "node-agent.node_outbox_flusher")] | length) == 1'

log "completed $pass_count checks"
