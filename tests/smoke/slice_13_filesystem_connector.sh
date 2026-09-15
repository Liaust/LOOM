#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." && pwd)"
WORKSPACE_SCRIPT="$REPO_ROOT/scripts/loom-workspace"
PRE_SLICE_SMOKE="$REPO_ROOT/tests/smoke/pre_slice_10_part_2_wireguard.sh"

MAIN_HOST="${LOOM_DEV_HOST:-loom-dev}"
MAIN_URL="${LOOM_MAIN_URL:-http://10.44.0.2:8080}"
RUN_ID="$(date +%Y%m%d%H%M%S)-$RANDOM"
NODE_KEY="slice-13-filesystem-${RUN_ID}"
REMOTE_TMP="/tmp/loom-node-agent-slice-13-${RUN_ID}"
REMOTE_CONFIG="${REMOTE_TMP}/config.json"
REMOTE_STATE="${REMOTE_TMP}/state.json"
REMOTE_DATA="${REMOTE_TMP}/data"
REMOTE_SAFE_ROOT="${REMOTE_TMP}/safe-root"
REMOTE_PRIVATE_ROOT="${REMOTE_TMP}/private-root"
REMOTE_LIMITED_ROOT="${REMOTE_TMP}/limited-root"
UNIQUE_TEXT="slice13 filesystem connector searchable ${RUN_ID}"
PRIVATE_UNIQUE_TEXT="slice13 filesystem private raw backup ${RUN_ID} should not index"
OVERSIZED_UNIQUE_TEXT="slice13 filesystem oversized denied ${RUN_ID} should not index"

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

json_string() {
  jq -rn --arg value "$1" '$value'
}

run_waited_remote_call() {
  local target="$1"
  local input_json="$2"
  local label="$3"
  local output_file
  local poll_json
  local processed=0

  output_file="$(mktemp)"
  main_ssh "loom capability call '$target' --input '$input_json' --wait --timeout-seconds 20 --json" >"$output_file" &
  local call_pid=$!

  for _ in $(seq 1 20); do
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

main_ssh 'loom health --json | jq -e ".ok == true and .data.status == \"ok\" and .data.checks.migrations.current_version >= 11" >/dev/null'
pass "main health ok at migration 11 or newer"

"$WORKSPACE_SCRIPT" ssh "rm -rf '$REMOTE_TMP' && mkdir -p '$REMOTE_SAFE_ROOT' '$REMOTE_PRIVATE_ROOT' '$REMOTE_LIMITED_ROOT'"
"$WORKSPACE_SCRIPT" ssh "printf '# Slice 13 File\n\n%s\n' '$UNIQUE_TEXT' > '$REMOTE_SAFE_ROOT/example.md'"
"$WORKSPACE_SCRIPT" ssh "printf 'hidden should not list\n' > '$REMOTE_SAFE_ROOT/.hidden.txt'"
"$WORKSPACE_SCRIPT" ssh "printf '%s\n' '$PRIVATE_UNIQUE_TEXT' > '$REMOTE_PRIVATE_ROOT/private-note.txt'"
"$WORKSPACE_SCRIPT" ssh "printf '%s\n' '$OVERSIZED_UNIQUE_TEXT' > '$REMOTE_LIMITED_ROOT/oversized.txt'"
pass "workspace created safe, private, hidden, and oversized test files"

"$WORKSPACE_SCRIPT" run -- \
  --config "$REMOTE_CONFIG" \
  --state "$REMOTE_STATE" \
  --data-dir "$REMOTE_DATA" \
  --json \
  init \
  --main-url "$MAIN_URL" \
  --node-key "$NODE_KEY" \
  --display-name "Slice 13 Filesystem Node Agent ${RUN_ID}" \
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

ROOT_JSON="$("$WORKSPACE_SCRIPT" run -- \
  --config "$REMOTE_CONFIG" \
  --state "$REMOTE_STATE" \
  --data-dir "$REMOTE_DATA" \
  --json \
  filesystem roots add \
  --key slice13 \
  --path "$REMOTE_SAFE_ROOT")"
check_json_value "workspace configured public filesystem safe root" "$ROOT_JSON" \
  '.ok == true and ([.data.safe_roots[] | select(.root_key == "slice13" and .allow_list == true and .allow_metadata == true and .allow_ingest == true)] | length) == 1'

"$WORKSPACE_SCRIPT" run -- \
  --config "$REMOTE_CONFIG" \
  --state "$REMOTE_STATE" \
  --data-dir "$REMOTE_DATA" \
  --json \
  filesystem roots add \
  --key private \
  --path "$REMOTE_PRIVATE_ROOT" \
  --private-backup-only >/dev/null
pass "workspace configured private backup-only root"

"$WORKSPACE_SCRIPT" run -- \
  --config "$REMOTE_CONFIG" \
  --state "$REMOTE_STATE" \
  --data-dir "$REMOTE_DATA" \
  --json \
  filesystem roots add \
  --key limited \
  --path "$REMOTE_LIMITED_ROOT" \
  --max-file-bytes 8 >/dev/null
pass "workspace configured limited-size safe root"

LOCAL_PROVIDER_JSON="$("$WORKSPACE_SCRIPT" run -- \
  --config "$REMOTE_CONFIG" \
  --state "$REMOTE_STATE" \
  --data-dir "$REMOTE_DATA" \
  --json \
  providers local \
  --provider filesystem)"
PROVIDER_ADDRESS="$(printf '%s' "$LOCAL_PROVIDER_JSON" | jq -r '.data.provider.compact_address')"
SAFE_LIST_ADDRESS="${PROVIDER_ADDRESS}.safe_list"
METADATA_ADDRESS="${PROVIDER_ADDRESS}.read_metadata"
INGEST_ADDRESS="${PROVIDER_ADDRESS}.ingest_file"
check_json_value "workspace local filesystem provider payload is reviewable" "$LOCAL_PROVIDER_JSON" \
  '.ok == true and .data.provider.provider_key == "filesystem" and .data.provider.provider_type == "connector" and (.data.provider.capabilities | length) == 3 and .data.provider.runtime_profile_json.absolute_paths_exposed == false and .data.provider.runtime_profile_json.private_backup_only_root_count == 1 and (.data | has("credential_token") | not)'

ADVERTISE_JSON="$("$WORKSPACE_SCRIPT" run -- \
  --config "$REMOTE_CONFIG" \
  --state "$REMOTE_STATE" \
  --data-dir "$REMOTE_DATA" \
  --json \
  advertise \
  --once \
  --provider filesystem)"
ADVERTISEMENT_ID="$(printf '%s' "$ADVERTISE_JSON" | jq -r '.data.advertisement.provider_advertisement_id')"
MESSAGE_ID="$(printf '%s' "$ADVERTISE_JSON" | jq -r '.data.advertisement.communication_message_id')"
[[ "$ADVERTISEMENT_ID" == provider_advertisement_* ]] || fail "expected provider advertisement id"
[[ "$MESSAGE_ID" == communication_message_* ]] || fail "expected provider advertisement message id"
check_json_value "workspace advertised filesystem provider pending owner review" "$ADVERTISE_JSON" \
  '.ok == true and .data.advertisement.status == "pending_review" and .data.provider.status == "registered" and ([.data.endpoints[].endpoint_name] | sort) == ["ingest_file","read_metadata","safe_list"] and (.data.usage_documents | length) >= 4'

PENDING_LIST_JSON="$(main_ssh "loom provider-advertisements list --status pending_review --json")"
check_json_value "main lists pending filesystem provider advertisement" "$PENDING_LIST_JSON" \
  ".ok == true and ([.data[] | select(.provider_advertisement_id == \"$ADVERTISEMENT_ID\")] | length) == 1"

APPROVE_JSON="$(main_ssh "loom provider-advertisement approve '$ADVERTISEMENT_ID' --reason 'slice 13 filesystem smoke approval' --json")"
check_json_value "owner approval activates filesystem provider and endpoints" "$APPROVE_JSON" \
  '.ok == true and .data.advertisement.status == "approved" and .data.provider.status == "active" and ([.data.endpoints[].status] | all(. == "active")) and ([.data.usage_documents[].review_status] | all(. == "approved"))'

PROVIDER_JSON="$(main_ssh "loom provider inspect '$PROVIDER_ADDRESS' --json")"
check_json_value "approved filesystem provider is inspectable and healthy" "$PROVIDER_JSON" \
  ".ok == true and .data.provider.status == \"active\" and .data.provider.provider_type == \"connector\" and .data.health.health_status == \"ok\" and .data.health.availability_status == \"available\" and .data.provider.node_id == \"$NODE_ID\" and .data.health.details_json.safe_root_count == 2"

SAFE_LIST_CAP_JSON="$(main_ssh "loom capability inspect '$SAFE_LIST_ADDRESS' --json")"
check_json_value "safe_list capability metadata is active and low risk" "$SAFE_LIST_CAP_JSON" \
  '.ok == true and .data.endpoint.status == "active" and .data.endpoint.form == "query" and .data.endpoint.risk_level == "low" and .data.endpoint.execution_authorization_level == 1'

METADATA_CAP_JSON="$(main_ssh "loom capability inspect '$METADATA_ADDRESS' --json")"
check_json_value "read_metadata capability metadata is active and low risk" "$METADATA_CAP_JSON" \
  '.ok == true and .data.endpoint.status == "active" and .data.endpoint.form == "query" and .data.endpoint.risk_level == "low" and .data.endpoint.execution_authorization_level == 1'

INGEST_CAP_JSON="$(main_ssh "loom capability inspect '$INGEST_ADDRESS' --json")"
check_json_value "ingest_file capability metadata is active and level two" "$INGEST_CAP_JSON" \
  '.ok == true and .data.endpoint.status == "active" and .data.endpoint.form == "command" and .data.endpoint.risk_level == "medium" and .data.endpoint.execution_authorization_level == 2 and .data.active_version.status == "active"'

USAGE_DOCS_JSON="$(main_ssh "loom capability usage-docs '$INGEST_ADDRESS' --json")"
check_json_value "approved ingest_file usage document is inspectable" "$USAGE_DOCS_JSON" \
  '.ok == true and (.data | length) >= 1 and ([.data[].review_status] | all(. == "approved"))'

LIST_JSON="$(run_waited_remote_call "$SAFE_LIST_ADDRESS" '{"root":"slice13","path":".","max_entries":20}' "safe_list")"
check_json_value "remote safe_list returns visible relative entries only" "$LIST_JSON" \
  '.ok == true and .data.status == "completed" and ([.data.result.entries[].relative_path] | index("example.md") != null) and ([.data.result.entries[].relative_path] | index(".hidden.txt") == null) and ([.data.result.entries[].relative_path | startswith("/")] | any) == false'

METADATA_JSON="$(run_waited_remote_call "$METADATA_ADDRESS" '{"root":"slice13","path":"example.md"}' "read_metadata")"
check_json_value "remote read_metadata returns file metadata without absolute path" "$METADATA_JSON" \
  '.ok == true and .data.status == "completed" and .data.result.path == "example.md" and .data.result.kind == "file" and .data.result.mime_type == "text/markdown" and (.data.result | has("absolute_path") | not)'

PROJECT_SLUG="slice-13-filesystem-project-${RUN_ID}"
PROJECT_JSON="$(main_ssh "loom project create 'Slice 13 Filesystem Project ${RUN_ID}' --slug '$PROJECT_SLUG' --if-not-exists --json")"
PROJECT_ID="$(printf '%s' "$PROJECT_JSON" | jq -r '.data.project.project.project_id')"
[[ "$PROJECT_ID" == project_* ]] || fail "expected project id"
pass "owner created main project for filesystem ingest"

INGEST_INPUT="$(jq -cn --arg root slice13 --arg path example.md --arg project "$PROJECT_SLUG" '{root:$root,path:$path,project:$project,index_policy:"default"}')"
INGEST_JSON="$(run_waited_remote_call "$INGEST_ADDRESS" "$INGEST_INPUT" "ingest_file")"
OBJECT_ID="$(printf '%s' "$INGEST_JSON" | jq -r '.data.result.object_id')"
VERSION_ID="$(printf '%s' "$INGEST_JSON" | jq -r '.data.result.object_version_id')"
BLOB_ID="$(printf '%s' "$INGEST_JSON" | jq -r '.data.result.blob_id')"
SYNC_BATCH_ID="$(printf '%s' "$INGEST_JSON" | jq -r '.data.result.sync_batch_id')"
ROUTE_ID="$(printf '%s' "$INGEST_JSON" | jq -r '.data.route.route_id')"
CALL_ID="$(printf '%s' "$INGEST_JSON" | jq -r '.data.capability_call.capability_call_id')"
DISPATCH_MESSAGE_ID="$(printf '%s' "$INGEST_JSON" | jq -r '.data.dispatch_message_id')"
RESULT_MESSAGE_ID="$(printf '%s' "$INGEST_JSON" | jq -r '.data.result_refs.result_message_id')"
[[ "$OBJECT_ID" == object_* ]] || fail "expected object id"
[[ "$VERSION_ID" == version_* ]] || fail "expected version id"
[[ "$BLOB_ID" == blob_* ]] || fail "expected blob id"
[[ "$SYNC_BATCH_ID" == sync_batch_* ]] || fail "expected sync batch id"
[[ "$ROUTE_ID" == route_* ]] || fail "expected route id"
[[ "$CALL_ID" == capability_call_* ]] || fail "expected capability call id"
[[ "$DISPATCH_MESSAGE_ID" == communication_message_* ]] || fail "expected dispatch message id"
[[ "$RESULT_MESSAGE_ID" == communication_message_* ]] || fail "expected result message id"
check_json_value "remote ingest_file uploads object through sync API" "$INGEST_JSON" \
  '.ok == true and .data.status == "completed" and .data.result.logical_source == "filesystem://slice13/example.md" and .data.result.object_id != "" and .data.result.object_version_id != "" and .data.result.blob_id != "" and .data.result.sync_batch_id != "" and .data.result.replica_id != ""'

OBJECT_JSON="$(main_ssh "loom object inspect '$OBJECT_ID' --json")"
check_json_value "main object inspect shows filesystem logical source path" "$OBJECT_JSON" \
  ".ok == true and .data.object.object_id == \"$OBJECT_ID\" and .data.latest_version.object_version_id == \"$VERSION_ID\" and .data.blob.blob_id == \"$BLOB_ID\" and .data.file.source_node_id == \"$NODE_ID\" and .data.file.source_path == \"filesystem://slice13/example.md\""

INDEX_STATUS_BEFORE_JSON="$(main_ssh "loom indexes status --object '$OBJECT_ID' --json")"
check_json_value "filesystem-ingested object has queued or completed text index work" "$INDEX_STATUS_BEFORE_JSON" \
  ".ok == true and ([.data[] | select(.index_type == \"full_text\" and (.status == \"queued\" or .status == \"indexed\") and .object_id == \"$OBJECT_ID\" and .object_version_id == \"$VERSION_ID\")] | length) == 1"

main_ssh "loom indexes run text --once --json --correlation-id corr_smoke_slice_13_indexer > /tmp/loom-slice-13-indexer.json"
check_json_value "text indexer processes filesystem-ingested object" "$(main_ssh 'cat /tmp/loom-slice-13-indexer.json')" \
  '.ok == true and .data.run.run_status == "succeeded"'

INDEX_STATUS_AFTER_JSON="$(main_ssh "loom indexes status --object '$OBJECT_ID' --json")"
check_json_value "filesystem-ingested object is indexed" "$INDEX_STATUS_AFTER_JSON" \
  ".ok == true and ([.data[] | select(.index_type == \"full_text\" and .status == \"indexed\" and .object_id == \"$OBJECT_ID\" and .object_version_id == \"$VERSION_ID\")] | length) == 1"

SEARCH_JSON="$(main_ssh "loom search '$UNIQUE_TEXT' --limit 10 --json")"
check_json_value "main search finds filesystem-ingested object text" "$SEARCH_JSON" \
  ".ok == true and ([.data.results[] | select(.object_id == \"$OBJECT_ID\")] | length) == 1"

ROUTE_JSON="$(main_ssh "loom route inspect '$ROUTE_ID' --json")"
check_json_value "ingest route completed" "$ROUTE_JSON" \
  '.ok == true and .data.status == "completed" and .data.route_kind == "remote"'

CALL_JSON="$(main_ssh "loom capability-call inspect '$CALL_ID' --json")"
check_json_value "ingest capability call completed with result refs" "$CALL_JSON" \
  ".ok == true and .data.status == \"completed\" and .data.result_json.object_id == \"$OBJECT_ID\" and .data.result_refs_json.object_id == \"$OBJECT_ID\""

DISPATCH_MESSAGE_JSON="$(main_ssh "loom message inspect '$DISPATCH_MESSAGE_ID' --json")"
check_json_value "ingest dispatch message is acked" "$DISPATCH_MESSAGE_JSON" \
  ".ok == true and .data.direction == \"main_to_node\" and .data.kind == \"capability.dispatch\" and .data.status == \"acked\" and .data.route_id == \"$ROUTE_ID\""

RESULT_MESSAGE_JSON="$(main_ssh "loom message inspect '$RESULT_MESSAGE_ID' --json")"
check_json_value "ingest result message is inspectable" "$RESULT_MESSAGE_JSON" \
  ".ok == true and .data.direction == \"node_to_main\" and .data.kind == \"capability.result\" and .data.status == \"acked\" and .data.result_json.object_id == \"$OBJECT_ID\""

BATCHES_JSON="$(main_ssh "loom sync batches --node '$NODE_ID' --json")"
check_json_value "main lists filesystem ingest sync batch" "$BATCHES_JSON" \
  ".ok == true and ([.data[] | select(.sync_batch_id == \"$SYNC_BATCH_ID\" and .status == \"accepted\")] | length) == 1"

REPLICAS_JSON="$(main_ssh "loom sync replicas --node '$NODE_ID' --json")"
check_json_value "main lists filesystem ingest replica" "$REPLICAS_JSON" \
  ".ok == true and ([.data[] | select(.replicated_id == \"$VERSION_ID\" and .replica_mode == \"main_backup\" and .freshness_state == \"fresh\")] | length) == 1"

SYNC_STATUS_JSON="$(main_ssh "loom sync status --node '$NODE_ID' --json")"
check_json_value "workspace sync status includes filesystem object cursor and replica" "$SYNC_STATUS_JSON" \
  '.ok == true and .data.summary.replica_count >= 1 and ([.data.cursors[] | select(.stream_name == "object_blobs" and .last_accepted_sequence >= 1)] | length) >= 1'

ABSOLUTE_INPUT="$(jq -cn --arg path "$REMOTE_SAFE_ROOT/example.md" '{root:"slice13",path:$path}')"
ABSOLUTE_DENIAL_JSON="$(run_waited_remote_call "$METADATA_ADDRESS" "$ABSOLUTE_INPUT" "absolute_path_denial")"
check_json_value "absolute path input is denied without leaking path" "$ABSOLUTE_DENIAL_JSON" \
  '.ok == true and .data.status == "failed" and .data.capability_call.error_code == "filesystem.absolute_path_denied" and (.data.capability_call.error_message | contains("/tmp/") | not)'

ESCAPE_DENIAL_JSON="$(run_waited_remote_call "$METADATA_ADDRESS" '{"root":"slice13","path":"../outside.md"}' "path_escape_denial")"
check_json_value "relative path escape input is denied" "$ESCAPE_DENIAL_JSON" \
  '.ok == true and .data.status == "failed" and .data.capability_call.error_code == "filesystem.path_escape_denied"'

HIDDEN_DENIAL_JSON="$(run_waited_remote_call "$METADATA_ADDRESS" '{"root":"slice13","path":".hidden.txt"}' "hidden_path_denial")"
check_json_value "hidden file metadata is denied by default" "$HIDDEN_DENIAL_JSON" \
  '.ok == true and .data.status == "failed" and .data.capability_call.error_code == "filesystem.hidden_path_denied"'

PRIVATE_LIST_JSON="$(run_waited_remote_call "$SAFE_LIST_ADDRESS" '{"root":"private","path":"."}' "private_list_denial")"
check_json_value "private backup-only root cannot be listed" "$PRIVATE_LIST_JSON" \
  '.ok == true and .data.status == "failed" and .data.capability_call.error_code == "filesystem.private_root_denied"'

PRIVATE_METADATA_JSON="$(run_waited_remote_call "$METADATA_ADDRESS" '{"root":"private","path":"private-note.txt"}' "private_metadata_denial")"
check_json_value "private backup-only root metadata is denied" "$PRIVATE_METADATA_JSON" \
  '.ok == true and .data.status == "failed" and .data.capability_call.error_code == "filesystem.private_root_denied"'

PRIVATE_INGEST_JSON="$(run_waited_remote_call "$INGEST_ADDRESS" '{"root":"private","path":"private-note.txt","index_policy":"default"}' "private_ingest_denial")"
check_json_value "private backup-only root ingest is denied" "$PRIVATE_INGEST_JSON" \
  '.ok == true and .data.status == "failed" and .data.capability_call.error_code == "filesystem.private_root_denied"'

OVERSIZED_INGEST_JSON="$(run_waited_remote_call "$INGEST_ADDRESS" '{"root":"limited","path":"oversized.txt","index_policy":"default"}' "oversized_ingest_denial")"
check_json_value "oversized file ingest is denied" "$OVERSIZED_INGEST_JSON" \
  '.ok == true and .data.status == "failed" and .data.capability_call.error_code == "filesystem.file_too_large"'

PRIVATE_BACKUP_JSON="$("$WORKSPACE_SCRIPT" run -- \
  --config "$REMOTE_CONFIG" \
  --state "$REMOTE_STATE" \
  --data-dir "$REMOTE_DATA" \
  --json \
  private-backup push \
  --path "$REMOTE_PRIVATE_ROOT" \
  --once)"
PRIVATE_BACKUP_ID="$(printf '%s' "$PRIVATE_BACKUP_JSON" | jq -r '.data.operation.private_backup_operation_id')"
[[ "$PRIVATE_BACKUP_ID" == private_backup_* ]] || fail "expected private backup operation id"
check_json_value "workspace pushed private raw backup to main" "$PRIVATE_BACKUP_JSON" \
  '.ok == true and .data.operation.status == "stored" and .data.operation.metadata.source == "loom-node-agent" and .data.operation.metadata.backup_kind == "private_raw_folder"'

PRIVATE_SEARCH_JSON="$(main_ssh "loom search '$PRIVATE_UNIQUE_TEXT' --limit 10 --json")"
check_json_value "main search does not index private raw backup content" "$PRIVATE_SEARCH_JSON" \
  '.ok == true and .data.result_count == 0'

OVERSIZED_SEARCH_JSON="$(main_ssh "loom search '$OVERSIZED_UNIQUE_TEXT' --limit 10 --json")"
check_json_value "main search does not index denied oversized content" "$OVERSIZED_SEARCH_JSON" \
  '.ok == true and .data.result_count == 0'

if rg -n '"database/sql"|pgx|postgres' "$REPO_ROOT/cmd/loom-node-agent" "$REPO_ROOT/internal/nodeagent" >/tmp/loom-node-agent-db-rg.txt; then
  cat /tmp/loom-node-agent-db-rg.txt >&2
  fail "node-agent should not import database clients directly"
fi
pass "node-agent stays out of direct database-client ownership"

log "completed $pass_count checks"
