#!/usr/bin/env bash
set -euo pipefail

HOST="${LOOM_DEV_HOST:-loom-dev}"
REMOTE_DIR="${LOOM_DEV_REMOTE_DIR:-/srv/loom/current}"
RUN_ID="v0-3-slice-09-$(date +%s)-$RANDOM"
TOKEN_ID="${RUN_ID//-/}"
SLUG="smoke-connector-$RUN_ID"
PROVIDER_KEY="conn_${TOKEN_ID:0:24}"
PROVIDER="main@$PROVIDER_KEY"
CAPABILITY="$PROVIDER.ping"
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
log "provider: $PROVIDER"
log "capability: $CAPABILITY"

check_remote "SSH target reachable" 'hostname'
check_remote "loomd service active" 'systemctl is-active loomd'
check_remote "loom CLI installed" 'command -v loom && loom version'
check_remote "jq available for smoke assertions" 'command -v jq'

check_json "health reports project connector migration" \
  'loom health --json' \
  '.ok == true and .data.status == "ok" and .data.checks.migrations.current_version >= 32'

TMP_DIR="$(remote "mkdir -p '$REMOTE_DIR/tmp' && mktemp -d '$REMOTE_DIR/tmp/v0-3-slice-09.XXXXXX'")"
trap 'remote "rm -rf '\''$TMP_DIR'\''"' EXIT
PROJECT_ROOT="$TMP_DIR/$SLUG"
log "project root: $PROJECT_ROOT"

check_remote "scaffold connector project" "
  loom project scaffold 'Smoke Connector $RUN_ID' \
    --owner-node main \
    --preset connector \
    --slug '$SLUG' \
    --directory '$TMP_DIR' > '$TMP_DIR/scaffold.out'
  perl -0pi -e 's/key: example_connector/key: $PROVIDER_KEY/' '$PROJECT_ROOT/connectors/example_connector/loom.connector.yaml'
  sed -i 's/{\"message\":\"pong\"}/{\"message\":\"pong\",\"token\":\"$TOKEN_ID\"}/' '$PROJECT_ROOT/connectors/example_connector/scripts/ping/run.sh'
  chmod a+rx '$TMP_DIR'
  chmod -R a+rX '$PROJECT_ROOT'
  grep -q 'Project scaffold: created' '$TMP_DIR/scaffold.out'
"

check_remote "project validates with connector facet" "
  loom project validate '$PROJECT_ROOT' > '$TMP_DIR/validate.out'
  grep -q 'Project contract: ok' '$TMP_DIR/validate.out'
  grep -q 'Connectors: 1 discovered, 1 activatable endpoints' '$TMP_DIR/validate.out'
  grep -q '$CAPABILITY' '$TMP_DIR/validate.out'
"

check_json "project connector plan includes provider endpoint runtime and usage docs" \
  "loom project plan '$PROJECT_ROOT' --json" \
  ".registerable == true and
   (.connectors | length) == 1 and
   .connectors[0].provider_address == \"$PROVIDER\" and
   .connectors[0].capabilities[0].capability_address == \"$CAPABILITY\" and
   ([.actions[] | select(.action == \"would_register_connector_provider\")] | length) == 1 and
   ([.actions[] | select(.action == \"would_register_connector_capability_endpoint\")] | length) == 1 and
   ([.actions[] | select(.action == \"would_register_connector_runtime_binding\")] | length) == 1 and
   ([.actions[] | select(.action == \"would_register_connector_usage_document\")] | length) == 1"

check_remote "register connector project without activating behavior" "
  loom project register '$PROJECT_ROOT' > '$TMP_DIR/register.out'
  grep -q 'Project registration: created' '$TMP_DIR/register.out'
  grep -q 'Activation: inactive' '$TMP_DIR/register.out'
"

check_json "registered status has no connector rows before activation" \
  "loom project status '$SLUG' --json" \
  '.ok == true and ((.data.connector_registrations // []) | length) == 0'

check_remote "connector capability unavailable before activation" "
  if loom capability inspect '$CAPABILITY' > '$TMP_DIR/pre-activation-capability.out' 2>&1; then
    cat '$TMP_DIR/pre-activation-capability.out'
    exit 1
  fi
"

check_remote "activate connectors facet" "
  loom project activate '$SLUG' --facet connectors > '$TMP_DIR/activate-connectors.out'
  grep -q 'Project activation: base_active' '$TMP_DIR/activate-connectors.out'
  grep -q 'Activated facets: connectors' '$TMP_DIR/activate-connectors.out'
  grep -q 'Facet: connectors activated' '$TMP_DIR/activate-connectors.out'
  grep -q '$PROVIDER' '$TMP_DIR/activate-connectors.out'
  grep -q '$CAPABILITY' '$TMP_DIR/activate-connectors.out'
"

check_json "project status records active connector registration" \
  "loom project status '$SLUG' --json" \
  ".ok == true and
   (.data.facets[] | select(.facet_key == \"connectors\" and .facet_status == \"activated\")) and
   ([.data.connector_registrations[] | select(.provider_address == \"$PROVIDER\" and .activation_status == \"active\" and .active_capability_count == 1)] | length) == 1"

check_json "provider project filter returns connector provider" \
  "loom providers list --project '$SLUG' --json" \
  ".ok == true and ([.data[] | select(.compact_address == \"$PROVIDER\" and .provider_type == \"connector\" and .status == \"active\" and .health_status == \"ok\")] | length) == 1"

check_json "connector provider inspect exposes health endpoint and usage doc" \
  "loom provider inspect '$PROVIDER' --json" \
  ".ok == true and
   .data.provider.compact_address == \"$PROVIDER\" and
   .data.provider.provider_type == \"connector\" and
   .data.health.health_status == \"ok\" and
   (.data.endpoints | length) >= 1 and
   (.data.usage_documents | length) >= 1"

check_json "capability list by provider returns connector endpoint" \
  "loom capabilities list --provider '$PROVIDER' --json" \
  ".ok == true and ([.data[] | select(.compact_address == \"$CAPABILITY\" and .status == \"active\")] | length) == 1"

check_json "connector capability is inspectable with script runtime binding" \
  "loom capability inspect '$CAPABILITY' --json" \
  ".ok == true and
   .data.endpoint.compact_address == \"$CAPABILITY\" and
   .data.endpoint.status == \"active\" and
   .data.provider.compact_address == \"$PROVIDER\" and
   .data.runtime_binding.runtime_kind == \"script\" and
   .data.runtime_binding.status == \"active\""

CALL_JSON="$TMP_DIR/connector-call.json"
check_remote "calling connector capability creates completed job" "
  loom capability call '$CAPABILITY' --input '{\"message\":\"hello\"}' --wait --timeout-seconds 45 --json > '$CALL_JSON'
  jq -e '.ok == true and
    .data.status == \"completed\" and
    .data.route.status == \"completed\" and
    .data.capability_call.status == \"completed\" and
    (.data.job_id | startswith(\"job_\")) and
    .data.result.job.job.status == \"completed\" and
    .data.result_refs.job_id == .data.job_id' '$CALL_JSON' >/dev/null
"

JOB_ID="$(json_value "cat '$CALL_JSON'" '.data.job_id')"
[[ "$JOB_ID" == job_* ]] || fail "expected connector script job id"
pass "captured connector script job id"

check_remote "connector script logs include unique token" \
  "loom job logs '$JOB_ID' | grep -q '$TOKEN_ID'"

check_json "jobs list by project includes connector script job" \
  "loom jobs list --project '$SLUG' --json" \
  ".ok == true and ([.data[].job_id] | index(\"$JOB_ID\"))"

check_json "capability calls list includes connector capability" \
  "loom capability-calls list --capability '$CAPABILITY' --json" \
  ".ok == true and ([.data[] | select(.job_id == \"$JOB_ID\" and .status == \"completed\")] | length) >= 1"

check_json "routes list includes completed connector capability route" \
  "loom routes list --capability '$CAPABILITY' --json" \
  ".ok == true and ([.data[] | select(.job_id == \"$JOB_ID\" and .status == \"completed\")] | length) >= 1"

check_remote "re-activating connectors facet is idempotent" "
  loom project activate '$SLUG' --facet connectors > '$TMP_DIR/reactivate-connectors.out'
  grep -q 'Activated facets: connectors' '$TMP_DIR/reactivate-connectors.out'
  grep -q '$CAPABILITY' '$TMP_DIR/reactivate-connectors.out'
"

check_json "reactivation keeps one connector registration row" \
  "loom project status '$SLUG' --json" \
  ".ok == true and ([.data.connector_registrations[] | select(.connector_key == \"$PROVIDER_KEY\" and .provider_address == \"$PROVIDER\")] | length) == 1"

check_json "reactivation keeps one connector provider row" \
  "loom providers list --project '$SLUG' --json" \
  ".ok == true and ([.data[] | select(.compact_address == \"$PROVIDER\" and .provider_type == \"connector\")] | length) == 1"

check_json "reactivation keeps one active connector endpoint" \
  "loom capabilities list --provider '$PROVIDER' --json" \
  ".ok == true and ([.data[] | select(.compact_address == \"$CAPABILITY\" and .status == \"active\")] | length) == 1"

check_json "reactivation keeps one active connector runtime binding" \
  "loom capability runtime-bindings list --capability '$CAPABILITY' --json" \
  ".ok == true and ([.data[] | select(.binding.runtime_kind == \"script\" and .binding.status == \"active\" and .endpoint.compact_address == \"$CAPABILITY\")] | length) == 1"

log "completed $pass_count checks"
