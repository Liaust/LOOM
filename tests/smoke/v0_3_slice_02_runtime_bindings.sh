#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." && pwd)"

HOST="${LOOM_DEV_HOST:-loom-dev}"
REMOTE_DIR="${LOOM_DEV_REMOTE_DIR:-/srv/loom/current}"
RUN_ID="v0-3-slice-02-$(date +%s)-$RANDOM"

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

check_json "health JSON reports runtime binding migration" \
  'loom health --json' \
  '.ok == true and .data.status == "ok" and .data.checks.migrations.current_version >= 26'

check_remote "system status capability has an active endpoint version" '
  loom capability inspect main@system.status.read --json > /tmp/loom-v0-3-slice-02-system-capability.json
  jq -e ".ok == true and (.data.active_version.capability_endpoint_version_id | startswith(\"endpv_\"))" /tmp/loom-v0-3-slice-02-system-capability.json
'

ENDPOINT_VERSION_ID="$(json_value 'cat /tmp/loom-v0-3-slice-02-system-capability.json' '.data.active_version.capability_endpoint_version_id')"
[[ "$ENDPOINT_VERSION_ID" == endpv_* ]] || fail "expected active endpoint version id"
pass "captured active endpoint version"

check_remote "register runtime binding against active endpoint version" "
  loom capability runtime-binding register '$ENDPOINT_VERSION_ID' \
    --kind script \
    --status registered \
    --config-json '{\"script_ref\":\"runtime_binding_smoke\",\"execution_mode\":\"enqueue_only\",\"source\":\"$RUN_ID\"}' \
    --metadata-json '{\"smoke\":\"v0.3.slice.02\",\"run_id\":\"$RUN_ID\"}' \
    --json > /tmp/loom-v0-3-slice-02-binding.json
  jq -e '
    .ok == true and
    .data.binding.runtime_kind == \"script\" and
    .data.binding.status == \"registered\" and
    .data.endpoint_version.capability_endpoint_version_id == \"$ENDPOINT_VERSION_ID\" and
    .data.endpoint.compact_address == \"main@system.status.read\"
  ' /tmp/loom-v0-3-slice-02-binding.json
"

RUNTIME_BINDING_ID="$(json_value 'cat /tmp/loom-v0-3-slice-02-binding.json' '.data.binding.runtime_binding_id')"
[[ "$RUNTIME_BINDING_ID" == runtime_binding_* ]] || fail "expected runtime binding id"
pass "captured runtime binding id"

check_json "runtime bindings list finds registered binding by capability" \
  "loom capability runtime-bindings list --capability main@system.status.read --json" \
  ".ok == true and ([.data[] | select(.binding.runtime_binding_id == \"$RUNTIME_BINDING_ID\" and .endpoint.compact_address == \"main@system.status.read\")] | length) == 1"

check_json "runtime binding inspect by binding id works" \
  "loom capability runtime-binding inspect '$RUNTIME_BINDING_ID' --json" \
  ".ok == true and .data.binding.runtime_binding_id == \"$RUNTIME_BINDING_ID\" and .data.binding.runtime_kind == \"script\""

check_json "runtime binding inspect by capability address resolves active version binding" \
  "loom capability runtime-binding inspect main@system.status.read --json" \
  ".ok == true and .data.binding.runtime_binding_id == \"$RUNTIME_BINDING_ID\" and .data.endpoint.compact_address == \"main@system.status.read\""

check_json "capability inspect includes runtime binding summary" \
  "loom capability inspect main@system.status.read --json" \
  ".ok == true and .data.runtime_binding.runtime_binding_id == \"$RUNTIME_BINDING_ID\""

check_json "explicit system adapter still completes despite runtime binding" \
  "loom capability call main@system.status.read --json" \
  ".ok == true and .data.status == \"completed\" and .data.result.status == \"ok\""

check_json "script-runner provider still inspects normally" \
  "loom capability inspect main@script-runner.script.run --json" \
  ".ok == true and .data.endpoint.compact_address == \"main@script-runner.script.run\" and .data.provider.compact_address == \"main@script-runner\""

log "completed $pass_count checks"
