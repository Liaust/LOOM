#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." && pwd)"
WORKSPACE_SCRIPT="$REPO_ROOT/scripts/loom-workspace"
PRE_SLICE_SMOKE="$REPO_ROOT/tests/smoke/pre_slice_10_part_2_wireguard.sh"

MAIN_HOST="${LOOM_DEV_HOST:-loom-dev}"
MAIN_URL="${LOOM_MAIN_URL:-http://10.44.0.2:8080}"
RUN_ID="$(date +%Y%m%d%H%M%S)-$RANDOM"
NODE_KEY="slice-11-node-agent-${RUN_ID}"
REMOTE_TMP="/tmp/loom-node-agent-slice-11-${RUN_ID}"
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

run_waited_remote_call() {
  local target="$1"
  local input_json="$2"
  local label="$3"
  local output_file
  output_file="$(mktemp)"

  main_ssh "loom capability call '$target' --input '$input_json' --wait --timeout-seconds 15 --json" >"$output_file" &
  local call_pid=$!
  local processed=0
  local poll_json

  for _ in $(seq 1 15); do
    sleep 1
    poll_json="$("$WORKSPACE_SCRIPT" run -- \
      --config "$REMOTE_CONFIG" \
      --state "$REMOTE_STATE" \
      --data-dir "$REMOTE_DATA" \
      --json \
      poll \
      --once \
      --max-messages 10)"
    if printf '%s' "$poll_json" | jq -e '[.data.processed[]? | select(.kind == "capability.dispatch")] | length > 0' >/dev/null; then
      processed=1
    fi
    if ! kill -0 "$call_pid" >/dev/null 2>&1; then
      break
    fi
  done

  if ! wait "$call_pid"; then
    cat "$output_file" >&2 || true
    fail "$label remote capability call did not complete"
  fi
  if [[ "$processed" != "1" ]]; then
    cat "$output_file" >&2 || true
    fail "$label remote capability dispatch was not processed by node-agent"
  fi
  cat "$output_file"
  rm -f "$output_file"
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

main_ssh 'loom health --json | jq -e ".ok == true and .data.status == \"ok\" and .data.checks.migrations.current_version >= 10" >/dev/null'
pass "main health ok at migration 10 or newer"

"$WORKSPACE_SCRIPT" ssh "rm -rf '$REMOTE_TMP' && mkdir -p '$REMOTE_TMP'"
"$WORKSPACE_SCRIPT" run -- \
  --config "$REMOTE_CONFIG" \
  --state "$REMOTE_STATE" \
  --data-dir "$REMOTE_DATA" \
  --json \
  init \
  --main-url "$MAIN_URL" \
  --node-key "$NODE_KEY" \
  --display-name "Slice 11 Node Agent ${RUN_ID}" \
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

LOCAL_PROVIDER_JSON="$("$WORKSPACE_SCRIPT" run -- \
  --config "$REMOTE_CONFIG" \
  --state "$REMOTE_STATE" \
  --data-dir "$REMOTE_DATA" \
  --json \
  providers local)"
PROVIDER_ADDRESS="$(printf '%s' "$LOCAL_PROVIDER_JSON" | jq -r '.data.provider.compact_address')"
check_json_value "workspace local provider payload is remote-call enabled and node-specific" "$LOCAL_PROVIDER_JSON" \
  '.ok == true and (.data.provider.compact_address | startswith("workspace/slice-11-node-agent-")) and .data.provider.runtime_profile_json.remote_execution_enabled == true and (.data.provider.capabilities | length) == 2 and (.data | has("credential_token") | not)'

ADVERTISE_JSON="$("$WORKSPACE_SCRIPT" run -- \
  --config "$REMOTE_CONFIG" \
  --state "$REMOTE_STATE" \
  --data-dir "$REMOTE_DATA" \
  --json \
  advertise \
  --once)"
ADVERTISEMENT_ID="$(printf '%s' "$ADVERTISE_JSON" | jq -r '.data.advertisement.provider_advertisement_id')"
PROVIDER_ID="$(printf '%s' "$ADVERTISE_JSON" | jq -r '.data.provider.provider_id')"
ECHO_ADDRESS="$(printf '%s' "$ADVERTISE_JSON" | jq -r '.data.endpoints[] | select(.endpoint_name == "echo") | .compact_address')"
STATUS_ADDRESS="$(printf '%s' "$ADVERTISE_JSON" | jq -r '.data.endpoints[] | select(.endpoint_name == "status.read") | .compact_address')"
MESSAGE_ID="$(printf '%s' "$ADVERTISE_JSON" | jq -r '.data.advertisement.communication_message_id')"
[[ "$ADVERTISEMENT_ID" == provider_advertisement_* ]] || fail "expected provider advertisement id"
[[ "$PROVIDER_ID" == prov_* ]] || fail "expected provider id"
[[ "$MESSAGE_ID" == communication_message_* ]] || fail "expected communication message id"
[[ "$ECHO_ADDRESS" == "$PROVIDER_ADDRESS.echo" ]] || fail "unexpected echo address: $ECHO_ADDRESS"
[[ "$STATUS_ADDRESS" == "$PROVIDER_ADDRESS.status.read" ]] || fail "unexpected status address: $STATUS_ADDRESS"
check_json_value "workspace advertised provider pending owner review" "$ADVERTISE_JSON" \
  '.ok == true and .data.advertisement.status == "pending_review" and .data.provider.status == "registered" and ([.data.endpoints[].status] | all(. == "registered")) and (.data.usage_documents | length) >= 3'

PENDING_LIST_JSON="$(main_ssh "loom provider-advertisements list --status pending_review --json")"
check_json_value "main lists pending provider advertisement" "$PENDING_LIST_JSON" \
  ".ok == true and ([.data[] | select(.provider_advertisement_id == \"$ADVERTISEMENT_ID\")] | length) == 1"

PENDING_CAPABILITY_JSON="$(main_ssh "loom capability inspect '$ECHO_ADDRESS' --json")"
check_json_value "advertised capability exists but is not active before approval" "$PENDING_CAPABILITY_JSON" \
  '.ok == true and .data.endpoint.status == "registered" and .data.active_version == null'

PRE_APPROVAL_SEARCH_JSON="$(main_ssh "loom capabilities search 'node echo' --limit 20 --json")"
check_json_value "pending advertised capability is not returned by active search" "$PRE_APPROVAL_SEARCH_JSON" \
  ".ok == true and ([(.data // [])[] | select(.compact_address == \"$ECHO_ADDRESS\")] | length) == 0"

if main_ssh "loom capability call '$ECHO_ADDRESS' --input '{\"text\":\"blocked\"}' --json >/tmp/loom-slice-11-unapproved-call.json 2>/tmp/loom-slice-11-unapproved-call.err"; then
  fail "unapproved advertised endpoint should not be callable"
fi
pass "unapproved advertised endpoint is not callable"

MESSAGE_JSON="$(main_ssh "loom message inspect '$MESSAGE_ID' --json")"
check_json_value "advertisement created an acked node-to-main communication message" "$MESSAGE_JSON" \
  '.ok == true and .data.direction == "node_to_main" and .data.kind == "provider.advertisement" and .data.status == "acked"'

APPROVE_JSON="$(main_ssh "loom provider-advertisement approve '$ADVERTISEMENT_ID' --reason 'slice 11 smoke approval' --json")"
check_json_value "owner approval activates provider advertisement package" "$APPROVE_JSON" \
  '.ok == true and .data.advertisement.status == "approved" and .data.provider.status == "active" and ([.data.endpoints[].status] | all(. == "active")) and ([.data.usage_documents[].review_status] | all(. == "approved"))'

ACTIVE_CAPABILITY_JSON="$(main_ssh "loom capability inspect '$ECHO_ADDRESS' --json")"
check_json_value "approved remote capability has active endpoint version" "$ACTIVE_CAPABILITY_JSON" \
  '.ok == true and .data.endpoint.status == "active" and .data.provider.status == "active" and .data.active_version.status == "active"'

PROVIDER_INSPECT_JSON="$(main_ssh "loom provider inspect '$PROVIDER_ADDRESS' --json")"
check_json_value "approved remote provider is inspectable and healthy" "$PROVIDER_INSPECT_JSON" \
  '.ok == true and .data.provider.status == "active" and .data.health.health_status == "ok" and .data.health.availability_status == "available"'

USAGE_DOCS_JSON="$(main_ssh "loom capability usage-docs '$ECHO_ADDRESS' --json")"
check_json_value "approved remote usage document is inspectable" "$USAGE_DOCS_JSON" \
  '.ok == true and (.data | length) >= 1 and ([.data[].review_status] | all(. == "approved"))'

SEARCH_JSON="$(main_ssh "loom capabilities search '$ECHO_ADDRESS' --limit 10 --json")"
check_json_value "approved remote capability is discoverable through capability search" "$SEARCH_JSON" \
  ".ok == true and ([(.data // [])[] | select(.compact_address == \"$ECHO_ADDRESS\")] | length) == 1 and ([.data[] | has(\"input_schema_json\") or has(\"output_schema_json\")] | any) == false"

CALL_JSON="$(run_waited_remote_call "$ECHO_ADDRESS" '{"text":"hello remote from slice 11"}' "echo")"
ROUTE_ID="$(printf '%s' "$CALL_JSON" | jq -r '.data.route.route_id')"
CALL_ID="$(printf '%s' "$CALL_JSON" | jq -r '.data.capability_call.capability_call_id')"
DISPATCH_MESSAGE_ID="$(printf '%s' "$CALL_JSON" | jq -r '.data.dispatch_message_id')"
RESULT_MESSAGE_ID="$(printf '%s' "$CALL_JSON" | jq -r '.data.result_refs.result_message_id')"
CALL_CORRELATION_ID="$(printf '%s' "$CALL_JSON" | jq -r '.meta.correlation_id')"
[[ "$ROUTE_ID" == route_* ]] || fail "expected remote route id"
[[ "$CALL_ID" == capability_call_* ]] || fail "expected remote capability call id"
[[ "$DISPATCH_MESSAGE_ID" == communication_message_* ]] || fail "expected dispatch message id"
[[ "$RESULT_MESSAGE_ID" == communication_message_* ]] || fail "expected result message id"
check_json_value "waited remote echo capability call returns final result" "$CALL_JSON" \
  ".ok == true and .data.status == \"completed\" and .data.result.echo == \"hello remote from slice 11\" and .data.route.route_kind == \"remote\" and .data.route.target_node_id == \"$NODE_ID\" and .data.capability_call.target_node_id == \"$NODE_ID\""

DISPATCH_MESSAGE_JSON="$(main_ssh "loom message inspect '$DISPATCH_MESSAGE_ID' --json")"
check_json_value "remote call creates acked capability dispatch message" "$DISPATCH_MESSAGE_JSON" \
  ".ok == true and .data.direction == \"main_to_node\" and .data.kind == \"capability.dispatch\" and .data.status == \"acked\" and .data.route_id == \"$ROUTE_ID\" and .data.capability_call_id == \"$CALL_ID\""

RESULT_MESSAGE_JSON="$(main_ssh "loom message inspect '$RESULT_MESSAGE_ID' --json")"
check_json_value "remote result message is inspectable" "$RESULT_MESSAGE_JSON" \
  ".ok == true and .data.direction == \"node_to_main\" and .data.kind == \"capability.result\" and .data.status == \"acked\" and .data.route_id == \"$ROUTE_ID\" and .data.capability_call_id == \"$CALL_ID\" and .data.result_json.echo == \"hello remote from slice 11\""

COMPLETED_CALL_JSON="$(main_ssh "loom capability-call inspect '$CALL_ID' --json")"
check_json_value "main marks remote capability call completed with result" "$COMPLETED_CALL_JSON" \
  '.ok == true and .data.status == "completed" and .data.result_json.echo == "hello remote from slice 11" and (.data.result_refs_json.result_message_id | startswith("communication_message_"))'

COMPLETED_ROUTE_JSON="$(main_ssh "loom route inspect '$ROUTE_ID' --json")"
check_json_value "main marks remote route completed" "$COMPLETED_ROUTE_JSON" \
  '.ok == true and .data.status == "completed" and .data.route_kind == "remote"'

CALL_EVENTS_JSON="$(main_ssh "loom events list --correlation '$CALL_CORRELATION_ID' --json")"
check_json_value "remote call correlation shows route call and message events" "$CALL_EVENTS_JSON" \
  '.ok == true and ([.data[].event_type] | index("route.dispatched") != null) and ([.data[].event_type] | index("capability_call.completed") != null) and ([.data[].event_type] | index("communication.message.created") != null)'

STATUS_CALL_JSON="$(run_waited_remote_call "$STATUS_ADDRESS" '{}' "status")"
check_json_value "waited remote status capability returns safe node-agent status" "$STATUS_CALL_JSON" \
  ".ok == true and .data.status == \"completed\" and .data.result.node_id == \"$NODE_ID\" and .data.result.node_key == \"$NODE_KEY\" and .data.result.version == \"0.0.0-dev\" and .data.result.queue_counts.available == true and (.data.result.current_time | type) == \"string\" and .data.result.last_heartbeat.available == true"

if main_ssh "loom capability call '$PROVIDER_ADDRESS.unsupported' --json >/tmp/loom-slice-11-unsupported-call.json 2>/tmp/loom-slice-11-unsupported-call.err"; then
  fail "unsupported remote capability should fail"
fi
pass "unsupported remote capability fails without dispatch"

RESULT_IDEMPOTENCY_KEY="$(printf '%s' "$RESULT_MESSAGE_JSON" | jq -r '.data.idempotency_key')"
DUPLICATE_RESULT_BODY="$(jq -n \
  --arg node "$NODE_ID" \
  --arg token "$CREDENTIAL_TOKEN" \
  --arg idem "$RESULT_IDEMPOTENCY_KEY" \
  --argjson payload "$(printf '%s' "$RESULT_MESSAGE_JSON" | jq -c '.data.payload_json')" \
  '{node_ref:$node, credential_token:$token, idempotency_key:$idem, payload:$payload, metadata:{source:"slice_11_smoke_duplicate"}}')"
DUPLICATE_RESULT_BODY_B64="$(printf '%s' "$DUPLICATE_RESULT_BODY" | base64 | tr -d '\n')"
DUPLICATE_RESULT_JSON="$(main_ssh "printf '%s' '$DUPLICATE_RESULT_BODY_B64' | base64 -d | curl -fsS -H 'Content-Type: application/json' -H 'X-LOOM-Correlation-ID: slice-11-result-duplicate-$RUN_ID' --data-binary @- '$MAIN_URL/v1/node-agent/capability-result'")"
check_json_value "duplicate remote capability result is idempotent" "$DUPLICATE_RESULT_JSON" \
  ".ok == true and .data.idempotent == true and .data.message_id == \"$RESULT_MESSAGE_ID\" and .data.capability_call.capability_call_id == \"$CALL_ID\""

EVENTS_JSON="$(main_ssh "loom events list --target-kind provider_advertisement --target-id '$ADVERTISEMENT_ID' --json")"
check_json_value "provider advertisement emits audit events" "$EVENTS_JSON" \
  '.ok == true and ([.data[].event_type] | index("provider.advertisement.received") != null) and ([.data[].event_type] | index("provider.advertisement.approved") != null)'

if rg -n "database/sql|pgx|postgres" "$REPO_ROOT/cmd/loom-node-agent" "$REPO_ROOT/internal/nodeagent" >/tmp/loom-node-agent-db-rg.txt; then
  cat /tmp/loom-node-agent-db-rg.txt >&2
  fail "node-agent package should not import database clients"
fi
pass "node-agent code path has no direct database dependency"

log "completed $pass_count checks"
