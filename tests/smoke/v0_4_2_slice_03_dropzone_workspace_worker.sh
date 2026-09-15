#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." && pwd)"
WORKSPACE_SCRIPT="$REPO_ROOT/scripts/loom-workspace"
PRE_SLICE_SMOKE="$REPO_ROOT/tests/smoke/pre_slice_10_part_2_wireguard.sh"

MAIN_HOST="${LOOM_DEV_HOST:-loom-dev}"
MAIN_URL="${LOOM_MAIN_URL:-http://10.44.0.2:8080}"
WORKSPACE_REMOTE_DIR="${LOOM_WORKSPACE_REMOTE_DIR:-/home/loomworkspace/loom/current}"
RUN_ID="v0-4-2-slice-03-$(date +%s)-$RANDOM"
NODE_KEY="dropzone-${RUN_ID//[^A-Za-z0-9]/-}"
REMOTE_TMP="/tmp/loom-dropzone-slice-03-${RUN_ID}"
REMOTE_CONFIG="${REMOTE_TMP}/config.json"
REMOTE_STATE="${REMOTE_TMP}/state.json"
REMOTE_DATA="${REMOTE_TMP}/data"
REMOTE_BOX="${REMOTE_TMP}/LOOM Box"
MAIN_DROP_PREFIX="/var/lib/loom/LOOM Box/.loom/storage/dropzone/$NODE_KEY"
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
log "workspace node key: $NODE_KEY"

"$PRE_SLICE_SMOKE"
pass "remote WireGuard prerequisite passed"

"$WORKSPACE_SCRIPT" sync
"$WORKSPACE_SCRIPT" run -- version >/dev/null
pass "loom-node-agent builds on workspace VM"

main_ssh 'loom health --json | jq -e ".ok == true and .data.status == \"ok\"" >/dev/null'
pass "main health ok"

main_ssh "sudo -u loom env LOOM_CONFIG_FILE=/etc/loom/loom.env loom --json box init --path '/var/lib/loom/LOOM Box' --profile main | jq -e '.status_after.state == \"ok\"' >/dev/null"
pass "main Box initialized for Dropzone custody"

workspace_ssh "rm -rf '$REMOTE_TMP' && mkdir -p '$REMOTE_TMP'"
main_ssh "sudo rm -rf '$MAIN_DROP_PREFIX'" >/dev/null 2>&1 || true
cleanup() {
  workspace_ssh "rm -rf '$REMOTE_TMP'" >/dev/null 2>&1 || true
  main_ssh "sudo rm -rf '$MAIN_DROP_PREFIX'" >/dev/null 2>&1 || true
}
trap cleanup EXIT

agent_json \
  init \
  --main-url "$MAIN_URL" \
  --node-key "$NODE_KEY" \
  --display-name "v0.4.2 Slice 03 Dropzone ${RUN_ID}" \
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
  '.ok == true and .data.request.status == "approved" and .data.node.status == "active"'

agent_json \
  credential import \
  --node-id "$NODE_ID" \
  --credential-id "$CREDENTIAL_ID" \
  --credential-token "$CREDENTIAL_TOKEN" >/dev/null
pass "workspace imported approved credential"

workspace_ssh "
  cd '$WORKSPACE_REMOTE_DIR'
  nix --extra-experimental-features 'nix-command flakes' run .#loom -- --json box init --path '$REMOTE_BOX' --profile workspace > '$REMOTE_TMP/box-init.json'
"
BOX_INIT_JSON="$(workspace_ssh "cat '$REMOTE_TMP/box-init.json'")"
check_json_value "workspace Box initialized with active Dropzone policy" "$BOX_INIT_JSON" \
  '.status_after.state == "ok" and .status_after.dropzone_transfers.runtime_state == "active"'
BOX_ID="$(printf '%s' "$BOX_INIT_JSON" | jq -r '.status_after.contract.box_id')"
[[ "$BOX_ID" == box_* ]] || fail "expected workspace Box id"

workspace_ssh "
  policy='$REMOTE_BOX/.loom/policies/dropzone.transfer.yaml'
  set_policy_field() {
    key=\"\$1\"
    value=\"\$2\"
    if grep -q \"^\${key}:\" \"\$policy\"; then
      sed -i \"s|^\${key}:.*|\${key}: \${value}|\" \"\$policy\"
    else
      printf '%s: %s\n' \"\$key\" \"\$value\" >> \"\$policy\"
    fi
  }
  set_policy_field settle_duration 0s
  set_policy_field retry_backoff 0s
  set_policy_field retry_count 0
  set_policy_field chunk_size_bytes 7
"
pass "workspace Dropzone policy tuned for deterministic chunked smoke"

PAYLOAD_TEXT="Dropzone Slice 03 payload ${RUN_ID} crossing chunk boundaries"
workspace_ssh "
  printf '%s\n' '$PAYLOAD_TEXT' > '$REMOTE_BOX/Dropzone/payload.txt'
  touch -d '2 minutes ago' '$REMOTE_BOX/Dropzone/payload.txt'
"
WORKSPACE_CHECKSUM="$(workspace_ssh "sha256sum '$REMOTE_BOX/Dropzone/payload.txt' | awk '{print \$1}'")"
pass "workspace wrote stable Dropzone payload"

CONFIGURE_JSON="$(agent_json dropzone configure --box-path "$REMOTE_BOX" --profile workspace --box-id "$BOX_ID" --settle-duration 0s --interval-seconds 60)"
check_json_value "workspace configured Dropzone worker" "$CONFIGURE_JSON" \
  '.ok == true and .data.worker.worker_key == "node-agent.dropzone_transfer" and .data.worker.kind == "dropzone_transfer"'

STATUS_BEFORE_JSON="$(agent_json dropzone status)"
check_json_value "Dropzone status previews ready transfer before upload" "$STATUS_BEFORE_JSON" \
  '.ok == true and .data.status.runtime_state == "active" and .data.status.counts.ready == 1'

RUN_JSON="$(agent_json workers run node-agent.dropzone_transfer --once)"
check_json_value "Dropzone worker uploads and accepts custody" "$RUN_JSON" \
  '.ok == true
   and .data.run.status == "succeeded"
   and .data.health.status == "healthy"
   and .data.run.result_json.counts.accepted == 1
   and .data.run.result_json.processed[-1].safe_to_delete == true'

TRANSFER_PATH="$(workspace_ssh "find '$REMOTE_BOX/.loom/state/dropzone/transfers' -name '*.json' -print -quit")"
[[ -n "$TRANSFER_PATH" ]] || fail "expected transfer record"
TRANSFER_JSON="$(workspace_ssh "cat '$TRANSFER_PATH'")"
check_json_value "workspace transfer record is accepted and safe to delete" "$TRANSFER_JSON" \
  '.status == "accepted" and .safe_to_delete == true and (.main_custody_id | startswith("drop_custody_")) and .remote_storage_path != ""'

REMOTE_STORAGE_PATH="$(printf '%s' "$TRANSFER_JSON" | jq -r '.remote_storage_path')"
MAIN_CUSTODY_ID="$(printf '%s' "$TRANSFER_JSON" | jq -r '.main_custody_id')"
[[ "$MAIN_CUSTODY_ID" == drop_custody_* ]] || fail "expected custody id"
main_ssh "sudo -u loom test -f '$REMOTE_STORAGE_PATH'"
MAIN_CHECKSUM="$(main_ssh "sudo -u loom sha256sum '$REMOTE_STORAGE_PATH' | awk '{print \$1}'")"
[[ "$MAIN_CHECKSUM" == "$WORKSPACE_CHECKSUM" ]] || fail "custody checksum mismatch: main=$MAIN_CHECKSUM workspace=$WORKSPACE_CHECKSUM"
pass "main custody file exists with matching checksum"

STATUS_AFTER_JSON="$(agent_json dropzone status)"
check_json_value "Dropzone status reports accepted safe-to-delete file" "$STATUS_AFTER_JSON" \
  '.ok == true and .data.status.counts.accepted == 1 and .data.status.accepted[0].safe_to_delete == true'

SECOND_RUN_JSON="$(agent_json dropzone run --once)"
check_json_value "Dropzone worker second run is idempotent" "$SECOND_RUN_JSON" \
  '.ok == true and (.data.run.status == "skipped" or .data.run.status == "succeeded") and .data.health.status == "healthy" and .data.run.result_json.counts.accepted == 1'

log "completed $pass_count checks"
