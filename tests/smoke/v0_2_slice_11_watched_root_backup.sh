#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." && pwd)"
WORKSPACE_SCRIPT="$REPO_ROOT/scripts/loom-workspace"
PRE_SLICE_SMOKE="$REPO_ROOT/tests/smoke/pre_slice_10_part_2_wireguard.sh"

MAIN_HOST="${LOOM_DEV_HOST:-loom-dev}"
MAIN_URL="${LOOM_MAIN_URL:-http://10.44.0.2:8080}"
RUN_ID="$(date +%Y%m%d%H%M%S)-$RANDOM"
TOKEN_ID="${RUN_ID//-/}"
NODE_KEY="v0-2-slice-11-watched-root-${RUN_ID}"
REMOTE_TMP="/tmp/loom-node-agent-v0-2-slice-11-${RUN_ID}"
REMOTE_CONFIG="${REMOTE_TMP}/config.json"
REMOTE_STATE="${REMOTE_TMP}/state.json"
REMOTE_DATA="${REMOTE_TMP}/data"
REMOTE_VAULT="${REMOTE_TMP}/vault"
INITIAL_TOKEN="v02slice11initial${TOKEN_ID}"
UPDATED_TOKEN="v02slice11updated${TOKEN_ID}"

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

check_main_json() {
  local label="$1"
  local command="$2"
  local filter="$3"

  main_ssh "$command | jq -e '$filter' >/dev/null"
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

main_ssh 'loom health --json | jq -e ".ok == true and .data.status == \"ok\" and .data.checks.migrations.current_version >= 25" >/dev/null'
pass "main health ok at watched-root backup migration"

workspace_ssh "rm -rf '$REMOTE_TMP' && mkdir -p '$REMOTE_VAULT/.obsidian' && printf '# Slice 11 Project\n\nInitial token: $INITIAL_TOKEN\n' > '$REMOTE_VAULT/Project.md' && printf '{\"workspace\":\"ignored\"}\n' > '$REMOTE_VAULT/.obsidian/workspace.json'"
pass "created remote watched-root backup fixture"

agent_json \
  init \
  --main-url "$MAIN_URL" \
  --node-key "$NODE_KEY" \
  --display-name "v0.2 Slice 11 Watched Root ${RUN_ID}" \
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

ROOTS_JSON="$(agent_json filesystem roots add --key slice11 --path "$REMOTE_VAULT")"
check_json_value "filesystem safe root configured" "$ROOTS_JSON" \
  '.ok == true and ([.data.safe_roots[] | select(.root_key == "slice11" and .allow_list == true)] | length) == 1'

ADD_JSON="$(agent_json watched-roots add notes --safe-root slice11 --path . --include '**/*.md' --exclude '.obsidian/**' --backup-mode incremental_raw --backup-max-file-bytes 1048576 --sync-mode none --index-mode none --delete-mode local_state_only)"
check_json_value "watched root configured with backup-only policy" "$ADD_JSON" \
  '.ok == true
   and .data.config.root_key == "notes"
   and .data.config.backup_policy.mode == "incremental_raw"
   and .data.config.sync_policy.mode == "none"
   and .data.config.index_policy.mode == "none"'

RUN_INITIAL_JSON="$(agent_json watched-roots run notes --once --mode full --stability-window 0s --flush)"
check_json_value "watched-root run uploads initial backup artifact and report" "$RUN_INITIAL_JSON" \
  '.ok == true
   and .data.status == "healthy"
   and .data.output_plan.counts.backup_files >= 1
   and .data.output_plan.counts.sync_objects == 0
   and .data.backup_output_flush.status == "recorded"
   and .data.backup_output_flush.submitted_items >= 1
   and .data.main_report.status == "recorded"'

LOCAL_BACKUP_JSON="$(agent_json watched-roots backups status notes)"
check_json_value "local backup queue accepted initial artifact" "$LOCAL_BACKUP_JSON" \
  '.ok == true and .data.counts.pending == 0 and .data.counts.accepted >= 1 and .data.counts.artifacts >= 1 and .data.limits.mode == "incremental_raw"'

LOCAL_ARTIFACTS_JSON="$(agent_json watched-roots backups artifacts notes)"
PRIVATE_BACKUP_ID="$(printf '%s' "$LOCAL_ARTIFACTS_JSON" | jq -r '.data[0].private_backup_operation_id')"
[[ "$PRIVATE_BACKUP_ID" == private_backup_* ]] || fail "expected private backup operation id, got $PRIVATE_BACKUP_ID"
check_json_value "local backup artifact links to private backup operation" "$LOCAL_ARTIFACTS_JSON" \
  '.ok == true and (.data | length) >= 1 and .data[0].status == "accepted" and (.data[0].private_backup_operation_id | startswith("private_backup_"))'

EXPLAIN_INITIAL_JSON="$(agent_json watched-roots explain notes --path Project.md)"
HASH_INITIAL="$(printf '%s' "$EXPLAIN_INITIAL_JSON" | jq -r '.data.state.last_backed_up_hash_uri')"
[[ "$HASH_INITIAL" == sha256:* ]] || fail "expected backed-up hash, got $HASH_INITIAL"
check_json_value "local path state records accepted backup refs" "$EXPLAIN_INITIAL_JSON" \
  '.ok == true
   and .data.state.backup_status == "recorded"
   and (.data.state.main_backup_batch_id | startswith("watched_root_backup_batch_"))
   and (.data.state.main_backup_item_id | startswith("watched_root_backup_item_"))
   and (.data.state.private_backup_operation_id | startswith("private_backup_"))'

check_main_json "main private backup operation records watched-root metadata" \
  "loom sync private-backups --node '$NODE_ID' --json --correlation-id corr_smoke_v0_2_slice_11_private_backups" \
  ".ok == true and ([.data[] | select(.private_backup_operation_id == \"$PRIVATE_BACKUP_ID\" and .metadata.backup_kind == \"watched_root_file\" and .metadata.root_key == \"notes\" and .metadata.relative_path == \"Project.md\")] | length) == 1"

check_main_json "main watched-root backup status is healthy" \
  "loom watched-roots backups status --node '$NODE_ID' --root notes --json --correlation-id corr_smoke_v0_2_slice_11_backup_status_initial" \
  '.ok == true and .data.root.root_key == "notes" and .data.status == "healthy" and .data.accepted_count >= 1'

check_main_json "main watched-root backup batches list initial batch" \
  "loom watched-roots backups batches --node '$NODE_ID' --root notes --json --correlation-id corr_smoke_v0_2_slice_11_backup_batches_initial" \
  '.ok == true and ([.data[] | select(.status == "accepted" and .item_count >= 1 and .artifact_count >= 1)] | length) >= 1'

check_main_json "main watched-root backup items list initial file item" \
  "loom watched-roots backups items --node '$NODE_ID' --root notes --path Project.md --json --correlation-id corr_smoke_v0_2_slice_11_backup_items_initial" \
  ".ok == true and ([.data[] | select(.item_kind == \"file\" and .status == \"accepted\" and .private_backup_operation_id == \"$PRIVATE_BACKUP_ID\")] | length) >= 1"

RUN_UNCHANGED_JSON="$(agent_json watched-roots run notes --once --mode full --stability-window 0s --flush)"
check_json_value "unchanged run does not queue duplicate backup work" "$RUN_UNCHANGED_JSON" \
  '.ok == true
   and .data.output_plan.counts.backup_files == 0
   and .data.backup_output_flush.status == "already_current"
   and .data.backup_status.pending == 0'

workspace_ssh "printf '# Slice 11 Project\n\nUpdated token: $UPDATED_TOKEN\n' > '$REMOTE_VAULT/Project.md'"
RUN_UPDATED_JSON="$(agent_json watched-roots run notes --once --mode full --stability-window 0s --flush)"
check_json_value "changed markdown creates a second backup item" "$RUN_UPDATED_JSON" \
  '.ok == true and .data.status == "healthy" and .data.output_plan.counts.backup_files >= 1 and .data.backup_output_flush.status == "recorded"'

check_main_json "main watched-root backup items include update" \
  "loom watched-roots backups items --node '$NODE_ID' --root notes --path Project.md --json --correlation-id corr_smoke_v0_2_slice_11_backup_items_updated" \
  '.ok == true and ([.data[] | select(.item_kind == "file" and .status == "accepted")] | length) >= 2'

workspace_ssh "rm -f '$REMOTE_VAULT/Project.md'"
RUN_DELETED_JSON="$(agent_json watched-roots run notes --once --mode full --stability-window 0s --flush)"
check_json_value "deleted markdown records backup deletion marker" "$RUN_DELETED_JSON" \
  '.ok == true and .data.status == "healthy" and .data.output_plan.counts.backup_deletion_markers >= 1 and .data.backup_output_flush.status == "recorded"'

EXPLAIN_DELETED_JSON="$(agent_json watched-roots explain notes --path Project.md)"
check_json_value "local path state records accepted backup deletion marker" "$EXPLAIN_DELETED_JSON" \
  '.ok == true and .data.state.status == "deleted" and (.data.state.backup_deletion_marker_id | startswith("watched_root_backup_item_"))'

check_main_json "main watched-root backup items include deletion marker" \
  "loom watched-roots backups items --node '$NODE_ID' --root notes --path Project.md --json --correlation-id corr_smoke_v0_2_slice_11_backup_items_deleted" \
  '.ok == true and ([.data[] | select(.item_kind == "deletion_marker" and .status == "accepted")] | length) >= 1'

check_main_json "backup-only root did not create sync batches" \
  "loom sync batches --node '$NODE_ID' --json --correlation-id corr_smoke_v0_2_slice_11_sync_batches" \
  '.ok == true and (.data | length) == 0'

check_main_json "watched-root backup findings command returns JSON array" \
  "loom watched-roots findings --node '$NODE_ID' --root notes --status open --json --correlation-id corr_smoke_v0_2_slice_11_wr_findings" \
  '.ok == true and (.data | type) == "array"'

log "completed $pass_count checks"
