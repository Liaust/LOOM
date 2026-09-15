#!/usr/bin/env bash
set -euo pipefail

HOST="${LOOM_DEV_HOST:-loom-dev}"
REMOTE_DIR="${LOOM_DEV_REMOTE_DIR:-/srv/loom/current}"
RUN_ID="v0-3-slice-12-$(date +%s)-$RANDOM"
SLUG="smoke-workflow-$RUN_ID"
SCRIPT_SLUG="smoke-workflow-script-$RUN_ID"
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
log "placeholder project slug: $SLUG"
log "script-backed project slug: $SCRIPT_SLUG"

check_remote "SSH target reachable" 'hostname'
check_remote "loomd service active" 'systemctl is-active loomd'
check_remote "loom CLI installed" 'command -v loom && loom version'
check_remote "jq available for smoke assertions" 'command -v jq'

check_json "health reports workflow-era migrations" \
  'loom health --json' \
  '.ok == true and .data.status == "ok" and .data.checks.migrations.current_version >= 33'

TMP_DIR="$(remote "mkdir -p '$REMOTE_DIR/tmp' && mktemp -d '$REMOTE_DIR/tmp/v0-3-slice-12.XXXXXX'")"
trap 'remote "rm -rf '\''$TMP_DIR'\''"' EXIT
PROJECT_ROOT="$TMP_DIR/$SLUG"
SCRIPT_PROJECT_ROOT="$TMP_DIR/$SCRIPT_SLUG"
SCRIPT_CAPABILITY="main@$SCRIPT_SLUG.hello_world"
WORKFLOW_ALIAS="main@$SCRIPT_SLUG.example_workflow"
log "placeholder project root: $PROJECT_ROOT"
log "script-backed project root: $SCRIPT_PROJECT_ROOT"

check_remote "scaffold placeholder workflow project" "
  loom project scaffold 'Smoke Workflow $RUN_ID' \
    --owner-node main \
    --facets workflows \
    --slug '$SLUG' \
    --directory '$TMP_DIR' > '$TMP_DIR/scaffold-placeholder.out'
  chmod a+rx '$TMP_DIR'
  chmod -R a+rX '$PROJECT_ROOT'
  grep -q 'Project scaffold: created' '$TMP_DIR/scaffold-placeholder.out'
"

check_remote "placeholder workflow validates and renders workflow inventory" "
  loom project validate '$PROJECT_ROOT' > '$TMP_DIR/validate-placeholder.out'
  grep -q 'Project contract: ok' '$TMP_DIR/validate-placeholder.out'
  grep -q 'Workflows: 1 discovered' '$TMP_DIR/validate-placeholder.out'
  grep -q 'example_workflow' '$TMP_DIR/validate-placeholder.out'
"

check_json "placeholder workflow plan includes explicit blocked action" \
  "loom project plan '$PROJECT_ROOT' --json" \
  ".registerable == true and
   (.workflows | length) == 1 and
   .workflows[0].workflow_id == \"example_workflow\" and
   .workflows[0].implementation_kind == \"placeholder\" and
   ([.actions[] | select(.action == \"would_validate_workflow_contract\" and .target_ref == \"example_workflow\")] | length) == 1 and
   ([.actions[] | select(.action == \"would_block_first_class_workflow_runtime\" and .target_ref == \"example_workflow\")] | length) == 1"

check_json "register placeholder workflow project with workflow plan" \
  "loom project register '$PROJECT_ROOT' --json" \
  ".ok == true and
   .data.detail.registration.activation_status == \"inactive\" and
   (.data.detail.registration.registration_plan.workflows | length) == 1 and
   .data.detail.registration.registration_plan.workflows[0].implementation_kind == \"placeholder\""

check_json "local workflow list reads placeholder contract" \
  "loom project workflows list '$PROJECT_ROOT' --json" \
  ".source == \"local_analysis\" and
   (.workflows | length) == 1 and
   .workflows[0].workflow_id == \"example_workflow\" and
   .workflows[0].implementation_kind == \"placeholder\""

check_json "registered workflow list reads stored plan" \
  "loom project workflows list '$SLUG' --json" \
  ".source == \"registered_plan\" and
   (.workflows | length) == 1 and
   .workflows[0].workflow_id == \"example_workflow\" and
   .workflows[0].implementation_kind == \"placeholder\""

check_remote "placeholder workflow activation fails with workflow-specific runtime error" "
  if loom project activate '$SLUG' --facet workflows --json > '$TMP_DIR/activate-placeholder.out' 2> '$TMP_DIR/activate-placeholder.err'; then
    cat '$TMP_DIR/activate-placeholder.out'
    exit 1
  fi
  grep -Eq 'first-class workflow runtime is not implemented|workflows facet' '$TMP_DIR/activate-placeholder.out' '$TMP_DIR/activate-placeholder.err'
  if grep -q 'Facet activation is implemented in later v0.3 slices' '$TMP_DIR/activate-placeholder.out' '$TMP_DIR/activate-placeholder.err'; then
    cat '$TMP_DIR/activate-placeholder.out'
    cat '$TMP_DIR/activate-placeholder.err'
    exit 1
  fi
"

check_remote "scaffold script-backed workflow project" "
  loom project scaffold 'Smoke Workflow Script $RUN_ID' \
    --owner-node main \
    --facets scripts,workflows \
    --slug '$SCRIPT_SLUG' \
    --directory '$TMP_DIR' > '$TMP_DIR/scaffold-script.out'
  chmod -R a+rX '$SCRIPT_PROJECT_ROOT'
  grep -q 'Project scaffold: created' '$TMP_DIR/scaffold-script.out'
"

check_remote "convert workflow placeholder to script-backed shim" "
  perl -0pi -e 's/enabled: false/enabled: true/' '$SCRIPT_PROJECT_ROOT/scripts/hello_world/loom.exposure.yaml'
  perl -0pi -e 's/default_mode: wait_until_started/default_mode: wait_for_completion/' '$SCRIPT_PROJECT_ROOT/scripts/hello_world/loom.exposure.yaml'
  perl -0pi -e 's/wait_timeout_seconds: 10/wait_timeout_seconds: 30/' '$SCRIPT_PROJECT_ROOT/scripts/hello_world/loom.exposure.yaml'
  perl -0pi -e 's/implementation:\n  kind: placeholder/implementation:\n  kind: script\n  script_ref: ..\\/..\\/scripts\\/hello_world\\/loom.script.yaml/' '$SCRIPT_PROJECT_ROOT/workflows/example_workflow/loom.workflow.yaml'
"

check_remote "script-backed workflow validates with script shim reference" "
  loom project validate '$SCRIPT_PROJECT_ROOT' > '$TMP_DIR/validate-script.out'
  grep -q 'Project contract: ok' '$TMP_DIR/validate-script.out'
  grep -q 'Scripts: 1 discovered, 1 exposed' '$TMP_DIR/validate-script.out'
  grep -q 'Workflows: 1 discovered, 1 script-backed' '$TMP_DIR/validate-script.out'
  grep -q '$SCRIPT_CAPABILITY' '$TMP_DIR/validate-script.out'
"

check_json "script-backed workflow plan records shim reference" \
  "loom project plan '$SCRIPT_PROJECT_ROOT' --json" \
  ".registerable == true and
   (.workflows | length) == 1 and
   .workflows[0].implementation_kind == \"script\" and
   .workflows[0].script_capability_address == \"$SCRIPT_CAPABILITY\" and
   ([.actions[] | select(.action == \"would_link_script_backed_workflow\" and .target_ref == \"example_workflow\")] | length) == 1"

check_json "register script-backed workflow project" \
  "loom project register '$SCRIPT_PROJECT_ROOT' --json" \
  ".ok == true and
   (.data.detail.registration.registration_plan.workflows | length) == 1 and
   .data.detail.registration.registration_plan.workflows[0].implementation_kind == \"script\""

check_remote "script-backed workflow activation fails before script capability activation" "
  if loom project activate '$SCRIPT_SLUG' --facet workflows > '$TMP_DIR/activate-script-before.out' 2> '$TMP_DIR/activate-script-before.err'; then
    cat '$TMP_DIR/activate-script-before.out'
    exit 1
  fi
  grep -Eq 'script-backed workflow shim is not available|requires active script capability' '$TMP_DIR/activate-script-before.out' '$TMP_DIR/activate-script-before.err'
"

check_remote "activate scripts facet for script-backed workflow project" "
  loom project activate '$SCRIPT_SLUG' --facet scripts > '$TMP_DIR/activate-scripts.out'
  grep -q 'Facet: scripts activated' '$TMP_DIR/activate-scripts.out'
  grep -q '$SCRIPT_CAPABILITY' '$TMP_DIR/activate-scripts.out'
"

check_json "script capability is active before workflow shim activation" \
  "loom capability inspect '$SCRIPT_CAPABILITY' --json" \
  ".ok == true and
   .data.endpoint.compact_address == \"$SCRIPT_CAPABILITY\" and
   .data.runtime_binding.runtime_kind == \"script\" and
   .data.runtime_binding.status == \"active\""

check_remote "activate script-backed workflows facet" "
  loom project activate '$SCRIPT_SLUG' --facet workflows > '$TMP_DIR/activate-workflows.out'
  grep -q 'Activated facets:' '$TMP_DIR/activate-workflows.out'
  grep -q 'workflows' '$TMP_DIR/activate-workflows.out'
  grep -q 'Facet: workflows activated' '$TMP_DIR/activate-workflows.out'
  grep -q 'script-backed' '$TMP_DIR/activate-workflows.out'
  grep -q '$SCRIPT_CAPABILITY' '$TMP_DIR/activate-workflows.out'
"

check_json "project status records activated workflows facet" \
  "loom project status '$SCRIPT_SLUG' --json" \
  ".ok == true and
   (.data.facets[] | select(.facet_key == \"workflows\" and .facet_status == \"activated\")) and
   ([.data.script_exposures[] | select(.script_key == \"hello_world\" and .activation_status == \"active\" and .capability_address == \"$SCRIPT_CAPABILITY\")] | length) == 1"

check_json "registered workflow list shows script implementation" \
  "loom project workflows list '$SCRIPT_SLUG' --json" \
  ".source == \"registered_plan\" and
   (.workflows | length) == 1 and
   .workflows[0].workflow_id == \"example_workflow\" and
   .workflows[0].implementation_kind == \"script\" and
   .workflows[0].script_capability_address == \"$SCRIPT_CAPABILITY\""

check_remote "script-backed workflow shim does not create a workflow endpoint" "
  if loom capability inspect '$WORKFLOW_ALIAS' --json > '$TMP_DIR/workflow-alias.out' 2> '$TMP_DIR/workflow-alias.err'; then
    cat '$TMP_DIR/workflow-alias.out'
    exit 1
  fi
"

log "completed $pass_count checks"
