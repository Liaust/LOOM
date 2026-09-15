#!/usr/bin/env bash
set -euo pipefail

HOST="${LOOM_DEV_HOST:-loom-dev}"
REMOTE_DIR="${LOOM_DEV_REMOTE_DIR:-/srv/loom/current}"
RUN_ID="v0-4-1-box-init-$(date +%s)-$RANDOM"
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

TMP_DIR="$(remote "mkdir -p '$REMOTE_DIR/tmp' && mktemp -d '$REMOTE_DIR/tmp/v0-4-1-box-init.XXXXXX'")"
cleanup() {
  remote "rm -rf '$TMP_DIR'" || true
}
trap cleanup EXIT

WORKSPACE_BOX="$TMP_DIR/LOOM Box"
MAIN_BOX="$TMP_DIR/Main LOOM Box"
DRY_RUN_BOX="$TMP_DIR/Dry Run Box"

log "workspace Box: $WORKSPACE_BOX"

check_json "read-only status reports missing workspace Box without creating it" \
  "loom --json box status --path '$WORKSPACE_BOX' --profile workspace" \
  ".state == \"missing\" and .initialized == false and .dropzone_state == \"not_initialized\""

check_remote "read-only status did not create workspace Box path" "
  test ! -e '$WORKSPACE_BOX'
"

check_json "workspace Box init creates canonical scaffold" \
  "loom --json box init --path '$WORKSPACE_BOX' --profile workspace" \
  ".dry_run == false and
   .profile == \"workspace\" and
   .status_after.state == \"ok\" and
   .status_after.dropzone_state == \"scaffolded_for_v0.4.2\" and
   (.created_dirs | length) >= 9 and
   (.created_files | length) >= 10"

check_remote "workspace Box folders, contract, docs, and policies exist" "
  test -d '$WORKSPACE_BOX/Projects'
  test -d '$WORKSPACE_BOX/Notes'
  test -d '$WORKSPACE_BOX/Launchpad'
  test -d '$WORKSPACE_BOX/Dropzone'
  test -d '$WORKSPACE_BOX/.loom/policies'
  test -d '$WORKSPACE_BOX/.loom/state'
  test -d '$WORKSPACE_BOX/.loom/state/dropzone'
  test -f '$WORKSPACE_BOX/README.md'
  test -f '$WORKSPACE_BOX/.loom/README.md'
  test -f '$WORKSPACE_BOX/Projects/README.md'
  test -f '$WORKSPACE_BOX/Notes/README.md'
  test -f '$WORKSPACE_BOX/Launchpad/README.md'
  test -f '$WORKSPACE_BOX/Dropzone/README.md'
  test -f '$WORKSPACE_BOX/.loom/box.yaml'
  test -f '$WORKSPACE_BOX/.loom/policies/notes.watch.yaml'
  test -f '$WORKSPACE_BOX/.loom/policies/launchpad.watch.yaml'
  test -f '$WORKSPACE_BOX/.loom/policies/dropzone.transfer.yaml'
"

check_remote "workspace Box contract and policy stubs declare v0.4.1 semantics" "
  grep -q 'schema_version: loom.box.v0.4.1' '$WORKSPACE_BOX/.loom/box.yaml'
  grep -q 'profile: workspace' '$WORKSPACE_BOX/.loom/box.yaml'
  grep -q 'default_project_path: Projects' '$WORKSPACE_BOX/.loom/box.yaml'
  grep -q 'transfer_status: scaffolded_for_v0.4.2' '$WORKSPACE_BOX/.loom/box.yaml'
  grep -q 'mode: mirror_to_main' '$WORKSPACE_BOX/.loom/policies/notes.watch.yaml'
  grep -q 'mode: workspace_to_main' '$WORKSPACE_BOX/.loom/policies/launchpad.watch.yaml'
  grep -q 'mode: custody_transfer' '$WORKSPACE_BOX/.loom/policies/dropzone.transfer.yaml'
  grep -q 'enabled: false' '$WORKSPACE_BOX/.loom/policies/dropzone.transfer.yaml'
"

check_json "workspace Box status reports complete scaffold after init" \
  "loom --json box status --path '$WORKSPACE_BOX' --profile workspace" \
  ".state == \"ok\" and
   .initialized == true and
   .contract_state == \"valid\" and
   .dropzone_state == \"scaffolded_for_v0.4.2\" and
   ([.areas[] | select(.status != \"ok\")] | length) == 0 and
   ([.policies[] | select(.status != \"ok\")] | length) == 0"

check_json "workspace Box init is idempotent on second run" \
  "loom --json box init --path '$WORKSPACE_BOX' --profile workspace" \
  ".status_after.state == \"ok\" and
   (.created_dirs | length) == 0 and
   (.created_files | length) == 0 and
   (.skipped_dirs | length) >= 9 and
   (.skipped_files | length) >= 10"

check_remote "workspace Box init preserves existing user docs" "
  printf 'custom root readme\n' > '$WORKSPACE_BOX/README.md'
  loom box init --path '$WORKSPACE_BOX' --profile workspace >/dev/null
  grep -qx 'custom root readme' '$WORKSPACE_BOX/README.md'
"

check_remote "workspace Box Notes and Launchpad accept local intake files" "
  printf '# Slice 04 note\n\nwatched root note\n' > '$WORKSPACE_BOX/Notes/slice-04-note.md'
  printf 'launchpad intake\n' > '$WORKSPACE_BOX/Launchpad/slice-04-intake.txt'
"

check_json "Box watch plan maps Notes and Launchpad only" \
  "loom --json box watch-plan --path '$WORKSPACE_BOX' --profile workspace" \
  ".schema_version == \"loom.box.v0.4.1\" and
   (.watched_roots | length) == 2 and
   ([.watched_roots[].backend_root_key] | sort) == [\"loom_box__launchpad\", \"loom_box__notes\"] and
   ([.watched_roots[].backend_root_key] | index(\"loom_box__dropzone\") | not) and
   ([.watched_roots[].backend_root_key] | index(\"loom_box__projects\") | not)"

check_json "Box watch apply dry-run returns desired roots without writing" \
  "loom --json box watch-apply --dry-run --path '$WORKSPACE_BOX' --profile workspace" \
  ".ok == true and
   .data.dry_run == true and
   (.data.plan.watched_roots | length) == 2 and
   (.data.registrations == null or (.data.registrations | length) == 0)"

check_json "Box watch apply records Notes and Launchpad registrations" \
  "loom --json box watch-apply --path '$WORKSPACE_BOX' --profile workspace" \
  ".ok == true and
   .data.dry_run == false and
   (.data.registrations | length) == 2 and
   ([.data.registrations[].backend_root_key] | sort) == [\"loom_box__launchpad\", \"loom_box__notes\"] and
   ([.data.registrations[] | select(.activation_status == \"pending_agent_apply\" or .activation_status == \"reported\" or .activation_status == \"stale\")] | length) == 2"

check_json "Box watch apply is idempotent on second run" \
  "loom --json box watch-apply --path '$WORKSPACE_BOX' --profile workspace" \
  ".ok == true and
   (.data.registrations | length) == 2 and
   ([.data.registrations[].backend_root_key] | sort) == [\"loom_box__launchpad\", \"loom_box__notes\"]"

check_json "Box watch status reports recorded registrations" \
  "loom --json box watch-status --path '$WORKSPACE_BOX' --profile workspace" \
  ".ok == true and
   (.data.plan.watched_roots | length) == 2 and
   (.data.registrations | length) == 2 and
   ([.data.registrations[].backend_root_key] | sort) == [\"loom_box__launchpad\", \"loom_box__notes\"]"

BOX_PROJECT_ROOT="$WORKSPACE_BOX/Projects/box-smoke-project"
EXPLICIT_PROJECT_PARENT="$TMP_DIR/explicit-projects"
EXPLICIT_PROJECT_ROOT="$EXPLICIT_PROJECT_PARENT/explicit-outside-box"

check_remote "project scaffold defaults into Box Projects when --directory is omitted" "
  LOOM_BOX_PATH='$WORKSPACE_BOX' LOOM_BOX_PROFILE=workspace \
    loom project scaffold 'Box Smoke Project' --owner-node macbook --preset minimal > '$TMP_DIR/box-project.out'
  grep -q 'Project scaffold: created' '$TMP_DIR/box-project.out'
  grep -q 'Box: $WORKSPACE_BOX' '$TMP_DIR/box-project.out'
  grep -q 'Directory source: box_default' '$TMP_DIR/box-project.out'
  test -f '$BOX_PROJECT_ROOT/loom.project.yaml'
  loom project validate '$BOX_PROJECT_ROOT' > '$TMP_DIR/box-project-validate.out'
  grep -q 'Project contract: ok' '$TMP_DIR/box-project-validate.out'
"

check_json "project scaffold JSON reports Box default metadata" \
  "LOOM_BOX_PATH='$WORKSPACE_BOX' LOOM_BOX_PROFILE=workspace loom --json project scaffold 'Box JSON Project' --owner-node macbook --preset minimal" \
  ".ok == true and
   .slug == \"box-json-project\" and
   .box_default_used == true and
   .directory_source == \"box_default\" and
   .box_root == \"$WORKSPACE_BOX\" and
   .project_root == \"$WORKSPACE_BOX/Projects/box-json-project\""

check_remote "explicit project scaffold directory still overrides Box default" "
  mkdir -p '$EXPLICIT_PROJECT_PARENT'
  LOOM_BOX_PATH='$WORKSPACE_BOX' LOOM_BOX_PROFILE=workspace \
    loom project scaffold 'Explicit Outside Box' --owner-node macbook --directory '$EXPLICIT_PROJECT_PARENT' > '$TMP_DIR/explicit-project.out'
  grep -q 'Project scaffold: created' '$TMP_DIR/explicit-project.out'
  grep -q 'Directory source: explicit_directory' '$TMP_DIR/explicit-project.out'
  test -f '$EXPLICIT_PROJECT_ROOT/loom.project.yaml'
  test ! -e '$WORKSPACE_BOX/Projects/explicit-outside-box'
"

check_json "main Box init creates a Box with inactive Dropzone transfer" \
  "loom --json box init --path '$MAIN_BOX' --profile main" \
  ".profile == \"main\" and
   .status_after.state == \"ok\" and
   .status_after.dropzone_state == \"inactive\""

check_json "Box init dry-run plans files without writing" \
  "loom --json box init --dry-run --path '$DRY_RUN_BOX' --profile workspace" \
  ".dry_run == true and
   .status_after.state == \"missing\" and
   (.planned_dirs | length) >= 9 and
   (.planned_files | length) >= 10"

check_remote "Box init dry-run did not create target path" "
  test ! -e '$DRY_RUN_BOX'
"

log "completed $pass_count checks"
