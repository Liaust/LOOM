#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." && pwd)"
WORKSPACE_SCRIPT="$REPO_ROOT/scripts/loom-workspace"
PRE_SLICE_SMOKE="$REPO_ROOT/tests/smoke/pre_slice_10_part_2_wireguard.sh"

MAIN_HOST="${LOOM_DEV_HOST:-loom-dev}"
MAIN_URL="${LOOM_MAIN_URL:-http://10.44.0.2:8080}"
RUN_ID="$(date +%Y%m%d%H%M%S)-$RANDOM"
NODE_KEY="slice-10-node-agent-${RUN_ID}"
REMOTE_TMP="/tmp/loom-node-agent-slice-10-${RUN_ID}"
REMOTE_CONFIG="${REMOTE_TMP}/config.json"
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
"$WORKSPACE_SCRIPT" run -- \
  --config "$REMOTE_CONFIG" \
  --state "$REMOTE_STATE" \
  --data-dir "$REMOTE_DATA" \
  --json \
  init \
  --main-url "$MAIN_URL" \
  --node-key "$NODE_KEY" \
  --display-name "Slice 10 Node Agent ${RUN_ID}" \
  --kind workspace \
  --role workspace \
  --runtime-class workspace >/dev/null
pass "workspace node-agent initialized"

TOKEN_JSON="$(main_ssh "loom node enrollment-token create --ttl-seconds 1800 --json")"
TOKEN_VALUE="$(printf '%s' "$TOKEN_JSON" | jq -r '.data.token_value')"
[[ "$TOKEN_VALUE" == node_enroll_* ]] || fail "expected enrollment token"
pass "owner created enrollment token"

ENROLL_JSON="$("$WORKSPACE_SCRIPT" run -- \
  --config "$REMOTE_CONFIG" \
  --state "$REMOTE_STATE" \
  --data-dir "$REMOTE_DATA" \
  --json \
  enroll \
  --token "$TOKEN_VALUE")"
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

"$WORKSPACE_SCRIPT" run -- \
  --config "$REMOTE_CONFIG" \
  --state "$REMOTE_STATE" \
  --data-dir "$REMOTE_DATA" \
  --json \
  credential import \
  --node-id "$NODE_ID" \
  --credential-id "$CREDENTIAL_ID" \
  --credential-token "$CREDENTIAL_TOKEN" >/dev/null
pass "workspace imported approved credential"

HEARTBEAT_JSON="$("$WORKSPACE_SCRIPT" run -- \
  --config "$REMOTE_CONFIG" \
  --state "$REMOTE_STATE" \
  --data-dir "$REMOTE_DATA" \
  --json \
  heartbeat \
  --once \
  --reported-status ok)"
check_json_value "workspace heartbeat marked node online" "$HEARTBEAT_JSON" \
  '.ok == true and .data.presence_state == "online"'

MESSAGE_IDEMPOTENCY_KEY="slice-10-agent-message-${RUN_ID}"
MESSAGE_JSON="$(main_ssh "loom message enqueue --node '$NODE_ID' --kind main.ping --input '{\"text\":\"hello from slice 10 part 3\"}' --idempotency-key '$MESSAGE_IDEMPOTENCY_KEY' --json")"
MESSAGE_ID="$(printf '%s' "$MESSAGE_JSON" | jq -r '.data.communication_message_id')"
[[ "$MESSAGE_ID" == communication_message_* ]] || fail "expected communication message id"
check_json_value "main enqueued ping for workspace node" "$MESSAGE_JSON" \
  '.ok == true and .data.kind == "main.ping" and .data.status == "available" and .data.payload_json.text == "hello from slice 10 part 3"'

POLL_JSON="$("$WORKSPACE_SCRIPT" run -- \
  --config "$REMOTE_CONFIG" \
  --state "$REMOTE_STATE" \
  --data-dir "$REMOTE_DATA" \
  --json \
  poll \
  --once \
  --max-messages 10)"
check_json_value "workspace poll claimed and acked message" "$POLL_JSON" \
  ".ok == true and (.data.poll.messages | length) == 1 and .data.poll.messages[0].communication_message_id == \"$MESSAGE_ID\" and (.data.processed | length) == 1 and .data.processed[0].ack.message.status == \"acked\" and .data.processed[0].ack.ack.ack_status == \"completed\""

"$WORKSPACE_SCRIPT" ssh "test -f '$REMOTE_DATA/inbox/$MESSAGE_ID.json'"
pass "workspace persisted inbox message"

ACK_ID="$(printf '%s' "$POLL_JSON" | jq -r '.data.processed[0].ack.ack.communication_ack_id')"
"$WORKSPACE_SCRIPT" ssh "test -f '$REMOTE_DATA/outbox/$ACK_ID.json'"
pass "workspace persisted outbox ack"

INSPECT_JSON="$(main_ssh "loom message inspect '$MESSAGE_ID' --json")"
check_json_value "main message inspect shows ack result" "$INSPECT_JSON" \
  '.ok == true and .data.status == "acked" and .data.result_json.pong == true and .data.result_json.received_text == "hello from slice 10 part 3"'

HEALTH_JSON="$(main_ssh "loom node health '$NODE_ID' --json")"
check_json_value "node health has no pending messages after ack" "$HEALTH_JSON" \
  '.ok == true and .data.pending_messages == 0 and .data.failed_messages == 0 and .data.dead_letter_messages == 0'

DUPLICATE_POLL_JSON="$("$WORKSPACE_SCRIPT" run -- \
  --config "$REMOTE_CONFIG" \
  --state "$REMOTE_STATE" \
  --data-dir "$REMOTE_DATA" \
  --json \
  poll \
  --once \
  --max-messages 10)"
check_json_value "duplicate poll does not redeliver acked message" "$DUPLICATE_POLL_JSON" \
  '.ok == true and (.data.poll.messages | length) == 0 and (.data.processed | length) == 0'

COMM_HEALTH_JSON="$(main_ssh "loom communication health --json")"
check_json_value "communication health remains inspectable" "$COMM_HEALTH_JSON" \
  '.ok == true and .data.total_nodes >= 1 and .data.pending_messages >= 0 and .data.failed_messages >= 0 and .data.dead_letter_messages >= 0'

if rg -n "database/sql|pgx|postgres" "$REPO_ROOT/cmd/loom-node-agent" "$REPO_ROOT/internal/nodeagent" >/tmp/loom-node-agent-db-rg.txt; then
  cat /tmp/loom-node-agent-db-rg.txt >&2
  fail "node-agent package should not import database clients"
fi
pass "node-agent code path has no direct database dependency"

log "completed $pass_count checks"
