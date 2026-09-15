#!/usr/bin/env bash
set -euo pipefail

HOST="${LOOM_DEV_HOST:-loom-dev}"
REMOTE_DIR="${LOOM_DEV_REMOTE_DIR:-/srv/loom/current}"
RUN_ID="v0-3-slice-07-$(date +%s)-$RANDOM"
SLUG="smoke-event-$RUN_ID"
BACKEND_ENDPOINT="${SLUG//-/_}__example_event"
BACKEND_INTEGRATION="${SLUG//-/_}__example_integration"
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

drive_direct_event_succeeded() {
  local direct_event_id="$1"
  local output_file="$2"
  local key_prefix="$3"

  remote "for i in \$(seq 1 80); do
    loom worker run main.direct_event_ingest --once --json --idempotency-key '${key_prefix}_ingest_'\$i > '/tmp/${key_prefix}_ingest_'\$i'.json' 2> '/tmp/${key_prefix}_ingest_'\$i'.err' || true
    loom direct-event inspect '$direct_event_id' --json > '$output_file'
    status=\$(jq -r '.data.direct_event.status' '$output_file')
    case \"\$status\" in
      completed)
        invocation_id=\$(jq -r '.data.direct_event.invocation_id // empty' '$output_file')
        if [ -n \"\$invocation_id\" ]; then
          loom worker run main.automation_dispatcher --once --json --idempotency-key '${key_prefix}_dispatcher_final_'\$i > '/tmp/${key_prefix}_dispatcher_final_'\$i'.json' 2> '/tmp/${key_prefix}_dispatcher_final_'\$i'.err' || true
          exit 0
        fi
        ;;
      invocation_created|mapped|accepted|authenticated)
        loom worker run main.automation_dispatcher --once --json --idempotency-key '${key_prefix}_dispatcher_'\$i > '/tmp/${key_prefix}_dispatcher_'\$i'.json' 2> '/tmp/${key_prefix}_dispatcher_'\$i'.err' || true
        ;;
      rejected|mapping_failed|failed|timed_out)
        cat '$output_file' >&2
        exit 1
        ;;
    esac
    sleep 1
  done
  cat '$output_file' >&2
  exit 1"
  pass "direct event reached completed status"
}

log "target: $HOST"
log "remote dir: $REMOTE_DIR"
log "run id: $RUN_ID"
log "backend endpoint: $BACKEND_ENDPOINT"

check_remote "SSH target reachable" 'hostname'
check_remote "loomd service active" 'systemctl is-active loomd'
check_remote "loom CLI installed" 'command -v loom && loom version'
check_remote "jq available for smoke assertions" 'command -v jq'

check_json "health reports project direct-event migration" \
  'loom health --json' \
  '.ok == true and .data.status == "ok" and .data.checks.migrations.current_version >= 30'

TMP_DIR="$(remote "mkdir -p '$REMOTE_DIR/tmp' && mktemp -d '$REMOTE_DIR/tmp/v0-3-slice-07.XXXXXX'")"
trap 'remote "rm -rf '\''$TMP_DIR'\''"' EXIT
PROJECT_ROOT="$TMP_DIR/$SLUG"
log "project root: $PROJECT_ROOT"
log "capability: $CAPABILITY"

check_remote "scaffold automation project with direct-event facet" "
  loom project scaffold 'Smoke Event $RUN_ID' \
    --owner-node main \
    --preset automation \
    --slug '$SLUG' \
    --directory '$TMP_DIR' > '$TMP_DIR/scaffold.out'
  chmod a+rx '$TMP_DIR'
  chmod -R a+rX '$PROJECT_ROOT'
  grep -q 'Project scaffold: created' '$TMP_DIR/scaffold.out'
"

check_remote "enable script exposure for direct-event target" "
  perl -0pi -e 's/enabled: false/enabled: true/' '$PROJECT_ROOT/scripts/hello_world/loom.exposure.yaml'
  perl -0pi -e 's/default_mode: wait_until_started/default_mode: wait_for_completion/' '$PROJECT_ROOT/scripts/hello_world/loom.exposure.yaml'
  perl -0pi -e 's/wait_timeout_seconds: 10/wait_timeout_seconds: 30/' '$PROJECT_ROOT/scripts/hello_world/loom.exposure.yaml'
"

check_remote "project validates with discovered direct event" "
  loom project validate '$PROJECT_ROOT' > '$TMP_DIR/validate.out'
  grep -q 'Project contract: ok' '$TMP_DIR/validate.out'
  grep -q 'Scripts: 1 discovered, 1 exposed' '$TMP_DIR/validate.out'
  grep -q 'Direct events: 1 discovered' '$TMP_DIR/validate.out'
  grep -q 'example_event -> $CAPABILITY accepted pending_later_slice' '$TMP_DIR/validate.out'
"

check_json "project plan JSON includes direct-event facet item" \
  "loom project plan '$PROJECT_ROOT' --json" \
  ".registerable == true and
   ([.direct_events[] | select(.key == \"example_event\" and .backend_integration_key == \"$BACKEND_INTEGRATION\" and .backend_endpoint_slug == \"$BACKEND_ENDPOINT\" and .target_capability == \"$CAPABILITY\")] | length) == 1 and
   ([.actions[] | select(.action == \"would_pause_project_direct_event\" and .target_ref == \"$BACKEND_ENDPOINT\")] | length) == 1"

check_remote "register direct-event project without activating behavior" "
  loom project register '$PROJECT_ROOT' > '$TMP_DIR/register.out'
  grep -q 'Project registration: created' '$TMP_DIR/register.out'
  grep -q 'Activation: inactive' '$TMP_DIR/register.out'
"

check_remote "direct-event activation fails before target capability exists" "
  if loom project activate '$SLUG' --facet direct-events > '$TMP_DIR/activate-direct-events-before-scripts.out' 2>&1; then
    cat '$TMP_DIR/activate-direct-events-before-scripts.out'
    exit 1
  fi
  grep -q 'target capability is not active or does not exist' '$TMP_DIR/activate-direct-events-before-scripts.out'
"

check_remote "activate scripts facet for direct-event target" "
  loom project activate '$SLUG' --facet scripts > '$TMP_DIR/activate-scripts.out'
  grep -q 'Activated facets: scripts' '$TMP_DIR/activate-scripts.out'
  grep -q '$CAPABILITY' '$TMP_DIR/activate-scripts.out'
"

check_remote "activate direct-events facet as paused endpoint" "
  loom project activate '$SLUG' --facet direct-events > '$TMP_DIR/activate-direct-events.out'
  grep -q 'Activated facets:' '$TMP_DIR/activate-direct-events.out'
  grep -q 'Facet: direct_events activated' '$TMP_DIR/activate-direct-events.out'
  grep -q '$BACKEND_ENDPOINT' '$TMP_DIR/activate-direct-events.out'
  grep -q 'paused' '$TMP_DIR/activate-direct-events.out'
"

check_json "project status records paused direct-event registration" \
  "loom project status '$SLUG' --json" \
  ".ok == true and
   (.data.facets[] | select(.facet_key == \"direct_events\" and .facet_status == \"activated\")) and
   ([.data.direct_event_registrations[] | select(.event_key == \"example_event\" and .backend_endpoint_slug == \"$BACKEND_ENDPOINT\" and .activation_status == \"paused\" and .target_capability == \"$CAPABILITY\")] | length) == 1"

check_json "direct-event endpoints list can filter by project" \
  "loom direct-event endpoints list --project '$SLUG' --json" \
  ".ok == true and
   ([.data[] | select(.endpoint_slug == \"$BACKEND_ENDPOINT\" and .status == \"paused\" and .event_type == \"example.received\")] | length) == 1"

check_remote "endpoint inspect renders paused backend endpoint" "
  loom direct-event endpoint inspect '$BACKEND_ENDPOINT' > '$TMP_DIR/endpoint-inspect.out'
  grep -q 'Direct event endpoint: $BACKEND_ENDPOINT' '$TMP_DIR/endpoint-inspect.out'
  grep -q 'Status: paused' '$TMP_DIR/endpoint-inspect.out'
  grep -q '/v1/direct-events/ingest/$BACKEND_ENDPOINT' '$TMP_DIR/endpoint-inspect.out'
"

check_json "endpoint mapping preview returns target input" \
  "loom direct-event endpoint preview '$BACKEND_ENDPOINT' --body-json '{\"id\":\"preview-$RUN_ID\",\"message\":\"hello from direct event\"}' --json" \
  ".ok == true and .data.input_json.message == \"hello from direct event\" and (.data.missing_fields | length) == 0"

check_remote "resume project direct-event endpoint for local test ingest" "
  loom direct-event endpoint resume '$BACKEND_ENDPOINT' --reason 'slice 07 smoke local ingest' > '$TMP_DIR/resume-endpoint.out'
  grep -q 'Status: active' '$TMP_DIR/resume-endpoint.out'
"

INGEST_JSON="$TMP_DIR/ingest.json"
DIRECT_EVENT_JSON="$TMP_DIR/direct-event.json"
check_remote "local direct-event ingest accepts private-network auth profile" "
  loom direct-event ingest '$BACKEND_ENDPOINT' --body-json '{\"id\":\"msg-$RUN_ID\",\"message\":\"hello from direct event\"}' --json > '$INGEST_JSON'
  jq -e '.ok == true and .data.status == \"accepted\" and (.data.direct_event.direct_event_id | startswith(\"direct_event_\"))' '$INGEST_JSON' >/dev/null
"

DIRECT_EVENT_ID="$(json_value "cat '$INGEST_JSON'" '.data.direct_event.direct_event_id')"
[[ "$DIRECT_EVENT_ID" == direct_event_* ]] || fail "expected direct event id"
pass "captured direct event id"

drive_direct_event_succeeded "$DIRECT_EVENT_ID" "$DIRECT_EVENT_JSON" "smoke_v0_3_slice_07_drive_$RUN_ID"

check_json "completed direct event keeps project target context" \
  "loom direct-event inspect '$DIRECT_EVENT_ID' --json" \
  ".ok == true and
   .data.direct_event.status == \"completed\" and
   .data.automation.source_kind == \"direct_event\" and
   .data.automation.project_id != null and
   (.data.direct_event.invocation_id | startswith(\"invocation_\"))"

check_remote "re-activating direct-events facet is idempotent and returns endpoint to paused" "
  loom project activate '$SLUG' --facet direct-events > '$TMP_DIR/reactivate-direct-events.out'
  grep -q 'Activated facets:' '$TMP_DIR/reactivate-direct-events.out'
  grep -q '$BACKEND_ENDPOINT' '$TMP_DIR/reactivate-direct-events.out'
"

check_json "reactivation keeps one project direct-event registration" \
  "loom project status '$SLUG' --json" \
  ".ok == true and ([.data.direct_event_registrations[] | select(.event_key == \"example_event\" and .backend_endpoint_slug == \"$BACKEND_ENDPOINT\")] | length) == 1"

log "completed $pass_count checks"
