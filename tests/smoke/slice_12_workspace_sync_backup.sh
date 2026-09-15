#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." && pwd)"
WORKSPACE_SCRIPT="$REPO_ROOT/scripts/loom-workspace"
PRE_SLICE_SMOKE="$REPO_ROOT/tests/smoke/pre_slice_10_part_2_wireguard.sh"

MAIN_HOST="${LOOM_DEV_HOST:-loom-dev}"
MAIN_URL="${LOOM_MAIN_URL:-http://10.44.0.2:8080}"
RUN_ID="$(date +%Y%m%d%H%M%S)-$RANDOM"
NODE_KEY="slice-12-sync-node-agent-${RUN_ID}"
REMOTE_TMP="/tmp/loom-node-agent-slice-12-${RUN_ID}"
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

main_ssh 'loom health --json | jq -e ".ok == true and .data.status == \"ok\" and .data.checks.migrations.current_version >= 11" >/dev/null'
pass "main health ok at migration 11 or newer"

"$WORKSPACE_SCRIPT" ssh "rm -rf '$REMOTE_TMP' && mkdir -p '$REMOTE_TMP'"
"$WORKSPACE_SCRIPT" run -- \
  --config "$REMOTE_CONFIG" \
  --state "$REMOTE_STATE" \
  --data-dir "$REMOTE_DATA" \
  --json \
  init \
  --main-url "$MAIN_URL" \
  --node-key "$NODE_KEY" \
  --display-name "Slice 12 Sync Node Agent ${RUN_ID}" \
  --kind workspace \
  --role workspace \
  --runtime-class workspace >/dev/null
pass "workspace node-agent initialized"

"$WORKSPACE_SCRIPT" run -- \
  --config "$REMOTE_CONFIG" \
  --state "$REMOTE_STATE" \
  --data-dir "$REMOTE_DATA" \
  --json \
  sync init-local >/dev/null
pass "workspace local sync queue initialized"

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

APPEND_JSON="$("$WORKSPACE_SCRIPT" run -- \
  --config "$REMOTE_CONFIG" \
  --state "$REMOTE_STATE" \
  --data-dir "$REMOTE_DATA" \
  --json \
  sync append-test-event \
  --message "hello from slice 12")"
LOCAL_EVENT_ID="$(printf '%s' "$APPEND_JSON" | jq -r '.data.local_event_id')"
check_json_value "workspace appended local sync event" "$APPEND_JSON" \
  '.ok == true and (.data.local_event_id | startswith("local_event_")) and .data.local_sequence == 1 and .data.sync_status == "pending"'

LOCAL_STATUS_JSON="$("$WORKSPACE_SCRIPT" run -- \
  --config "$REMOTE_CONFIG" \
  --state "$REMOTE_STATE" \
  --data-dir "$REMOTE_DATA" \
  --json \
  sync status)"
check_json_value "workspace local sync status shows pending item" "$LOCAL_STATUS_JSON" \
  '.ok == true and .data.counts.events == 1 and .data.counts.pending == 1 and .data.counts.conflicts == 0'

PUSH_JSON="$("$WORKSPACE_SCRIPT" run -- \
  --config "$REMOTE_CONFIG" \
  --state "$REMOTE_STATE" \
  --data-dir "$REMOTE_DATA" \
  --json \
  sync push \
  --once \
  --max-items 10)"
BATCH_ID="$(printf '%s' "$PUSH_JSON" | jq -r '.data.batch.batch.sync_batch_id')"
GLOBAL_EVENT_ID="$(printf '%s' "$PUSH_JSON" | jq -r '.data.batch.items[0].global_ref')"
[[ "$BATCH_ID" == sync_batch_* ]] || fail "expected sync batch id"
[[ "$GLOBAL_EVENT_ID" == event_* ]] || fail "expected global event id"
check_json_value "workspace pushed local sync event to main" "$PUSH_JSON" \
  '.ok == true and .data.submitted_items == 1 and .data.batch.batch.status == "accepted" and .data.batch.items[0].status == "accepted" and .data.local_status.counts.pending == 0 and .data.local_status.counts.accepted == 1'

MAIN_STATUS_JSON="$(main_ssh "loom sync status --node '$NODE_ID' --json")"
check_json_value "main sync status exposes accepted cursor" "$MAIN_STATUS_JSON" \
  '.ok == true and .data.summary.cursor_count == 1 and .data.cursors[0].stream_name == "events" and .data.cursors[0].last_accepted_sequence == 1'

MAIN_EVENTS_JSON="$(main_ssh "loom events list --node '$NODE_ID' --type node.local_test_event --limit 20 --json")"
check_json_value "main durable events include synced local event" "$MAIN_EVENTS_JSON" \
  ".ok == true and ([.data[] | select(.event_id == \"$GLOBAL_EVENT_ID\" and .payload.local_event_id == \"$LOCAL_EVENT_ID\")] | length) == 1"

DUPLICATE_PUSH_JSON="$("$WORKSPACE_SCRIPT" run -- \
  --config "$REMOTE_CONFIG" \
  --state "$REMOTE_STATE" \
  --data-dir "$REMOTE_DATA" \
  --json \
  sync push \
  --once \
  --include-synced \
  --max-items 10)"
check_json_value "duplicate sync push returns idempotent accepted result" "$DUPLICATE_PUSH_JSON" \
  ".ok == true and .data.batch.batch.sync_batch_id == \"$BATCH_ID\" and .data.batch.items[0].status == \"accepted\""

"$WORKSPACE_SCRIPT" run -- \
  --config "$REMOTE_CONFIG" \
  --state "$REMOTE_STATE" \
  --data-dir "$REMOTE_DATA" \
  --json \
  sync append-test-event \
  --local-event-id "$LOCAL_EVENT_ID" \
  --sequence 1 \
  --message "changed payload for conflict smoke" >/dev/null
pass "workspace appended conflicting local sync event"

CONFLICT_PUSH_JSON="$("$WORKSPACE_SCRIPT" run -- \
  --config "$REMOTE_CONFIG" \
  --state "$REMOTE_STATE" \
  --data-dir "$REMOTE_DATA" \
  --json \
  sync push \
  --once \
  --max-items 10)"
check_json_value "main rejects duplicate local ref with changed payload as conflict" "$CONFLICT_PUSH_JSON" \
  '.ok == true and .data.batch.batch.status == "conflicted" and .data.batch.items[0].status == "conflicted" and .data.local_status.counts.conflicted == 1 and .data.local_status.counts.conflicts == 1'

MAIN_CONFLICTS_JSON="$(main_ssh "loom sync conflicts --node '$NODE_ID' --status open --json")"
check_json_value "main lists open sync conflict" "$MAIN_CONFLICTS_JSON" \
  ".ok == true and ([.data[] | select(.local_ref == \"$LOCAL_EVENT_ID\" and .conflict_type == \"duplicate_payload_mismatch\")] | length) == 1"

PROJECT_SLUG="slice-12-sync-project-${RUN_ID}"
PROJECT_JSON="$(main_ssh "loom project create 'Slice 12 Sync Project ${RUN_ID}' --slug '$PROJECT_SLUG' --if-not-exists --json")"
PROJECT_ID="$(printf '%s' "$PROJECT_JSON" | jq -r '.data.project.project.project_id')"
[[ "$PROJECT_ID" == project_* ]] || fail "expected project id"
pass "owner created main project for object sync"

UNIQUE_TEXT="slice12 searchable ${RUN_ID} workspace object upload"
REMOTE_OBJECT_PATH="${REMOTE_TMP}/slice-12-object-${RUN_ID}.md"
"$WORKSPACE_SCRIPT" ssh "printf '# Slice 12 Object\n\n%s\n' '$UNIQUE_TEXT' > '$REMOTE_OBJECT_PATH'"
pass "workspace created local markdown file"

OBJECT_QUEUE_JSON="$("$WORKSPACE_SCRIPT" run -- \
  --config "$REMOTE_CONFIG" \
  --state "$REMOTE_STATE" \
  --data-dir "$REMOTE_DATA" \
  --json \
  sync create-test-object \
  --path "$REMOTE_OBJECT_PATH" \
  --project "$PROJECT_SLUG" \
  --name "Slice 12 Synced Object ${RUN_ID}.md" \
  --index-policy text_later)"
LOCAL_OBJECT_ID="$(printf '%s' "$OBJECT_QUEUE_JSON" | jq -r '.data.local_object_id')"
check_json_value "workspace queued local object sync item" "$OBJECT_QUEUE_JSON" \
  '.ok == true and (.data.local_object_id | startswith("object_")) and (.data.local_version_id | startswith("version_")) and .data.local_sequence == 1 and .data.sync_status == "pending"'

OBJECT_PUSH_JSON="$("$WORKSPACE_SCRIPT" run -- \
  --config "$REMOTE_CONFIG" \
  --state "$REMOTE_STATE" \
  --data-dir "$REMOTE_DATA" \
  --json \
  sync push \
  --once \
  --max-items 10)"
MAIN_OBJECT_ID="$(printf '%s' "$OBJECT_PUSH_JSON" | jq -r '.data.object_uploads[0].object_id')"
MAIN_VERSION_ID="$(printf '%s' "$OBJECT_PUSH_JSON" | jq -r '.data.object_uploads[0].object_version_id')"
MAIN_BLOB_ID="$(printf '%s' "$OBJECT_PUSH_JSON" | jq -r '.data.object_uploads[0].blob_id')"
[[ "$MAIN_OBJECT_ID" == object_* ]] || fail "expected main object id"
[[ "$MAIN_VERSION_ID" == version_* ]] || fail "expected main object version id"
[[ "$MAIN_BLOB_ID" == blob_* ]] || fail "expected main blob id"
check_json_value "workspace uploaded local object to main" "$OBJECT_PUSH_JSON" \
  '.ok == true and .data.submitted_items == 1 and .data.object_uploads[0].batch.status == "accepted" and .data.object_uploads[0].item.status == "accepted" and .data.object_uploads[0].replica.replica_mode == "main_backup" and .data.object_uploads[0].index_status == "queued"'

OBJECT_DETAIL_JSON="$(main_ssh "loom object inspect '$MAIN_OBJECT_ID' --json")"
check_json_value "main object inspect shows synced source node and locations" "$OBJECT_DETAIL_JSON" \
  ".ok == true and .data.object.object_id == \"$MAIN_OBJECT_ID\" and .data.latest_version.source_node_id == \"$NODE_ID\" and .data.file.source_node_id == \"$NODE_ID\" and ([.data.locations[] | select(.location_type == \"local_filesystem\" and .node_id == \"$NODE_ID\")] | length) == 1 and ([.data.locations[] | select(.location_type == \"object_store\")] | length) == 1"

REPLICAS_JSON="$(main_ssh "loom sync replicas --node '$NODE_ID' --json")"
check_json_value "main lists synced object replica" "$REPLICAS_JSON" \
  ".ok == true and ([.data[] | select(.replicated_id == \"$MAIN_VERSION_ID\" and .replica_mode == \"main_backup\" and .freshness_state == \"fresh\")] | length) == 1"

OBJECT_STATUS_JSON="$(main_ssh "loom sync status --node '$NODE_ID' --json")"
check_json_value "main sync status exposes object cursor and replica count" "$OBJECT_STATUS_JSON" \
  '.ok == true and .data.summary.replica_count >= 1 and ([.data.cursors[] | select(.stream_name == "object_blobs" and .last_accepted_sequence == 1)] | length) == 1'

main_ssh "loom indexes run text --once --json --correlation-id corr_smoke_slice_12_indexer > /tmp/loom-slice-12-indexer.json"
check_json_value "text indexer processes synced object" "$(main_ssh 'cat /tmp/loom-slice-12-indexer.json')" \
  '.ok == true and .data.run.run_status == "succeeded" and .data.run.result_summary_json.indexed >= 1'

INDEX_STATUS_JSON="$(main_ssh "loom indexes status --object '$MAIN_OBJECT_ID' --json")"
check_json_value "main index status shows synced object indexed" "$INDEX_STATUS_JSON" \
  ".ok == true and ([.data[] | select(.index_type == \"full_text\" and .status == \"indexed\" and .object_id == \"$MAIN_OBJECT_ID\" and .object_version_id == \"$MAIN_VERSION_ID\")] | length) == 1"

SEARCH_JSON="$(main_ssh "loom search '$UNIQUE_TEXT' --limit 10 --json")"
check_json_value "main search finds synced object text" "$SEARCH_JSON" \
  ".ok == true and ([.data.results[] | select(.object_id == \"$MAIN_OBJECT_ID\")] | length) == 1"

DUPLICATE_OBJECT_PUSH_JSON="$("$WORKSPACE_SCRIPT" run -- \
  --config "$REMOTE_CONFIG" \
  --state "$REMOTE_STATE" \
  --data-dir "$REMOTE_DATA" \
  --json \
  sync push \
  --once \
  --include-synced \
  --max-items 10)"
check_json_value "duplicate object sync push returns idempotent upload result" "$DUPLICATE_OBJECT_PUSH_JSON" \
  ".ok == true and ([.data.object_uploads[] | select(.object_id == \"$MAIN_OBJECT_ID\" and .item.status == \"accepted\")] | length) == 1"

PRIVATE_UNIQUE_TEXT="slice12 private raw backup ${RUN_ID} should not index"
REMOTE_PRIVATE_DIR="${REMOTE_TMP}/private-${RUN_ID}"
"$WORKSPACE_SCRIPT" ssh "mkdir -p '$REMOTE_PRIVATE_DIR' && printf '%s\n' '$PRIVATE_UNIQUE_TEXT' > '$REMOTE_PRIVATE_DIR/private-note.txt'"
pass "workspace created tiny private raw backup folder"

PRIVATE_BACKUP_JSON="$("$WORKSPACE_SCRIPT" run -- \
  --config "$REMOTE_CONFIG" \
  --state "$REMOTE_STATE" \
  --data-dir "$REMOTE_DATA" \
  --json \
  private-backup push \
  --path "$REMOTE_PRIVATE_DIR" \
  --once)"
PRIVATE_BACKUP_ID="$(printf '%s' "$PRIVATE_BACKUP_JSON" | jq -r '.data.operation.private_backup_operation_id')"
PRIVATE_BACKUP_STORAGE_REF="$(printf '%s' "$PRIVATE_BACKUP_JSON" | jq -r '.data.operation.storage_ref')"
[[ "$PRIVATE_BACKUP_ID" == private_backup_* ]] || fail "expected private backup operation id"
check_json_value "workspace pushed private raw backup to main" "$PRIVATE_BACKUP_JSON" \
  '.ok == true and .data.operation.status == "stored" and .data.operation.metadata.source == "loom-node-agent" and .data.operation.metadata.backup_kind == "private_raw_folder" and ((.data.operation.metadata | keys_unsorted - ["source", "backup_kind", "client_generated_at", "item_count_coarse"]) | length) == 0'

PRIVATE_BACKUPS_JSON="$(main_ssh "loom sync private-backups --node '$NODE_ID' --json")"
check_json_value "main lists private backup without content metadata" "$PRIVATE_BACKUPS_JSON" \
  ".ok == true and ([.data[] | select(.private_backup_operation_id == \"$PRIVATE_BACKUP_ID\" and .origin_node_id == \"$NODE_ID\" and .status == \"stored\" and (.metadata.path? == null) and (.metadata.filename? == null) and (.metadata.source_path? == null))] | length) == 1"

PRIVATE_SEARCH_JSON="$(main_ssh "loom search '$PRIVATE_UNIQUE_TEXT' --limit 10 --json")"
check_json_value "main search does not index private raw backup content" "$PRIVATE_SEARCH_JSON" \
  '.ok == true and .data.result_count == 0'

main_ssh "sudo -n test -f '$PRIVATE_BACKUP_STORAGE_REF'"
pass "main stored private raw backup outside object store pipeline"

DELETION_JSON="$("$WORKSPACE_SCRIPT" run -- \
  --config "$REMOTE_CONFIG" \
  --state "$REMOTE_STATE" \
  --data-dir "$REMOTE_DATA" \
  --json \
  sync request-delete \
  --local-object "$LOCAL_OBJECT_ID" \
  --reason "slice 12 smoke requested tombstone")"
DELETION_REQUEST_ID="$(printf '%s' "$DELETION_JSON" | jq -r '.data.request.deletion_request_id')"
[[ "$DELETION_REQUEST_ID" == deletion_request_* ]] || fail "expected deletion request id"
check_json_value "workspace recorded deletion request without hard delete" "$DELETION_JSON" \
  ".ok == true and .data.request.status == \"recorded\" and .data.request.target_kind == \"object\" and .data.request.target_ref == \"$MAIN_OBJECT_ID\" and .data.request.requested_action == \"tombstone\""

DELETIONS_JSON="$(main_ssh "loom sync deletion-requests --node '$NODE_ID' --json")"
check_json_value "main lists sync deletion request" "$DELETIONS_JSON" \
  ".ok == true and ([.data[] | select(.deletion_request_id == \"$DELETION_REQUEST_ID\" and .target_ref == \"$MAIN_OBJECT_ID\" and .status == \"recorded\")] | length) == 1"

POST_DELETE_OBJECT_JSON="$(main_ssh "loom object inspect '$MAIN_OBJECT_ID' --json")"
MAIN_BLOB_STORAGE_PATH="$(printf '%s' "$POST_DELETE_OBJECT_JSON" | jq -r '.data.blob.storage_path')"
check_json_value "main object remains inspectable after deletion request" "$POST_DELETE_OBJECT_JSON" \
  ".ok == true and .data.object.object_id == \"$MAIN_OBJECT_ID\" and .data.blob.blob_id == \"$MAIN_BLOB_ID\" and .data.latest_version.object_version_id == \"$MAIN_VERSION_ID\""
main_ssh "sudo -n test -f '$MAIN_BLOB_STORAGE_PATH' && sudo -n test -f '$PRIVATE_BACKUP_STORAGE_REF'"
pass "deletion request did not hard-delete object blob or private backup history"

"$WORKSPACE_SCRIPT" ssh "test -f '$REMOTE_DATA/sync/events.json' && test -f '$REMOTE_DATA/sync/objects.json' && test -f '$REMOTE_DATA/sync/outbox.json' && test -f '$REMOTE_DATA/sync/cursors.json' && test -f '$REMOTE_DATA/sync/conflicts.json'"
pass "workspace persisted sync queue files"

if rg -n '"database/sql"|pgx|postgres' "$REPO_ROOT/cmd/loom-node-agent" "$REPO_ROOT/internal/nodeagent" >/tmp/loom-node-agent-db-rg.txt; then
  cat /tmp/loom-node-agent-db-rg.txt >&2
  fail "node-agent should not import database clients directly"
fi
pass "node-agent stays out of direct database-client ownership"

log "completed $pass_count checks"
