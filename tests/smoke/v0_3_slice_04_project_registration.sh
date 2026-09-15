#!/usr/bin/env bash
set -euo pipefail

HOST="${LOOM_DEV_HOST:-loom-dev}"
REMOTE_DIR="${LOOM_DEV_REMOTE_DIR:-/srv/loom/current}"
RUN_ID="v0-3-slice-04-$(date +%s)-$RANDOM"
SLUG="smoke-registration-$RUN_ID"
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
check_remote "loomd service active" 'systemctl is-active loomd'
check_remote "loom CLI installed" 'command -v loom && loom version'
check_remote "jq available for smoke assertions" 'command -v jq'

check_json "health reports project registration migration" \
  'loom health --json' \
  '.ok == true and .data.status == "ok" and .data.checks.migrations.current_version >= 27'

TMP_DIR="$(remote "mkdir -p '$REMOTE_DIR/tmp' && mktemp -d '$REMOTE_DIR/tmp/v0-3-slice-04.XXXXXX'")"
trap 'remote "rm -rf '\''$TMP_DIR'\''"' EXIT
PROJECT_ROOT="$TMP_DIR/$SLUG"
log "project root: $PROJECT_ROOT"

check_remote "scaffold registration project" "
  loom project scaffold 'Smoke Registration $RUN_ID' \
    --owner-node main \
    --preset automation \
    --slug '$SLUG' \
    --directory '$TMP_DIR' > '$TMP_DIR/scaffold.out'
  chmod a+rx '$TMP_DIR'
  chmod -R a+rX '$PROJECT_ROOT'
  grep -q 'Project scaffold: created' '$TMP_DIR/scaffold.out'
"

check_remote "project validates before registration" "
  loom project validate '$PROJECT_ROOT' > '$TMP_DIR/validate.out'
  grep -q 'Project contract: ok' '$TMP_DIR/validate.out'
"

check_remote "first registration creates backend project" "
  loom project register '$PROJECT_ROOT' > '$TMP_DIR/register-created.out'
  grep -q 'Project registration: created' '$TMP_DIR/register-created.out'
  grep -q 'Activation: inactive' '$TMP_DIR/register-created.out'
"

check_remote "status reports inactive registration and pending facets" "
  loom project status '$SLUG' > '$TMP_DIR/status-inactive.out'
  grep -q 'Registration: registered' '$TMP_DIR/status-inactive.out'
  grep -q 'Activation: inactive' '$TMP_DIR/status-inactive.out'
  grep -q 'pending_later_slice' '$TMP_DIR/status-inactive.out'
"

check_remote "re-register unchanged project is idempotent" "
  loom project register '$PROJECT_ROOT' > '$TMP_DIR/register-unchanged.out'
  grep -q 'Project registration: unchanged' '$TMP_DIR/register-unchanged.out'
"

check_remote "changed project description re-registers as update" "
  perl -0pi -e 's/description: \"\"/description: Updated by Slice 04 smoke test/' '$PROJECT_ROOT/loom.project.yaml'
  loom project register '$PROJECT_ROOT' > '$TMP_DIR/register-updated.out'
  grep -q 'Project registration: updated' '$TMP_DIR/register-updated.out'
  grep -q 'project.description' '$TMP_DIR/register-updated.out'
"

check_remote "base activation does not activate facets" "
  loom project activate '$SLUG' > '$TMP_DIR/activate.out'
  grep -q 'Project activation: base_active' '$TMP_DIR/activate.out'
  grep -q 'Activated facets: none' '$TMP_DIR/activate.out'
"

check_remote "status reports base activation only" "
  loom project status '$SLUG' > '$TMP_DIR/status-active.out'
  grep -q 'Activation: base_active' '$TMP_DIR/status-active.out'
  grep -q 'Pending facets:' '$TMP_DIR/status-active.out'
"

check_remote "existing project inspect still works" "
  loom project inspect '$SLUG' > '$TMP_DIR/project-inspect.out'
  grep -q 'Project: $SLUG' '$TMP_DIR/project-inspect.out'
"

check_remote "existing project list includes registered project" "
  loom project list > '$TMP_DIR/project-list.out'
  grep -q '$SLUG' '$TMP_DIR/project-list.out'
"

check_remote "scripts facet activation succeeds with disabled default exposure" "
  loom project activate '$SLUG' --facet scripts > '$TMP_DIR/activate-facet.out'
  grep -q 'Activated facets: scripts' '$TMP_DIR/activate-facet.out'
  grep -q 'Script capabilities:' '$TMP_DIR/activate-facet.out'
  grep -q 'disabled' '$TMP_DIR/activate-facet.out'
"

log "completed $pass_count checks"
