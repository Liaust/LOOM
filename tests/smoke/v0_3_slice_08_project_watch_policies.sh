#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." && pwd)"
WORKSPACE_SCRIPT="$REPO_ROOT/scripts/loom-workspace"
PRE_SLICE_SMOKE="$REPO_ROOT/tests/smoke/pre_slice_10_part_2_wireguard.sh"

MAIN_HOST="${LOOM_DEV_HOST:-loom-dev}"
MAIN_URL="${LOOM_MAIN_URL:-http://10.44.0.2:8080}"
REMOTE_DIR="${LOOM_DEV_REMOTE_DIR:-/srv/loom/current}"
RUN_ID="v0-3-slice-08-$(date +%s)-$RANDOM"
TOKEN_ID="${RUN_ID//-/}"
NODE_KEY="slice08-$TOKEN_ID"
SLUG="slice08-project-$TOKEN_ID"
ROOT_PREFIX="${SLUG//-/_}"
NOTES_ROOT="${ROOT_PREFIX}__notes"
PROJECT_ROOT_KEY="${ROOT_PREFIX}__project"
NOTE_TOKEN="slice08note${TOKEN_ID}"
UPDATED_TOKEN="slice08updated${TOKEN_ID}"
REPO_TOKEN="slice08repo${TOKEN_ID}"
REMOTE_TMP="/tmp/loom-node-agent-v0-3-slice-08-${TOKEN_ID}"
REMOTE_CONFIG="${REMOTE_TMP}/config.json"
REMOTE_STATE="${REMOTE_TMP}/state.json"
REMOTE_DATA="${REMOTE_TMP}/data"
REMOTE_PROJECT_ROOT="${REMOTE_TMP}/${SLUG}"
REMOTE_PLAN_JSON="${REMOTE_TMP}/watch-plan.json"
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

copy_main_project_to_workspace() {
  ssh -o BatchMode=yes "$MAIN_HOST" "tar -C '$PROJECT_ROOT' -cf - ." | \
    "$WORKSPACE_SCRIPT" ssh "rm -rf '$REMOTE_PROJECT_ROOT' && mkdir -p '$REMOTE_PROJECT_ROOT' && tar -C '$REMOTE_PROJECT_ROOT' -xf - && chmod -R u+rwX '$REMOTE_PROJECT_ROOT'"
}

copy_main_plan_to_workspace() {
  ssh -o BatchMode=yes "$MAIN_HOST" "cat '$PLAN_JSON'" | \
    "$WORKSPACE_SCRIPT" ssh "cat > '$REMOTE_PLAN_JSON'"
}

require_command jq
require_command ssh
require_command rsync

log "main: $MAIN_HOST"
log "workspace node key: $NODE_KEY"
log "project slug: $SLUG"

"$PRE_SLICE_SMOKE"
pass "remote WireGuard prerequisite passed"

"$WORKSPACE_SCRIPT" sync
"$WORKSPACE_SCRIPT" run -- version >/dev/null
pass "loom-node-agent builds on workspace VM"

main_ssh 'loom health --json | jq -e ".ok == true and .data.status == \"ok\" and .data.checks.migrations.current_version >= 31" >/dev/null'
pass "main health ok at project watched-root migration"

workspace_ssh "rm -rf '$REMOTE_TMP' && mkdir -p '$REMOTE_TMP'"
agent_json \
  init \
  --main-url "$MAIN_URL" \
  --node-key "$NODE_KEY" \
  --display-name "v0.3 Slice 08 Workspace ${RUN_ID}" \
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

TMP_DIR="$(main_ssh "mkdir -p '$REMOTE_DIR/tmp' && mktemp -d '$REMOTE_DIR/tmp/v0-3-slice-08.XXXXXX'")"
trap 'main_ssh "rm -rf '\''$TMP_DIR'\''" >/dev/null 2>&1 || true; workspace_ssh "rm -rf '\''$REMOTE_TMP'\''" >/dev/null 2>&1 || true' EXIT
PROJECT_ROOT="$TMP_DIR/$SLUG"
PLAN_JSON="$TMP_DIR/watch-plan.json"

main_ssh "
  loom project scaffold 'Slice 08 Project $TOKEN_ID' \
    --owner-node '$NODE_KEY' \
    --preset research \
    --slug '$SLUG' \
    --directory '$TMP_DIR' > '$TMP_DIR/scaffold.out'
  mkdir -p '$PROJECT_ROOT/notes' '$PROJECT_ROOT/repos/example'
  printf '# Slice 08 Note\n\nToken: $NOTE_TOKEN\n' > '$PROJECT_ROOT/notes/Project.md'
  printf '# Slice 08 Repo\n\nToken: $REPO_TOKEN\n' > '$PROJECT_ROOT/repos/example/README.md'
  chmod a+rx '$TMP_DIR'
  chmod -R a+rX '$PROJECT_ROOT'
  grep -q 'Project scaffold: created' '$TMP_DIR/scaffold.out'
"
pass "scaffolded project with notes and repo fixture on main"

copy_main_project_to_workspace
pass "copied project fixture to workspace owner node"

main_ssh "loom project validate '$PROJECT_ROOT' > '$TMP_DIR/validate.out' && grep -q 'Project contract: ok' '$TMP_DIR/validate.out'"
pass "project validates with notes/sync/backup policy"

check_main_json "local watch-plan JSON compiles desired roots" \
  "loom project watch-plan '$PROJECT_ROOT' --json" \
  "(.watched_roots | length) >= 2 and
   ([.watched_roots[] | select(.backend_root_key == \"$NOTES_ROOT\" and .sync_mode == \"selected_files\" and .index_mode == \"markdown_text\")] | length) == 1 and
   ([.watched_roots[] | select(.backend_root_key == \"$PROJECT_ROOT_KEY\" and .backup_mode == \"incremental_raw\")] | length) == 1 and
   (.commands | length) >= 2"

main_ssh "loom project register '$PROJECT_ROOT' > '$TMP_DIR/register.out' && grep -q 'Project registration: created' '$TMP_DIR/register.out'"
pass "registered project contract on main"

main_ssh "loom project watch-plan '$SLUG' --json > '$PLAN_JSON'"
check_main_json "registered watch-plan JSON uses response envelope" \
  "cat '$PLAN_JSON'" \
  ".ok == true and
   ([.data.watched_roots[] | select(.backend_root_key == \"$NOTES_ROOT\")] | length) == 1 and
   ([.data.watched_roots[] | select(.backend_root_key == \"$PROJECT_ROOT_KEY\")] | length) == 1"

APPLY_JSON="$(main_ssh "loom project apply-watch-policy '$SLUG' --json")"
check_json_value "project watch policy persists desired state" "$APPLY_JSON" \
  ".ok == true and
   ([.data.watched_roots[] | select(.backend_root_key == \"$NOTES_ROOT\" and .activation_status == \"pending_agent_apply\")] | length) == 1 and
   ([.data.watched_roots[] | select(.backend_root_key == \"$PROJECT_ROOT_KEY\" and .activation_status == \"pending_agent_apply\")] | length) == 1"

check_main_json "project status includes watched-root registrations" \
  "loom project status '$SLUG' --json" \
  ".ok == true and (.data.watched_root_registrations | length) >= 2"

main_ssh "
  loom project sync-status '$SLUG' > '$TMP_DIR/sync-status-pending.out'
  grep -q '$NOTES_ROOT' '$TMP_DIR/sync-status-pending.out'
  grep -q 'pending_agent_apply' '$TMP_DIR/sync-status-pending.out'
  grep -q 'Next:' '$TMP_DIR/sync-status-pending.out'
"
pass "project sync-status human output includes roots, status, and next action"

copy_main_plan_to_workspace
APPLY_PLAN_JSON="$(agent_json watched-roots apply-plan "$REMOTE_PLAN_JSON" --project-root "$REMOTE_PROJECT_ROOT")"
check_json_value "workspace node-agent applied watched-root plan" "$APPLY_PLAN_JSON" \
  ".ok == true and
   ([.data.applied[] | select(.backend_root_key == \"$NOTES_ROOT\")] | length) == 1 and
   ([.data.applied[] | select(.backend_root_key == \"$PROJECT_ROOT_KEY\")] | length) == 1"

RUN_NOTES_JSON="$(agent_json watched-roots run "$NOTES_ROOT" --once --mode full --stability-window 0s --flush)"
check_json_value "notes watched root syncs and reports markdown" "$RUN_NOTES_JSON" \
  '.ok == true
   and .data.status == "healthy"
   and .data.output_flush.status == "recorded"
   and .data.main_report.status == "recorded"'

EXPLAIN_INITIAL_JSON="$(agent_json watched-roots explain "$NOTES_ROOT" --path Project.md)"
OBJECT_ID="$(printf '%s' "$EXPLAIN_INITIAL_JSON" | jq -r '.data.state.main_object_id')"
VERSION_ID="$(printf '%s' "$EXPLAIN_INITIAL_JSON" | jq -r '.data.state.main_version_id')"
[[ "$OBJECT_ID" == object_* ]] || fail "expected synced object id, got $OBJECT_ID"
[[ "$VERSION_ID" == version_* ]] || fail "expected synced version id, got $VERSION_ID"
pass "captured synced note object"

check_main_json "main watched-root status filters by project" \
  "loom watched-roots status --project '$SLUG' --json" \
  ".ok == true and ([.data[] | select(.root.root_key == \"$NOTES_ROOT\" and .root.status == \"healthy\")] | length) == 1"

check_main_json "sync replicas can filter by project" \
  "loom sync replicas --project '$SLUG' --json" \
  ".ok == true and ([.data[] | select(.replicated_id == \"$VERSION_ID\" and .metadata.object_id == \"$OBJECT_ID\")] | length) >= 1"

main_ssh "loom indexes run text --once --json > '$TMP_DIR/indexer-initial.json'"
check_main_json "search finds synced project note" \
  "loom search '$NOTE_TOKEN' --project '$SLUG' --json" \
  ".ok == true and .data.result_count >= 1 and ([.data.results[] | select(.object_id == \"$OBJECT_ID\")] | length) >= 1"

RUN_BACKUP_JSON="$(agent_json watched-roots run "$PROJECT_ROOT_KEY" --once --mode full --stability-window 0s --flush)"
check_json_value "project backup root uploads backup artifacts" "$RUN_BACKUP_JSON" \
  '.ok == true
   and .data.status == "healthy"
   and .data.output_plan.counts.backup_files >= 1
   and .data.backup_output_flush.status == "recorded"
   and .data.main_report.status == "recorded"'

check_main_json "watched-root backup status filters by project" \
  "loom watched-roots backups status --project '$SLUG' --json" \
  ".ok == true and .data.status == \"healthy\" and .data.root.root_key == \"$PROJECT_ROOT_KEY\""

check_main_json "private backups include watched-root project artifact metadata" \
  "loom sync private-backups --node '$NODE_ID' --json" \
  ".ok == true and ([.data[] | select(.metadata.backup_kind == \"watched_root_file\" and .metadata.root_key == \"$PROJECT_ROOT_KEY\")] | length) >= 1"

check_main_json "project sync-status summarizes watched roots" \
  "loom project sync-status '$SLUG' --json" \
  ".ok == true and .data.sync_roots >= 1 and
   (.data.node_statuses | length) >= 1 and
   ([.data.watched_roots[] | select(.backend_root_key == \"$NOTES_ROOT\")] | length) == 1 and
   ([.data.replicas[] | select(.replicated_id == \"$VERSION_ID\")] | length) >= 1"

check_main_json "project backup-status summarizes watched roots" \
  "loom project backup-status '$SLUG' --json" \
  ".ok == true and .data.backup_roots >= 1 and
   (.data.backup_batches | length) >= 1 and
   (.data.backup_items | length) >= 1 and
   ([.data.watched_roots[] | select(.backend_root_key == \"$PROJECT_ROOT_KEY\")] | length) == 1"

main_ssh "
  loom project backup-status '$SLUG' > '$TMP_DIR/backup-status.out'
  grep -q '$PROJECT_ROOT_KEY' '$TMP_DIR/backup-status.out'
  grep -q 'Latest backup reports:' '$TMP_DIR/backup-status.out'
  grep -q 'Recent backup batches:' '$TMP_DIR/backup-status.out'
  grep -q 'Recent backup items:' '$TMP_DIR/backup-status.out'
"
pass "project backup-status human output includes root, batches, and items"

workspace_ssh "printf '# Slice 08 Note\n\nUpdated token: $UPDATED_TOKEN\n' > '$REMOTE_PROJECT_ROOT/notes/Project.md'"
RUN_UPDATED_JSON="$(agent_json watched-roots run "$NOTES_ROOT" --once --mode full --stability-window 0s --flush)"
check_json_value "changed note syncs as a new version" "$RUN_UPDATED_JSON" \
  '.ok == true and .data.status == "healthy" and .data.output_flush.status == "recorded"'

EXPLAIN_UPDATED_JSON="$(agent_json watched-roots explain "$NOTES_ROOT" --path Project.md)"
UPDATED_VERSION_ID="$(printf '%s' "$EXPLAIN_UPDATED_JSON" | jq -r '.data.state.main_version_id')"
[[ "$UPDATED_VERSION_ID" == version_* && "$UPDATED_VERSION_ID" != "$VERSION_ID" ]] || fail "expected changed note to create new version"
pass "captured updated note version"

main_ssh "loom indexes run text --once --json > '$TMP_DIR/indexer-updated.json'"
check_main_json "search finds updated synced project note" \
  "loom search '$UPDATED_TOKEN' --project '$SLUG' --json" \
  ".ok == true and .data.result_count >= 1 and ([.data.results[] | select(.object_version_id == \"$UPDATED_VERSION_ID\")] | length) >= 1"

workspace_ssh "rm -f '$REMOTE_PROJECT_ROOT/notes/Project.md'"
RUN_DELETED_JSON="$(agent_json watched-roots run "$NOTES_ROOT" --once --mode full --stability-window 0s --flush)"
check_json_value "deleted note records tombstone request" "$RUN_DELETED_JSON" \
  '.ok == true and .data.status == "healthy" and .data.output_flush.status == "recorded"'

EXPLAIN_DELETED_JSON="$(agent_json watched-roots explain "$NOTES_ROOT" --path Project.md)"
DELETION_REQUEST_ID="$(printf '%s' "$EXPLAIN_DELETED_JSON" | jq -r '.data.state.deletion_request_id')"
[[ "$DELETION_REQUEST_ID" == deletion_request_* ]] || fail "expected deletion request id"
pass "captured deletion request"

check_main_json "main deletion request records project tombstone" \
  "loom sync deletion-requests --node '$NODE_ID' --json" \
  ".ok == true and ([.data[] | select(.deletion_request_id == \"$DELETION_REQUEST_ID\" and .target_ref == \"$OBJECT_ID\" and .status == \"recorded\")] | length) == 1"

REAPPLY_JSON="$(main_ssh "loom project apply-watch-policy '$SLUG' --json")"
check_json_value "re-applying watch policy is idempotent" "$REAPPLY_JSON" \
  ".ok == true and ([.data.watched_roots[] | select(.backend_root_key == \"$NOTES_ROOT\")] | length) == 1 and ([.data.watched_roots[] | select(.backend_root_key == \"$PROJECT_ROOT_KEY\")] | length) == 1"

log "completed $pass_count checks"
