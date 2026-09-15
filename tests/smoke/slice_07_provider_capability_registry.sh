#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." && pwd)"

HOST="${LOOM_DEV_HOST:-loom-dev}"
REMOTE_DIR="${LOOM_DEV_REMOTE_DIR:-/srv/loom/current}"

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

check_remote "SSH target reachable" 'hostname'
check_remote "loomd service active" 'systemctl is-active loomd'
check_remote "loom CLI installed" 'command -v loom && loom version'
check_remote "jq available for smoke assertions" 'command -v jq'

check_json "health JSON returns ok" \
  'loom health --json' \
  '.ok == true and .data.status == "ok"'

check_json "status JSON returns ok" \
  'loom status --json' \
  '.ok == true and .data.status == "ok"'

check_json "provider registry includes seeded providers" \
  'loom providers list --json --limit 20' \
  '.ok == true and ([.data[].compact_address] | index("main@system") and index("main@object-store") and index("main@script-runner"))'

check_remote "providers human table has stable columns" \
  'loom providers list | head -n 1 | grep -q "ADDRESS.*TYPE.*STATUS.*HEALTH.*NODE"'

check_json "provider inspect returns health and endpoints" \
  'loom provider inspect main@system --json' \
  '.ok == true and .data.provider.compact_address == "main@system" and .data.health.health_status == "ok" and (.data.endpoints | length) >= 2'

check_json "provider health is inspectable" \
  'loom provider health main@system --json' \
  '.ok == true and .data.health_status == "ok" and .data.availability_status == "available"'

check_json "capability registry includes seeded endpoint" \
  'loom capabilities list --json --limit 30' \
  '.ok == true and ([.data[].compact_address] | index("main@system.status.read"))'

check_remote "capabilities human table has stable columns" \
  'loom capabilities list | head -n 1 | grep -q "ADDRESS.*CLASS.*FORM.*RISK.*AUTH.*STATUS.*HEALTH"'

check_json "capability inspect returns metadata and schemas" \
  'loom capability inspect main@system.status.read --json' \
  '.ok == true and .data.endpoint.compact_address == "main@system.status.read" and .data.class.namespace == "system" and .data.provider.compact_address == "main@system" and .data.endpoint.input_schema_json.type == "object" and .data.endpoint.output_schema_json.type == "object" and .data.endpoint.risk_level == "low" and .data.endpoint.execution_authorization_level == 1'

check_json "usage documents are approved and attached" \
  'loom capability usage-docs main@system.status.read --json' \
  '.ok == true and (.data | length) >= 1 and .data[0].review_status == "approved" and .data[0].body_format == "markdown" and (.data[0].content_hash | startswith("sha256:"))'

check_json "address search returns compact status candidate" \
  'loom capabilities search status --json' \
  '.ok == true and ([.data[].compact_address] | index("main@system.status.read"))'

check_json "script search returns script runner candidate" \
  'loom capabilities search script --json' \
  '.ok == true and ([.data[].compact_address] | index("main@script-runner.script.run"))'

check_json "usage document search returns object ingest candidate" \
  'loom capabilities search notes --json' \
  '.ok == true and ([.data[].compact_address] | index("main@object-store.object.ingest"))'

check_json "search candidates stay compact" \
  'loom capabilities search status --json' \
  '.ok == true and all(.data[]; (has("input_schema_json") | not) and (has("output_schema_json") | not) and (has("tool_schema_json") | not))'

check_remote "search human table includes match column" \
  'loom capabilities search status | head -n 1 | grep -q "ADDRESS.*CLASS.*RISK.*AUTH.*HEALTH.*MATCH"'

check_remote "missing capability returns stable error envelope" \
  'set +e; loom capability inspect main@system.nope --json > /tmp/loom-slice-07-missing.json 2> /tmp/loom-slice-07-missing.err; status=$?; set -e; test "$status" -ne 0 && test ! -s /tmp/loom-slice-07-missing.err && jq -e ".ok == false and .error.code == \"capabilities.not_found\" and .meta.correlation_id == .error.correlation_id" /tmp/loom-slice-07-missing.json'

check_remote "CLI does not query PostgreSQL directly" \
  "if grep -R \"psql\\|QueryContext\\|QueryRowContext\" '$REMOTE_DIR/internal/loomcli' >/dev/null 2>&1; then exit 1; fi"

if grep -R "psql\|QueryContext\|QueryRowContext" "$REPO_ROOT/internal/loomcli" >/dev/null 2>&1; then
  fail "local loom CLI contains direct PostgreSQL access"
fi
pass "local CLI does not query PostgreSQL directly"

log "passed checks: $pass_count"
