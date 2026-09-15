#!/usr/bin/env bash
set -euo pipefail

HOST="${LOOM_DEV_HOST:-loom-dev}"
REMOTE_DIR="${LOOM_DEV_REMOTE_DIR:-/srv/loom/current}"
RUN_ID="v0-3-slice-05-$(date +%s)-$RANDOM"
SLUG="smoke-script-$RUN_ID"
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

check_remote "SSH target reachable" 'hostname'
check_remote "loomd service active" 'systemctl is-active loomd'
check_remote "loom CLI installed" 'command -v loom && loom version'
check_remote "jq available for smoke assertions" 'command -v jq'

check_json "health reports project script exposure migration" \
  'loom health --json' \
  '.ok == true and .data.status == "ok" and .data.checks.migrations.current_version >= 28'

TMP_DIR="$(remote "mkdir -p '$REMOTE_DIR/tmp' && mktemp -d '$REMOTE_DIR/tmp/v0-3-slice-05.XXXXXX'")"
trap 'remote "rm -rf '\''$TMP_DIR'\''"' EXIT
PROJECT_ROOT="$TMP_DIR/$SLUG"
CAPABILITY="main@$SLUG.hello_world"
log "project root: $PROJECT_ROOT"
log "capability: $CAPABILITY"

check_remote "scaffold script project" "
  loom project scaffold 'Smoke Script $RUN_ID' \
    --owner-node main \
    --preset automation \
    --slug '$SLUG' \
    --directory '$TMP_DIR' > '$TMP_DIR/scaffold.out'
  chmod a+rx '$TMP_DIR'
  chmod -R a+rX '$PROJECT_ROOT'
  grep -q 'Project scaffold: created' '$TMP_DIR/scaffold.out'
"

check_remote "enable script exposure and completion wait mode" "
  perl -0pi -e 's/enabled: false/enabled: true/' '$PROJECT_ROOT/scripts/hello_world/loom.exposure.yaml'
  perl -0pi -e 's/default_mode: wait_until_started/default_mode: wait_for_completion/' '$PROJECT_ROOT/scripts/hello_world/loom.exposure.yaml'
  perl -0pi -e 's/wait_timeout_seconds: 10/wait_timeout_seconds: 30/' '$PROJECT_ROOT/scripts/hello_world/loom.exposure.yaml'
"

check_remote "project validates with enabled script exposure" "
  loom project validate '$PROJECT_ROOT' > '$TMP_DIR/validate.out'
  grep -q 'Project contract: ok' '$TMP_DIR/validate.out'
  grep -q 'Scripts: 1 discovered, 1 exposed' '$TMP_DIR/validate.out'
  grep -q '$CAPABILITY' '$TMP_DIR/validate.out'
"

check_remote "register script project without activating behavior" "
  loom project register '$PROJECT_ROOT' > '$TMP_DIR/register.out'
  grep -q 'Project registration: created' '$TMP_DIR/register.out'
  grep -q 'Activation: inactive' '$TMP_DIR/register.out'
"

check_json "registered status has no script exposure rows before activation" \
  "loom project status '$SLUG' --json" \
  '.ok == true and (.data.script_exposures | length) == 0'

check_remote "generated capability is unavailable before scripts activation" "
  if loom capability inspect '$CAPABILITY' > '$TMP_DIR/pre-activation-capability.out' 2>&1; then
    cat '$TMP_DIR/pre-activation-capability.out'
    exit 1
  fi
"

check_remote "activate scripts facet" "
  loom project activate '$SLUG' --facet scripts > '$TMP_DIR/activate-scripts.out'
  grep -q 'Project activation: base_active' '$TMP_DIR/activate-scripts.out'
  grep -q 'Activated facets: scripts' '$TMP_DIR/activate-scripts.out'
  grep -q 'Facet: scripts activated' '$TMP_DIR/activate-scripts.out'
  grep -q 'Script capabilities:' '$TMP_DIR/activate-scripts.out'
  grep -q '$CAPABILITY' '$TMP_DIR/activate-scripts.out'
"

check_json "project status records active script exposure" \
  "loom project status '$SLUG' --json" \
  ".ok == true and
   (.data.facets[] | select(.facet_key == \"scripts\" and .facet_status == \"activated\")) and
   ([.data.script_exposures[] | select(.script_key == \"hello_world\" and .activation_status == \"active\" and .capability_address == \"$CAPABILITY\")] | length) == 1"

check_json "generated capability is inspectable with script runtime binding" \
  "loom capability inspect '$CAPABILITY' --json" \
  ".ok == true and
   .data.endpoint.compact_address == \"$CAPABILITY\" and
   .data.endpoint.status == \"active\" and
   .data.provider.compact_address == \"main@$SLUG\" and
   .data.provider_health.health_status == \"ok\" and
   .data.runtime_binding.runtime_kind == \"script\" and
   .data.runtime_binding.status == \"active\""

check_remote "capability inspect renders script runtime metadata" "
  loom capability inspect '$CAPABILITY' > '$TMP_DIR/capability-inspect.out'
  grep -q 'Runtime: script active' '$TMP_DIR/capability-inspect.out'
  grep -q 'Script: hello_world' '$TMP_DIR/capability-inspect.out'
  grep -q 'Project: $SLUG' '$TMP_DIR/capability-inspect.out'
"

check_json "runtime binding inspect resolves generated capability" \
  "loom capability runtime-binding inspect '$CAPABILITY' --json" \
  ".ok == true and
   .data.binding.runtime_kind == \"script\" and
   .data.binding.status == \"active\" and
   .data.endpoint.compact_address == \"$CAPABILITY\""

check_json "provider list includes active project script provider" \
  "loom providers list --project '$SLUG' --json" \
  ".ok == true and ([.data[] | select(.compact_address == \"main@$SLUG\" and .status == \"active\" and .health_status == \"ok\")] | length) == 1"

check_json "calling project script capability creates completed job" \
  "loom capability call '$CAPABILITY' --input '{\"message\":\"hello\"}' --wait --timeout-seconds 45 --json" \
  ".ok == true and
   .data.status == \"completed\" and
   .data.route.status == \"completed\" and
   .data.capability_call.status == \"completed\" and
   (.data.job_id | startswith(\"job_\")) and
   .data.result.job.job.status == \"completed\" and
   .data.result_refs.job_id == .data.job_id"

JOB_ID="$(json_value "loom capability call '$CAPABILITY' --input '{\"message\":\"again\"}' --wait --timeout-seconds 45 --json" '.data.job_id')"
[[ "$JOB_ID" == job_* ]] || fail "expected project script job id"
pass "captured project script job id"

check_json "jobs list by project includes project script job" \
  "loom jobs list --project '$SLUG' --json" \
  ".ok == true and ([.data[].job_id] | index(\"$JOB_ID\"))"

check_json "capability calls list includes generated capability" \
  "loom capability-calls list --capability '$CAPABILITY' --json" \
  ".ok == true and ([.data[] | select(.job_id == \"$JOB_ID\" and .status == \"completed\")] | length) >= 1"

check_json "routes list includes completed generated capability route" \
  "loom routes list --capability '$CAPABILITY' --json" \
  ".ok == true and ([.data[] | select(.job_id == \"$JOB_ID\" and .status == \"completed\")] | length) >= 1"

check_remote "re-activating scripts facet is idempotent" "
  loom project activate '$SLUG' --facet scripts > '$TMP_DIR/reactivate-scripts.out'
  grep -q 'Activated facets: scripts' '$TMP_DIR/reactivate-scripts.out'
  grep -q '$CAPABILITY' '$TMP_DIR/reactivate-scripts.out'
"

check_json "reactivation keeps one script exposure row" \
  "loom project status '$SLUG' --json" \
  ".ok == true and ([.data.script_exposures[] | select(.script_key == \"hello_world\" and .capability_address == \"$CAPABILITY\")] | length) == 1"

log "completed $pass_count checks"
