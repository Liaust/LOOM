#!/usr/bin/env bash
set -euo pipefail

HOST="${LOOM_DEV_HOST:-loom-dev}"
REMOTE_DIR="${LOOM_DEV_REMOTE_DIR:-/srv/loom/current}"
RUN_ID="v0-3-slice-06-$(date +%s)-$RANDOM"
SLUG="smoke-schedule-$RUN_ID"
BACKEND_KEY="${SLUG//-/_}__example_schedule"
CAPABILITY="main@$SLUG.hello_world"
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

drive_invocation_succeeded() {
  local invocation_id="$1"
  local output_file="$2"
  local key_prefix="$3"

  remote "for i in \$(seq 1 60); do
    loom invocation inspect '$invocation_id' --json > '$output_file'
    status=\$(jq -r '.data.status' '$output_file')
    if [ \"\$status\" = succeeded ]; then exit 0; fi
    case \"\$status\" in
      failed|timed_out|requires_manual_action|cancelled)
        cat '$output_file' >&2
        exit 1
        ;;
    esac
    loom worker run main.automation_dispatcher --once --json --idempotency-key '${key_prefix}_'\$i > '/tmp/${key_prefix}_dispatcher_'\$i'.json' 2> '/tmp/${key_prefix}_dispatcher_'\$i'.err' || true
    sleep 1
  done
  cat '$output_file' >&2
  exit 1"
  pass "manual project schedule invocation succeeded"
}

log "target: $HOST"
log "remote dir: $REMOTE_DIR"
log "run id: $RUN_ID"
log "backend schedule key: $BACKEND_KEY"

check_remote "SSH target reachable" 'hostname'
check_remote "loomd service active" 'systemctl is-active loomd'
check_remote "loom CLI installed" 'command -v loom && loom version'
check_remote "jq available for smoke assertions" 'command -v jq'

check_json "health reports project schedule migration" \
  'loom health --json' \
  '.ok == true and .data.status == "ok" and .data.checks.migrations.current_version >= 29'

TMP_DIR="$(remote "mkdir -p '$REMOTE_DIR/tmp' && mktemp -d '$REMOTE_DIR/tmp/v0-3-slice-06.XXXXXX'")"
trap 'remote "rm -rf '\''$TMP_DIR'\''"' EXIT
PROJECT_ROOT="$TMP_DIR/$SLUG"
log "project root: $PROJECT_ROOT"
log "capability: $CAPABILITY"

check_remote "scaffold automation project with schedule facet" "
  loom project scaffold 'Smoke Schedule $RUN_ID' \
    --owner-node main \
    --preset automation \
    --slug '$SLUG' \
    --directory '$TMP_DIR' > '$TMP_DIR/scaffold.out'
  chmod a+rx '$TMP_DIR'
  chmod -R a+rX '$PROJECT_ROOT'
  grep -q 'Project scaffold: created' '$TMP_DIR/scaffold.out'
"

check_remote "enable script exposure for scheduled target" "
  perl -0pi -e 's/enabled: false/enabled: true/' '$PROJECT_ROOT/scripts/hello_world/loom.exposure.yaml'
  perl -0pi -e 's/default_mode: wait_until_started/default_mode: wait_for_completion/' '$PROJECT_ROOT/scripts/hello_world/loom.exposure.yaml'
  perl -0pi -e 's/wait_timeout_seconds: 10/wait_timeout_seconds: 30/' '$PROJECT_ROOT/scripts/hello_world/loom.exposure.yaml'
"

check_remote "project validates with discovered schedule" "
  loom project validate '$PROJECT_ROOT' > '$TMP_DIR/validate.out'
  grep -q 'Project contract: ok' '$TMP_DIR/validate.out'
  grep -q 'Scripts: 1 discovered, 1 exposed' '$TMP_DIR/validate.out'
  grep -q 'Schedules: 1 discovered' '$TMP_DIR/validate.out'
  grep -q 'example_schedule -> $CAPABILITY interval 24h pending_later_slice' '$TMP_DIR/validate.out'
"

check_json "project plan JSON includes schedule facet item" \
  "loom project plan '$PROJECT_ROOT' --json" \
  ".registerable == true and
   ([.schedules[] | select(.key == \"example_schedule\" and .backend_schedule_key == \"$BACKEND_KEY\" and .target_capability == \"$CAPABILITY\")] | length) == 1 and
   ([.actions[] | select(.action == \"would_pause_project_schedule\" and .target_ref == \"$BACKEND_KEY\")] | length) == 1"

check_remote "register schedule project without activating behavior" "
  loom project register '$PROJECT_ROOT' > '$TMP_DIR/register.out'
  grep -q 'Project registration: created' '$TMP_DIR/register.out'
  grep -q 'Activation: inactive' '$TMP_DIR/register.out'
"

check_remote "schedule activation fails before target capability exists" "
  if loom project activate '$SLUG' --facet schedules > '$TMP_DIR/activate-schedules-before-scripts.out' 2>&1; then
    cat '$TMP_DIR/activate-schedules-before-scripts.out'
    exit 1
  fi
  grep -q 'target capability is not active or does not exist' '$TMP_DIR/activate-schedules-before-scripts.out'
"

check_remote "activate scripts facet for schedule target" "
  loom project activate '$SLUG' --facet scripts > '$TMP_DIR/activate-scripts.out'
  grep -q 'Activated facets: scripts' '$TMP_DIR/activate-scripts.out'
  grep -q '$CAPABILITY' '$TMP_DIR/activate-scripts.out'
"

check_remote "activate schedules facet as paused automation schedule" "
  loom project activate '$SLUG' --facet schedules > '$TMP_DIR/activate-schedules.out'
  grep -q 'Activated facets: schedules' '$TMP_DIR/activate-schedules.out'
  grep -q 'Facet: schedules activated' '$TMP_DIR/activate-schedules.out'
  grep -q '$BACKEND_KEY' '$TMP_DIR/activate-schedules.out'
  grep -q 'paused' '$TMP_DIR/activate-schedules.out'
"

check_json "project status records paused schedule registration" \
  "loom project status '$SLUG' --json" \
  ".ok == true and
   (.data.facets[] | select(.facet_key == \"schedules\" and .facet_status == \"activated\")) and
   ([.data.schedule_registrations[] | select(.schedule_key == \"example_schedule\" and .backend_schedule_key == \"$BACKEND_KEY\" and .activation_status == \"paused\" and .target_capability == \"$CAPABILITY\")] | length) == 1"

check_json "schedules list can filter by project" \
  "loom schedules list --project '$SLUG' --json" \
  ".ok == true and
   ([.data[] | select(.schedule_key == \"$BACKEND_KEY\" and .status == \"paused\" and .schedule_kind == \"interval\" and .schedule_expr == \"24h\")] | length) == 1"

check_remote "schedule inspect renders paused backend schedule" "
  loom schedule inspect '$BACKEND_KEY' > '$TMP_DIR/schedule-inspect.out'
  grep -q 'Schedule: $BACKEND_KEY' '$TMP_DIR/schedule-inspect.out'
  grep -q 'Status: paused' '$TMP_DIR/schedule-inspect.out'
"

FIRE_JSON="$TMP_DIR/fire.json"
INVOCATION_JSON="$TMP_DIR/invocation.json"
check_remote "manual fire is allowed for paused project schedule" "
  loom schedule fire '$BACKEND_KEY' --reason 'slice 06 smoke manual fire' --json > '$FIRE_JSON'
  jq -e '.ok == true and .data.schedule.schedule_key == \"$BACKEND_KEY\" and .data.schedule.status == \"paused\" and (.data.invocation.invocation_id | startswith(\"invocation_\"))' '$FIRE_JSON' >/dev/null
"

INVOCATION_ID="$(json_value "cat '$FIRE_JSON'" '.data.invocation.invocation_id')"
[[ "$INVOCATION_ID" == invocation_* ]] || fail "expected invocation id"
pass "captured manual schedule invocation id"

drive_invocation_succeeded "$INVOCATION_ID" "$INVOCATION_JSON" "smoke_v0_3_slice_06_drive_$RUN_ID"

check_json "completed invocation keeps schedule project context" \
  "loom invocation inspect '$INVOCATION_ID' --json" \
  ".ok == true and
   .data.status == \"succeeded\" and
   .data.source_kind == \"schedule\" and
   .data.target_capability == \"$CAPABILITY\" and
   (.data.project_id | startswith(\"project_\"))"

check_remote "re-activating schedules facet is idempotent" "
  loom project activate '$SLUG' --facet schedules > '$TMP_DIR/reactivate-schedules.out'
  grep -q 'Activated facets:' '$TMP_DIR/reactivate-schedules.out'
  grep -q '$BACKEND_KEY' '$TMP_DIR/reactivate-schedules.out'
"

check_json "reactivation keeps one project schedule registration" \
  "loom project status '$SLUG' --json" \
  ".ok == true and ([.data.schedule_registrations[] | select(.schedule_key == \"example_schedule\" and .backend_schedule_key == \"$BACKEND_KEY\")] | length) == 1"

log "completed $pass_count checks"
