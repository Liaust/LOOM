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
NODE_KEY="v0-2-slice-10-watched-root-${RUN_ID}"
SMOKE_SLUG="v0-2-slice-10-$RUN_ID"
SMOKE_NAME="v0.2 Slice 10 Smoke $RUN_ID"
REMOTE_TMP="/tmp/loom-node-agent-v0-2-slice-10-${RUN_ID}"
REMOTE_CONFIG="${REMOTE_TMP}/config.json"
REMOTE_STATE="${REMOTE_TMP}/state.json"
REMOTE_DATA="${REMOTE_TMP}/data"
REMOTE_VAULT="${REMOTE_TMP}/vault"
INITIAL_TOKEN="v02slice10initial${TOKEN_ID}"
UPDATED_TOKEN="v02slice10updated${TOKEN_ID}"
EXCLUDED_TOKEN="v02slice10excluded${TOKEN_ID}"

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

main_json_value() {
  local command="$1"
  local filter="$2"

  main_ssh "$command | jq -r '$filter'"
}

require_command jq
require_command ssh
require_command rsync

log "main: $MAIN_HOST"
log "main URL: $MAIN_URL"
log "node key: $NODE_KEY"
log "project: $SMOKE_SLUG"

"$PRE_SLICE_SMOKE"
pass "remote WireGuard prerequisite passed"

"$WORKSPACE_SCRIPT" sync
"$WORKSPACE_SCRIPT" run -- version >/dev/null
pass "loom-node-agent builds on workspace VM"

main_ssh 'loom health --json | jq -e ".ok == true and .data.status == \"ok\" and .data.checks.migrations.current_version >= 24" >/dev/null'
pass "main health ok at watched-root report migration"

workspace_ssh "rm -rf '$REMOTE_TMP' && mkdir -p '$REMOTE_VAULT/.obsidian' && printf '# Slice 10 Project\n\nInitial token: $INITIAL_TOKEN\n' > '$REMOTE_VAULT/Project.md' && printf 'excluded token: $EXCLUDED_TOKEN\n' > '$REMOTE_VAULT/Project.tmp' && printf '{\"workspace\":\"ignored\"}\n' > '$REMOTE_VAULT/.obsidian/workspace.json'"
pass "created remote watched-root vault fixture"

agent_json \
  init \
  --main-url "$MAIN_URL" \
  --node-key "$NODE_KEY" \
  --display-name "v0.2 Slice 10 Watched Root ${RUN_ID}" \
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

check_main_json "create smoke project for watched-root sync" \
  "loom project create '$SMOKE_NAME' --slug '$SMOKE_SLUG' --description 'v0.2 Slice 10 watched-root sync/index smoke' --if-not-exists --json --correlation-id corr_smoke_v0_2_slice_10_project_create" \
  ".ok == true and .data.project.project.slug == \"$SMOKE_SLUG\""

ROOTS_JSON="$(agent_json filesystem roots add --key slice10 --path "$REMOTE_VAULT")"
check_json_value "filesystem safe root configured" "$ROOTS_JSON" \
  '.ok == true and ([.data.safe_roots[] | select(.root_key == "slice10" and .allow_list == true)] | length) == 1'

ADD_JSON="$(agent_json watched-roots add notes --safe-root slice10 --path . --include '**/*.md' --exclude '.obsidian/**' --exclude '**/*.tmp' --sync-mode selected_files --index-mode markdown_text --delete-mode tombstone --project "$SMOKE_SLUG")"
check_json_value "watched root configured with sync and index policy" "$ADD_JSON" \
  '.ok == true
   and .data.config.root_key == "notes"
   and .data.config.sync_policy.mode == "selected_files"
   and .data.config.index_policy.mode == "markdown_text"
   and .data.config.delete_policy.mode == "tombstone"'

RUN_INITIAL_JSON="$(agent_json watched-roots run notes --once --mode full --stability-window 0s --flush)"
check_json_value "watched-root run syncs and reports initial markdown" "$RUN_INITIAL_JSON" \
  '.ok == true
   and .data.status == "healthy"
   and .data.output_flush.status == "recorded"
   and .data.output_flush.submitted_items >= 1
   and .data.main_report.status == "recorded"
   and (.data.main_report.watched_root_id | startswith("watched_root_"))'

LOCAL_SYNC_JSON="$(agent_json sync status)"
check_json_value "local sync queue records accepted object upload" "$LOCAL_SYNC_JSON" \
  '.ok == true and .data.counts.pending == 0 and .data.counts.accepted >= 1 and .data.counts.objects >= 1'

EXPLAIN_INITIAL_JSON="$(agent_json watched-roots explain notes --path Project.md)"
OBJECT_ID="$(printf '%s' "$EXPLAIN_INITIAL_JSON" | jq -r '.data.state.main_object_id')"
VERSION_ID="$(printf '%s' "$EXPLAIN_INITIAL_JSON" | jq -r '.data.state.main_version_id')"
[[ "$OBJECT_ID" == object_* ]] || fail "expected synced main object id, got $OBJECT_ID"
[[ "$VERSION_ID" == version_* ]] || fail "expected synced main version id, got $VERSION_ID"
check_json_value "local path state records main sync and queued index status" "$EXPLAIN_INITIAL_JSON" \
  ".ok == true and .data.state.main_object_id == \"$OBJECT_ID\" and .data.state.main_version_id == \"$VERSION_ID\" and .data.state.index_status == \"queued\""

check_main_json "main watched-root status includes latest report" \
  "loom watched-roots status --node '$NODE_ID' --root notes --json --correlation-id corr_smoke_v0_2_slice_10_wr_status" \
  ".ok == true and (.data | length) == 1 and .data[0].root.root_key == \"notes\" and .data[0].root.status == \"healthy\""

check_main_json "main sync replicas include watched-root object" \
  "loom sync replicas --node '$NODE_ID' --json --correlation-id corr_smoke_v0_2_slice_10_replicas_initial" \
  ".ok == true and ([.data[] | select(.replicated_id == \"$VERSION_ID\" and .metadata.object_id == \"$OBJECT_ID\" and .source_node_id == \"$NODE_ID\")] | length) >= 1"

check_main_json "initial markdown object has queued text index work" \
  "loom indexes status --object '$OBJECT_ID' --json --correlation-id corr_smoke_v0_2_slice_10_index_initial" \
  ".ok == true and ([.data[] | select(.index_type == \"full_text\" and .status == \"queued\" and .object_id == \"$OBJECT_ID\" and .object_version_id == \"$VERSION_ID\")] | length) == 1"

main_ssh "loom indexes run text --once --json --correlation-id corr_smoke_v0_2_slice_10_indexer_initial > /tmp/loom-v0-2-slice-10-indexer-initial.json"
check_main_json "text indexer processes watched-root markdown" \
  'cat /tmp/loom-v0-2-slice-10-indexer-initial.json' \
  '.ok == true and .data.run.run_status == "succeeded" and .data.run.result_summary_json.indexed >= 1'

check_main_json "search finds initial watched-root token" \
  "loom search '$INITIAL_TOKEN' --project '$SMOKE_SLUG' --json --correlation-id corr_smoke_v0_2_slice_10_search_initial" \
  ".ok == true and .data.result_count >= 1 and ([.data.results[] | select(.object_id == \"$OBJECT_ID\" and .object_version_id == \"$VERSION_ID\")] | length) >= 1"

workspace_ssh "printf '# Slice 10 Project\n\nUpdated token: $UPDATED_TOKEN\n' > '$REMOTE_VAULT/Project.md'"
RUN_UPDATED_JSON="$(agent_json watched-roots run notes --once --mode full --stability-window 0s --flush)"
check_json_value "changed markdown syncs as a new version" "$RUN_UPDATED_JSON" \
  '.ok == true and .data.status == "healthy" and .data.output_flush.status == "recorded" and .data.main_report.status == "recorded"'

EXPLAIN_UPDATED_JSON="$(agent_json watched-roots explain notes --path Project.md)"
UPDATED_OBJECT_ID="$(printf '%s' "$EXPLAIN_UPDATED_JSON" | jq -r '.data.state.main_object_id')"
UPDATED_VERSION_ID="$(printf '%s' "$EXPLAIN_UPDATED_JSON" | jq -r '.data.state.main_version_id')"
[[ "$UPDATED_OBJECT_ID" == "$OBJECT_ID" ]] || fail "expected same main object after update, got $UPDATED_OBJECT_ID want $OBJECT_ID"
[[ "$UPDATED_VERSION_ID" == version_* && "$UPDATED_VERSION_ID" != "$VERSION_ID" ]] || fail "expected new main version after update, got $UPDATED_VERSION_ID"
pass "captured updated object version"

main_ssh "loom indexes run text --once --json --correlation-id corr_smoke_v0_2_slice_10_indexer_updated > /tmp/loom-v0-2-slice-10-indexer-updated.json"
check_main_json "text indexer processes updated watched-root markdown" \
  'cat /tmp/loom-v0-2-slice-10-indexer-updated.json' \
  '.ok == true and .data.run.run_status == "succeeded" and .data.run.result_summary_json.indexed >= 1'

check_main_json "search finds updated watched-root token" \
  "loom search '$UPDATED_TOKEN' --project '$SMOKE_SLUG' --json --correlation-id corr_smoke_v0_2_slice_10_search_updated" \
  ".ok == true and .data.result_count >= 1 and ([.data.results[] | select(.object_id == \"$OBJECT_ID\" and .object_version_id == \"$UPDATED_VERSION_ID\")] | length) >= 1"

check_main_json "excluded temp token is not indexed" \
  "loom search '$EXCLUDED_TOKEN' --project '$SMOKE_SLUG' --json --correlation-id corr_smoke_v0_2_slice_10_search_excluded" \
  '.ok == true and .data.result_count == 0'

workspace_ssh "rm -f '$REMOTE_VAULT/Project.md'"
RUN_DELETED_JSON="$(agent_json watched-roots run notes --once --mode full --stability-window 0s --flush)"
check_json_value "deleted markdown records tombstone request" "$RUN_DELETED_JSON" \
  '.ok == true and .data.status == "healthy" and .data.output_flush.status == "recorded" and .data.main_report.status == "recorded"'

EXPLAIN_DELETED_JSON="$(agent_json watched-roots explain notes --path Project.md)"
DELETION_REQUEST_ID="$(printf '%s' "$EXPLAIN_DELETED_JSON" | jq -r '.data.state.deletion_request_id')"
[[ "$DELETION_REQUEST_ID" == deletion_request_* ]] || fail "expected main deletion request id, got $DELETION_REQUEST_ID"
check_json_value "local path state records deleted tombstone" "$EXPLAIN_DELETED_JSON" \
  '.ok == true and .data.state.status == "deleted" and .data.state.deletion_status == "recorded"'

check_main_json "main deletion request includes watched-root tombstone" \
  "loom sync deletion-requests --node '$NODE_ID' --json --correlation-id corr_smoke_v0_2_slice_10_deletions" \
  ".ok == true and ([.data[] | select(.deletion_request_id == \"$DELETION_REQUEST_ID\" and .target_ref == \"$OBJECT_ID\" and .status == \"recorded\")] | length) == 1"

check_main_json "main object remains inspectable after tombstone" \
  "loom object inspect '$OBJECT_ID' --json --correlation-id corr_smoke_v0_2_slice_10_object_after_delete" \
  ".ok == true and .data.object.object_id == \"$OBJECT_ID\""

check_main_json "watched-root findings command returns JSON array" \
  "loom watched-roots findings --node '$NODE_ID' --root notes --status open --json --correlation-id corr_smoke_v0_2_slice_10_wr_findings" \
  '.ok == true and (.data | type) == "array"'

log "completed $pass_count checks"
