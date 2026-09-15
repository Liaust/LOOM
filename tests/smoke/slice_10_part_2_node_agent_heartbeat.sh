#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORKSPACE_SCRIPT="$ROOT_DIR/scripts/loom-workspace"
PRE_SLICE_SMOKE="$ROOT_DIR/tests/smoke/pre_slice_10_part_2_wireguard.sh"
MAIN_HOST="${LOOM_MAIN_HOST:-loom-dev}"
MAIN_URL="${LOOM_MAIN_URL:-http://10.44.0.2:8080}"

require_command() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing required command: $1" >&2
    exit 2
  fi
}

check_json_field() {
  local json="$1"
  local jq_filter="$2"
  local label="$3"
  if ! printf '%s' "$json" | jq -e "$jq_filter" >/dev/null; then
    echo "check failed: $label" >&2
    printf '%s\n' "$json" >&2
    exit 1
  fi
  echo "ok: $label"
}

require_command jq
require_command ssh
require_command rsync

if [[ -x "$PRE_SLICE_SMOKE" ]]; then
  "$PRE_SLICE_SMOKE"
else
  echo "warning: pre-slice WireGuard smoke not found at $PRE_SLICE_SMOKE" >&2
fi

"$WORKSPACE_SCRIPT" sync
"$WORKSPACE_SCRIPT" run -- version >/dev/null
echo "ok: loom-node-agent builds on workspace VM"

RUN_ID="$(date +%Y%m%d%H%M%S)"
NODE_KEY="slice-10-workspace-${RUN_ID}"
REMOTE_TMP="/tmp/loom-node-agent-${NODE_KEY}"
REMOTE_CONFIG="${REMOTE_TMP}/config.json"
REMOTE_STATE="${REMOTE_TMP}/state.json"
REMOTE_DATA="${REMOTE_TMP}/data"

"$WORKSPACE_SCRIPT" ssh "rm -rf '$REMOTE_TMP' && mkdir -p '$REMOTE_TMP'"
"$WORKSPACE_SCRIPT" run -- \
  --config "$REMOTE_CONFIG" \
  --state "$REMOTE_STATE" \
  --data-dir "$REMOTE_DATA" \
  --json \
  init \
  --main-url "$MAIN_URL" \
  --node-key "$NODE_KEY" \
  --display-name "Slice 10 Workspace ${RUN_ID}" \
  --kind workspace \
  --role workspace \
  --runtime-class workspace >/dev/null
echo "ok: node-agent initialized local workspace state"

TOKEN_JSON="$(ssh "$MAIN_HOST" "loom node enrollment-token create --ttl-seconds 1800 --json")"
TOKEN_VALUE="$(printf '%s' "$TOKEN_JSON" | jq -r '.data.token_value')"
if [[ -z "$TOKEN_VALUE" || "$TOKEN_VALUE" == "null" ]]; then
  echo "failed to create enrollment token" >&2
  printf '%s\n' "$TOKEN_JSON" >&2
  exit 1
fi
echo "ok: enrollment token created on main"

ENROLL_JSON="$("$WORKSPACE_SCRIPT" run -- \
  --config "$REMOTE_CONFIG" \
  --state "$REMOTE_STATE" \
  --data-dir "$REMOTE_DATA" \
  --json \
  enroll \
  --token "$TOKEN_VALUE")"
REQUEST_ID="$(printf '%s' "$ENROLL_JSON" | jq -r '.data.node_enrollment_request_id')"
check_json_field "$ENROLL_JSON" '.ok == true and .data.status == "pending"' "workspace created pending enrollment request"

APPROVAL_JSON="$(ssh "$MAIN_HOST" "loom node enrollment-request approve '$REQUEST_ID' --json")"
NODE_ID="$(printf '%s' "$APPROVAL_JSON" | jq -r '.data.node.node_id')"
CREDENTIAL_ID="$(printf '%s' "$APPROVAL_JSON" | jq -r '.data.credential.node_credential_id')"
CREDENTIAL_TOKEN="$(printf '%s' "$APPROVAL_JSON" | jq -r '.data.credential_token')"
if [[ -z "$NODE_ID" || "$NODE_ID" == "null" || -z "$CREDENTIAL_ID" || "$CREDENTIAL_ID" == "null" || -z "$CREDENTIAL_TOKEN" || "$CREDENTIAL_TOKEN" == "null" ]]; then
  echo "failed to approve enrollment request" >&2
  printf '%s\n' "$APPROVAL_JSON" >&2
  exit 1
fi
echo "ok: enrollment approved on main"

"$WORKSPACE_SCRIPT" run -- \
  --config "$REMOTE_CONFIG" \
  --state "$REMOTE_STATE" \
  --data-dir "$REMOTE_DATA" \
  --json \
  credential import \
  --node-id "$NODE_ID" \
  --credential-id "$CREDENTIAL_ID" \
  --credential-token "$CREDENTIAL_TOKEN" >/dev/null
echo "ok: workspace imported approved node credential"

HEARTBEAT_JSON="$("$WORKSPACE_SCRIPT" run -- \
  --config "$REMOTE_CONFIG" \
  --state "$REMOTE_STATE" \
  --data-dir "$REMOTE_DATA" \
  --json \
  heartbeat \
  --once \
  --reported-status ok)"
check_json_field "$HEARTBEAT_JSON" '.ok == true and .data.presence_state == "online"' "workspace heartbeat marked node online"

HEALTH_JSON="$(ssh "$MAIN_HOST" "loom node health '$NODE_ID' --json")"
check_json_field "$HEALTH_JSON" '.ok == true and .data.node.presence_state == "online"' "main health sees workspace online"

DEGRADED_JSON="$("$WORKSPACE_SCRIPT" run -- \
  --config "$REMOTE_CONFIG" \
  --state "$REMOTE_STATE" \
  --data-dir "$REMOTE_DATA" \
  --json \
  heartbeat \
  --once \
  --reported-status degraded)"
check_json_field "$DEGRADED_JSON" '.ok == true and .data.presence_state == "degraded"' "workspace heartbeat can report degraded"

DEGRADED_HEALTH_JSON="$(ssh "$MAIN_HOST" "loom node health '$NODE_ID' --json")"
check_json_field "$DEGRADED_HEALTH_JSON" '.ok == true and .data.node.presence_state == "degraded"' "main health sees workspace degraded"

STATUS_JSON="$("$WORKSPACE_SCRIPT" run -- \
  --config "$REMOTE_CONFIG" \
  --state "$REMOTE_STATE" \
  --data-dir "$REMOTE_DATA" \
  --json \
  status)"
check_json_field "$STATUS_JSON" '.ok == true and .data.credential_configured == true and (.data | has("credential_token") | not)' "status redacts credential token"

"$WORKSPACE_SCRIPT" run -- \
  --config "$REMOTE_CONFIG" \
  --state "$REMOTE_STATE" \
  --data-dir "$REMOTE_DATA" \
  --json \
  credential import \
  --node-id "$NODE_ID" \
  --credential-id "$CREDENTIAL_ID" \
  --credential-token "node_cred_invalid_for_smoke" >/dev/null
set +e
BAD_HEARTBEAT_OUTPUT="$("$WORKSPACE_SCRIPT" run -- \
  --config "$REMOTE_CONFIG" \
  --state "$REMOTE_STATE" \
  --data-dir "$REMOTE_DATA" \
  --json \
  heartbeat \
  --once \
  --reported-status ok 2>&1)"
BAD_HEARTBEAT_STATUS=$?
set -e
if [[ "$BAD_HEARTBEAT_STATUS" -eq 0 ]]; then
  echo "heartbeat unexpectedly succeeded with invalid credential" >&2
  printf '%s\n' "$BAD_HEARTBEAT_OUTPUT" >&2
  exit 1
fi
echo "ok: invalid node credential cannot heartbeat"

if rg -n "database/sql|pgx|postgres" "$ROOT_DIR/cmd/loom-node-agent" "$ROOT_DIR/internal/nodeagent" >/tmp/loom-node-agent-db-rg.txt; then
  echo "node-agent package should not import database clients" >&2
  cat /tmp/loom-node-agent-db-rg.txt >&2
  exit 1
fi
echo "ok: node-agent code path has no direct database dependency"

echo "Slice 10 Part 2 node-agent heartbeat smoke passed."
