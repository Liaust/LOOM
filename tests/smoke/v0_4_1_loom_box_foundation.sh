#!/usr/bin/env bash
set -euo pipefail

HOST="${LOOM_DEV_HOST:-loom-dev}"
REMOTE_DIR="${LOOM_DEV_REMOTE_DIR:-/srv/loom/current}"
RUN_ID="v0-4-1-foundation-$(date +%s)-$RANDOM"
pass_count=0

log() {
  printf '[smoke] %s\n' "$*"
}

pass() {
  pass_count=$((pass_count + 1))
  printf '[ok] %s\n' "$*"
}

remote() {
  ssh -o BatchMode=yes "$HOST" "$@"
}

check_remote() {
  local label="$1"
  local command="$2"
  remote "$command" >/dev/null
  pass "$label"
}

check_json() {
  local label="$1"
  local command="$2"
  local filter="$3"
  remote "$command | jq -e '$filter' >/dev/null"
  pass "$label"
}

log "target: $HOST"
log "remote dir: $REMOTE_DIR"
log "run id: $RUN_ID"

check_remote "SSH target reachable" 'hostname'
check_remote "loom CLI installed" 'command -v loom && loom version'
check_remote "jq available for smoke assertions" 'command -v jq'

TMP_DIR="$(remote "mkdir -p '$REMOTE_DIR/tmp' && mktemp -d '$REMOTE_DIR/tmp/v0-4-1-foundation.XXXXXX'")"
cleanup() {
  remote "rm -rf '$TMP_DIR'" || true
}
trap cleanup EXIT

WORKSPACE_BOX="$TMP_DIR/LOOM Box"
MAIN_BOX="$TMP_DIR/Main LOOM Box"
EXPLICIT_PARENT="$TMP_DIR/explicit-projects"

check_json "box status reports missing without creating folders" \
  "loom --json box status --path '$WORKSPACE_BOX' --profile workspace" \
  ".state == \"missing\" and .initialized == false and .dropzone_state == \"not_initialized\""

check_remote "missing status is read-only" "test ! -e '$WORKSPACE_BOX'"

check_json "box init creates workspace scaffold" \
  "loom --json box init --path '$WORKSPACE_BOX' --profile workspace" \
  ".dry_run == false and .status_after.state == \"ok\" and .status_after.contract_state == \"valid\" and .status_after.dropzone_state == \"scaffolded_for_v0.4.2\""

check_json "box init is idempotent" \
  "loom --json box init --path '$WORKSPACE_BOX' --profile workspace" \
  ".status_after.state == \"ok\" and (.created_dirs | length) == 0 and (.created_files | length) == 0"

check_remote "box folders and generated contracts exist" "
  test -d '$WORKSPACE_BOX/Projects'
  test -d '$WORKSPACE_BOX/Notes'
  test -d '$WORKSPACE_BOX/Launchpad'
  test -d '$WORKSPACE_BOX/Dropzone'
  test -d '$WORKSPACE_BOX/.loom/state/dropzone'
  test -f '$WORKSPACE_BOX/.loom/box.yaml'
  test -f '$WORKSPACE_BOX/.loom/policies/notes.watch.yaml'
  test -f '$WORKSPACE_BOX/.loom/policies/launchpad.watch.yaml'
  test -f '$WORKSPACE_BOX/.loom/policies/dropzone.transfer.yaml'
"

check_remote "box contract and policies validate v0.4.1 semantics" "
  grep -q 'schema_version: loom.box.v0.4.1' '$WORKSPACE_BOX/.loom/box.yaml'
  grep -q 'default_project_path: Projects' '$WORKSPACE_BOX/.loom/box.yaml'
  grep -q 'transfer_status: scaffolded_for_v0.4.2' '$WORKSPACE_BOX/.loom/box.yaml'
  grep -q 'mode: watched_root' '$WORKSPACE_BOX/.loom/policies/notes.watch.yaml'
  grep -q 'mode: watched_root' '$WORKSPACE_BOX/.loom/policies/launchpad.watch.yaml'
  grep -q 'mode: custody_transfer' '$WORKSPACE_BOX/.loom/policies/dropzone.transfer.yaml'
  grep -q 'enabled: false' '$WORKSPACE_BOX/.loom/policies/dropzone.transfer.yaml'
"

check_json "dropzone is scaffolded but inactive for transfers" \
  "loom --json box status --path '$WORKSPACE_BOX' --profile workspace" \
  ".dropzone_state == \"scaffolded_for_v0.4.2\" and ([.areas[] | select(.key == \"dropzone\" and .status == \"ok\")] | length) == 1"

check_json "project scaffold defaults into Box Projects" \
  "LOOM_BOX_PATH='$WORKSPACE_BOX' LOOM_BOX_PROFILE=workspace loom --json project scaffold 'Foundation Box Project' --owner-node macbook --preset minimal" \
  ".ok == true and .box_default_used == true and .directory_source == \"box_default\" and .project_root == \"$WORKSPACE_BOX/Projects/foundation-box-project\""

check_remote "explicit project scaffold override remains outside Box" "
  mkdir -p '$EXPLICIT_PARENT'
  LOOM_BOX_PATH='$WORKSPACE_BOX' LOOM_BOX_PROFILE=workspace loom project scaffold 'Explicit Outside Box' --owner-node macbook --directory '$EXPLICIT_PARENT' >/dev/null
  test -f '$EXPLICIT_PARENT/explicit-outside-box/loom.project.yaml'
  test ! -e '$WORKSPACE_BOX/Projects/explicit-outside-box'
"

check_json "watch plan includes Notes and Launchpad only" \
  "loom --json box watch-plan --path '$WORKSPACE_BOX' --profile workspace" \
  "(.watched_roots | length) == 2 and ([.watched_roots[].backend_root_key] | sort) == [\"loom_box__launchpad\", \"loom_box__notes\"] and ([.watched_roots[].backend_root_key] | index(\"loom_box__dropzone\") | not)"

check_json "watch apply records registrations idempotently" \
  "loom --json box watch-apply --path '$WORKSPACE_BOX' --profile workspace" \
  ".ok == true and (.data.registrations | length) == 2 and ([.data.registrations[].backend_root_key] | sort) == [\"loom_box__launchpad\", \"loom_box__notes\"]"

check_json "watch status reports recorded registrations" \
  "loom --json box watch-status --path '$WORKSPACE_BOX' --profile workspace" \
  ".ok == true and (.data.plan.watched_roots | length) == 2 and (.data.registrations | length) == 2"

check_json "main profile keeps Dropzone inactive" \
  "loom --json box init --path '$MAIN_BOX' --profile main" \
  ".profile == \"main\" and .status_after.state == \"ok\" and .status_after.dropzone_state == \"inactive\""

check_remote "portal Box screen renders live Box state" "
  LOOM_BOX_PATH='$WORKSPACE_BOX' LOOM_BOX_PROFILE=workspace loom enter --exit-after-render --start box > '$TMP_DIR/portal-box.txt'
  grep -q 'LOOM Box' '$TMP_DIR/portal-box.txt'
  grep -q 'Dropzone' '$TMP_DIR/portal-box.txt'
  grep -q 'transfer runtime arrives in v0.4.2' '$TMP_DIR/portal-box.txt'
  grep -q 'Watch Policy' '$TMP_DIR/portal-box.txt'
  grep -q 'Create Project In Box' '$TMP_DIR/portal-box.txt'
"

check_remote "portal search can reach the Box surface" "
  LOOM_BOX_PATH='$WORKSPACE_BOX' LOOM_BOX_PROFILE=workspace loom enter --exit-after-render --search box > '$TMP_DIR/portal-search.txt'
  grep -q 'LOOM Box' '$TMP_DIR/portal-search.txt'
"

check_remote "portal Box action preview exposes raw Box command" "
  LOOM_BOX_PATH='$WORKSPACE_BOX' LOOM_BOX_PROFILE=workspace loom enter --exit-after-render --start box --preview-action box.init > '$TMP_DIR/portal-action.txt'
  grep -q 'loom box init' '$TMP_DIR/portal-action.txt'
  grep -q '$WORKSPACE_BOX' '$TMP_DIR/portal-action.txt'
"

log "completed $pass_count checks"
