#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." && pwd)"

HOST="${LOOM_DEV_HOST:-loom-dev}"
SMOKE_SLUG="${LOOM_SLICE_02_SMOKE_SLUG:-slice-02-smoke}"
SMOKE_NAME="${LOOM_SLICE_02_SMOKE_NAME:-Slice 2 Smoke}"

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
log "project scope slug: $SMOKE_SLUG"

check_remote "SSH target reachable" 'hostname'
check_remote "loomd service active" 'systemctl is-active loomd'
check_remote "loom CLI installed" 'command -v loom && loom version'
check_remote "jq available for smoke assertions" 'command -v jq'

check_json "CLI health JSON reports migration and bootstrap ok" \
  'loom health --json' \
  '.ok == true and .data.status == "ok" and .data.checks.migrations.status == "ok" and .data.checks.bootstrap.status == "ok"'

check_json "bootstrap status is ready" \
  'curl --fail --silent --show-error --unix-socket /run/loom/loomd.sock http://loom/v1/bootstrap/status' \
  '.ok == true and .data.ready == true and .data.bootstrap_event_count == 1'

check_remote "actor inspect human output works" 'loom actor inspect owner | grep -q "Actor: owner"'
check_json "actor inspect JSON returns owner" \
  'loom actor inspect owner --json' \
  '.ok == true and .data.actor_key == "owner" and .data.actor_kind == "human" and .data.status == "active"'

check_remote "node inspect human output works" 'loom node inspect main | grep -q "Node: main"'
check_json "node inspect JSON returns main node" \
  'loom node inspect main --json' \
  '.ok == true and .data.node_key == "main" and .data.node_role == "main" and .data.status == "active"'

check_remote "scope inspect human output works" 'loom scope inspect system | grep -q "Scope: system"'
check_json "scope inspect JSON returns system scope" \
  'loom scope inspect system --json' \
  '.ok == true and .data.scope_key == "system" and .data.scope_type == "system"'

check_json "scope inspect JSON returns node-main scope" \
  'loom scope inspect node-main --json' \
  '.ok == true and .data.scope_key == "node-main" and .data.scope_type == "node"'

check_json "scope list JSON includes bootstrap scopes" \
  'loom scope list --json --limit 200' \
  '.ok == true and ([.data[].scope_key] | index("system") and index("node-main") and index("actor-owner"))'

check_json "project scope create succeeds" \
  "loom scope create --type project --slug '$SMOKE_SLUG' --name '$SMOKE_NAME' --if-not-exists --json --correlation-id corr_smoke_slice_02_scope_create" \
  ".ok == true and .data.scope.slug == \"$SMOKE_SLUG\" and .data.scope.scope_type == \"project\""

check_remote "project scope create human output works" \
  "loom scope create --type project --slug '$SMOKE_SLUG' --name '$SMOKE_NAME' --if-not-exists | grep -q \"Scope existing:\\|Scope created:\""

check_json "project scope inspect returns project scope" \
  "loom scope inspect '$SMOKE_SLUG' --json" \
  ".ok == true and .data.slug == \"$SMOKE_SLUG\" and .data.scope_type == \"project\""

check_remote "events list human output works" 'loom events list | grep -q "EVENT ID"'
check_json "events list JSON returns events" \
  'loom events list --json' \
  '.ok == true and (.data | length) >= 1 and (.data[0].event_id | startswith("event_"))'

check_json "bootstrap event exists exactly once" \
  'loom events list --type system.bootstrapped --json' \
  '.ok == true and (.data | length) == 1 and .data[0].event_type == "system.bootstrapped"'

check_json "scope created event exists for smoke scope" \
  "loom events list --type scope.created --scope '$SMOKE_SLUG' --json" \
  ".ok == true and (.data | length) >= 1 and .data[0].event_type == \"scope.created\" and .data[0].payload.slug == \"$SMOKE_SLUG\""

event_id="$(json_value "loom events list --type scope.created --scope '$SMOKE_SLUG' --json" '.data[0].event_id')"
[[ "$event_id" == event_* ]] || fail "expected scope.created event id, got $event_id"
pass "captured scope.created event id"

check_remote "event inspect human output works" "loom event inspect '$event_id' | grep -q \"Event: $event_id\""
check_json "event inspect JSON returns context fields" \
  "loom event inspect '$event_id' --json" \
  ".ok == true and .data.event_id == \"$event_id\" and (.data.actor_id | startswith(\"actor_\")) and (.data.origin_node_id | startswith(\"node_\")) and (.data.scope_id | startswith(\"scope_\")) and .data.correlation_id != null"

check_json "events list filters by correlation id" \
  'loom events list --correlation corr_smoke_slice_02_scope_create --json' \
  '.ok == true and (.data | length) >= 1 and .data[0].correlation_id == "corr_smoke_slice_02_scope_create"'

if grep -R "psql\|QueryContext\|QueryRowContext" "$REPO_ROOT/internal/loomcli" >/dev/null 2>&1; then
  fail "loom CLI contains direct PostgreSQL access"
fi
pass "CLI does not query PostgreSQL directly"

log "passed checks: $pass_count"
