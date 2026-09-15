#!/usr/bin/env bash
set -euo pipefail

HOST="${LOOM_DEV_HOST:-loom-dev}"
REMOTE_DIR="${LOOM_DEV_REMOTE_DIR:-/srv/loom/current}"
RUN_ID="v0-3-slice-11-$(date +%s)-$RANDOM"
SLUG="smoke-module-$RUN_ID"
MODULE_ID="loom.$SLUG"
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

json_value() {
  local command="$1"
  local filter="$2"

  remote "$command | jq -r '$filter'"
}

log "target: $HOST"
log "remote dir: $REMOTE_DIR"
log "run id: $RUN_ID"
log "project slug: $SLUG"
log "module id: $MODULE_ID"

check_remote "SSH target reachable" 'hostname'
check_remote "loomd service active" 'systemctl is-active loomd'
check_remote "loom CLI installed" 'command -v loom && loom version'
check_remote "jq available for smoke assertions" 'command -v jq'

check_json "health reports project module migration" \
  'loom health --json' \
  '.ok == true and .data.status == "ok" and .data.checks.migrations.current_version >= 33'

TMP_DIR="$(remote "mkdir -p '$REMOTE_DIR/tmp' && mktemp -d '$REMOTE_DIR/tmp/v0-3-slice-11.XXXXXX'")"
trap 'remote "rm -rf '\''$TMP_DIR'\''"' EXIT
PROJECT_ROOT="$TMP_DIR/$SLUG"
log "project root: $PROJECT_ROOT"

check_remote "scaffold module project" "
  loom project scaffold 'Smoke Module $RUN_ID' \
    --owner-node main \
    --preset module \
    --slug '$SLUG' \
    --directory '$TMP_DIR' > '$TMP_DIR/scaffold.out'
  chmod a+rx '$TMP_DIR'
  chmod -R a+rX '$PROJECT_ROOT'
  grep -q 'Project scaffold: created' '$TMP_DIR/scaffold.out'
"

check_remote "project validates with module facet" "
  loom project validate '$PROJECT_ROOT' > '$TMP_DIR/validate.out'
  grep -q 'Project contract: ok' '$TMP_DIR/validate.out'
  grep -q 'Modules: 1 discovered, 1 registrable' '$TMP_DIR/validate.out'
  grep -q '$MODULE_ID' '$TMP_DIR/validate.out'
"

check_json "project module plan includes registration actions" \
  "loom project plan '$PROJECT_ROOT' --json" \
  ".registerable == true and
   (.modules | length) == 1 and
   .modules[0].module_id == \"$MODULE_ID\" and
   .modules[0].registration_enabled == true and
   .modules[0].install_plan.plan == \"explicit_only\" and
   .modules[0].exposure_plan.plan == \"explicit_only\" and
   ([.actions[] | select(.action == \"would_register_module_package\" and .target_ref == \"$MODULE_ID\")] | length) == 1 and
   ([.actions[] | select(.action == \"would_record_project_module_registration\" and .target_ref == \"$MODULE_ID\")] | length) == 1"

check_remote "register module project without activating module package" "
  loom project register '$PROJECT_ROOT' > '$TMP_DIR/register.out'
  grep -q 'Project registration: created' '$TMP_DIR/register.out'
  grep -q 'Activation: inactive' '$TMP_DIR/register.out'
"

check_json "registered status has no module rows before activation" \
  "loom project status '$SLUG' --json" \
  '.ok == true and ((.data.module_registrations // []) | length) == 0'

check_json "project-owned module not listed before activation" \
  "loom modules list --project '$SLUG' --json" \
  ".ok == true and ([.data[] | select(.module_id == \"$MODULE_ID\")] | length) == 0"

check_remote "activate modules facet" "
  loom project activate '$SLUG' --facet modules > '$TMP_DIR/activate-modules.out'
  grep -q 'Project activation: base_active' '$TMP_DIR/activate-modules.out'
  grep -q 'Activated facets: modules' '$TMP_DIR/activate-modules.out'
  grep -q 'Facet: modules activated' '$TMP_DIR/activate-modules.out'
  grep -q 'Modules: 1 registered' '$TMP_DIR/activate-modules.out'
  grep -q '$MODULE_ID' '$TMP_DIR/activate-modules.out'
"

check_json "project status records registered module" \
  "loom project status '$SLUG' --json" \
  ".ok == true and
   (.data.facets[] | select(.facet_key == \"modules\" and .facet_status == \"activated\")) and
   ([.data.module_registrations[] | select(.module_id == \"$MODULE_ID\" and .activation_status == \"registered\" and .registration_enabled == true and (.module_version_id | startswith(\"module_version_\")))] | length) == 1"

check_json "modules list by project returns project module" \
  "loom modules list --project '$SLUG' --json" \
  ".ok == true and ([.data[] | select(.module_id == \"$MODULE_ID\" and .status == \"valid\")] | length) == 1"

check_json "module inspect returns registered package declarations" \
  "loom module inspect '$MODULE_ID' --json" \
  ".ok == true and .data.module.module_id == \"$MODULE_ID\" and .data.latest_version.module_id == \"$MODULE_ID\""

MODULE_VERSION_ID="$(json_value "loom project status '$SLUG' --json" '.data.module_registrations[0].module_version_id')"
[[ "$MODULE_VERSION_ID" == module_version_* ]] || fail "expected module version id"
pass "captured project module version id"

check_json "module version inspect works from project registration" \
  "loom module version inspect '$MODULE_VERSION_ID' --json" \
  ".ok == true and .data.version.module_version_id == \"$MODULE_VERSION_ID\" and .data.version.module_id == \"$MODULE_ID\""

remote "loom module install '$MODULE_VERSION_ID' --node main --json > '$TMP_DIR/install-module.json'"
INSTALLATION_ID="$(json_value "cat '$TMP_DIR/install-module.json'" '.data.installation.module_installation_id')"
[[ "$INSTALLATION_ID" == module_installation_* ]] || fail "expected module installation id"
pass "explicit module install remains a direct module command"

check_json "explicit module install succeeds after project registration" \
  "cat '$TMP_DIR/install-module.json'" \
  ".ok == true and
   .data.installation.module_version_id == \"$MODULE_VERSION_ID\" and
   (.data.installation.status == \"installed\" or .data.installation.status == \"enabled\" or .data.installation.status == \"disabled\")"

check_remote "re-activating modules facet is idempotent" "
  loom project activate '$SLUG' --facet modules > '$TMP_DIR/reactivate-modules.out'
  grep -q 'Activated facets: modules' '$TMP_DIR/reactivate-modules.out'
  grep -q '$MODULE_ID' '$TMP_DIR/reactivate-modules.out'
"

check_json "reactivation keeps one project module row" \
  "loom project status '$SLUG' --json" \
  ".ok == true and ([.data.module_registrations[] | select(.module_key == \"example_module\" and .module_id == \"$MODULE_ID\" and .activation_status == \"registered\")] | length) == 1"

check_json "reactivation keeps one project module list item" \
  "loom modules list --project '$SLUG' --json" \
  ".ok == true and ([.data[] | select(.module_id == \"$MODULE_ID\")] | length) == 1"

log "completed $pass_count checks"
